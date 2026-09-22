// Hashing helpers for the ingest path (plan todo 9).
//
// PII-minimal rule: raw user IDs and network addresses are NEVER stored.
// Clients must send the opaque "userHash" field; when server-side code must
// derive one, HashUser applies sha256 and keeps hex[:16] (64 bits — enough
// to join fetch/exposure rows per user while being non-reversible).
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
)

// KeyPrefix returns the lookup prefix for a full SDK key: its first 8 chars.
// Short keys (<8 chars) yield the whole key; empty key yields "".
func KeyPrefix(fullKey string) string {
	if len(fullKey) > 8 {
		return fullKey[:8]
	}
	return fullKey
}

// KeyHash returns lowercase hex(sha256(fullKey)), the stored comparator for
// sdk_keys.hash. The full key itself is never persisted.
func KeyHash(fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(sum[:])
}

// HashUser derives the storable per-user bucket from a raw user ID:
// lowercase hex(sha256(userID))[:16]. Raw IDs must be hashed at the edge and
// dropped — only this digest reaches the events table.
// Empty input -> "" (matches eval.HashUserID's frozen contract exactly;
// either function may be used by later todos).
func HashUser(userID string) string {
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:16]
}
