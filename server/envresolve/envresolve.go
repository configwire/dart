// Package envresolve centralizes environment identity for the
// multi-project isolation fixes.
//
// BACKGROUND: environment slugs are NOT globally unique — two projects
// may each own a "dev" env. Every slug-keyed route must therefore
// resolve deterministically instead of taking the first row with a
// matching slug. This package holds the pure predicates (unit-tested
// here) plus thin core.App wrappers used by the fetch, ingest, stream,
// releases, and stats handlers.
package envresolve

// EnvRow is the minimal environment projection the pure helpers need —
// DB-free so the isolation predicates are unit-testable on fake rows.
type EnvRow struct {
	ID      string
	Slug    string
	Project string
}

// SlugTaken reports whether (project, slug) collides with another
// environment row. selfID excludes the row being updated (empty on
// create, where the row is not in the table yet). Empty project or
// slug never counts as taken — the collection's Required constraint
// rejects those separately.
func SlugTaken(rows []EnvRow, project, slug, selfID string) bool {
	if project == "" || slug == "" {
		return false
	}
	for _, r := range rows {
		if r.ID != "" && r.ID == selfID {
			continue
		}
		if r.Project == project && r.Slug == slug {
			return true
		}
	}
	return false
}
