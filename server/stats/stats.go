// Package stats implements the stats query API (plan todo 11).
//
//	GET /api/v1/admin/env/{env}/stats?flag=X&since=7d
//
// A small read-model over the events rows that T9 persists. Aggregate
// counts only — userHash never leaves the server through this endpoint
// (no raw-event export, no PII in the response).
//
// SINCE GRAMMAR: ?since=<N>d (days), default 7d when absent, clamped to a
// max of 90d. Malformed values (abc, -5d, 0d, 7h, bare numbers) -> 400.
// FLAG POLICY: ?flag=<key> filters by flag relation -> key resolution;
// unknown flag keys (including path-ish "../x") -> zeros with 200, never
// 404. Unknown env slugs -> 404. Auth is superuser-only via
// apis.RequireSuperuserAuth().
//
// SCALE NOTE (v1-appropriate, documented per contract): aggregation runs
// in-Go over FindAllRecords("events") filtered by env/flag/kind/ts — O(n)
// in total events. Fine at v1 scale. FOLLOW-UP: if events ever grow past
// ~100k rows, replace the scan with indexed filter queries on
// env+flag+kind+ts (or a pre-aggregated counters collection); the pure
// Aggregate helper keeps that migration handler-local.
package stats

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"confignest/releases"
	"confignest/security"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// Since bounds for the ?since=<N>d parameter.
const (
	// DefaultSinceDays applies when ?since= is absent.
	DefaultSinceDays = 7
	// MaxSinceDays clamps large ?since= values (e.g. 400d -> 90d).
	MaxSinceDays = 90
)

// ParseSince parses the ?since= query value purely (no I/O), so it is
// unit-testable. "" -> DefaultSinceDays. "<N>d" with N >= 1 -> N clamped
// to MaxSinceDays. Anything else (abc, -5d, 0d, 7h, "7") -> error, which
// the handler maps to 400.
func ParseSince(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultSinceDays, nil
	}
	s := strings.TrimSpace(raw)
	if !strings.HasSuffix(s, "d") {
		return 0, errBadSince()
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if err != nil || n < 1 {
		return 0, errBadSince()
	}
	if n > MaxSinceDays {
		n = MaxSinceDays
	}
	return n, nil
}

type sinceError struct{ msg string }

func (e *sinceError) Error() string { return e.msg }

func errBadSince() error {
	return &sinceError{msg: "invalid since: must be <N>d (1-90 days), e.g. 7d."}
}

// EventRow is the minimal event projection Aggregate needs — pure and
// DB-free so aggregation is unit-testable on fake rows.
type EventRow struct {
	EnvID   string
	FlagID  string // "" when the ingest-time flag key matched nothing (relation unset)
	Kind    string // "fetch" | "exposure"
	Variant string // verbatim stored variant ("" possible, counted as-is)
	Ts      time.Time
}

// Stats is the aggregation result: exact integer counts, no rounding.
type Stats struct {
	Fetches    int
	Exposures  int
	PerVariant map[string]int // over exposures only, verbatim variant names
}

// Aggregate folds rows into Stats purely (no I/O). Filters: envID must
// match; when filterByFlag is true only rows whose flag relation equals
// flagID match (rows with an unset flag relation are excludable and drop
// out here); rows with ts before cutoff (or zero ts, which proves no
// recency) are excluded. Fetches and exposures are both flag-filtered;
// PerVariant counts exposures only.
func Aggregate(rows []EventRow, envID, flagID string, filterByFlag bool, cutoff time.Time) Stats {
	out := Stats{PerVariant: map[string]int{}}
	for _, r := range rows {
		if r.EnvID != envID {
			continue
		}
		if filterByFlag && r.FlagID != flagID {
			continue
		}
		if r.Ts.IsZero() || r.Ts.Before(cutoff) {
			continue
		}
		switch r.Kind {
		case "fetch":
			out.Fetches++
		case "exposure":
			out.Exposures++
			out.PerVariant[r.Variant]++
		}
	}
	return out
}

// Register mounts the admin stats route (superuser-only; SDK keys -> 401).
func Register(se *core.ServeEvent) {
	se.Router.GET("/api/v1/admin/env/{env}/stats", getStats).Bind(apis.RequireSuperuserAuth())
}

// getStats handles GET /api/v1/admin/env/:env/stats.
// Order: 401 (superuser, via middleware) -> 404 (unknown env slug) ->
// 400 (malformed since) -> 200 (counts, or zeros for unknown flags).
// Uses re.App for every request-scoped lookup (never a captured app).
func getStats(re *core.RequestEvent) error {
	security.SetHeaders(re)
	slug := re.Request.PathValue("env")
	env, err := re.App.FindFirstRecordByFilter("environments", "slug = {:slug}", map[string]any{"slug": slug})
	if err != nil {
		return re.NotFoundError("Unknown env.", nil)
	}

	days, perr := ParseSince(re.Request.URL.Query().Get("since"))
	if perr != nil {
		return re.BadRequestError(perr.Error(), nil)
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	// Flag filter by relation -> key resolution. Unknown flag keys yield
	// zeros with 200 (not 404): there is simply nothing recorded under
	// that key. Events whose flag relation is unset never match a filter.
	flagKey := re.Request.URL.Query().Get("flag")
	flagID := ""
	filterByFlag := false
	if flagKey != "" {
		filterByFlag = true
		if fr, ferr := re.App.FindFirstRecordByFilter("flags", "key = {:k}", map[string]any{"k": flagKey}); ferr == nil {
			flagID = fr.Id
		}
	}

	rows, err := loadRows(re.App)
	if err != nil {
		return err
	}
	st := Aggregate(rows, env.Id, flagID, filterByFlag, cutoff)

	version, err := releases.MaxVersionForEnv(re.App, env.Id)
	if err != nil {
		return err
	}

	return re.JSON(http.StatusOK, map[string]any{
		"fetches":    st.Fetches,
		"exposures":  st.Exposures,
		"perVariant": st.PerVariant,
		"version":    version,
	})
}

// loadRows projects the events collection into EventRows in one O(n)
// scan (see the package SCALE NOTE). userHash is deliberately never
// read — aggregate counts only.
func loadRows(app core.App) ([]EventRow, error) {
	recs, err := app.FindAllRecords("events")
	if err != nil {
		return nil, err
	}
	rows := make([]EventRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, EventRow{
			EnvID:   r.GetString("env"),
			FlagID:  r.GetString("flag"),
			Kind:    r.GetString("kind"),
			Variant: r.GetString("variant"),
			Ts:      r.GetDateTime("ts").Time(),
		})
	}
	return rows, nil
}
