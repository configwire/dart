package fetch

import (
	"net/url"
	"strings"
	"testing"

	"configwire/eval"
	"configwire/releases"
)

func query(raw string) url.Values {
	q, err := url.ParseQuery(raw)
	if err != nil {
		panic(err)
	}
	return q
}

func TestBuildContextFull(t *testing.T) {
	q := query(`uid=u1&platform=android&appVersion=1.2.3&locale=en-US&country=US&attrs=%7B%22tier%22%3A%22pro%22%2C%22n%22%3A7%7D`)
	ctx, err := BuildContext(q)
	if err != nil {
		t.Fatalf("BuildContext err = %v", err)
	}
	if ctx.UserID != "u1" || ctx.Platform != "android" || ctx.AppVersion != "1.2.3" ||
		ctx.Locale != "en-US" || ctx.Country != "US" {
		t.Fatalf("scalar fields wrong: %+v", ctx)
	}
	if ctx.CustomAttrs["tier"] != "pro" {
		t.Fatalf("custom tier = %v", ctx.CustomAttrs["tier"])
	}
}

func TestBuildContextEmpty(t *testing.T) {
	ctx, err := BuildContext(query(``))
	if err != nil {
		t.Fatalf("BuildContext err = %v", err)
	}
	if ctx.UserID != "" || len(ctx.CustomAttrs) != 0 {
		t.Fatalf("expected anonymous empty ctx, got %+v", ctx)
	}
}

func TestBuildContextAttrsEdges(t *testing.T) {
	for name, raw := range map[string]string{
		"not-json":   `attrs=notjson`,
		"array":      `attrs=%5B1%2C2%5D`,
		"string":     `attrs=%22hi%22`,
		"number":     `attrs=42`,
		"null":       `attrs=null`,
		"truncated":  `attrs=%7B%22a%22%3A`,
		"empty-obj":  `attrs=%7B%7D`,
		"nested-obj": `attrs=%7B%22a%22%3A%7B%22b%22%3A1%7D%7D`,
	} {
		ctx, err := BuildContext(query(raw))
		switch name {
		case "empty-obj", "nested-obj":
			if err != nil {
				t.Errorf("%s: unexpected err %v", name, err)
			}
		default:
			if err == nil {
				t.Errorf("%s: expected 400-mapped error, got ctx %+v", name, ctx)
			}
		}
	}
}

func TestBuildContextAttrsOversized(t *testing.T) {
	big := strings.Repeat("a", MaxAttrsBytes+1)
	q := url.Values{"attrs": []string{`{"k":"` + big + `"}`}}
	if _, err := BuildContext(q); err == nil {
		t.Fatalf("expected oversized error for %d-byte attrs", len(big))
	} else if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized error should mention size, got %q", err)
	}
	// Exactly at the bound parses (if valid JSON): bound is on raw length.
	at := `{"k":"` + strings.Repeat("b", MaxAttrsBytes-len(`{"k":""}`)) + `"}`
	if len(at) != MaxAttrsBytes {
		t.Fatalf("test setup wrong: len=%d", len(at))
	}
	if _, err := BuildContext(url.Values{"attrs": []string{at}}); err != nil {
		t.Fatalf("at-bound attrs should parse, got %v", err)
	}
}

func TestEtagMatches(t *testing.T) {
	if !EtagMatches("abc123", "abc123") {
		t.Errorf("exact match should hit")
	}
	for _, h := range []string{"", "W/\"abc123\"", `"abc123"`, "ABC123", "abc123 "} {
		if EtagMatches(h, "abc123") {
			t.Errorf("header %q must NOT match (exact only)", h)
		}
	}
}

func fakeSnapshot() releases.Snapshot {
	return releases.Snapshot{
		Flags: []releases.SnapshotFlag{
			{
				Key:     "welcome",
				Type:    "string",
				Default: "hello",
				Rules: []releases.SnapshotRule{
					{Priority: 1,
						Condition: map[string]any{"field": "platform", "op": "==", "value": "android"},
						Value:     "hello-android"},
				},
			},
			{Key: "new_ui", Type: "bool", Default: false},
		},
		Experiments: []releases.SnapshotExperiment{
			{
				ID:   "exp1",
				Flag: "new_ui",
				Seed: "seed-1",
				Variants: []any{
					map[string]any{"name": "control", "weightBps": float64(5000), "values": map[string]any{"new_ui": false}},
					map[string]any{"name": "treatment", "weightBps": float64(5000), "values": map[string]any{"new_ui": true}},
				},
				Status: "running",
			},
		},
	}
}

func TestEvaluateSnapshotShaping(t *testing.T) {
	snap := fakeSnapshot()
	ctx := eval.Context{UserID: "user-42", Platform: "android"}
	values, variants := EvaluateSnapshot(snap, ctx, "")

	if values["welcome"] != "hello-android" {
		t.Errorf("rule should win for android, got %v", values["welcome"])
	}
	wantVariant := eval.Assign(eval.Experiment{
		Seed: "seed-1",
		Variants: []eval.Variant{
			{Name: "control", WeightBps: 5000},
			{Name: "treatment", WeightBps: 5000},
		},
	}, "user-42")
	if variants["new_ui"] != wantVariant {
		t.Errorf("variant = %q, want Assign %q", variants["new_ui"], wantVariant)
	}
	wantVal := false
	if wantVariant == "treatment" {
		wantVal = true
	}
	if values["new_ui"] != wantVal {
		t.Errorf("overlay value = %v, want %v for variant %q", values["new_ui"], wantVal, wantVariant)
	}
	if _, ok := variants["welcome"]; ok {
		t.Errorf("flag with no experiment must have no variants entry")
	}
}

func TestEvaluateSnapshotControlPaths(t *testing.T) {
	snap := fakeSnapshot()
	// Anonymous user -> Assign falls back to DefaultVariant ("", snapshot
	// carries none) and the control values apply.
	values, variants := EvaluateSnapshot(snap, eval.Context{}, "")
	if variants["new_ui"] != "" {
		t.Errorf("anonymous variant = %q, want DefaultVariant %q", variants["new_ui"], "")
	}
	if values["new_ui"] != false {
		t.Errorf("anonymous overlay value = %v, want base false", values["new_ui"])
	}
	// exp override forces the control path even for an assigned user.
	values, variants = EvaluateSnapshot(snap, eval.Context{UserID: "user-42"}, "draft")
	if values["new_ui"] != false {
		t.Errorf("draft override value = %v, want base false", values["new_ui"])
	}
	if variants["new_ui"] != "" {
		t.Errorf("draft override variant = %q, want %q", variants["new_ui"], "")
	}
	// Non-semver appVersion never errors: platform-gated rule still wins.
	values, _ = EvaluateSnapshot(snap, eval.Context{Platform: "android", AppVersion: "not-semver"}, "")
	if values["welcome"] != "hello-android" {
		t.Errorf("not-semver must fall through to rule match, got %v", values["welcome"])
	}
}

// TestAllowHeadersIsConfigWire guards the fetch preflight contract: the
// fetch OPTIONS response must permit the SDK key header (served via
// the allowHeaders const, also asserted live in
// .omo/evidence/configwire-t5-wire.log).
func TestAllowHeadersIsConfigWire(t *testing.T) {
	if allowHeaders != "X-ConfigWire-Key, If-None-Match" {
		t.Fatalf("allowHeaders = %q, want %q", allowHeaders, "X-ConfigWire-Key, If-None-Match")
	}
}

func TestCorsOriginSingleRead(t *testing.T) {
	t.Setenv("CONFIGWIRE_CORS_ORIGIN", "https://app.example.com")
	if got := corsOrigin(); got != "https://app.example.com" {
		t.Fatalf("corsOrigin = %q, want configured origin", got)
	}

	t.Setenv("CONFIGWIRE_CORS_ORIGIN", "")
	if got := corsOrigin(); got != "*" {
		t.Fatalf("unset: corsOrigin = %q, want dev default", got)
	}
}
