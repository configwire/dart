package envresolve

import (
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
)

// ErrNotFound reports that no environment matches the slug (or the
// slug+project qualifier). Handlers map it to 404 "Unknown env.".
var ErrNotFound = errors.New("unknown env")

// ErrScopeMismatch reports that an SDK key's env does not match the
// requested slug. Handlers map it to 401.
var ErrScopeMismatch = errors.New("SDK key is not authorized for this env.")

// AmbiguousError reports that several environments share one slug and
// no project qualifier disambiguated them. Handlers map it to 400 and
// surface the message, which names the collision and the qualifier.
// Value semantics: compare with errors.As into an AmbiguousError value.
type AmbiguousError struct {
	Slug  string
	Count int
}

func (e AmbiguousError) Error() string {
	return fmt.Sprintf("ambiguous env slug %q: it exists in %d projects; specify ?project=<projectId>", e.Slug, e.Count)
}

// PickIndex is pure (no I/O), so resolution policy is unit-testable.
// O(n) over same-slug rows (tens at most) — documented, v1-appropriate.
func PickIndex(slug string, projects []string, project string) (int, error) {
	if len(projects) == 0 {
		return -1, ErrNotFound
	}
	if project != "" {
		for i, p := range projects {
			if p == project {
				return i, nil
			}
		}
		return -1, ErrNotFound
	}
	if len(projects) > 1 {
		return -1, AmbiguousError{Slug: slug, Count: len(projects)}
	}
	return 0, nil
}

// Resolve maps a slug (+ optional project qualifier) to its record. Never silently picks the first of several rows.
func Resolve(app core.App, slug, project string) (*core.Record, error) {
	recs, err := app.FindAllRecords("environments")
	if err != nil {
		return nil, err
	}
	var cands []*core.Record
	var projects []string
	for _, r := range recs {
		if r.GetString("slug") != slug {
			continue
		}
		cands = append(cands, r)
		projects = append(projects, r.GetString("project"))
	}
	idx, err := PickIndex(slug, projects, project)
	if err != nil {
		return nil, err
	}
	return cands[idx], nil
}

// ResolveForKey maps a slug through the SDK key's bound env. The key's env IS
// the env, so slug collisions across projects cannot misroute. Keys with
// no env set (legacy) fall back to unqualified Resolve.
func ResolveForKey(app core.App, slug, keyEnvID string) (*core.Record, error) {
	if keyEnvID == "" {
		return Resolve(app, slug, "")
	}
	env, err := app.FindRecordById("environments", keyEnvID)
	if err != nil {
		return nil, ErrNotFound
	}
	if env.GetString("slug") != slug {
		return nil, ErrScopeMismatch
	}
	return env, nil
}

// ToRequestError maps Resolve/ResolveForKey failures onto handler error shapes.
// Anything else (e.g. a store failure) passes through untouched.
func ToRequestError(re *core.RequestEvent, err error) error {
	if errors.Is(err, ErrNotFound) {
		return re.NotFoundError("Unknown env.", nil)
	}
	var amb AmbiguousError
	if errors.As(err, &amb) {
		return re.BadRequestError(amb.Error(), nil)
	}
	if errors.Is(err, ErrScopeMismatch) {
		return re.UnauthorizedError("SDK key is not authorized for this env.", nil)
	}
	return err
}
