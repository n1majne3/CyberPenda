package runtimeoutput

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// StableProviderItemID returns one deterministic projection id for a provider
// item and row kind. It is stable across Event windows and runtime-tail merges.
func StableProviderItemID(providerItemID, rowKind string) string {
	providerItemID = strings.TrimSpace(providerItemID)
	if providerItemID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(providerItemID))
	return "provider-item-" + hex.EncodeToString(sum[:]) + "-" + strings.TrimSpace(rowKind)
}
