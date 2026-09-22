// Package releases implements the ConfigNest versioning heart (plan todo 8).
//
// FROZEN SNAPSHOT SCHEMA (T10 contract — no deviations):
//
//		{"flags":[{"key","type","default","group","rules":[{"priority","condition","value}]}],
//		 "experiments":[{"id","flag","seed","variants","status"}]}
//
//	  - flags/rules are scoped to the publish target env's project
//	    (env.project relation). flags sorted by key, rules by priority
//	    ascending so the canonical bytes (and therefore the etag) are
//	    deterministic.
//	  - experiments are scoped to the publish target env's project via
//	    their linked flag: rows targeting a flag of another project are
//	    EXCLUDED (no cross-project leakage into snapshots, and therefore
//	    into fetch overlays, stats, or ingest). Untargeted rows (flag
//	    relation unset or dangling) are included with flag "" (legacy
//	    shape). experiment.flag is the linked flag's KEY ("" when the
//	    relation is unset/unresolvable), experiment.id is the record id.
//	  - flag.group is the linked groups record id ("" when unset).
//
// ETAG RULE (T10/T13/T17 contract):
//
//	etag = hex(sha256(version + ":" + snapshot))[:16]
//
// where snapshot is the canonical server-marshaled snapshot bytes
// (encoding/json over the structs below). The version is part of the
// hash input, so a rollback row carries a FRESH etag even though its
// snapshot bytes are byte-identical to the source row.
//
// 409 SEMANTICS: publish carries {note, baseVersion}; baseVersion must
// equal the env's current max version (0 when no release exists yet).
// Mismatch -> 409 {"currentVersion":N} with NO write.
//
// SERVER-SIDE BUILD RATIONALE: the snapshot is assembled from the live
// flags/rules/experiments collections inside the handler, never accepted
// from the client. This reuses the key-shape/cap/type hooks on a dry
// assemble (validation runs over live rows; no writes happen except the
// single releases row), so a malicious or stale client cannot ship a
// snapshot that the collections themselves would reject.
//
// AUDIT: structured server log line per publish/rollback
// (who/version/etag/note). No new collection — events kinds are
// fetch|exposure only.
package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"confignest/eval"

	"github.com/pocketbase/pocketbase/core"
)

// Flag key shape mirrors the main.go hook (todo 5) so the dry assemble
// rejects exactly what the write path would reject.
var flagKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	maxFlagKeyLength   = 128
	maxFlagsPerProject = 1000
)

// Flag types accepted in a snapshot (mirrors the flags.type select).
var validFlagTypes = map[string]bool{
	eval.TypeNumber: true,
	eval.TypeString: true,
	eval.TypeBool:   true,
	eval.TypeJSON:   true,
}

// SnapshotRule is one ordered conditional value inside a flag.
type SnapshotRule struct {
	Priority  int `json:"priority"`
	Condition any `json:"condition"`
	Value     any `json:"value"`
}

// SnapshotFlag is one flag with its ordered rules.
type SnapshotFlag struct {
	Key     string         `json:"key"`
	Type    string         `json:"type"`
	Default any            `json:"default"`
	Group   string         `json:"group"`
	Rules   []SnapshotRule `json:"rules"`
}

// SnapshotExperiment is one experiment row, included as-is.
type SnapshotExperiment struct {
	ID       string `json:"id"`
	Flag     string `json:"flag"`
	Seed     string `json:"seed"`
	Variants any    `json:"variants"`
	Status   string `json:"status"`
}

// Snapshot is the FROZEN publish payload stored on releases.snapshot.
type Snapshot struct {
	Flags       []SnapshotFlag       `json:"flags"`
	Experiments []SnapshotExperiment `json:"experiments"`
}

// EtagFor derives the release etag from the new version number and the
// canonical snapshot bytes: hex(sha256(version+":"+snapshot))[:16].
// Same inputs -> same etag; any version bump -> fresh etag even when the
// snapshot bytes are identical (rollback requirement).
func EtagFor(version int, snapshot []byte) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", version, snapshot)))
	return hex.EncodeToString(sum[:])[:16]
}

// NextVersion returns the version number for a new release row given the
// env's current max version (0 when no release exists yet).
func NextVersion(currentMax int) int {
	return currentMax + 1
}

// CheckBaseVersion reports whether a publish request's baseVersion matches
// the env's current max version. Mismatch -> caller returns 409 with
// currentVersion and performs NO write.
func CheckBaseVersion(baseVersion, currentMax int) bool {
	return baseVersion == currentMax
}

// MarshalCanonical renders a snapshot to its canonical bytes (struct field
// order + sorted flags/rules from the builder). The etag is computed over
// exactly these bytes.
func MarshalCanonical(snap Snapshot) ([]byte, error) {
	if snap.Flags == nil {
		snap.Flags = []SnapshotFlag{}
	}
	if snap.Experiments == nil {
		snap.Experiments = []SnapshotExperiment{}
	}
	return json.Marshal(snap)
}

// jsonAny normalizes a PocketBase record field value (types.JSONRaw,
// string, []byte, or already-decoded any) into plain Go values.
func jsonAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		var out any
		if err := json.Unmarshal(t, &out); err != nil {
			return string(t)
		}
		return out
	case string:
		var out any
		if err := json.Unmarshal([]byte(t), &out); err != nil {
			return t
		}
		return out
	case []byte:
		var out any
		if err := json.Unmarshal(t, &out); err != nil {
			return string(t)
		}
		return out
	default:
		// PocketBase JSON fields surface as types.JSONRaw which is a
		// []byte kind underneath but a distinct named type; handle it
		// via a marshal round-trip.
		raw, err := json.Marshal(t)
		if err != nil {
			return t
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return t
		}
		return out
	}
}

// BuildSnapshot assembles the FROZEN snapshot for env from the live
// collections (server-side only — never from client input). Flags,
// rules, and experiments are scoped to env's project; experiments
// targeting another project's flags are excluded (no cross-project
// leakage). Returned flags are sorted by key, rules by priority
// ascending, experiments by id.
func BuildSnapshot(app core.App, env *core.Record) (Snapshot, error) {
	snap := Snapshot{Flags: []SnapshotFlag{}, Experiments: []SnapshotExperiment{}}
	projectID := env.GetString("project")

	flagRecs, err := app.FindAllRecords("flags")
	if err != nil {
		return snap, err
	}
	// Index flags of this project by record id.
	byID := map[string]*core.Record{}
	flagKeys := map[string]string{} // flag record id -> flag key
	for _, fr := range flagRecs {
		if fr.GetString("project") != projectID {
			continue
		}
		byID[fr.Id] = fr
		flagKeys[fr.Id] = fr.GetString("key")
	}

	ruleRecs, err := app.FindAllRecords("rules")
	if err != nil {
		return snap, err
	}
	rulesByFlag := map[string][]SnapshotRule{}
	for _, rr := range ruleRecs {
		flagID := rr.GetString("flag")
		if _, ok := byID[flagID]; !ok {
			continue // rule on a flag outside this env's project
		}
		rulesByFlag[flagID] = append(rulesByFlag[flagID], SnapshotRule{
			Priority:  rr.GetInt("priority"),
			Condition: jsonAny(rr.Get("condition")),
			Value:     jsonAny(rr.Get("value")),
		})
	}

	for id, fr := range byID {
		rules := rulesByFlag[id]
		if rules == nil {
			rules = []SnapshotRule{}
		}
		sort.SliceStable(rules, func(i, j int) bool {
			return rules[i].Priority < rules[j].Priority
		})
		snap.Flags = append(snap.Flags, SnapshotFlag{
			Key:     flagKeys[id],
			Type:    fr.GetString("type"),
			Default: jsonAny(fr.Get("defaultValue")),
			Group:   fr.GetString("group"),
			Rules:   rules,
		})
	}
	sort.SliceStable(snap.Flags, func(i, j int) bool {
		return snap.Flags[i].Key < snap.Flags[j].Key
	})

	expRecs, err := app.FindAllRecords("experiments")
	if err != nil {
		return snap, err
	}
	flagProjects := make(map[string]string, len(flagRecs))
	for _, fr := range flagRecs {
		flagProjects[fr.Id] = fr.GetString("project")
	}
	for _, er := range expRecs {
		flagKey, include := ResolveExperimentFlag(projectID, er.GetString("flag"), flagProjects, flagKeys)
		if !include {
			continue
		}
		snap.Experiments = append(snap.Experiments, SnapshotExperiment{
			ID:       er.Id,
			Flag:     flagKey,
			Seed:     er.GetString("seed"),
			Variants: jsonAny(er.Get("variants")),
			Status:   er.GetString("status"),
		})
	}
	sort.SliceStable(snap.Experiments, func(i, j int) bool {
		return snap.Experiments[i].ID < snap.Experiments[j].ID
	})

	return snap, nil
}

// ResolveExperimentFlag decides whether one experiment row belongs in
// the snapshot for projectID, purely (no I/O) so scoping is
// unit-testable. projects/keys index every flag by record id (built
// once per BuildSnapshot from the already-loaded flags scan, so no
// extra queries). Untargeted rows (flagID "") or dangling relations
// are included with key "" (legacy shape); rows whose flag lives in
// another project are excluded — that exclusion is what keeps one
// project's experiments out of another project's snapshots.
func ResolveExperimentFlag(projectID, flagID string, projects, keys map[string]string) (key string, include bool) {
	if flagID == "" {
		return "", true
	}
	p, ok := projects[flagID]
	if !ok {
		return "", true
	}
	if p != projectID {
		return "", false
	}
	return keys[flagID], true
}

// coerceToType mirrors eval's rule-value coercion (eval.coerce is private):
// numbers accept any Go numeric, strings/bools require their exact type,
// json accepts objects (maps) and arrays (slices) only.
func coerceToType(v any, t string) bool {
	switch t {
	case eval.TypeNumber:
		return isNumber(v)
	case eval.TypeString:
		_, ok := v.(string)
		return ok
	case eval.TypeBool:
		_, ok := v.(bool)
		return ok
	case eval.TypeJSON:
		return isJSONObject(v) || isJSONArray(v)
	default:
		return false
	}
}

func isNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return true
	default:
		return false
	}
}

func isJSONObject(v any) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(map[string]any); ok {
		return true
	}
	return false
}

func isJSONArray(v any) bool {
	if v == nil {
		return false
	}
	switch v.(type) {
	case []any, []map[string]any:
		return true
	case string, []byte:
		return false
	default:
		return false
	}
}

// Condition op allowlists mirror server/eval/eval.go EXACTLY (the frozen
// contract header + the matchCondition switch): adding an op to eval
// requires adding it here, or publish keeps rejecting it.
var (
	stringFieldOps  = map[string]bool{"==": true, "!=": true, "contains": true, "regex": true}
	versionFieldOps = map[string]bool{
		"<": true, "<=": true, "==": true, "!=": true,
		">=": true, ">": true, "contains": true, "regex": true,
	}
	percentileOps  = map[string]bool{"<=": true, "between": true}
	customFieldOps = map[string]bool{
		"==": true, "!=": true, "<": true, "<=": true,
		">": true, ">=": true, "contains": true, "regex": true,
	}
)

// validateCondition rejects rule conditions that eval would silently treat
// as false (unknown field/op, non-object shape, missing value key).
// Without this, a corrupt condition decodes to zero conditions downstream
// and vacuously MATCHES — fail-open. Publish-time rejection keeps corrupt
// conditions fail-closed: they never reach a snapshot. The value key must
// EXIST (any JSON including null counts); a missing value key is rejected.
func validateCondition(flagKey string, ruleIndex int, cond any) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("flag %q: rule %d has invalid condition: "+format, append([]any{flagKey, ruleIndex}, args...)...)
	}
	raw, err := json.Marshal(cond)
	if err != nil {
		return bad("must be a JSON object with field/op/value")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return bad("must be a JSON object with field/op/value")
	}
	var field, op string
	if f, ok := m["field"]; !ok || json.Unmarshal(f, &field) != nil || field == "" {
		return bad("field must be a non-empty string")
	}
	if o, ok := m["op"]; !ok || json.Unmarshal(o, &op) != nil || op == "" {
		return bad("op must be a non-empty string")
	}
	if _, ok := m["value"]; !ok {
		return bad("missing value")
	}
	var allowed map[string]bool
	switch {
	case field == "platform" || field == "locale" || field == "country":
		allowed = stringFieldOps
	case field == "appVersion":
		allowed = versionFieldOps
	case field == "percentile":
		allowed = percentileOps
	case strings.HasPrefix(field, "custom."):
		if len(field) == len("custom.") {
			return bad("custom attribute name must not be empty")
		}
		allowed = customFieldOps
	default:
		return bad("unknown field %q", field)
	}
	if !allowed[op] {
		return bad("op %q not allowed for field %q", op, field)
	}
	return nil
}

// ValidateSnapshot runs the publish dry-assemble checks over a built
// snapshot (no writes): non-empty, key shape, 1000/project cap, flag
// types with JSON type-coercion of defaults and rule values, per-rule
// condition shape (field/op allowlists mirroring eval, value key present),
// and eval.ValidateExperiment reuse for every included experiment.
func ValidateSnapshot(snap Snapshot) error {
	if len(snap.Flags) == 0 {
		return errors.New("nothing to publish: no flags in this env's project")
	}
	if len(snap.Flags) > maxFlagsPerProject {
		return fmt.Errorf("flag limit reached: max %d flags per project", maxFlagsPerProject)
	}
	for _, f := range snap.Flags {
		if len(f.Key) == 0 || len(f.Key) > maxFlagKeyLength || !flagKeyPattern.MatchString(f.Key) {
			return fmt.Errorf("invalid flag key %q: must match ^[A-Za-z_][A-Za-z0-9_]*$ and be 1-128 chars", f.Key)
		}
		if !validFlagTypes[f.Type] {
			return fmt.Errorf("invalid flag type %q for flag %q: must be number|string|bool|json", f.Type, f.Key)
		}
		if f.Default != nil && !coerceToType(f.Default, f.Type) {
			return fmt.Errorf("flag %q: default value does not match type %q", f.Key, f.Type)
		}
		for i, r := range f.Rules {
			if r.Value != nil && !coerceToType(r.Value, f.Type) {
				return fmt.Errorf("flag %q: rule value does not match type %q", f.Key, f.Type)
			}
			if err := validateCondition(f.Key, i, r.Condition); err != nil {
				return err
			}
		}
	}
	for _, e := range snap.Experiments {
		raw, err := json.Marshal(e.Variants)
		if err != nil {
			return fmt.Errorf("experiment %q: variants are not valid JSON", e.ID)
		}
		var variants []eval.Variant
		if err := json.Unmarshal(raw, &variants); err != nil {
			return fmt.Errorf("experiment %q: variants shape invalid: %v", e.ID, err)
		}
		if err := eval.ValidateExperiment(eval.Experiment{Variants: variants}); err != nil {
			return fmt.Errorf("experiment %q: %v", e.ID, err)
		}
	}
	return nil
}

// MaxVersionForEnv scans releases for the env's current max version
// (0 when no release exists yet).
func MaxVersionForEnv(app core.App, envID string) (int, error) {
	recs, err := app.FindAllRecords("releases")
	if err != nil {
		return 0, err
	}
	max := 0
	for _, r := range recs {
		if r.GetString("env") != envID {
			continue
		}
		if v := r.GetInt("version"); v > max {
			max = v
		}
	}
	return max, nil
}
