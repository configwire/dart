// Package eval is the pure condition-evaluation contract for ConfigWire.
//
// CROSS-TASK CONTRACT:
//
//	Evaluate(flag Flag, rules []Rule, ctx Context) any
//	  - No DB, no network, no I/O. Pure function.
//	  - Rules are evaluated top-down; a rule matches iff ALL of its
//	    Conditions are true (AND). First matching rule wins.
//	  - The winning rule's Value is coerced to flag.Type; on type
//	    mismatch the rule is skipped (treated as non-matching).
//	  - No rule matches (or all skipped) -> return flag.Default.
//	  - Callers apply the in-app default when the flag itself is absent.
//	  - NEVER panics, NEVER returns an error: any invalid input
//	    (unknown field/op, bad semver, missing seed, unknown attr)
//	    makes that condition false -> fallthrough to default.
//
//	Assign(exp Experiment, userID string) string
//	  - Sticky by construction: pure function of (userID, exp.Seed).
//	  - Empty userID or empty Variants -> exp.DefaultVariant.
//	  - Else variant = cumulative weightBps mapping of Bucket(userID, Seed).
//
//	Bucket(userID, seed string) int
//	  - Formula: int(fnv64a(userID+seed) % 10000), range 0..9999.
//	  - Empty userID or empty seed -> -1 (invalid; callers treat as false).
//
// Condition JSON schema (field -> allowed ops):
//
//	{"field":"platform","op":"==|!=|contains|regex","value":"android"}
//	  Exact, case-sensitive compare for ==/!=; unknown platform strings
//	  never error, they just compare.
//	{"field":"appVersion","op":"<|<=|==|!=|>=|>|contains|regex","value":"1.2.4"}
//	  Relational ops parse strict semver MAJOR.MINOR.PATCH with optional
//	  -prerelease/+build suffixes; numeric compare, prerelease < release.
//	  Unparseable either side (e.g. "1.2.x", "not-semver") -> condition
//	  false. contains/regex operate on the raw string.
//	{"field":"locale","op":"==|!=|contains|regex","value":"en-US"}
//	{"field":"country","op":"==|!=|contains|regex","value":"US"}
//	  == is exact (no implicit prefix: "en" != "en-US"; use contains).
//	{"field":"percentile","op":"<=|between","value":5000| [lo,hi],"seed":"rollout-1"}
//	  Bucketed via Bucket(ctx.UserID, cond.Seed or ctx.PercentileSeed).
//	  <= is inclusive; between [lo,hi] is inclusive both ends.
//	  Missing/empty seed or userID -> condition false.
//	{"field":"custom.<name>","op":"==|!=|<|<=|>|>=|contains|regex","value":...}
//	  Flat lookup in ctx.CustomAttrs; missing key -> false; strict typed
//	  compare (number/string/bool), type mismatch -> false (except
//	  contains/regex on strings only).
//
// Minimal v1 set is FROZEN: platform, appVersion, locale/country,
// percentile+seed, flat custom attrs. No audiences/firstOpen/build rules,
// no significance math, no DB access in this package.
package eval

import (
	"encoding/json"
	"hash/fnv"
	"reflect"
	"regexp"
	"strings"
)

// Flag value types enforced by coerce: numbers accept any Go numeric,
// strings and bools require exact types, json accepts objects and arrays only.
const (
	TypeNumber = "number"
	TypeString = "string"
	TypeBool   = "bool"
	TypeJSON   = "json"
)

// Flag identifies a feature flag and its fallback when no rule matches.
type Flag struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Default any    `json:"default"`
}

// Condition is one predicate inside a rule (AND-ed with siblings).
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
	Seed  string `json:"seed,omitempty"`
}

// Rule is an ordered conditional value: first match wins.
type Rule struct {
	ID         string      `json:"id"`
	Conditions []Condition `json:"conditions"`
	Value      any         `json:"value"`
}

// Context carries the request attributes Evaluate matches rules against.
type Context struct {
	UserID         string         `json:"userId"`
	Platform       string         `json:"platform"`
	AppVersion     string         `json:"appVersion"`
	Locale         string         `json:"locale"`
	Country        string         `json:"country"`
	CustomAttrs    map[string]any `json:"customAttrs"`
	PercentileSeed string         `json:"percentileSeed"`
}

// Variant is one experiment arm: its Values map holds per-flag overrides
// keyed by flag key. Only EvaluateWithExperiment reads it; Assign/Bucket/Evaluate ignore it, so existing behavior is unchanged.
type Variant struct {
	Name      string         `json:"name"`
	WeightBps int            `json:"weightBps"`
	Values    map[string]any `json:"values,omitempty"`
}

// Experiment splits users across variants by Bucket(userID, Seed).
type Experiment struct {
	ID             string    `json:"id"`
	Seed           string    `json:"seed"`
	Variants       []Variant `json:"variants"`
	DefaultVariant string    `json:"defaultVariant"`
}

// Evaluate returns the first matching rule's value coerced to flag.Type,
// else flag.Default. Never panics, never errors.
func Evaluate(flag Flag, rules []Rule, ctx Context) any {
	for _, r := range rules {
		if !ruleMatches(r, ctx) {
			continue
		}
		if v, ok := coerce(r.Value, flag.Type); ok {
			return v
		}
	}
	return flag.Default
}

// Unknown field/op, bad semver, missing seed/attr -> false.
func MatchCondition(c Condition, ctx Context) bool {
	return matchCondition(c, ctx)
}

// Assign returns the sticky variant for userID, or DefaultVariant when
// userID is empty or Variants is empty.
func Assign(exp Experiment, userID string) string {
	if userID == "" || len(exp.Variants) == 0 {
		return exp.DefaultVariant
	}
	b := Bucket(userID, exp.Seed)
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

// Bucket returns int(fnv64a(userID+seed)%10000) in 0..9999, or -1 when
// userID or seed is empty.
func Bucket(userID, seed string) int {
	if userID == "" || seed == "" {
		return -1
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID + seed))
	return int(h.Sum64() % 10000)
}

// ruleMatches reports whether ALL conditions of r hold (AND).
// A rule with zero conditions vacuously matches; any invalid condition
// makes the rule non-matching. Never panics.
func ruleMatches(r Rule, ctx Context) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	for _, c := range r.Conditions {
		if !matchCondition(c, ctx) {
			return false
		}
	}
	return true
}

// matchCondition is the panic-guarded core of MatchCondition.
func matchCondition(c Condition, ctx Context) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	switch {
	case c.Field == "platform":
		return matchStringField(ctx.Platform, c.Op, c.Value)
	case c.Field == "appVersion":
		return matchVersionField(ctx.AppVersion, c.Op, c.Value)
	case c.Field == "locale":
		return matchStringField(ctx.Locale, c.Op, c.Value)
	case c.Field == "country":
		return matchStringField(ctx.Country, c.Op, c.Value)
	case c.Field == "percentile":
		return matchPercentile(c, ctx)
	case strings.HasPrefix(c.Field, "custom."):
		name := strings.TrimPrefix(c.Field, "custom.")
		if name == "" || ctx.CustomAttrs == nil {
			return false
		}
		attr, present := ctx.CustomAttrs[name]
		if !present {
			return false
		}
		return matchCustom(attr, c.Op, c.Value)
	default:
		// Unknown field (including bare "custom" without a name).
		return false
	}
}

// matchStringField handles platform/locale/country: exact case-sensitive
// ==/!=, substring contains, and regex. Non-string target -> false.
func matchStringField(actual, op string, target any) bool {
	t, ok := target.(string)
	if !ok {
		return false
	}
	switch op {
	case "==":
		return actual == t
	case "!=":
		return actual != t
	case "contains":
		return strings.Contains(actual, t)
	case "regex":
		return matchRegex(actual, t)
	default:
		return false
	}
}

// matchVersionField handles appVersion: relational ops on strict semver,
// raw-string contains/regex. Unparseable either side -> false.
func matchVersionField(actual, op string, target any) bool {
	t, ok := target.(string)
	if !ok {
		return false
	}
	switch op {
	case "contains":
		return strings.Contains(actual, t)
	case "regex":
		return matchRegex(actual, t)
	case "<", "<=", "==", "!=", ">=", ">":
		cmp, ok := compareSemver(actual, t)
		if !ok {
			return false
		}
		switch op {
		case "<":
			return cmp < 0
		case "<=":
			return cmp <= 0
		case "==":
			return cmp == 0
		case "!=":
			return cmp != 0
		case ">=":
			return cmp >= 0
		default: // ">"
			return cmp > 0
		}
	default:
		return false
	}
}

// matchPercentile buckets via Bucket(ctx.UserID, cond.Seed or
// ctx.PercentileSeed). Missing/empty seed or userID -> false.
// "<=" is inclusive; "between" [lo,hi] is inclusive on both ends.
func matchPercentile(c Condition, ctx Context) bool {
	seed := c.Seed
	if seed == "" {
		seed = ctx.PercentileSeed
	}
	b := Bucket(ctx.UserID, seed)
	if b < 0 || b > 9999 {
		return false
	}
	switch c.Op {
	case "<=":
		thr, ok := toNumber(c.Value)
		if !ok {
			return false
		}
		return b <= int(thr)
	case "between":
		lo, hi, ok := toNumberPair(c.Value)
		if !ok {
			return false
		}
		return b >= int(lo) && b <= int(hi)
	default:
		return false
	}
}

// matchCustom compares one flat custom attr with strict typing:
// numbers compare numerically, strings exactly (relational ops are
// lexicographic), bools only ==/!=. Type mismatch -> false; contains and
// regex require strings on both sides.
func matchCustom(attr any, op string, target any) bool {
	if op == "contains" || op == "regex" {
		a, ok := attr.(string)
		if !ok {
			return false
		}
		t, ok := target.(string)
		if !ok {
			return false
		}
		if op == "contains" {
			return strings.Contains(a, t)
		}
		return matchRegex(a, t)
	}
	if af, ok := toNumber(attr); ok {
		tf, ok := toNumber(target)
		if !ok {
			return false
		}
		switch op {
		case "==":
			return af == tf
		case "!=":
			return af != tf
		case "<":
			return af < tf
		case "<=":
			return af <= tf
		case ">":
			return af > tf
		case ">=":
			return af >= tf
		default:
			return false
		}
	}
	// String compare (exact, case-sensitive; relational lexicographic).
	if a, ok := attr.(string); ok {
		t, ok := target.(string)
		if !ok {
			return false
		}
		switch op {
		case "==":
			return a == t
		case "!=":
			return a != t
		case "<":
			return a < t
		case "<=":
			return a <= t
		case ">":
			return a > t
		case ">=":
			return a >= t
		default:
			return false
		}
	}
	// Bool compare (equality only).
	if a, ok := attr.(bool); ok {
		t, ok := target.(bool)
		if !ok {
			return false
		}
		switch op {
		case "==":
			return a == t
		case "!=":
			return a != t
		default:
			return false
		}
	}
	return false
}

// coerce checks rule value v against flag type t. Numbers accept any Go
// numeric (JSON float/int); strings and bools require their exact Go
// type; json accepts objects (string-keyed maps) and arrays (slices)
// only — scalars fall through. The second result reports acceptance.
func coerce(v any, t string) (any, bool) {
	switch t {
	case TypeNumber:
		if _, ok := toNumber(v); ok {
			return v, true
		}
		return nil, false
	case TypeString:
		if s, ok := v.(string); ok {
			return s, true
		}
		return nil, false
	case TypeBool:
		if b, ok := v.(bool); ok {
			return b, true
		}
		return nil, false
	case TypeJSON:
		if isJSONObject(v) || isJSONArray(v) {
			return v, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// isJSONObject reports whether v is a string-keyed map (any map type,
// so hand-built values beyond encoding/json also qualify).
func isJSONObject(v any) bool {
	if v == nil {
		return false
	}
	// Fast paths for the shapes encoding/json produces.
	switch v.(type) {
	case map[string]any:
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String
}

// isJSONArray reports whether v is a slice or array (but never a string
// or []byte, which are scalar-ish for flag purposes).
func isJSONArray(v any) bool {
	if v == nil {
		return false
	}
	switch v.(type) {
	case []any:
		return true
	case string, []byte:
		return false
	}
	k := reflect.ValueOf(v).Kind()
	return k == reflect.Slice || k == reflect.Array
}

// toNumber converts any Go numeric (all int/uint/float widths plus
// json.Number) to float64. Non-numerics -> false. Bools and strings
// are never numbers: "yes" and "42" both reject.
func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// toNumberPair extracts an inclusive [lo,hi] bound pair from the
// condition value: any slice/array of exactly two numerics (the
// encoding/json shape is []any{float64, float64}).
func toNumberPair(v any) (float64, float64, bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return 0, 0, false
	}
	if k := rv.Kind(); k != reflect.Slice && k != reflect.Array {
		return 0, 0, false
	}
	if rv.Len() != 2 {
		return 0, 0, false
	}
	lo, ok := toNumber(rv.Index(0).Interface())
	if !ok {
		return 0, 0, false
	}
	hi, ok := toNumber(rv.Index(1).Interface())
	if !ok {
		return 0, 0, false
	}
	return lo, hi, true
}

// matchRegex reports whether pattern matches anywhere in s. An invalid
// pattern never panics and never errors: it is simply false.
func matchRegex(s, pattern string) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

type semver struct {
	major, minor, patch int
	pre                 string // raw prerelease ("" = release)
}

// compareSemver compares two strict semver strings: numeric
// MAJOR.MINOR.PATCH, prerelease < release, build metadata ignored.
// Either side unparseable -> ok=false (callers treat as condition false).
//
// Hand-rolled in stdlib (no third-party semver dependency): the frozen
// vectors only need strict X.Y.Z with optional -prerelease/+build, so a
// dependency would add supply-chain surface for no behavioral gain.
func compareSemver(a, b string) (cmp int, ok bool) {
	va, ok := parseSemver(a)
	if !ok {
		return 0, false
	}
	vb, ok := parseSemver(b)
	if !ok {
		return 0, false
	}
	if va.major != vb.major {
		return cmpInt(va.major, vb.major), true
	}
	if va.minor != vb.minor {
		return cmpInt(va.minor, vb.minor), true
	}
	if va.patch != vb.patch {
		return cmpInt(va.patch, vb.patch), true
	}
	return comparePrerelease(va.pre, vb.pre), true
}

// parseSemver parses strict MAJOR.MINOR.PATCH with optional
// -prerelease and +build suffixes. Numeric identifiers must be digits
// with no leading zeros (unless the value is exactly "0"); prerelease
// and build identifiers must be non-empty dot-separated
// [0-9A-Za-z-] runs.
func parseSemver(s string) (semver, bool) {
	var v semver
	rest := s
	// Split off +build (ignored for precedence, but must be well-formed
	// to count as strict semver).
	if i := strings.Index(rest, "+"); i >= 0 {
		if !validDotIdents(rest[i+1:], false) {
			return v, false
		}
		rest = rest[:i]
	}
	if i := strings.Index(rest, "-"); i >= 0 {
		pre := rest[i+1:]
		if !validDotIdents(pre, true) {
			return v, false
		}
		v.pre = pre
		rest = rest[:i]
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, ok := parseSemverNum(p)
		if !ok {
			return v, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, true
}

func parseSemverNum(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<31-1 {
			return 0, false // absurdly large: not strict semver
		}
	}
	return n, true
}

// validDotIdents validates dot-separated prerelease/build identifier
// runs. Numeric prerelease identifiers additionally reject leading zeros
// per semver §9; build identifiers allow them.
func validDotIdents(s string, prerelease bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		numeric := true
		for i := 0; i < len(id); i++ {
			c := id[i]
			if c >= '0' && c <= '9' {
				continue
			}
			numeric = false
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
				return false
			}
		}
		if prerelease && numeric && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

// comparePrerelease orders prerelease strings: release ("") sorts after
// any prerelease; two prereleases compare identifier by identifier
// (numeric identifiers numerically, numeric < alphanumeric,
// alphanumeric lexically, fewer identifiers < more on prefix tie).
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1 // release > prerelease
	}
	if b == "" {
		return -1
	}
	ai, bi := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if ai[i] == bi[i] {
			continue
		}
		an, aok := prereleaseNum(ai[i])
		bn, bok := prereleaseNum(bi[i])
		switch {
		case aok && bok:
			return cmpInt(an, bn)
		case aok:
			return -1 // numeric < alphanumeric
		case bok:
			return 1
		default:
			if ai[i] < bi[i] {
				return -1
			}
			return 1
		}
	}
	return cmpInt(len(ai), len(bi))
}

func prereleaseNum(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
