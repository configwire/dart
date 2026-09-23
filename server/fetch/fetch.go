// Package fetch implements the SDK delivery endpoint.
//
//	GET /api/v1/env/:env/config?platform=&appVersion=&locale=&country=&uid=&attrs=<json>&exp=<status>
//
// FETCH CONTRACT:
//   - Auth: X-ConfigWire-Key via ingest.RequireSDKKey reuse. Unknown, missing,
//     revoked, or env-mismatched keys -> 401. SDK keys and userIDs are never
//     logged (hashes only); evaluation cost is O(flags + rules) per request.
//   - Env: resolved deterministically from the key's env (the key's env
//     IS the env, so a slug shared by several projects can never
//     misroute). Unknown slug -> 404. Order is 401 (key) -> 404/400
//     (env) -> 401 (key not scoped to this env) -> 304/200.
//   - Empty release: an env with NO release row yet -> 200 with
//     {"version":0,"etag":"none","values":{},"variants":{},"fetchAt":...}.
//     Never 404, so fresh envs boot clean. values is EMPTY (not flag
//     defaults): the admin has published nothing, so there is nothing to
//     serve; SDKs fall back to their in-app defaults.
//   - Latest release: the row with max version for the env. Its STORED etag
//     and STORED snapshot bytes are served VERBATIM (never recomputed —
//     the etag input was the canonical pre-save bytes, see
//     releases.EtagFor). Response headers carry ETag: <etag>.
//   - 304: If-None-Match == stored etag (EXACT string match only; a "W/"-
//     prefixed value does NOT match) -> 304 with an empty body.
//   - Evaluation: for each snapshot flag, eval.Flag/Rules are rebuilt from
//     the stored snapshot and evaluated with eval.Evaluate; flags targeted
//     by a RUNNING experiment are overlaid via eval.EvaluateWithExperiment
//     (variant recorded in the "variants" map). Flags with no experiment
//     targeting them have NO variants entry. Response:
//     {"version","etag","values":{key:typed},"variants":{key:variant},
//     "fetchAt":rfc3339}. A flag whose snapshot default has an unexpected
//     shape is served as-is (publish-time validation already coerced it).
//   - Context: uid->UserID (absent -> anonymous: rules still evaluate,
//     Assign falls back to DefaultVariant), platform/appVersion/locale/
//     country passed through (appVersion=not-semver -> condition false ->
//     defaults fallthrough, still 200, never 400), attrs=<json> parsed as a
//     flat map for custom.* conditions.
//   - attrs error policy: malformed attrs (invalid JSON, or valid JSON that
//     is not an object) -> 400 documented; attrs longer than 8192 bytes ->
//     414 documented. Missing attrs -> empty custom map (anonymous attrs).
//   - exp override: when the ?exp= query param is present it OVERRIDES the
//     stored experiment status passed to EvaluateWithExperiment (e.g.
//     exp=draft forces the control path for QA); when absent each
//     experiment's own stored status is used, so only stored-"running"
//     experiments overlay.
//   - Gzip: route binds apis.Gzip(), so Accept-Encoding: gzip responses
//     carry Content-Encoding: gzip.
//   - CORS: every fetch response (including 304) sets
//     Access-Control-Allow-Origin, default "*" for Flutter/web dev. Restrict
//     in production with env CONFIGWIRE_CORS_ORIGIN=https://app.example.com
//     (single origin, no paid infra, simple header). An OPTIONS preflight
//     route answers 204 with Allow-Origin/Methods/Headers.
package fetch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"configwire/envresolve"
	"configwire/eval"
	"configwire/ingest"
	"configwire/releases"
	"configwire/security"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// MaxAttrsBytes bounds the raw ?attrs= parameter; longer -> 414.
const MaxAttrsBytes = 8192

// emptyReleaseEtag is served when an env has no release row yet.
const emptyReleaseEtag = "none"

const allowHeaders = "X-ConfigWire-Key, If-None-Match"

func corsOrigin() string {
	if v := os.Getenv("CONFIGWIRE_CORS_ORIGIN"); v != "" {
		return v
	}
	return "*"
}

// Register mounts the SDK delivery endpoint with gzip encoding.
func Register(se *core.ServeEvent) {
	se.Router.GET("/api/v1/env/{env}/config", getConfig).Bind(apis.Gzip())
	se.Router.OPTIONS("/api/v1/env/{env}/config", optionsConfig)
}

// optionsConfig answers the CORS preflight for fetch (no auth: preflights
// carry no credentials by spec).
func optionsConfig(re *core.RequestEvent) error {
	security.SetHeaders(re)
	h := re.Response.Header()
	h.Set("Access-Control-Allow-Origin", corsOrigin())
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", allowHeaders)
	h.Set("Access-Control-Max-Age", "86400")
	return re.NoContent(http.StatusNoContent)
}

// BuildContext maps fetch query params onto an eval.Context purely (no I/O),
// so it is unit-testable. uid->UserID; platform/appVersion/locale/country
// pass through untouched (invalid values evaluate to false downstream, never
// 400 here); attrs must be absent or a JSON object (flat map for custom.*);
// malformed attrs or attrs over MaxAttrsBytes -> error (caller maps to
// 400/414). Missing uid/attrs -> anonymous context (empty UserID/customs).
func BuildContext(q url.Values) (eval.Context, error) {
	ctx := eval.Context{
		UserID:      q.Get("uid"),
		Platform:    q.Get("platform"),
		AppVersion:  q.Get("appVersion"),
		Locale:      q.Get("locale"),
		Country:     q.Get("country"),
		CustomAttrs: map[string]any{},
	}
	raw := q.Get("attrs")
	if raw == "" {
		return ctx, nil
	}
	if len(raw) > MaxAttrsBytes {
		return ctx, fmt.Errorf("attrs too large: max %d bytes", MaxAttrsBytes)
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return ctx, fmt.Errorf("malformed attrs: not a JSON object")
	}
	if m == nil {
		return ctx, fmt.Errorf("malformed attrs: not a JSON object")
	}
	ctx.CustomAttrs = m
	return ctx, nil
}

// EtagMatches reports whether an If-None-Match header exactly equals the
// stored etag. Exact match only: weak ("W/"-prefixed) or quoted values do
// NOT match (stored etags are bare hex, never quoted).
func EtagMatches(ifNoneMatch, etag string) bool {
	return ifNoneMatch != "" && ifNoneMatch == etag
}

// EvaluateSnapshot evaluates every flag in a stored snapshot for ctx purely
// (no I/O), so it is unit-testable with a fake latest-release input.
// Returns values (typed per flag) and variants (one entry per flag targeted
// by at least one experiment row; the assigned variant name, including the
// DefaultVariant fallthrough for non-running/anonymous paths).
// statusOverride, when non-empty, replaces each experiment's stored status
// (the ?exp= query param); otherwise the stored status is used.
func EvaluateSnapshot(snap releases.Snapshot, ctx eval.Context, statusOverride string) (map[string]any, map[string]string) {
	values := make(map[string]any, len(snap.Flags))
	variants := map[string]string{}
	for _, sf := range snap.Flags {
		flag := eval.Flag{Key: sf.Key, Type: sf.Type, Default: sf.Default}
		rules := make([]eval.Rule, 0, len(sf.Rules))
		for i, sr := range sf.Rules {
			rules = append(rules, decodeRule(sr, i))
		}
		exps := expsForFlag(snap.Experiments, sf.Key)
		if len(exps) == 0 {
			values[sf.Key] = eval.Evaluate(flag, rules, ctx)
			continue
		}
		// First matching experiment row wins (snapshot order is by id,
		// deterministic from the publish path).
		se := exps[0]
		status := se.Status
		if statusOverride != "" {
			status = statusOverride
		}
		v, variant := eval.EvaluateWithExperiment(flag, rules, ctx, decodeExperiment(se), status)
		values[sf.Key] = v
		variants[sf.Key] = variant
	}
	return values, variants
}

// decodeRule converts one stored snapshot rule to an eval.Rule. The stored
// condition is a single object; it is wrapped as one eval.Condition. An
// undecodable/null condition decodes to zero conditions, which vacuously
// matches — the same fallthrough eval applies to any invalid input.
func decodeRule(sr releases.SnapshotRule, i int) eval.Rule {
	r := eval.Rule{ID: fmt.Sprintf("rule-%d", i)}
	if sr.Condition == nil {
		return r
	}
	raw, err := json.Marshal(sr.Condition)
	if err != nil {
		return r
	}
	var c eval.Condition
	if err := json.Unmarshal(raw, &c); err != nil {
		return r
	}
	r.Conditions = []eval.Condition{c}
	r.Value = sr.Value
	return r
}

func expsForFlag(all []releases.SnapshotExperiment, key string) []releases.SnapshotExperiment {
	var out []releases.SnapshotExperiment
	for _, e := range all {
		if e.Flag == key {
			out = append(out, e)
		}
	}
	return out
}

// decodeExperiment converts one stored snapshot experiment to an
// eval.Experiment. The snapshot carries no defaultVariant field, so
// DefaultVariant is "" (Assign falls back to it for empty users or bad
// weights). Undecodable variants -> empty table -> control-path fallthrough.
func decodeExperiment(se releases.SnapshotExperiment) eval.Experiment {
	exp := eval.Experiment{ID: se.ID, Seed: se.Seed}
	if se.Variants == nil {
		return exp
	}
	raw, err := json.Marshal(se.Variants)
	if err != nil {
		return exp
	}
	var variants []eval.Variant
	if err := json.Unmarshal(raw, &variants); err != nil {
		return exp
	}
	exp.Variants = variants
	return exp
}

// rawBytes extracts stored snapshot bytes verbatim from a releases record
// field (covers the PocketBase JSON storage shapes).
func rawBytes(v any) ([]byte, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case string:
		return []byte(t), true
	case json.RawMessage:
		return []byte(t), true
	case []byte:
		return t, true
	case interface{ String() string }:
		return []byte(t.String()), true
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, false
		}
		return b, true
	}
}

// latestRelease returns the env's max-version release row (nil when the env
// has no release yet). Latest-release-per-env = max version row.
func latestRelease(app core.App, envID string) (*core.Record, error) {
	recs, err := app.FindAllRecords("releases")
	if err != nil {
		return nil, err
	}
	var best *core.Record
	bestV := -1
	for _, r := range recs {
		if r.GetString("env") != envID {
			continue
		}
		if v := r.GetInt("version"); v > bestV {
			bestV, best = v, r
		}
	}
	return best, nil
}

func setCORS(re *core.RequestEvent) {
	re.Response.Header().Set("Access-Control-Allow-Origin", corsOrigin())
}

// getConfig handles GET /api/v1/env/:env/config.
// Order: CORS stamp -> 401 (key) -> 404 (env) -> 401 (env scope) ->
// 400/414 (attrs) -> 200 empty (no release) -> 304 (etag match) -> 200.
func getConfig(re *core.RequestEvent) error {
	security.SetHeaders(re)
	setCORS(re)
	key, err := ingest.RequireSDKKey(re)
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

	ctx, cerr := BuildContext(re.Request.URL.Query())
	if cerr != nil {
		if len(re.Request.URL.Query().Get("attrs")) > MaxAttrsBytes {
			return re.JSON(http.StatusRequestURITooLong, map[string]any{"message": cerr.Error(), "status": http.StatusRequestURITooLong})
		}
		return re.BadRequestError(cerr.Error(), nil)
	}

	rel, err := latestRelease(re.App, env.Id)
	if err != nil {
		return err
	}
	if rel == nil {
		return re.JSON(http.StatusOK, map[string]any{
			"version":  0,
			"etag":     emptyReleaseEtag,
			"values":   map[string]any{},
			"variants": map[string]string{},
			"fetchAt":  time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	etag := rel.GetString("etag")
	re.Response.Header().Set("ETag", etag)
	if EtagMatches(re.Request.Header.Get("If-None-Match"), etag) {
		return re.NoContent(http.StatusNotModified)
	}

	snapBytes, ok := rawBytes(rel.GetRaw("snapshot"))
	if !ok || !json.Valid(snapBytes) {
		return re.JSON(http.StatusInternalServerError, map[string]any{"message": "Release snapshot is corrupt.", "status": 500})
	}
	var snap releases.Snapshot
	if err := json.Unmarshal(snapBytes, &snap); err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]any{"message": "Release snapshot is corrupt.", "status": 500})
	}

	values, variants := EvaluateSnapshot(snap, ctx, re.Request.URL.Query().Get("exp"))
	return re.JSON(http.StatusOK, map[string]any{
		"version":  rel.GetInt("version"),
		"etag":     etag,
		"values":   values,
		"variants": variants,
		"fetchAt":  time.Now().UTC().Format(time.RFC3339Nano),
	})
}
