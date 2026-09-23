// Package ingest implements the analytics events write path (plan todo 9).
//
// Accepted JSON (defined here because T7's server/eval/experiment.go has not
// landed yet; T7 is parallel-disjoint, coordinate via notepad not edits):
//
//	POST /api/v1/env/:env/events
//	Header: X-ConfigWire-Key: <full sdk key>
//	Body: {"events":[{"kind":"fetch|exposure","flag":"<flag key>","variant":"<name>",
//	    "userHash":"<opaque>", "ts":"2026-09-22T00:00:00Z | 1726870000"}]}
//	flag/variant/userHash/ts are all optional; ts accepts RFC3339 string or
//	unix-seconds number, absent means server time. Raw "userId"/"ip" keys are
//	STRICTLY REJECTED (400) — PII must never reach storage.
//
// SDK key format (contract for T10/T11/T16):
//   - The full key is an opaque string presented in X-ConfigWire-Key.
//   - sdk_keys rows store prefix = first 8 chars of the full key (fast
//     prefilter) and hash = lowercase hex(sha256(fullKey)) (constant-time
//     comparison). The full key is never stored.
//   - Unknown, mismatched, revoked, or env-mismatched keys → 401.
//   - Rate limiting is per-key fixed-window: sdk_keys.rateLimit requests per
//     60s window, default 60 when missing/non-positive. Exhausted → 429.
//     Every authenticated hit to this endpoint consumes one token, including
//     requests later rejected as 400 (documented choice: simplest accounting,
//     no free probing).
//   - Batching: valid events enqueue to a buffered channel and the handler
//     returns 202 immediately. A background goroutine flushes every 1s or
//     every 500 rows (whichever first) via app.Save, so worst-case ingest
//     lag is ~1s + write time (bounded ≤2s). Batch sizes >100 → 400.
package ingest

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// Tunables for the ingest path.
const (
	// HeaderKey carries the full SDK key.
	HeaderKey = "X-ConfigWire-Key"
	// MaxBatchEvents bounds a single request body; larger → 400.
	MaxBatchEvents = 100
	// MaxBodyBytes bounds the whole request body; larger → 413.
	MaxBodyBytes = 128 << 10
	// MaxEventBytes bounds a single event object; larger → 413.
	MaxEventBytes = 64 << 10
	// DefaultRateLimit applies when sdk_keys.rateLimit is missing/non-positive.
	DefaultRateLimit = 60
	// RateWindow is the fixed window for per-key rate limiting.
	RateWindow = time.Minute
)

// EventIn is one decoded event from the request body.
type EventIn struct {
	Kind     string
	Flag     string
	Variant  string
	UserHash string
	Ts       time.Time // zero when absent (handler substitutes time.Now)
}

// apiError carries an HTTP status through validation so the handler can
// respond without branching on message strings.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

func badRequest(msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Msg: msg}
}

func entityTooLarge(msg string) *apiError {
	return &apiError{Status: http.StatusRequestEntityTooLarge, Msg: msg}
}

// rawEvent mirrors the wire shape; Ts stays raw for strict parsing and
// UserID/IP are deliberately absent — their presence is detected on the
// raw map first (strict reject, never decoded into a storable field).
type rawEvent struct {
	Kind     string          `json:"kind"`
	Flag     string          `json:"flag"`
	Variant  string          `json:"variant"`
	UserHash string          `json:"userHash"`
	Ts       json.RawMessage `json:"ts"`
}

// ValidateBody parses and validates a request body purely (no I/O), so it is
// unit-testable. It enforces: non-empty JSON with an "events" array,
// 1..MaxBatchEvents items, per-event ≤ MaxEventBytes, kind ∈
// {fetch,exposure}, no raw "userId"/"ip" keys, parseable ts.
func ValidateBody(body []byte) ([]EventIn, *apiError) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, badRequest("empty body: expected {\"events\":[...]}.")
	}
	var top struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, badRequest("malformed JSON: " + err.Error())
	}
	if top.Events == nil {
		return nil, badRequest("missing required field \"events\".")
	}
	if len(top.Events) == 0 {
		return nil, badRequest("empty batch: at least one event is required.")
	}
	if len(top.Events) > MaxBatchEvents {
		return nil, badRequest("batch too large: max 100 events per request.")
	}
	out := make([]EventIn, 0, len(top.Events))
	for i, raw := range top.Events {
		ev, aerr := validateRawEvent(raw)
		if aerr != nil {
			aerr.Msg = jsonErrorPrefix(i) + aerr.Msg
			return nil, aerr
		}
		out = append(out, ev)
	}
	return out, nil
}

func jsonErrorPrefix(i int) string {
	return "events[" + strconv.Itoa(i) + "]: "
}

// validateRawEvent validates one event object purely.
func validateRawEvent(raw json.RawMessage) (EventIn, *apiError) {
	var ev EventIn
	if len(raw) > MaxEventBytes {
		return ev, entityTooLarge("event exceeds 64KB.")
	}
	// Strict PII guard: detect raw userId/ip keys on the untyped map so
	// that even null-valued keys are rejected (presence, not value).
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ev, badRequest("malformed event JSON: " + err.Error())
	}
	if _, ok := m["userId"]; ok {
		return ev, badRequest("raw \"userId\" is forbidden: send \"userHash\" instead.")
	}
	if _, ok := m["ip"]; ok {
		return ev, badRequest("raw \"ip\" is forbidden and never stored.")
	}
	var r rawEvent
	if err := json.Unmarshal(raw, &r); err != nil {
		return ev, badRequest("malformed event JSON: " + err.Error())
	}
	if r.Kind != "fetch" && r.Kind != "exposure" {
		return ev, badRequest("invalid kind: must be \"fetch\" or \"exposure\".")
	}
	ts, aerr := parseTs(r.Ts)
	if aerr != nil {
		return ev, aerr
	}
	ev = EventIn{Kind: r.Kind, Flag: r.Flag, Variant: r.Variant, UserHash: r.UserHash, Ts: ts}
	return ev, nil
}

// parseTs accepts absent/null (zero time = server time), RFC3339 strings,
// and unix-seconds numbers (int or float). Anything else → 400.
func parseTs(raw json.RawMessage) (time.Time, *apiError) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return time.Time{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parsed, perr := time.Parse(time.RFC3339Nano, s)
		if perr != nil {
			if parsed, perr = time.Parse(time.RFC3339, s); perr != nil {
				return time.Time{}, badRequest("invalid ts: must be RFC3339 or unix seconds.")
			}
		}
		return parsed.UTC(), nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		sec, nsec := int64(f), int64((f-float64(int64(f)))*1e9)
		return time.Unix(sec, nsec).UTC(), nil
	}
	return time.Time{}, badRequest("invalid ts: must be RFC3339 or unix seconds.")
}

// ResolveUserHash picks the storable hash: an explicit userHash wins;
// otherwise a (server-side only) userID is hashed; neither → "".
// NOTE: the HTTP path rejects raw userId before this is ever reached, so the
// userID branch exists for completeness/testability and future server-side
// callers — raw PII from clients is never stored.
func ResolveUserHash(userHash, userID string) string {
	if userHash != "" {
		return userHash
	}
	if userID != "" {
		return HashUser(userID)
	}
	return ""
}

// RequireSDKKey authenticates X-ConfigWire-Key against sdk_keys (exported for
// T10 reuse). Lookup prefilters on prefix (first 8 chars) then compares
// hex(sha256(fullKey)) in constant time. Unknown/missing/revoked → 401 error
// suitable for returning directly from a handler.
func RequireSDKKey(re *core.RequestEvent) (*core.Record, error) {
	denied := func() (*core.Record, error) {
		return nil, re.UnauthorizedError("Missing or invalid SDK key.", nil)
	}
	full := re.Request.Header.Get(HeaderKey)
	if full == "" {
		return denied()
	}
	prefix, want := KeyPrefix(full), KeyHash(full)
	recs, err := re.App.FindAllRecords("sdk_keys")
	if err != nil {
		return denied()
	}
	// O(n) scan over sdk_keys is fine at ConfigWire scale (tens of keys);
	// revisit with an indexed prefix query if key counts ever grow.
	for _, r := range recs {
		if r.GetString("prefix") != prefix {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(r.GetString("hash")), []byte(want)) != 1 {
			continue
		}
		if r.GetBool("revoked") {
			return denied()
		}
		return r, nil
	}
	return denied()
}

// RateLimitFor returns the per-minute quota for a key record, defaulting to
// DefaultRateLimit when the field is missing or non-positive.
func RateLimitFor(key *core.Record) int {
	if n := key.GetInt("rateLimit"); n > 0 {
		return n
	}
	return DefaultRateLimit
}

// readBody caps the request body purely at the HTTP layer: over-limit → 413.
func readBody(re *core.RequestEvent) ([]byte, *apiError) {
	body, err := io.ReadAll(io.LimitReader(re.Request.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, badRequest("unreadable body: " + err.Error())
	}
	if len(body) > MaxBodyBytes {
		return nil, entityTooLarge("body exceeds 128KB.")
	}
	return body, nil
}
