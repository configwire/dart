package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHashUserKnownVector(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	want := hex.EncodeToString(sum[:])[:16]
	if got := HashUser("abc"); got != want {
		t.Fatalf("HashUser(abc) = %q, want %q", got, want)
	}
	if got := HashUser("abc"); len(got) != 16 {
		t.Fatalf("HashUser length = %d, want 16", len(got))
	}
}

func TestHashUserProps(t *testing.T) {
	a, b := HashUser("user-1"), HashUser("user-2")
	if a == b {
		t.Fatal("distinct users must hash distinctly")
	}
	if a == "user-1" || strings.Contains(a, "user") {
		t.Fatal("hash must not embed the input")
	}
	if HashUser("user-1") != a {
		t.Fatal("hash must be deterministic")
	}
	if got := HashUser(""); got != "" {
		t.Fatalf("empty input must map to empty (eval.HashUserID contract), got %q", got)
	}
}

func TestKeyPrefixHash(t *testing.T) {
	full := "t9qawkey-9f2c4d"
	if got := KeyPrefix(full); got != "t9qawkey" {
		t.Fatalf("KeyPrefix = %q, want %q", got, "t9qawkey")
	}
	if got := KeyPrefix("short"); got != "short" {
		t.Fatalf("short KeyPrefix = %q", got)
	}
	sum := sha256.Sum256([]byte(full))
	if got, want := KeyHash(full), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("KeyHash = %q, want %q", got, want)
	}
	if len(KeyHash(full)) != 64 {
		t.Fatal("KeyHash must be 64 hex chars")
	}
}

func TestResolveUserHash(t *testing.T) {
	if got := ResolveUserHash("explicit", "raw"); got != "explicit" {
		t.Fatalf("explicit userHash must win, got %q", got)
	}
	if got := ResolveUserHash("", "raw"); got != HashUser("raw") {
		t.Fatalf("userID must be hashed server-side, got %q", got)
	}
	if got := ResolveUserHash("", ""); got != "" {
		t.Fatalf("neither → empty, got %q", got)
	}
}

func body(t *testing.T, events []any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ev(kind, flag string) map[string]any {
	return map[string]any{"kind": kind, "flag": flag, "variant": "control", "userHash": "abcdef0123456789"}
}

func TestValidateBodyOK(t *testing.T) {
	events, aerr := ValidateBody(body(t, []any{ev("fetch", "f1"), ev("exposure", "f2")}))
	if aerr != nil {
		t.Fatalf("valid batch rejected: %v", aerr)
	}
	if len(events) != 2 || events[0].Kind != "fetch" || events[1].Flag != "f2" {
		t.Fatalf("unexpected decode: %+v", events)
	}
}

func TestValidateBodyMinimal(t *testing.T) {
	events, aerr := ValidateBody(body(t, []any{map[string]any{"kind": "fetch"}}))
	if aerr != nil {
		t.Fatalf("minimal event rejected: %v", aerr)
	}
	if events[0].Ts.IsZero() == false {
		t.Fatal("absent ts must decode to zero time (handler substitutes now)")
	}
}

func TestValidateBodyTooLarge(t *testing.T) {
	many := make([]any, MaxBatchEvents+1)
	for i := range many {
		many[i] = ev("fetch", "f")
	}
	_, aerr := ValidateBody(body(t, many))
	if aerr == nil || aerr.Status != 400 {
		t.Fatalf("101-batch must be 400, got %v", aerr)
	}
}

func TestValidateBodyRejectsPII(t *testing.T) {
	for _, key := range []string{"userId", "ip"} {
		e := ev("exposure", "f")
		e[key] = "raw-value"
		_, aerr := ValidateBody(body(t, []any{e}))
		if aerr == nil || aerr.Status != 400 {
			t.Fatalf("raw %q must be 400, got %v", key, aerr)
		}
	}
	// Null-valued keys are presence too.
	e := ev("exposure", "f")
	e["userId"] = nil
	if _, aerr := ValidateBody(body(t, []any{e})); aerr == nil || aerr.Status != 400 {
		t.Fatalf("null userId must be 400, got %v", aerr)
	}
}

func TestValidateBodyBogusKind(t *testing.T) {
	_, aerr := ValidateBody(body(t, []any{ev("bogus", "f")}))
	if aerr == nil || aerr.Status != 400 {
		t.Fatalf("bogus kind must be 400, got %v", aerr)
	}
}

func TestValidateBodyMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"non-json", "not json"},
		{"missing events", `{"foo":[]}`},
		{"null events", `{"events":null}`},
		{"empty batch", `{"events":[]}`},
		{"bad ts", bodyString(map[string]any{"kind": "fetch", "ts": "yesterday"})},
		{"numeric-string ts", bodyString(map[string]any{"kind": "fetch", "ts": true})},
	} {
		if _, aerr := ValidateBody([]byte(tc.raw)); aerr == nil || aerr.Status != 400 {
			t.Fatalf("%s must be 400, got %v", tc.name, aerr)
		}
	}
}

func bodyString(e map[string]any) string {
	b, _ := json.Marshal(map[string]any{"events": []any{e}})
	return string(b)
}

func TestValidateBodyTsShapes(t *testing.T) {
	rfs := bodyString(map[string]any{"kind": "fetch", "ts": "2026-09-22T10:00:00Z"})
	events, aerr := ValidateBody([]byte(rfs))
	if aerr != nil {
		t.Fatalf("RFC3339 ts rejected: %v", aerr)
	}
	if events[0].Ts.Year() != 2026 {
		t.Fatalf("bad ts parse: %v", events[0].Ts)
	}
	unix := bodyString(map[string]any{"kind": "exposure", "ts": 1726915200})
	events, aerr = ValidateBody([]byte(unix))
	if aerr != nil {
		t.Fatalf("unix ts rejected: %v", aerr)
	}
	if events[0].Ts.Unix() != 1726915200 {
		t.Fatalf("bad unix parse: %v", events[0].Ts)
	}
}

func TestValidateBodyOversizedEvent(t *testing.T) {
	e := ev("fetch", "f")
	e["variant"] = strings.Repeat("v", 70<<10) // >64KB single event
	_, aerr := ValidateBody(body(t, []any{e}))
	if aerr == nil || aerr.Status != 413 {
		t.Fatalf("oversized event must be 413, got %v", aerr)
	}
}

func TestSplitBatch(t *testing.T) {
	mk := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	chunks := SplitBatch(mk(1200), 500)
	if len(chunks) != 3 || len(chunks[0]) != 500 || len(chunks[1]) != 500 || len(chunks[2]) != 200 {
		t.Fatalf("bad chunks: %v", lens(chunks))
	}
	if got := SplitBatch(mk(100), 500); len(got) != 1 || len(got[0]) != 100 {
		t.Fatalf("small batch must be one chunk: %v", lens(got))
	}
	if got := SplitBatch([]int{}, 500); len(got) != 1 {
		t.Fatalf("empty batch: %v", lens(got))
	}
	joined := []int{}
	for _, c := range SplitBatch(mk(1200), 500) {
		joined = append(joined, c...)
	}
	for i, v := range joined {
		if v != i {
			t.Fatalf("order broken at %d", i)
		}
	}
}

func lens[T any](chunks [][]T) []int {
	out := make([]int, len(chunks))
	for i, c := range chunks {
		out[i] = len(c)
	}
	return out
}

func TestLimiterWindow(t *testing.T) {
	l := newLimiterWithWindow(50 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.Allow("k", 3) {
			t.Fatalf("token %d should be allowed", i)
		}
	}
	if l.Allow("k", 3) {
		t.Fatal("4th token over limit 3 must be denied")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("k", 3) {
		t.Fatal("new window must reset the count")
	}
	if !l.Allow("other", 3) {
		t.Fatal("distinct keys must have independent windows")
	}
	// Non-positive limit falls back to the default.
	l2 := newLimiterWithWindow(time.Minute)
	for i := 0; i < DefaultRateLimit; i++ {
		if !l2.Allow("k", 0) {
			t.Fatalf("default-limit token %d denied", i)
		}
	}
	if l2.Allow("k", 0) {
		t.Fatal("token 61 over default 60 must be denied")
	}
	if got := l2.Size(); got != 1 {
		t.Fatalf("limiter size = %d, want 1", got)
	}
}

func TestValidateBodyExactly100(t *testing.T) {
	many := make([]any, 100)
	for i := range many {
		many[i] = ev("fetch", fmt.Sprintf("f%d", i))
	}
	events, aerr := ValidateBody(body(t, many))
	if aerr != nil {
		t.Fatalf("100-batch must pass, got %v", aerr)
	}
	if len(events) != 100 {
		t.Fatalf("want 100 events, got %d", len(events))
	}
}

// TestHeaderKeyIsConfigWire guards the SDK key contract: the SDK key
// header is X-ConfigWire-Key, and RequireSDKKey reads ONLY this constant
// (single Header.Get site). Live 200/401 proof
// is in .omo/evidence/configwire-t5-wire.log.
func TestHeaderKeyIsConfigWire(t *testing.T) {
	if HeaderKey != "X-ConfigWire-Key" {
		t.Fatalf("HeaderKey = %q, want %q", HeaderKey, "X-ConfigWire-Key")
	}
}
