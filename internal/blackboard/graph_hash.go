package blackboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
)

// canonicalJSON encodes a value with canonical JSON v1 rules (storage contract
// §11.3): UTF-8, no insignificant whitespace, lexicographically ordered object
// keys, deterministic arrays, HTML escaping disabled, explicit nulls where
// required. It is the minimal form needed for C02 result/request hashing;
// CanonicalMainGraphV1 (C09) hardens the full document renderer.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; the canonical form has none
	// except where a single LF terminator is explicitly required (reports).
	out := bytes.TrimSpace(buf.Bytes())
	out = canonicalizeJSONKeys(out)
	return out, nil
}

// canonicalizeJSONKeys re-serializes JSON with sorted object keys. Go's
// encoding/json already preserves struct field order and sorts map keys
// lexicographically by bytes for map[string]any, so for struct-driven domain
// types the encoder output is already canonical aside from key ordering inside
// arbitrary maps. This helper walks map[string]any recursively to guarantee
// sorted keys everywhere.
func canonicalizeJSONKeys(data []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return data
	}
	v = sortKeysRecursive(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSpace(buf.Bytes())
}

func sortKeysRecursive(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortKeysRecursive(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = sortKeysRecursive(t[i])
		}
		return t
	}
	return v
}

// frame prepends a uint64 big-endian length prefix (storage contract §11.3).
func frame(b []byte) []byte {
	out := make([]byte, 8+len(b))
	binary.BigEndian.PutUint64(out, uint64(len(b)))
	copy(out[8:], b)
	return out
}

// framedHash computes H(domain, parts...) = SHA256(frame(domain) || frame(p1) || ...)
// per storage contract §11.3. No concatenation is unframed.
func framedHash(domain string, parts ...[]byte) []byte {
	h := sha256.New()
	h.Write(frame([]byte(domain)))
	for _, p := range parts {
		h.Write(frame(p))
	}
	return h.Sum(nil)
}

// u64Bytes returns the 8-byte big-endian encoding of an unsigned integer
// (storage contract §11.3).
func u64Bytes(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}

// nullableBytes prefixes a presence byte (storage contract §11.3: nullable
// fields are preceded by one presence byte).
func nullableBytes(present bool, b []byte) []byte {
	if present {
		return append([]byte{1}, b...)
	}
	return []byte{0}
}

// genesisHash is the per-Project first-mutation previous hash (storage
// contract §11.3).
func genesisHash(projectID string) []byte {
	return framedHash("CyberPenda.Blackboard.Genesis.v1", []byte(projectID))
}
