package ingest

import (
	"log"
	"net/http"
	"sync"
	"time"

	"configwire/envresolve"

	"github.com/pocketbase/pocketbase/core"
)

// Limiter is a per-key fixed-window rate limiter. Memory-growth risk (reported
// per contract): one entry per distinct key-hash ever seen; opportunistic
// sweeps (≤1/s) delete entries idle for >2 windows, so steady-state size ≈
// active keys, but a key-scan attack could still grow the map — acceptable
// for the SDK-key space (keys are server-issued, not attacker-chosen).
type Limiter struct {
	mu        sync.Mutex
	windows   map[string]*rateWindow
	window    time.Duration
	lastSweep time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

// NewLimiter builds a Limiter over the default rate window.
func NewLimiter() *Limiter {
	return &Limiter{windows: make(map[string]*rateWindow), window: RateWindow}
}

// newLimiterWithWindow is the test seam for window behavior.
func newLimiterWithWindow(d time.Duration) *Limiter {
	return &Limiter{windows: make(map[string]*rateWindow), window: d}
}

// Allow consumes one token for id, reporting false when the window is exhausted.
func (l *Limiter) Allow(id string, limit int) bool {
	if limit <= 0 {
		limit = DefaultRateLimit
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.windows[id]
	if !ok || now.Sub(w.start) >= l.window {
		w = &rateWindow{start: now}
		l.windows[id] = w
	}
	w.count++
	if now.Sub(l.lastSweep) >= l.window {
		l.lastSweep = now
		for k, v := range l.windows {
			if now.Sub(v.start) >= 2*l.window {
				delete(l.windows, k)
			}
		}
	}
	return w.count <= limit
}

// Size reports tracked key count for tests/QA only.
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.windows)
}

var module struct {
	batcher *Batcher
	limiter *Limiter
}

// Register mounts the events ingest route and starts the flush pipeline.
func Register(se *core.ServeEvent) {
	module.batcher = NewBatcher(se.App)
	module.limiter = NewLimiter()
	module.batcher.Start()
	se.Router.POST("/api/v1/env/{env}/events", postEvents)
	se.App.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		module.batcher.Stop()
		return e.Next()
	})
}

// EnableWAL switches SQLite to WAL mode on serve. The store is not yet open
// during OnBootstrap, so this binds OnServe.
// The mode is persistent in the DB file; verify with `sqlite3 <dir>/data.db "pragma journal_mode;"` → wal.
func EnableWAL(app core.App) {
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if _, err := e.App.DB().NewQuery("PRAGMA journal_mode=WAL").Execute(); err != nil {
			e.App.Logger().Warn("ingest: failed to enable WAL", "error", err)
		} else {
			log.Print("ingest: SQLite journal_mode set to WAL")
		}
		return e.Next()
	})
}

// Order: 401 (key) → 404 (env) / 400 (ambiguous slug) / 401 (scope) →
// 429 (rate) → 400/413 (body) → 202.
// Uses re.App for every request-scoped lookup (never a captured app).
func postEvents(re *core.RequestEvent) error {
	key, err := RequireSDKKey(re)
	if err != nil {
		return err
	}
	slug := re.Request.PathValue("env")
	// Deterministic: the key's env IS the env, so a slug shared by
	// several projects can never misroute here.
	env, err := envresolve.ResolveForKey(re.App, slug, key.GetString("env"))
	if err != nil {
		return envresolve.ToRequestError(re, err)
	}
	if !module.limiter.Allow(key.GetString("hash"), RateLimitFor(key)) {
		return re.JSON(http.StatusTooManyRequests, map[string]any{"message": "Rate limit exceeded.", "status": 429})
	}
	body, aerr := readBody(re)
	if aerr != nil {
		return writeAPIError(re, aerr)
	}
	events, aerr := ValidateBody(body)
	if aerr != nil {
		return writeAPIError(re, aerr)
	}
	now := time.Now().UTC()
	flagIDs := make(map[string]string, len(events))
	// Flag resolution is scoped to the env's project once per request
	// (key + project, never global key): the same key under another
	// project never matches, so events can't attach to a foreign flag.
	flagRows, _ := envresolve.FlagRows(re.App)
	projectID := env.GetString("project")
	stored := make([]StoredEvent, 0, len(events))
	for _, ev := range events {
		ts := ev.Ts
		if ts.IsZero() {
			ts = now
		}
		flagID := ""
		if ev.Flag != "" {
			if id, ok := flagIDs[ev.Flag]; ok {
				flagID = id
			} else {
				// Best-effort flag resolution: unknown flag keys are stored
				// with the relation unset (variant/kind/userHash preserved)
				// rather than rejecting the batch — the schema has no
				// flagKey text field and migrations are owned elsewhere.
				flagID, _ = envresolve.MatchFlag(flagRows, ev.Flag, projectID)
				flagIDs[ev.Flag] = flagID
			}
		}
		stored = append(stored, StoredEvent{
			EnvID: env.Id, FlagID: flagID,
			Kind: ev.Kind, Variant: ev.Variant,
			UserHash: ResolveUserHash(ev.UserHash, ""), Ts: ts,
		})
	}
	for _, s := range stored {
		if !module.batcher.Enqueue(s) {
			// Buffer saturated (see drop-vs-block note in batcher.go).
			return re.JSON(http.StatusServiceUnavailable, map[string]any{"message": "Ingest buffer full, retry.", "status": 503})
		}
	}
	return re.JSON(http.StatusAccepted, map[string]any{"accepted": len(stored), "status": 202})
}

func writeAPIError(re *core.RequestEvent, aerr *apiError) error {
	return re.JSON(aerr.Status, map[string]any{"message": aerr.Msg, "status": aerr.Status})
}
