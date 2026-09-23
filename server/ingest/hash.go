// PII-minimal rule: raw user IDs and network addresses are NEVER stored.
// Clients must send the opaque "userHash" field; when server-side code must
// derive one, HashUser applies sha256 and keeps hex[:16] (64 bits — enough
// to join fetch/exposure rows per user while being non-reversible).
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
)

// KeyPrefix returns the first 8 chars for prefilter lookup.
func KeyPrefix(fullKey string) string {
	if len(fullKey) > 8 {
		return fullKey[:8]
	}
	return fullKey
}

// KeyHash returns hex(sha256(fullKey)) for constant-time comparison. The full key itself is never persisted.
func KeyHash(fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(sum[:])
}

// HashUser maps a raw ID to its storable digest, dropping the input — only this digest reaches the events table.
// Empty input -> "" (matches eval.HashUserID's frozen contract exactly;
// either function satisfies the contract).
func HashUser(userID string) string {
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:16]
}
