package releases

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"configwire/eval"
)

func goodVariants() any {
	return []any{
		map[string]any{"name": "control", "weightBps": float64(5000)},
		map[string]any{"name": "treatment", "weightBps": float64(5000)},
	}
}

func goodSnapshot() Snapshot {
	return Snapshot{
		Flags: []SnapshotFlag{
			{
				Key: "hero_button", Type: "string", Default: "blue", Group: "",
				Rules: []SnapshotRule{
					{Priority: 1, Condition: map[string]any{"field": "platform", "op": "==", "value": "android"}, Value: "green"},
				},
			},
			{
				Key: "new_checkout", Type: "bool", Default: false, Group: "",
				Rules: []SnapshotRule{},
			},
		},
		Experiments: []SnapshotExperiment{
			{ID: "exp1", Flag: "hero_button", Seed: "seed-1", Variants: goodVariants(), Status: "draft"},
		},
	}
}

func TestEtagDeterminism(t *testing.T) {
	snap := []byte(`{"flags":[],"experiments":[]}`)
	if got := EtagFor(1, snap); got != EtagFor(1, snap) {
		t.Fatalf("etag not deterministic: %q", got)
	}
	etag := EtagFor(1, snap)
	if len(etag) != 16 {
		t.Fatalf("etag must be 16 hex chars, got %q", etag)
	}
	for _, c := range etag {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("etag must be lowercase hex, got %q", etag)
		}
	}
}

func TestEtagDiffersAcrossVersions(t *testing.T) {
	// Rollback requirement: identical snapshot bytes MUST still yield a
	// fresh etag on a new version.
	snap := []byte(`{"flags":[{"key":"a","type":"bool","default":true,"group":"","rules":[]}],"experiments":[]}`)
	if EtagFor(1, snap) == EtagFor(2, snap) {
		t.Fatal("etag must differ across versions for identical snapshot bytes")
	}
}

func TestEtagDiffersAcrossSnapshots(t *testing.T) {
	if EtagFor(1, []byte(`{"flags":[]}`)) == EtagFor(1, []byte(`{"flags":[1]}`)) {
		t.Fatal("etag must differ across snapshot bytes for the same version")
	}
}

func TestVersionIncrementLogic(t *testing.T) {
	if got := NextVersion(0); got != 1 {
		t.Fatalf("first version must be 1, got %d", got)
	}
	if got := NextVersion(7); got != 8 {
		t.Fatalf("next version must be max+1, got %d", got)
	}
	if !CheckBaseVersion(0, 0) || !CheckBaseVersion(3, 3) {
		t.Fatal("matching baseVersion must pass the guard")
	}
	if CheckBaseVersion(0, 1) || CheckBaseVersion(2, 3) {
		t.Fatal("stale baseVersion must fail the guard (409, no write)")
	}
}

func TestSnapshotShapeFrozenKeys(t *testing.T) {
	raw, err := MarshalCanonical(goodSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"flags", "experiments"} {
		if _, ok := top[k]; !ok {
			t.Fatalf("snapshot missing top-level key %q: %s", k, raw)
		}
	}
	var flag map[string]any
	if err := json.Unmarshal(raw, &flag); err != nil {
		t.Fatal(err)
	}
	_ = flag
	var decoded struct {
		Flags []map[string]any `json:"flags"`
		Exps  []map[string]any `json:"experiments"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"key", "type", "default", "group", "rules"} {
		if _, ok := decoded.Flags[0][want]; !ok {
			t.Fatalf("flag missing key %q: %s", want, raw)
		}
	}
	for _, rule := range []string{"priority", "condition", "value"} {
		rules := decoded.Flags[0]["rules"].([]any)
		if _, ok := rules[0].(map[string]any)[rule]; !ok {
			t.Fatalf("rule missing key %q: %s", rule, raw)
		}
	}
	for _, want := range []string{"id", "flag", "seed", "variants", "status"} {
		if _, ok := decoded.Exps[0][want]; !ok {
			t.Fatalf("experiment missing key %q: %s", want, raw)
		}
	}
}

func TestMarshalCanonicalEmptyIsArrayNotNull(t *testing.T) {
	raw, err := MarshalCanonical(Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"flags":[],"experiments":[]}` {
		t.Fatalf("empty snapshot must marshal with [] (not null): %s", raw)
	}
}

func TestValidateSnapshotAcceptsGood(t *testing.T) {
	if err := ValidateSnapshot(goodSnapshot()); err != nil {
		t.Fatalf("good snapshot rejected: %v", err)
	}
}

func TestValidateSnapshotRejectsEmpty(t *testing.T) {
	err := ValidateSnapshot(Snapshot{Flags: []SnapshotFlag{}, Experiments: []SnapshotExperiment{}})
	if err == nil || !strings.Contains(err.Error(), "nothing to publish") {
		t.Fatalf("empty snapshot must be rejected with 'nothing to publish', got %v", err)
	}
}

func TestValidateSnapshotRejectsBadKey(t *testing.T) {
	// Hooks forbid inserting a bad key via the API, so this path is
	// covered by unit test per plan: publish validation must reject it.
	snap := goodSnapshot()
	snap.Flags[0].Key = "1bad"
	if err := ValidateSnapshot(snap); err == nil {
		t.Fatal("bad flag key 1bad must be rejected")
	}
	snap.Flags[0].Key = "ok_key"
	if err := ValidateSnapshot(snap); err != nil {
		t.Fatalf("fixed key must pass: %v", err)
	}
}

func TestValidateSnapshotRejectsCap(t *testing.T) {
	flags := make([]SnapshotFlag, 1001)
	for i := range flags {
		flags[i] = SnapshotFlag{Key: "k", Type: "bool", Default: true, Rules: []SnapshotRule{}}
	}
	if err := ValidateSnapshot(Snapshot{Flags: flags}); err == nil {
		t.Fatal("1001 flags must breach the 1000/project cap")
	}
}

func TestValidateSnapshotRejectsTypeMismatch(t *testing.T) {
	snap := goodSnapshot()
	snap.Flags[0].Default = true
	if err := ValidateSnapshot(snap); err == nil {
		t.Fatal("default/type mismatch must be rejected")
	}
	snap = goodSnapshot()
	snap.Flags[1].Rules = []SnapshotRule{{Priority: 1, Condition: nil, Value: "yes"}}
	if err := ValidateSnapshot(snap); err == nil {
		t.Fatal("rule value/type mismatch must be rejected")
	}
}

func TestValidateSnapshotRejectsBadExperiment(t *testing.T) {
	// Reuses eval.ValidateExperiment: weights must sum to exactly 10000.
	snap := goodSnapshot()
	snap.Experiments[0].Variants = []any{
		map[string]any{"name": "control", "weightBps": float64(9900)},
	}
	if err := ValidateSnapshot(snap); err == nil {
		t.Fatal("experiment with weights summing to 9900 must be rejected")
	}
	if err := eval.ValidateExperiment(eval.Experiment{}); err == nil {
		t.Fatal("sanity: eval.ValidateExperiment must reject empty variants")
	}
}

func TestValidateSnapshotRejectsBadFlagType(t *testing.T) {
	snap := goodSnapshot()
	snap.Flags[0].Type = "text"
	if err := ValidateSnapshot(snap); err == nil {
		t.Fatal("unknown flag type must be rejected")
	}
}

func TestValidateSnapshotAcceptsConditionShapes(t *testing.T) {
	// One valid condition per eval field class must pass (null value
	// counts as present: the value KEY exists).
	for name, cond := range map[string]any{
		"platform":   map[string]any{"field": "platform", "op": "regex", "value": "^and"},
		"appVersion": map[string]any{"field": "appVersion", "op": ">=", "value": "1.2.3"},
		"locale":     map[string]any{"field": "locale", "op": "contains", "value": "en"},
		"country":    map[string]any{"field": "country", "op": "!=", "value": "US"},
		"percentile": map[string]any{"field": "percentile", "op": "between", "value": []any{float64(10), float64(20)}, "seed": "s"},
		"custom":     map[string]any{"field": "custom.tier", "op": "<=", "value": float64(3)},
		"null-value": map[string]any{"field": "platform", "op": "==", "value": nil},
	} {
		snap := goodSnapshot()
		snap.Flags[0].Rules = []SnapshotRule{{Priority: 1, Condition: cond, Value: "green"}}
		if err := ValidateSnapshot(snap); err != nil {
			t.Errorf("%s: valid condition rejected: %v", name, err)
		}
	}
}

func TestValidateSnapshotRejectsBadConditions(t *testing.T) {
	// Every shape eval would silently treat as false must fail closed at
	// publish (400) instead of vacuously matching at fetch.
	for name, cond := range map[string]any{
		"unknown-field": map[string]any{"field": "device", "op": "==", "value": "x"},
		"bare-custom":   map[string]any{"field": "custom", "op": "==", "value": "x"},
		"empty-custom":  map[string]any{"field": "custom.", "op": "==", "value": "x"},
		"unknown-op":    map[string]any{"field": "platform", "op": ">", "value": "x"},
		"percentile-op": map[string]any{"field": "percentile", "op": "==", "value": float64(5)},
		"array":         []any{map[string]any{"field": "platform"}},
		"string":        "platform==android",
		"number":        float64(42),
		"null":          nil,
		"missing-value": map[string]any{"field": "platform", "op": "=="},
		"missing-op":    map[string]any{"field": "platform", "value": "x"},
		"missing-field": map[string]any{"op": "==", "value": "x"},
		"nonstring-op":  map[string]any{"field": "platform", "op": float64(1), "value": "x"},
	} {
		snap := goodSnapshot()
		snap.Flags[0].Rules = []SnapshotRule{{Priority: 1, Condition: cond, Value: "green"}}
		err := ValidateSnapshot(snap)
		if err == nil || !strings.Contains(err.Error(), "invalid condition") {
			t.Errorf("%s: must be rejected with 'invalid condition', got %v", name, err)
		}
	}
}

func TestResolveExperimentFlagSameProject(t *testing.T) {
	projects := map[string]string{"flagA": "projA", "flagB": "projB"}
	keys := map[string]string{"flagA": "launch", "flagB": "launch"}
	key, include := ResolveExperimentFlag("projA", "flagA", projects, keys)
	if !include || key != "launch" {
		t.Fatalf("expected (launch, true), got (%q, %v)", key, include)
	}
}

func TestResolveExperimentFlagExcludesForeignProject(t *testing.T) {
	projects := map[string]string{"flagA": "projA", "flagB": "projB"}
	keys := map[string]string{"flagA": "launch", "flagB": "launch"}
	if _, include := ResolveExperimentFlag("projA", "flagB", projects, keys); include {
		t.Fatal("expected experiment targeting another project's flag to be excluded")
	}
}

func TestResolveExperimentFlagUntargetedAndDangling(t *testing.T) {
	projects := map[string]string{"flagA": "projA"}
	keys := map[string]string{"flagA": "launch"}
	if key, include := ResolveExperimentFlag("projA", "", projects, keys); !include || key != "" {
		t.Fatalf("expected (\"\", true) for untargeted, got (%q, %v)", key, include)
	}
	if key, include := ResolveExperimentFlag("projA", "ghost", projects, keys); !include || key != "" {
		t.Fatalf("expected (\"\", true) for dangling relation, got (%q, %v)", key, include)
	}
}

func TestRollbackPickGlobalUnique(t *testing.T) {
	rows := []ReleaseRow{{EnvID: "envA", Version: 1}, {EnvID: "envA", Version: 2}}
	idx, err := RollbackPick(rows, 1, "")
	if err != nil || idx != 0 {
		t.Fatalf("expected index 0, got %d, err %v", idx, err)
	}
}

func TestRollbackPickGlobalAmbiguousOnlyWhenShared(t *testing.T) {
	rows := []ReleaseRow{{EnvID: "envA", Version: 1}, {EnvID: "envB", Version: 1}}
	if _, err := RollbackPick(rows, 1, ""); !errors.Is(err, ErrReleaseAmbiguous) {
		t.Fatalf("expected ErrReleaseAmbiguous, got %v", err)
	}
}

func TestRollbackPickGlobalUnknown(t *testing.T) {
	rows := []ReleaseRow{{EnvID: "envA", Version: 1}}
	if _, err := RollbackPick(rows, 9, ""); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("expected ErrReleaseNotFound, got %v", err)
	}
}

func TestRollbackPickEnvScopedWithDuplicateVersions(t *testing.T) {
	rows := []ReleaseRow{{EnvID: "envA", Version: 1}, {EnvID: "envB", Version: 1}}
	idx, err := RollbackPick(rows, 1, "envB")
	if err != nil || idx != 1 {
		t.Fatalf("expected index 1, got %d, err %v", idx, err)
	}
	idx, err = RollbackPick(rows, 1, "envA")
	if err != nil || idx != 0 {
		t.Fatalf("expected index 0, got %d, err %v", idx, err)
	}
}

func TestRollbackPickEnvScopedMissingVersion(t *testing.T) {
	rows := []ReleaseRow{{EnvID: "envA", Version: 1}}
	if _, err := RollbackPick(rows, 1, "envB"); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("expected ErrReleaseNotFound for version absent in this env, got %v", err)
	}
}
