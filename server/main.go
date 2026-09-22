package main

import (
	"errors"
	"log"
	"os"
	"regexp"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"

	"confignest/envresolve"
	"confignest/fetch"
	"confignest/ingest"
	_ "confignest/migrations"
	"confignest/purge"
	"confignest/releases"
	"confignest/stats"
)

// ConfigNest data-integrity hooks (todo 5).
// Flag key shape + per-project cap and releases immutability live in
// code (not collection options) so they apply to every write path
// (API, dashboard, server-side e.App saves).

var flagKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	maxFlagKeyLength   = 128
	maxFlagsPerProject = 1000
)

func checkFlagKey(key string) error {
	if len(key) == 0 || len(key) > maxFlagKeyLength || !flagKeyPattern.MatchString(key) {
		return errors.New("invalid flag key: must match ^[A-Za-z_][A-Za-z0-9_]*$ and be 1-128 chars")
	}
	return nil
}

// countProjectFlags returns the number of flags rows for a project.
// Exact count-query equivalent:
//
//	SELECT COUNT(*) FROM flags WHERE project = '<projectId>'
//
// Implemented via FindAllRecords + in-Go filter (no extra deps;
// go.mod is owned by another todo).
func countProjectFlags(app core.App, project string) (int64, error) {
	records, err := app.FindAllRecords("flags")
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range records {
		if r.GetString("project") == project {
			n++
		}
	}
	return n, nil
}

func registerConfignestHooks(app core.App) {
	// Releases are immutable: rollback republishes the old snapshot
	// as a NEW row (todo 8). Deny every update path.
	releasesImmutable := errors.New("releases are immutable: publish a new release instead")
	app.OnRecordUpdate("releases").BindFunc(func(e *core.RecordEvent) error {
		return releasesImmutable
	})
	app.OnRecordValidate("releases").BindFunc(func(e *core.RecordEvent) error {
		if e.Record.IsNew() {
			return e.Next()
		}
		return releasesImmutable
	})

	// Flag key shape on create and update.
	app.OnRecordCreate("flags").BindFunc(func(e *core.RecordEvent) error {
		if err := checkFlagKey(e.Record.GetString("key")); err != nil {
			return err
		}
		n, err := countProjectFlags(e.App, e.Record.GetString("project"))
		if err != nil {
			return err
		}
		if n >= maxFlagsPerProject {
			return errors.New("flag limit reached: max 1000 flags per project")
		}
		return e.Next()
	})
	app.OnRecordUpdate("flags").BindFunc(func(e *core.RecordEvent) error {
		if err := checkFlagKey(e.Record.GetString("key")); err != nil {
			return err
		}
		return e.Next()
	})

	// Environment slugs are unique per project (composite uniqueness on
	// (project, slug): two projects may each own "dev", but one project
	// may not own it twice). Enforced in hooks — not a DB unique index —
	// so every write path is covered (API, dashboard, server-side saves;
	// superusers bypass rules but hooks still fire) and pre-existing
	// duplicate rows never break migration. O(n) scan over environments
	// is fine at ConfigNest scale (tens of rows).
	checkEnvSlug := func(app core.App, rec *core.Record) error {
		recs, err := app.FindAllRecords("environments")
		if err != nil {
			return err
		}
		rows := make([]envresolve.EnvRow, 0, len(recs))
		for _, r := range recs {
			rows = append(rows, envresolve.EnvRow{
				ID:      r.Id,
				Slug:    r.GetString("slug"),
				Project: r.GetString("project"),
			})
		}
		if envresolve.SlugTaken(rows, rec.GetString("project"), rec.GetString("slug"), rec.Id) {
			// ApiError (not a plain error) so the data-API 400 carries
			// the message verbatim instead of "Failed to create record."
			return apis.NewBadRequestError("environment slug already exists for this project", nil)
		}
		return nil
	}
	app.OnRecordCreate("environments").BindFunc(func(e *core.RecordEvent) error {
		if err := checkEnvSlug(e.App, e.Record); err != nil {
			return err
		}
		return e.Next()
	})
	app.OnRecordUpdate("environments").BindFunc(func(e *core.RecordEvent) error {
		if err := checkEnvSlug(e.App, e.Record); err != nil {
			return err
		}
		return e.Next()
	})
}

func main() {
	app := pocketbase.New()

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: true,
	})

	registerConfignestHooks(app)

	ingest.EnableWAL(app)

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// registers new "GET /hello" route
		se.Router.GET("/hello", func(re *core.RequestEvent) error {
			return re.String(200, "Hello world!")
		})

		// INGEST (todo 9) — analytics events write path.
		ingest.Register(se)

		// RELEASES (todo 8) — versioning heart: publish immutable +
		// rollback-as-new with baseVersion guard.
		releases.Register(se)

		// FETCH (todo 10) — SDK delivery: latest release + evaluation.
		// Replaces the todo-2 spike GET /config (removed from
		// spike_probe.go); spike /stream + /spike/publish stay for T14.
		fetch.Register(se)

		// STATS (todo 11) — admin read-model over ingested events.
		stats.Register(se)

		// PURGE (todo 15) — retention: 30d raw events, 90d daily rollups.
		purge.Register(se)

		// SPIKE (todo 2) — skeleton candidate, not production
		registerSpikeRoutes(se)

		// ADMIN UI SHELL (todo 13) — static files only; dynamic data via
		// fetch calls from pb_public/app.js. Mounted LAST on /{path...}
		// with indexFallback so /api/* (registered above) keeps
		// precedence: unknown /api/* paths still answer 404 JSON, while
		// unknown non-API paths fall back to index.html. Serve runs from
		// server/ so ./pb_public resolves (cwd=server required).
		se.Router.GET("/{path...}", apis.Static(os.DirFS("./pb_public"), true))

		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
