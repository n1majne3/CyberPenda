package projectinterface

import "encoding/json"

// jsonRaw wraps canonical graph bytes so they serialize as the decoded JSON
// object rather than a base64 string. CanonicalMainGraphV1 bytes are already
// canonical JSON (graph contract projection), so embedding them raw preserves
// byte-for-byte equivalence with the on-disk projection.
func jsonRaw(bytes []byte) json.RawMessage {
	if len(bytes) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(bytes))
	copy(out, bytes)
	return out
}
