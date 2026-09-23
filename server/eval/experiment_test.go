package eval

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testExp5050() Experiment {
	return Experiment{
		ID:   "exp1",
		Seed: "exp-seed-1",
		Variants: []Variant{
			{Name: "control", WeightBps: 5000, Values: map[string]any{"welcome_msg": "hello-control"}},
			{Name: "treatment", WeightBps: 5000, Values: map[string]any{"welcome_msg": "hello-treatment"}},
		},
		DefaultVariant: "control",
	}
}

func TestExperimentValidateWeights(t *testing.T) {
	if err := ValidateExperiment(testExp5050()); err != nil {
		t.Fatalf("50/50 bps must validate: %v", err)
	}
	bad := testExp5050()
	bad.Variants[1].WeightBps = 4900
	if err := ValidateExperiment(bad); err == nil {
		t.Fatalf("9900-sum experiment must be rejected, got nil error")
	}
	empty := Experiment{ID: "e", Seed: "s", DefaultVariant: "control"}
	if err := ValidateExperiment(empty); err == nil {
		t.Fatalf("empty variants must be rejected, got nil error")
	}
}

func TestExperimentStatusPaths(t *testing.T) {
	flag := Flag{Key: "welcome_msg", Type: "string", Default: "hello"}
	rules := []Rule{{ID: "r1", Value: "rule-value"}} // zero conditions: vacuously matches
	ctx := Context{UserID: "test-user-042"}
	exp := testExp5050()

	for _, status := range []string{"draft", "stopped", "bogus-status", ""} {
		got, variant := EvaluateWithExperiment(flag, rules, ctx, exp, status)
		if got != "rule-value" {
			t.Errorf("status %q: want control path rule-value, got %v", status, got)
		}
		if variant != "control" {
			t.Errorf("status %q: want variant control, got %q", status, variant)
		}
	}

	got, variant := EvaluateWithExperiment(flag, rules, ctx, exp, "running")
	wantVariant := Assign(exp, ctx.UserID)
	if variant != wantVariant {
		t.Errorf("running: got variant %q want Assign %q", variant, wantVariant)
	}
	wantVal := map[string]string{"control": "hello-control", "treatment": "hello-treatment"}[wantVariant]
	if got != wantVal {
		t.Errorf("running: got value %v want overlay %q for variant %q", got, wantVal, wantVariant)
	}
}

func TestExperimentOverlayFallthrough(t *testing.T) {
	flag := Flag{Key: "welcome_msg", Type: "string", Default: "hello"}
	rules := []Rule{{ID: "r1", Value: "rule-value"}}
	ctx := Context{UserID: "test-user-042"}

	absent := Experiment{
		ID: "e", Seed: "exp-seed-1", DefaultVariant: "control",
		Variants: []Variant{{Name: "control", WeightBps: 10000}},
	}
	if got, _ := EvaluateWithExperiment(flag, rules, ctx, absent, "running"); got != "rule-value" {
		t.Errorf("absent values entry: want rule-value, got %v", got)
	}

	wrongType := Experiment{
		ID: "e", Seed: "exp-seed-1", DefaultVariant: "control",
		Variants: []Variant{{Name: "control", WeightBps: 10000, Values: map[string]any{"welcome_msg": 42}}},
	}
	if got, _ := EvaluateWithExperiment(flag, rules, ctx, wrongType, "running"); got != "rule-value" {
		t.Errorf("wrong-type overlay: want rule-value fallthrough, got %v", got)
	}

	emptyExp := Experiment{ID: "e", Seed: "s", DefaultVariant: "control"}
	if got, variant := EvaluateWithExperiment(flag, rules, ctx, emptyExp, "running"); got != "rule-value" || variant != "control" {
		t.Errorf("empty variants: want (rule-value,control), got (%v,%q)", got, variant)
	}
}

func TestABSplit10k(t *testing.T) {
	exp := testExp5050()
	counts := func() map[string]int {
		m := map[string]int{}
		for i := 0; i < 10000; i++ {
			m[Assign(exp, fmt.Sprintf("user-%d", i))]++
		}
		return m
	}
	first := counts()
	for _, want := range []string{"control", "treatment"} {
		if first[want] < 4800 || first[want] > 5200 {
			t.Fatalf("split out of ±2%%: %v want each 4800..5200", first)
		}
	}
	for run := 0; run < 2; run++ {
		again := counts()
		for k, v := range first {
			if again[k] != v {
				t.Fatalf("flaky split on re-run %d: %v vs %v", run, again, first)
			}
		}
	}
	t.Logf("split 10k: %v", first)
}

func TestABSticky1k(t *testing.T) {
	exp := testExp5050()
	for i := 0; i < 1000; i++ {
		u := fmt.Sprintf("user-%d", i)
		if a, b := Assign(exp, u), Assign(exp, u); a != b {
			t.Fatalf("stickiness flapped for %q: %q vs %q", u, a, b)
		}
	}
}

func TestExposurePII(t *testing.T) {
	ts := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	ev := ExposureEvent("prod", "welcome_msg", "treatment", "test-user-042", ts)
	if ev.UserHash == "" {
		t.Fatalf("userHash must not be empty for non-empty userID")
	}
	if ev.UserHash == "test-user-042" {
		t.Fatalf("userHash must not carry the raw userID")
	}
	if len(ev.UserHash) != 16 {
		t.Fatalf("userHash must be sha256 hex truncated to 16, got %q", ev.UserHash)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal exposure: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "test-user-042") {
		t.Fatalf("marshalled exposure leaks raw userID: %s", s)
	}
	if strings.Contains(s, "userId") || strings.Contains(s, `"ip"`) {
		t.Fatalf("marshalled exposure must have zero userId/ip keys: %s", s)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode exposure: %v", err)
	}
	for _, k := range []string{"env", "flagKey", "variant", "userHash", "timestamp"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("exposure JSON missing frozen field %q: %s", k, s)
		}
	}

	empty := ExposureEvent("prod", "f", "control", "", ts)
	if empty.UserHash != "" {
		t.Fatalf("empty userID -> userHash %q, want empty", empty.UserHash)
	}
}
