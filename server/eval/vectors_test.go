// Vectors suite pinning the evaluator contract: every vector asserts the live
// implementation; fallthrough-to-default vectors prove invalid inputs never
// panic and never error.
//
// Coverage: type coercion incl. mismatches, first-true ordering (3 ordered
// rules), semver ops + invalid-semver fallthrough, locale/country,
// platform incl. unknown, percentile (+10k determinism/distribution
// subtest), unknown-attr fallthrough, experiment sticky re-check.
package eval

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"testing"
)

// oracleBucket duplicates the contract formula so vectors stay
// self-consistent: int(fnv64a(userID+seed) % 10000), identical to Bucket.
func oracleBucket(userID, seed string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID + seed))
	return int(h.Sum64() % 10000)
}

// oracleVariant maps a bucket onto cumulative weightBps, mirroring Assign.
func oracleVariant(exp Experiment, userID string) string {
	if userID == "" || len(exp.Variants) == 0 {
		return exp.DefaultVariant
	}
	b := oracleBucket(userID, exp.Seed)
	if b < 0 || b > 9999 {
		return exp.DefaultVariant
	}
	cum := 0
	for _, v := range exp.Variants {
		cum += v.WeightBps
		if b < cum {
			return v.Name
		}
	}
	return exp.DefaultVariant
}

func mustJSON(t *testing.T, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("bad vector JSON %q: %v", raw, err)
	}
}

func canon(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type vector struct {
	name           string
	kind           string // "eval" | "assign" | "bucket"
	flagJSON       string
	rulesJSON      string
	ctxJSON        string
	experimentJSON string
	userID         string
	bucketUser     string
	bucketSeed     string
	wantJSON       string
}

const (
	ctxAndroid   = `{"userId":"u-alex","platform":"android","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"pro","trial":true,"credits":120},"percentileSeed":"rollout-1"}`
	ctxIOSPro    = `{"userId":"u-alex","platform":"ios","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"pro","trial":true,"credits":120},"percentileSeed":"rollout-1"}`
	ctxIOSFreeEN = `{"userId":"u-alex","platform":"ios","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"free","trial":false,"credits":5},"percentileSeed":"rollout-1"}`
	ctxIOSFreeFR = `{"userId":"u-alex","platform":"ios","appVersion":"9.9.9","locale":"fr-FR","country":"FR","customAttrs":{"tier":"free","trial":false,"credits":5},"percentileSeed":"rollout-1"}`
	ctxBadVer    = `{"userId":"u-alex","platform":"android","appVersion":"not-semver","locale":"en-US","country":"US","customAttrs":{"tier":"pro"},"percentileSeed":"rollout-1"}`
	ctxUnknownPl = `{"userId":"u-alex","platform":"windowsphone","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"pro"},"percentileSeed":"rollout-1"}`
	ctxCasePlat  = `{"userId":"u-alex","platform":"Android","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"pro"},"percentileSeed":"rollout-1"}`
	ctxFR        = `{"userId":"u-alex","platform":"android","appVersion":"1.2.3","locale":"fr-FR","country":"FR","customAttrs":{"tier":"pro"},"percentileSeed":"rollout-1"}`
	ctxNoSeed    = `{"userId":"u-alex","platform":"android","appVersion":"1.2.3","locale":"en-US","country":"US","customAttrs":{"tier":"pro"},"percentileSeed":""}`
	fStr         = `{"key":"welcome_msg","type":"string","default":"hello"}`
	fNum         = `{"key":"credits","type":"number","default":0}`
	fBool        = `{"key":"new_ui","type":"bool","default":false}`
	fJSON        = `{"key":"home_config","type":"json","default":{"a":1}}`
	fOld         = `{"key":"gate","type":"string","default":"old"}`
	fOrd         = `{"key":"ord","type":"string","default":"base"}`
	fRoll        = `{"key":"roll","type":"string","default":"out"}`
	exp5050      = `{"id":"exp1","seed":"exp-seed-1","variants":[{"name":"control","weightBps":5000},{"name":"treatment","weightBps":5000}],"defaultVariant":"control"}`
	expEmpty     = `{"id":"exp-empty","seed":"s","variants":[],"defaultVariant":"control"}`
	orderedRules = `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"first"},{"id":"r2","conditions":[{"field":"custom.tier","op":"==","value":"pro"}],"value":"second"},{"id":"r3","conditions":[{"field":"locale","op":"==","value":"en-US"}],"value":"third"}]`
)

func TestVectors(t *testing.T) {
	table := []vector{
		{name: "coerce/number_match", kind: "eval", flagJSON: fNum, rulesJSON: `[{"id":"r1","conditions":[{"field":"custom.credits","op":">=","value":100}],"value":200}]`, ctxJSON: ctxAndroid, wantJSON: `200`},
		{name: "coerce/number_float_match", kind: "eval", flagJSON: fNum, rulesJSON: `[{"id":"r1","conditions":[{"field":"custom.tier","op":"==","value":"pro"}],"value":19.99}]`, ctxJSON: ctxAndroid, wantJSON: `19.99`},
		{name: "coerce/number_string_mismatch_fallthrough", kind: "eval", flagJSON: fNum, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"lots"}]`, ctxJSON: ctxAndroid, wantJSON: `0`},
		{name: "coerce/string_match", kind: "eval", flagJSON: fStr, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"hola-android"}]`, ctxJSON: ctxAndroid, wantJSON: `"hola-android"`},
		{name: "coerce/string_number_mismatch_fallthrough", kind: "eval", flagJSON: fStr, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":42}]`, ctxJSON: ctxAndroid, wantJSON: `"hello"`},
		{name: "coerce/bool_match", kind: "eval", flagJSON: fBool, rulesJSON: `[{"id":"r1","conditions":[{"field":"custom.tier","op":"==","value":"pro"}],"value":true}]`, ctxJSON: ctxAndroid, wantJSON: `true`},
		{name: "coerce/bool_string_mismatch_fallthrough", kind: "eval", flagJSON: fBool, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"yes"}]`, ctxJSON: ctxAndroid, wantJSON: `false`},
		{name: "coerce/json_object_match", kind: "eval", flagJSON: fJSON, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":{"a":2,"b":[1,2]}}]`, ctxJSON: ctxAndroid, wantJSON: `{"a":2,"b":[1,2]}`},
		{name: "coerce/json_array_match", kind: "eval", flagJSON: fJSON, rulesJSON: `[{"id":"r1","conditions":[{"field":"custom.trial","op":"==","value":true}],"value":["x","y"]}]`, ctxJSON: ctxAndroid, wantJSON: `["x","y"]`},
		{name: "coerce/json_scalar_mismatch_fallthrough", kind: "eval", flagJSON: fJSON, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":42}]`, ctxJSON: ctxAndroid, wantJSON: `{"a":1}`},
		{name: "order/first_wins_over_second", kind: "eval", flagJSON: fOrd, rulesJSON: orderedRules, ctxJSON: ctxAndroid, wantJSON: `"first"`},
		{name: "order/second_wins_when_first_false", kind: "eval", flagJSON: fOrd, rulesJSON: orderedRules, ctxJSON: ctxIOSPro, wantJSON: `"second"`},
		{name: "order/third_wins_when_first_two_false", kind: "eval", flagJSON: fOrd, rulesJSON: orderedRules, ctxJSON: ctxIOSFreeEN, wantJSON: `"third"`},
		{name: "order/none_match_returns_default", kind: "eval", flagJSON: fOrd, rulesJSON: orderedRules, ctxJSON: ctxIOSFreeFR, wantJSON: `"base"`},
		{name: "semver/lt_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"<","value":"1.2.4"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/lte_equal_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"<=","value":"1.2.3"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/eq_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"==","value":"1.2.3"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/neq_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"!=","value":"1.2.4"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/gte_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":">=","value":"1.2.3"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/gt_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":">","value":"1.2.2"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/contains_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"contains","value":"2.3"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/regex_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"regex","value":"^1\\.2\\."}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "semver/invalid_target_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"<","value":"1.2.x"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "semver/not_semver_ctx_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"appVersion","op":"<","value":"2.0.0"}],"value":"new"}]`, ctxJSON: ctxBadVer, wantJSON: `"old"`},
		{name: "locale/exact_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"locale","op":"==","value":"en-US"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "locale/contains_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"locale","op":"contains","value":"en"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "locale/mismatch_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"locale","op":"==","value":"en-US"}],"value":"new"}]`, ctxJSON: ctxFR, wantJSON: `"old"`},
		{name: "country/exact_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"country","op":"==","value":"US"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "country/mismatch_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"country","op":"==","value":"DE"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "platform/android_match", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"new"`},
		{name: "platform/ios_rule_android_ctx_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"ios"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "platform/unknown_value_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"new"}]`, ctxJSON: ctxUnknownPl, wantJSON: `"old"`},
		{name: "platform/case_sensitive_fallthrough", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"==","value":"android"}],"value":"new"}]`, ctxJSON: ctxCasePlat, wantJSON: `"old"`},
		{name: "percentile/full_range_always_matches", kind: "eval", flagJSON: fRoll, rulesJSON: `[{"id":"r1","conditions":[{"field":"percentile","op":"<=","value":9999,"seed":"rollout-1"}],"value":"in"}]`, ctxJSON: ctxAndroid, wantJSON: `"in"`},
		{name: "percentile/oracle_band_matches", kind: "eval", flagJSON: fRoll, rulesJSON: `[{"id":"r1","conditions":[{"field":"percentile","op":"between","value":$BUCKET_PLACEHOLDER,"seed":"rollout-1"}],"value":"in"}]`, ctxJSON: ctxAndroid, wantJSON: `"in"`},
		{name: "percentile/impossible_threshold_fallthrough", kind: "eval", flagJSON: fRoll, rulesJSON: `[{"id":"r1","conditions":[{"field":"percentile","op":"<=","value":-1,"seed":"rollout-1"}],"value":"in"}]`, ctxJSON: ctxAndroid, wantJSON: `"out"`},
		{name: "percentile/missing_seed_fallthrough", kind: "eval", flagJSON: fRoll, rulesJSON: `[{"id":"r1","conditions":[{"field":"percentile","op":"<=","value":9999}],"value":"in"}]`, ctxJSON: ctxNoSeed, wantJSON: `"out"`},
		{name: "percentile/between_full_range_matches", kind: "eval", flagJSON: fRoll, rulesJSON: `[{"id":"r1","conditions":[{"field":"percentile","op":"between","value":[0,9999],"seed":"rollout-1"}],"value":"in"}]`, ctxJSON: ctxAndroid, wantJSON: `"in"`},
		{name: "unknown_attr/missing_custom_key", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"custom.vip","op":"==","value":true}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "unknown_attr/unknown_op", kind: "eval", flagJSON: fOld, rulesJSON: `[{"id":"r1","conditions":[{"field":"platform","op":"FROBNICATE","value":"android"}],"value":"new"}]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "unknown_attr/empty_rules_default", kind: "eval", flagJSON: fOld, rulesJSON: `[]`, ctxJSON: ctxAndroid, wantJSON: `"old"`},
		{name: "experiment/sticky_same_user_oracle", kind: "assign", experimentJSON: exp5050, userID: "sticky-sam", wantJSON: `$ORACLE`},
		{name: "experiment/known_bucket_variant", kind: "assign", experimentJSON: exp5050, userID: "exp-rita", wantJSON: `$ORACLE`},
		{name: "experiment/empty_user_fallthrough", kind: "assign", experimentJSON: exp5050, userID: "", wantJSON: `"control"`},
		{name: "experiment/empty_variants_fallthrough", kind: "assign", experimentJSON: expEmpty, userID: "u-alex", wantJSON: `"control"`},
		{name: "bucket/oracle_spot_check", kind: "bucket", bucketUser: "u-alex", bucketSeed: "rollout-1", wantJSON: `$BUCKETNUM`},
	}
	if len(table) < 40 {
		t.Fatalf("vector suite must hold >=40 cases, got %d", len(table))
	}

	for _, tc := range table {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			switch tc.kind {
			case "eval":
				rulesRaw := strings.ReplaceAll(tc.rulesJSON, "$BUCKET_PLACEHOLDER",
					fmt.Sprintf("[%d,%d]", oracleBucket("u-alex", "rollout-1"), oracleBucket("u-alex", "rollout-1")))
				var flag Flag
				mustJSON(t, tc.flagJSON, &flag)
				var rules []Rule
				mustJSON(t, rulesRaw, &rules)
				var ctx Context
				mustJSON(t, tc.ctxJSON, &ctx)
				var want any
				mustJSON(t, tc.wantJSON, &want)
				got := Evaluate(flag, rules, ctx)
				if canon(got) != canon(want) {
					t.Errorf("TODO(todo6) vector %s: input flag=%s rules=%s ctx=%s: got %s want %s",
						tc.name, tc.flagJSON, rulesRaw, tc.ctxJSON, canon(got), canon(want))
				}
			case "assign":
				var exp Experiment
				mustJSON(t, tc.experimentJSON, &exp)
				want := strings.ReplaceAll(tc.wantJSON, "$ORACLE", strconv.Quote(oracleVariant(exp, tc.userID)))
				var wantStr string
				mustJSON(t, want, &wantStr)
				got := Assign(exp, tc.userID)
				if again := Assign(exp, tc.userID); again != got {
					t.Errorf("TODO(todo6) vector %s: sticky re-check flapped: %q vs %q", tc.name, got, again)
				}
				if got != wantStr {
					t.Errorf("TODO(todo6) vector %s: user %q: got %q want oracle %q", tc.name, tc.userID, got, wantStr)
				}
			case "bucket":
				want := strings.ReplaceAll(tc.wantJSON, "$BUCKETNUM",
					strconv.Itoa(oracleBucket(tc.bucketUser, tc.bucketSeed)))
				var wantNum float64
				mustJSON(t, want, &wantNum)
				got := Bucket(tc.bucketUser, tc.bucketSeed)
				if float64(got) != wantNum {
					t.Errorf("TODO(todo6) vector %s: bucket(%q+%q): got %d want %v",
						tc.name, tc.bucketUser, tc.bucketSeed, got, want)
				}
			default:
				t.Fatalf("unknown vector kind %q", tc.kind)
			}
		})
	}

	t.Run("percentile_determinism_10k", func(t *testing.T) {
		const n = 10000
		const seed = "rollout-1"
		b0 := Bucket("det-fixed-user", seed)
		for i := 0; i < n; i++ {
			if got := Bucket("det-fixed-user", seed); got != b0 {
				t.Errorf("TODO(todo6) same userId+seed flapped on run %d: %d vs %d", i, got, b0)
				break
			}
		}
		first := make([]int, n)
		for i := 0; i < n; i++ {
			first[i] = Bucket(fmt.Sprintf("det-user-%d", i), seed)
		}
		for i, b := range first {
			if b < 0 || b > 9999 {
				t.Errorf("TODO(todo6) bucket out of range at %d: %d", i, b)
				break
			}
		}
		lo := 0
		for _, b := range first {
			if b < 5000 {
				lo++
			}
		}
		if lo < 4750 || lo > 5250 {
			t.Errorf("TODO(todo6) distribution halves out of ±5%%: lo=%d/10000 want 5000±250", lo)
		}
		again := make([]int, n)
		for i := 0; i < n; i++ {
			again[i] = Bucket(fmt.Sprintf("det-user-%d", i), seed)
		}
		for i := range first {
			if again[i] != first[i] {
				t.Errorf("byte-identical re-run diverged at %d: %d vs %d", i, again[i], first[i])
				break
			}
		}
		t.Logf("determinism: fixed=%d lo=%d/10000", b0, lo)
	})

	t.Run("experiment_sticky_recheck_fixture", func(t *testing.T) {
		raw, err := os.ReadFile("fixtures/experiment_sticky.json")
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var fx struct {
			Experiment Experiment `json:"experiment"`
			Users      []string   `json:"users"`
		}
		mustJSON(t, string(raw), &fx)
		if len(fx.Users) == 0 {
			t.Fatalf("sticky fixture must list users")
		}
		for _, u := range fx.Users {
			a1 := Assign(fx.Experiment, u)
			a2 := Assign(fx.Experiment, u)
			if a1 != a2 {
				t.Errorf("sticky re-check flapped for %q: %q vs %q", u, a1, a2)
			}
			if want := oracleVariant(fx.Experiment, u); a1 != want {
				t.Errorf("TODO(todo6) user %q: got %q want oracle %q", u, a1, want)
			}
		}
	})

	t.Run("fixtures_type_matrix", func(t *testing.T) {
		rulesRaw, err := os.ReadFile("fixtures/rules_ordered.json")
		if err != nil {
			t.Fatalf("read rules fixture: %v", err)
		}
		ctxRaw, err := os.ReadFile("fixtures/context_android.json")
		if err != nil {
			t.Fatalf("read context fixture: %v", err)
		}
		var rules []Rule
		mustJSON(t, string(rulesRaw), &rules)
		var ctx Context
		mustJSON(t, string(ctxRaw), &ctx)
		cases := map[string]string{
			"flag_string.json": `"first"`,
			"flag_number.json": `0`,
			"flag_bool.json":   `false`,
			"flag_json.json":   `{"a":1}`,
		}
		for file, wantRaw := range cases {
			raw, err := os.ReadFile("fixtures/" + file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			var flag Flag
			mustJSON(t, string(raw), &flag)
			var want any
			mustJSON(t, wantRaw, &want)
			got := Evaluate(flag, rules, ctx)
			if canon(got) != canon(want) {
				t.Errorf("TODO(todo6) fixture %s: got %s want %s", file, canon(got), canon(want))
			}
		}
	})

	t.Run("broken_fixture_fallthrough", func(t *testing.T) {
		raw, err := os.ReadFile("fixtures/broken_condition.json")
		if err != nil {
			t.Fatalf("read broken fixture: %v", err)
		}
		var fx struct {
			Flag          Flag    `json:"flag"`
			Rules         []Rule  `json:"rules"`
			Ctx           Context `json:"context"`
			ExpectDefault string  `json:"expectDefault"`
		}
		mustJSON(t, string(raw), &fx)
		// Invalid semver target "1.2.x", unknown platform, missing
		// percentile seed: every condition false -> default, no panic.
		got := Evaluate(fx.Flag, fx.Rules, fx.Ctx)
		if canon(got) != canon(fx.ExpectDefault) {
			t.Errorf("broken fixture must fall through to default: got %s want %s", canon(got), canon(fx.ExpectDefault))
		}
		// Deliberately malformed JSON must surface as a decode error and
		// still fall through to defaults without panic.
		var bad Flag
		if err := json.Unmarshal([]byte(`{"flag":`), &bad); err == nil {
			t.Errorf("expected decode error for malformed fixture JSON")
		}
		fallback := Evaluate(Flag{Key: "x", Type: "string", Default: "d"}, nil, Context{})
		if canon(fallback) != `"d"` {
			t.Errorf("nil-rules fallback must return default: got %s", canon(fallback))
		}
	})
}
