package envresolve

import (
	"github.com/pocketbase/pocketbase/core"
)

// FlagRow is a DB-free projection so (key, project) matching is unit-testable on fake rows.
type FlagRow struct {
	ID      string
	Key     string
	Project string
}

// MatchFlag resolves a flag key within one project only. Same key under another
// project never matches: stats/ingest must not leak one project's flag
// identity into another's counts. First match wins (flag keys are not
// unique per project; the write path only enforces shape + cap).
func MatchFlag(rows []FlagRow, key, project string) (string, bool) {
	if key == "" || project == "" {
		return "", false
	}
	for _, r := range rows {
		if r.Key == key && r.Project == project {
			return r.ID, true
		}
	}
	return "", false
}

// FlagRows projects stored flags into matchable rows. A store failure yields
// a nil slice plus the error; callers treat an empty result as "unknown
// flag" (zeros, 202).
func FlagRows(app core.App) ([]FlagRow, error) {
	recs, err := app.FindAllRecords("flags")
	if err != nil {
		return nil, err
	}
	rows := make([]FlagRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, FlagRow{
			ID:      r.Id,
			Key:     r.GetString("key"),
			Project: r.GetString("project"),
		})
	}
	return rows, nil
}
