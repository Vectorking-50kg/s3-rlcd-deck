// Package sessionidentity owns the one-way identifiers shared by Codex
// adapters. Raw upstream identifiers never leave their adapter boundary.
package sessionidentity

import (
	"crypto/sha256"
	"encoding/hex"
)

// Codex returns the stable anonymous identifier used for one Codex session.
func Codex(upstreamID string) string {
	digest := sha256.Sum256([]byte(upstreamID))
	return "codex_" + hex.EncodeToString(digest[:8])
}
