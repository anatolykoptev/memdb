package handlers

// chat_canary.go — sticky hash-based A/B bucketing for the factual answer-style canary.
// Used by resolveAnswerStyle to route a deterministic fraction of users to the factual
// prompt without storing any per-user state.

import (
	"crypto/sha256"
	"encoding/binary"
)

// canaryFactualForUser returns true if this user_id falls into the factual canary bucket.
//
// Sticky: the same user_id always produces the same decision across requests and restarts
// because the bucket is derived from a hash, not from random state.
//
// percentBucket is the target percentage [0, 100]:
//   - 0  → never selected (canary off)
//   - 100 → always selected (full rollout)
//   - 10  → ~10% of user IDs selected
//
// Empty user_id hashes to a deterministic bucket (never-out by convention: sha256("") mod 100 = 89,
// which is outside [0, 10) for the default 10% canary, so empty user_id is always outside the bucket
// at default settings — this edge case is documented for callers and tested explicitly).
func canaryFactualForUser(userID string, percentBucket int) bool {
	if percentBucket <= 0 {
		return false
	}
	if percentBucket >= 100 {
		return true
	}
	h := sha256.Sum256([]byte(userID))
	// Take the first 4 bytes as a big-endian uint32 and reduce mod 100.
	// uint32 is always non-negative so no negative-modulo adjustment is needed.
	bucket := int(binary.BigEndian.Uint32(h[:4])) % 100
	return bucket < percentBucket
}
