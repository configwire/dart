// Package purge enforces the compliance retention policy:
// raw events die at 30d, daily aggregates live to 90d.
//
// Rollup-before-delete runs in the SAME operation: rows strictly older
// than the cutoff are first aggregated into event_daily upserts keyed by
// (day, env, flag, variant), then the raw rows are deleted. Stale
// event_daily rows older than 90d (cutoff minus RollupRetentionDays minus
// RawRetentionDays) are deleted too. Raw releases/rollups collections are
// never touched — events only.
//
// CRASH SEMANTIC: upserts land before deletes, so a crash can never lose
// data silently — at worst the crashed batch is counted twice on re-run
// (upsert re-adds the same rows' counts, then delete finishes). The
// (day,env,flag,variant) KEY itself is idempotent (one row per key, never
// duplicates); only the counters of the in-flight bucket can overshoot on
// a crash exactly between upsert and delete. Re-running the purge
// converges (second run finds nothing to delete).
//
// DELETION NOTE (no GDPR export UI by design): purged raw rows carry only
// userHash (never raw user ids — see ingest), and event_daily rows carry
// aggregate counts only, so no PII survives past the raw window.
//
// SCHEDULING: a 24h process-local ticker + an admin route. No PocketBase
// cron/scheduler dependency is needed on purpose: ConfigWire ships as a
// single binary, and a ticker plus a manually-triggerable route is the
// smallest correct scheduler for a daily retention job.
package purge

import (
	"log"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// RawRetentionDays bounds raw event lifetime before rollup-then-delete.
	RawRetentionDays    = 30
	// RollupRetentionDays bounds daily aggregate lifetime.
	RollupRetentionDays = 90
	// rollupExtraDays derives the event_daily cutoff from the raw-events
	// cutoff passed to PurgeOlderThan: 90d - 30d = 60d further back.
	rollupExtraDays = RollupRetentionDays - RawRetentionDays
)

// EventRow is the minimal event projection the purge needs — pure and
// DB-free so rollup math is unit-testable on fake rows.
type EventRow struct {
	EnvID   string
	FlagID  string // "" when the ingest-time flag key matched nothing (relation unset)
	Kind    string // "fetch" | "exposure"
	Variant string // verbatim stored variant ("" possible, counted as-is)
	Ts      time.Time
}

// Rollup is one per-(day, env, flag, variant) aggregate bucket stored in event_daily.
type Rollup struct {
	Day       time.Time // UTC midnight of the event day
	EnvID     string
	FlagID    string
	Variant   string
	Fetches   int
	Exposures int
}

// DayBucket truncates ts to its UTC calendar day (the rollup grain).
func DayBucket(ts time.Time) time.Time {
	y, m, d := ts.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ShouldDelete is the deterministic cutoff rule: strictly-older-than is
// deleted, exactly-at-cutoff is KEPT, and zero-ts rows (corrupt, no day
// bucket provable) are KEPT — never purge what cannot be rolled up.
func ShouldDelete(ts, cutoff time.Time) bool {
	if ts.IsZero() {
		return false
	}
	return ts.Before(cutoff)
}

// BuildRollups folds rows into per-(day,env,flag,variant) buckets purely
// (no I/O). Unknown kinds are ignored (same fail-closed posture as
// stats.Aggregate); zero-ts rows are skipped (unkeepable per ShouldDelete
// they are never in the delete set either).
func BuildRollups(rows []EventRow) []Rollup {
	type key struct {
		day     string // YYYY-MM-DD UTC
		env     string
		flag    string
		variant string
	}
	byKey := map[key]*Rollup{}
	var order []key
	for _, r := range rows {
		if r.Ts.IsZero() {
			continue
		}
		day := DayBucket(r.Ts)
		k := key{day: day.Format("2006-01-02"), env: r.EnvID, flag: r.FlagID, variant: r.Variant}
		b, ok := byKey[k]
		if !ok {
			b = &Rollup{Day: day, EnvID: r.EnvID, FlagID: r.FlagID, Variant: r.Variant}
			byKey[k] = b
			order = append(order, k)
		}
		switch r.Kind {
		case "fetch":
			b.Fetches++
		case "exposure":
			b.Exposures++
		}
	}
	out := make([]Rollup, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// PurgeOlderThan rolls raw events with ts strictly before cutoff into
// event_daily upserts, deletes those raw rows, then deletes event_daily
// rows with day strictly before cutoff minus 60d (i.e. older than 90d
// when cutoff is now-30d). It returns the total rows deleted
// (raw events + stale rollups).
func PurgeOlderThan(app core.App, cutoff time.Time) (deleted int, err error) {
	recs, err := app.FindAllRecords("events")
	if err != nil {
		return 0, err
	}
	var doomed []*core.Record
	var doomedRows []EventRow
	for _, r := range recs {
		ts := r.GetDateTime("ts").Time()
		if !ShouldDelete(ts, cutoff) {
			continue
		}
		doomed = append(doomed, r)
		doomedRows = append(doomedRows, EventRow{
			EnvID:   r.GetString("env"),
			FlagID:  r.GetString("flag"),
			Kind:    r.GetString("kind"),
			Variant: r.GetString("variant"),
			Ts:      ts,
		})
	}

	// Rollup BEFORE delete: no data-loss window beyond a crash (see the
	// package CRASH SEMANTIC note).
	if len(doomedRows) > 0 {
		if err := upsertRollups(app, BuildRollups(doomedRows)); err != nil {
			return 0, err
		}
	}
	for _, r := range doomed {
		if err := app.Delete(r); err != nil {
			return deleted, err
		}
		deleted++
	}

	// Stale aggregates: event_daily rows older than 90d die too.
	rollupCutoff := cutoff.AddDate(0, 0, -rollupExtraDays)
	daily, err := app.FindAllRecords("event_daily")
	if err != nil {
		return deleted, err
	}
	for _, r := range daily {
		day := r.GetDateTime("day").Time()
		if day.IsZero() || !day.Before(rollupCutoff) {
			continue
		}
		if err := app.Delete(r); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// CountOlderThan reports how many rows PurgeOlderThan would delete with
// the same cutoff, with zero writes (dry-run path).
func CountOlderThan(app core.App, cutoff time.Time) (int, error) {
	n := 0
	recs, err := app.FindAllRecords("events")
	if err != nil {
		return 0, err
	}
	for _, r := range recs {
		if ShouldDelete(r.GetDateTime("ts").Time(), cutoff) {
			n++
		}
	}
	rollupCutoff := cutoff.AddDate(0, 0, -rollupExtraDays)
	daily, err := app.FindAllRecords("event_daily")
	if err != nil {
		return n, err
	}
	for _, r := range daily {
		day := r.GetDateTime("day").Time()
		if !day.IsZero() && day.Before(rollupCutoff) {
			n++
		}
	}
	return n, nil
}

// upsertRollups adds each bucket's counts onto the matching event_daily
// row (matched in-Go on day+env+flag+variant; O(n) scan is v1-appropriate
// like stats.loadRows) or creates the row when absent. Re-running with
// the same buckets converges to one row per key — the upsert key is
// idempotent even though counters add (see CRASH SEMANTIC).
func upsertRollups(app core.App, buckets []Rollup) error {
	if len(buckets) == 0 {
		return nil
	}
	col, err := app.FindCollectionByNameOrId("event_daily")
	if err != nil {
		return err
	}
	existing, err := app.FindAllRecords("event_daily")
	if err != nil {
		return err
	}
	dayOf := func(r *core.Record) string {
		if t := r.GetDateTime("day").Time(); !t.IsZero() {
			return t.UTC().Format("2006-01-02")
		}
		return ""
	}
	for _, b := range buckets {
		wantDay := b.Day.UTC().Format("2006-01-02")
		var match *core.Record
		for _, r := range existing {
			if dayOf(r) == wantDay &&
				r.GetString("env") == b.EnvID &&
				r.GetString("flag") == b.FlagID &&
				r.GetString("variant") == b.Variant {
				match = r
				break
			}
		}
		if match == nil {
			match = core.NewRecord(col)
			match.Set("day", b.Day)
			if b.EnvID != "" {
				match.Set("env", b.EnvID)
			}
			if b.FlagID != "" {
				match.Set("flag", b.FlagID)
			}
			match.Set("variant", b.Variant)
			match.Set("fetches", b.Fetches)
			match.Set("exposures", b.Exposures)
			if err := app.Save(match); err != nil {
				return err
			}
			existing = append(existing, match)
			continue
		}
		match.Set("fetches", match.GetInt("fetches")+b.Fetches)
		match.Set("exposures", match.GetInt("exposures")+b.Exposures)
		if err := app.Save(match); err != nil {
			return err
		}
	}
	return nil
}

// Register mounts POST /api/v1/admin/maintenance/purge (superuser-only;
// ?dry=1 reports without writing) and starts the daily 24h ticker that
// purges raw events older than RawRetentionDays. Uses re.App / se.App
// per request (never a captured stale app beyond the process singleton
// the ticker legitimately owns, same discipline as ingest.Batcher).
func Register(se *core.ServeEvent) {
	se.Router.POST("/api/v1/admin/maintenance/purge", postPurge).Bind(apis.RequireSuperuserAuth())
	go dailyTicker(se.App)
}

func dailyTicker(app core.App) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().UTC().AddDate(0, 0, -RawRetentionDays)
		n, err := PurgeOlderThan(app, cutoff)
		if err != nil {
			log.Printf("purge: daily run failed: %v", err)
			continue
		}
		log.Printf("purge: daily run deleted %d rows (cutoff %s)", n, cutoff.Format(time.RFC3339))
	}
}

// postPurge handles POST /api/v1/admin/maintenance/purge.
// ?dry=1 (or true) -> 200 {"deleted":N,"dry":true} with ZERO writes.
// ?dry= absent/0/false -> live purge -> 200 {"deleted":N,"dry":false}.
// Any other dry value (e.g. abc) -> 400. Unauth/SDK-key -> 401 via
// middleware; empty events table -> 200 deleted:0.
func postPurge(re *core.RequestEvent) error {
	raw := re.Request.URL.Query().Get("dry")
	dry := false
	switch raw {
	case "", "0", "false":
		dry = false
	case "1", "true":
		dry = true
	default:
		return re.BadRequestError("invalid dry: must be 0 or 1.", nil)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -RawRetentionDays)
	if dry {
		n, err := CountOlderThan(re.App, cutoff)
		if err != nil {
			return err
		}
		return re.JSON(http.StatusOK, map[string]any{
			"deleted": n,
			"dry":     true,
			"cutoff":  cutoff.Format(time.RFC3339),
		})
	}
	n, err := PurgeOlderThan(re.App, cutoff)
	if err != nil {
		return err
	}
	log.Printf("purge: manual run deleted %d rows (cutoff %s)", n, cutoff.Format(time.RFC3339))
	return re.JSON(http.StatusOK, map[string]any{
		"deleted": n,
		"dry":     false,
		"cutoff":  cutoff.Format(time.RFC3339),
	})
}
