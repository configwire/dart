// Package envresolve resolves environment slugs deterministically.
// Background: slugs are NOT globally unique — two projects may each own a
// "dev" env, so every slug-keyed route resolves instead of taking the
// first row with a matching slug.
package envresolve

// EnvRow is a DB-free projection so the isolation predicates are unit-testable on fake rows.
type EnvRow struct {
	ID      string
	Slug    string
	Project string
}

// SlugTaken reports whether (project, slug) is already claimed by another row.
// selfID excludes the row being updated (empty on create).
// Empty project or slug never counts as taken — the collection's
// Required constraint rejects those separately.
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
