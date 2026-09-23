package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const (
	// StatusRunning overlays the assigned variant's values onto the base evaluation.
	StatusRunning = "running"
	// StatusDraft returns the base evaluation with DefaultVariant, skipping assignment.
	StatusDraft = "draft"
	// StatusStopped returns the base evaluation with DefaultVariant, skipping assignment.
	StatusStopped = "stopped"
)

// ValidateExperiment rejects weight tables that do not sum to exactly
// 10000 bps. No silent renormalize: callers must fix the weights.
func ValidateExperiment(exp Experiment) error {
	if len(exp.Variants) == 0 {
		return errors.New("eval: experiment has no variants")
	}
	sum := 0
	for _, v := range exp.Variants {
		if v.WeightBps < 0 {
			return errors.New("eval: negative weightBps")
		}
		sum += v.WeightBps
	}
	if sum != 10000 {
		return errors.New("eval: variant WeightBps must sum to 10000")
	}
	return nil
}

// Lookup rule: base = Evaluate(flag, rules, ctx). When status != "running"
// the base is returned untouched with variant = exp.DefaultVariant
// (covers draft, stopped, empty, and any unknown status string).
// When running, variant = Assign(exp, ctx.UserID); the assigned Variant's
// Values map is consulted by flag key: Values[flag.Key] present -> coerced
// to flag.Type via coerce and returned on success; absent or wrong-type ->
// base rule value unchanged (fallthrough, never an error).
//
// Never panics, never errors: invalid weights/empty variants simply fall
// back to the control path (base, DefaultVariant).
func EvaluateWithExperiment(flag Flag, rules []Rule, ctx Context, exp Experiment, status string) (value any, variant string) {
	base := Evaluate(flag, rules, ctx)
	if status != StatusRunning {
		return base, exp.DefaultVariant
	}
	variant = Assign(exp, ctx.UserID)
	for _, v := range exp.Variants {
		if v.Name != variant {
			continue
		}
		raw, ok := v.Values[flag.Key]
		if !ok {
			return base, variant
		}
		if coerced, ok := coerce(raw, flag.Type); ok {
			return coerced, variant
		}
		return base, variant
	}
	return base, variant
}

// Exposure is the input contract for T9 ingest persistence.
// JSON field names are frozen: env, flagKey, variant, userHash, timestamp.
// There is deliberately NO userId / ip / raw-identity field.
type Exposure struct {
	Env       string    `json:"env"`
	FlagKey   string    `json:"flagKey"`
	Variant   string    `json:"variant"`
	UserHash  string    `json:"userHash"`
	Timestamp time.Time `json:"timestamp"`
}

// HashUserID maps a raw user ID to its exposure identity:
// lowercase hex of sha256(userID) truncated to 16 chars.
// Empty input -> "" (caller decides whether to emit or drop).
func HashUserID(userID string) string {
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:16]
}

// ExposureEvent builds the PII-minimal exposure record T9 persists.
// Raw userID is hashed via HashUserID and never stored on the record.
func ExposureEvent(env, flagKey, variant, userID string, ts time.Time) Exposure {
	return Exposure{
		Env:       env,
		FlagKey:   flagKey,
		Variant:   variant,
		UserHash:  HashUserID(userID),
		Timestamp: ts,
	}
}
