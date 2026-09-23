// Package releases wires the publish + rollback routes, both superuser-only.
// No SDK-key auth here by design — admin routes never accept
// X-ConfigWire-Key (a request carrying only an SDK key has no superuser
// token, so RequireSuperuserAuth answers 401).
package releases

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"

	"configwire/envresolve"
	"configwire/security"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// writeMu serializes releases writes process-wide (single binary).
// Publish and rollback both assign version=max+1 per env; the mutex turns
// a concurrent double-publish with the same baseVersion into exactly one
// 200 + one 409 (the loser re-reads the fresh max under the lock), so
// versions are never oversold. Cross-process serialization is out of
// scope: ConfigWire runs as one binary.
var writeMu sync.Mutex

// Register mounts the admin releases routes. All routes bind
// RequireSuperuserAuth: missing/invalid auth -> 401, SDK keys -> 401
// (they are not superuser tokens), non-superuser auth -> 403.
func Register(se *core.ServeEvent) {
	se.Router.POST("/api/v1/admin/env/{env}/publish", postPublish).Bind(apis.RequireSuperuserAuth())
	se.Router.POST("/api/v1/admin/releases/{version}/rollback", postRollback).Bind(apis.RequireSuperuserAuth())
	se.Router.POST("/api/v1/admin/env/{env}/releases/{version}/rollback", postRollbackEnv).Bind(apis.RequireSuperuserAuth())
}

// baseVersion is required and must equal the env's current max version
// (0 on first publish). A baseVersion sent as a JSON string fails body
// decoding -> 400.
type publishRequest struct {
	Note        string `json:"note"`
	BaseVersion int    `json:"baseVersion"`
}

type rollbackRequest struct {
	Note string `json:"note"`
}

func callerAuthor(re *core.RequestEvent) string {
	if re.Auth != nil {
		if email := re.Auth.GetString("email"); email != "" {
			return email
		}
		if re.Auth.Id != "" {
			return re.Auth.Id
		}
	}
	return "superuser"
}

// Empty bodies -> 400 (BindBody alone would silently return nil);
// malformed JSON or wrong field types -> 400.
func decodeBody(re *core.RequestEvent, dst any) error {
	if re.Request.ContentLength == 0 {
		return re.BadRequestError("empty body: expected a JSON object.", nil)
	}
	if err := re.BindBody(dst); err != nil {
		return re.BadRequestError("malformed JSON body: "+err.Error(), nil)
	}
	return nil
}

// postPublish handles POST /api/v1/admin/env/:env/publish.
// Order: 401 (superuser, via middleware) -> 404 (unknown env slug) ->
// 400 (ambiguous slug without ?project=) -> 409 (stale baseVersion, NO
// write) -> 400 (validation) -> 200.
// The snapshot is built SERVER-SIDE from the live collections; the
// client only sends {note, baseVersion}.
func postPublish(re *core.RequestEvent) error {
	security.SetHeaders(re)
	var req publishRequest
	if err := decodeBody(re, &req); err != nil {
		return err
	}
	slug := re.Request.PathValue("env")
	// ?project= disambiguates a slug shared by several projects;
	// unambiguous slugs keep working without it.
	env, err := envresolve.Resolve(re.App, slug, re.Request.URL.Query().Get("project"))
	if err != nil {
		return envresolve.ToRequestError(re, err)
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	currentMax, err := MaxVersionForEnv(re.App, env.Id)
	if err != nil {
		return err
	}
	if !CheckBaseVersion(req.BaseVersion, currentMax) {
		return re.JSON(http.StatusConflict, map[string]any{
			"message":        "Stale baseVersion: a newer release exists.",
			"status":         http.StatusConflict,
			"currentVersion": currentMax,
		})
	}

	snap, err := BuildSnapshot(re.App, env)
	if err != nil {
		return err
	}
	if err := ValidateSnapshot(snap); err != nil {
		return re.BadRequestError(err.Error(), nil)
	}
	canonical, err := MarshalCanonical(snap)
	if err != nil {
		return err
	}
	version := NextVersion(currentMax)
	etag := EtagFor(version, canonical)

	collection, err := re.App.FindCollectionByNameOrId("releases")
	if err != nil {
		return err
	}
	rec := core.NewRecord(collection)
	rec.Set("version", version)
	rec.Set("etag", etag)
	// Canonical JSON text starts with '{' and is valid JSON, so the
	// JSON field stores it verbatim (see field_json.go PrepareValue):
	// the DB bytes equal the etag input bytes.
	rec.Set("snapshot", string(canonical))
	rec.Set("author", callerAuthor(re))
	rec.Set("note", req.Note)
	rec.Set("env", env.Id)
	if err := re.App.Save(rec); err != nil {
		return err
	}

	// Structured audit line (who/version/etag/note). Std log (not the PB
	// slog logger, whose Info level is silent by default) so the line is
	// guaranteed in server output; no new collection.
	log.Printf("releases: published env=%s version=%d etag=%s author=%s note=%q",
		slug, version, etag, callerAuthor(re), req.Note)

	return re.JSON(http.StatusOK, map[string]any{"version": version, "etag": etag})
}

// rawSnapshotBytes extracts the stored snapshot bytes verbatim so a
// rollback row is byte-identical to its source.
func rawSnapshotBytes(v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return nil, errors.New("source release has no snapshot")
	case string:
		return []byte(t), nil
	case json.RawMessage:
		return []byte(t), nil
	case []byte:
		return t, nil
	case interface{ String() string }:
		// Covers types.JSONRaw (PocketBase's JSON storage type).
		return []byte(t.String()), nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// ReleaseRow is the minimal release projection rollback selection
// needs — DB-free so source selection is unit-testable on fake rows.
type ReleaseRow struct {
	EnvID   string
	Version int
}

var (
	// ErrReleaseNotFound reports no release row matched the requested version.
	ErrReleaseNotFound = errors.New("unknown release version")
	// ErrReleaseAmbiguous reports several rows share one version, so RollbackPick refuses to guess.
	ErrReleaseAmbiguous = errors.New("ambiguous release version")
)

// RollbackPick selects the rollback source row purely (no I/O).
// envID == "" is the legacy global route: candidates share the version
// across envs (none -> not found, 2+ -> ambiguous). Otherwise (the
// env-scoped route) candidates share version AND env, so duplicate
// versions across envs resolve deterministically (none -> not found;
// 2+ in one env -> ambiguous, defensive: the write mutex never issues
// those). O(n) over releases — same discipline as MaxVersionForEnv.
func RollbackPick(rows []ReleaseRow, version int, envID string) (int, error) {
	best := -1
	for i, r := range rows {
		if r.Version != version {
			continue
		}
		if envID != "" && r.EnvID != envID {
			continue
		}
		if best != -1 {
			return -1, ErrReleaseAmbiguous
		}
		best = i
	}
	if best == -1 {
		return -1, ErrReleaseNotFound
	}
	return best, nil
}

// releaseRows projects release records into ReleaseRows in table order
// so a RollbackPick index maps back onto the same record.
func releaseRows(recs []*core.Record) []ReleaseRow {
	rows := make([]ReleaseRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, ReleaseRow{EnvID: r.GetString("env"), Version: r.GetInt("version")})
	}
	return rows
}

// rollbackToNewRow copies src's snapshot bytes verbatim into a NEW row
// with version=max+1 in the source row's env and a fresh etag (the
// version is part of the etag input, so identical bytes still hash
// differently). Releases rows stay immutable: no UPDATE path exists.
func rollbackToNewRow(app core.App, src *core.Record, note, author string, fromVersion int) (int, string, error) {
	snapBytes, err := rawSnapshotBytes(src.GetRaw("snapshot"))
	if err != nil {
		return 0, "", err
	}
	if !json.Valid(snapBytes) {
		return 0, "", errors.New("releases: source snapshot is corrupt")
	}

	envID := src.GetString("env")
	currentMax, err := MaxVersionForEnv(app, envID)
	if err != nil {
		return 0, "", err
	}
	version := NextVersion(currentMax)
	etag := EtagFor(version, snapBytes)

	collection, err := app.FindCollectionByNameOrId("releases")
	if err != nil {
		return 0, "", err
	}
	rec := core.NewRecord(collection)
	rec.Set("version", version)
	rec.Set("etag", etag)
	rec.Set("snapshot", string(snapBytes))
	rec.Set("author", author)
	rec.Set("note", note)
	rec.Set("env", envID)
	if err := app.Save(rec); err != nil {
		return 0, "", err
	}

	log.Printf("releases: rolled back fromVersion=%d version=%d etag=%s author=%s note=%q",
		fromVersion, version, etag, author, note)

	return version, etag, nil
}

// decodeRollbackNote reads the optional {note} body shared by both
// rollback routes: an empty body means "no note"; only malformed
// (non-empty, non-JSON) bodies are 400.
func decodeRollbackNote(re *core.RequestEvent, req *rollbackRequest) error {
	if re.Request.ContentLength != 0 {
		if err := re.BindBody(req); err != nil {
			return re.BadRequestError("malformed JSON body: "+err.Error(), nil)
		}
	}
	return nil
}

func parseRollbackVersion(re *core.RequestEvent) (int, error) {
	n, err := strconv.Atoi(re.Request.PathValue("version"))
	if err != nil {
		return 0, re.BadRequestError("invalid version: must be an integer.", nil)
	}
	return n, nil
}

// postRollback handles POST /api/v1/admin/releases/:version/rollback
// (legacy global route, behavior preserved). The source row is the one
// row with that version across envs.
// Order: 401 (middleware) -> 400 (bad version / ambiguous only when
// the version truly exists in 2+ envs) -> 404 (unknown version) -> 200.
// No UPDATE path exists: releases rows stay immutable (the main.go
// hooks deny updates).
func postRollback(re *core.RequestEvent) error {
	security.SetHeaders(re)
	var req rollbackRequest
	if err := decodeRollbackNote(re, &req); err != nil {
		return err
	}
	n, err := parseRollbackVersion(re)
	if err != nil {
		return err
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	recs, err := re.App.FindAllRecords("releases")
	if err != nil {
		return err
	}
	idx, rerr := RollbackPick(releaseRows(recs), n, "")
	if rerr != nil {
		if errors.Is(rerr, ErrReleaseAmbiguous) {
			return re.BadRequestError("ambiguous version: multiple envs share this version.", nil)
		}
		return re.NotFoundError("Unknown release version.", nil)
	}

	version, etag, err := rollbackToNewRow(re.App, recs[idx], req.Note, callerAuthor(re), n)
	if err != nil {
		return err
	}

	return re.JSON(http.StatusOK, map[string]any{"version": version, "etag": etag})
}

// postRollbackEnv handles POST
// /api/v1/admin/env/:env/releases/:version/rollback (env-scoped route).
// The source row is selected by (env, version), so duplicate versions
// across envs roll back deterministically. The env slug resolves with
// the Fix2 qualifier (?project= for shared slugs).
// Order: 401 (middleware) -> 404 (unknown env / version not in this
// env) -> 400 (bad version) -> 200.
func postRollbackEnv(re *core.RequestEvent) error {
	security.SetHeaders(re)
	var req rollbackRequest
	if err := decodeRollbackNote(re, &req); err != nil {
		return err
	}
	slug := re.Request.PathValue("env")
	env, err := envresolve.Resolve(re.App, slug, re.Request.URL.Query().Get("project"))
	if err != nil {
		return envresolve.ToRequestError(re, err)
	}
	n, err := parseRollbackVersion(re)
	if err != nil {
		return err
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	recs, err := re.App.FindAllRecords("releases")
	if err != nil {
		return err
	}
	idx, rerr := RollbackPick(releaseRows(recs), n, env.Id)
	if rerr != nil {
		if errors.Is(rerr, ErrReleaseAmbiguous) {
			return re.BadRequestError("ambiguous version: duplicate rows for this version in this env.", nil)
		}
		return re.NotFoundError("Unknown release version.", nil)
	}

	version, etag, err := rollbackToNewRow(re.App, recs[idx], req.Note, callerAuthor(re), n)
	if err != nil {
		return err
	}

	return re.JSON(http.StatusOK, map[string]any{"version": version, "etag": etag})
}
