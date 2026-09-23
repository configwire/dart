// BACKGROUND: environment slugs are NOT globally unique — two projects
// may each own a "dev" env. Every slug-keyed route must therefore
// resolve deterministically instead of taking the first row with a
// matching slug.
package envresolve

// DB-free so the isolation predicates are unit-testable on fake rows.
type EnvRow struct {
	ID      string
	Slug    string
	Project string
}

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
