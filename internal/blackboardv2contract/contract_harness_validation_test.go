package blackboardv2contract_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
)

func mustHarness(t *testing.T) *blackboardv2contract.Harness {
	t.Helper()
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatalf("load v2 contract harness: %v", err)
	}
	return harness
}

func TestHarnessRejectsAuthoritySmugglingUnknownFieldsAndUTF8Oversize(t *testing.T) {
	harness := mustHarness(t)

	tests := []struct {
		name       string
		schemaName string
		value      string
	}{
		{
			name:       "finish has no summary copy",
			schemaName: "finishRequest",
			value:      `{"idempotency_key":"finish","summary":"forbidden Task Summary"}`,
		},
		{
			name:       "semantic batch has server-owned authority",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"smuggle","changes":[],"project_id":"other-project"}`,
		},
		{
			name:       "Blackboard Key is at most 96 ASCII characters",
			schemaName: "readRequest",
			value:      `{"key":"` + strings.Repeat("k", 97) + `"}`,
		},
		{
			name:       "primary semantic text is at most 1024 UTF-8 bytes",
			schemaName: "checkpointAttemptRequest",
			value:      `{"idempotency_key":"checkpoint","key":"attempt:unicode","version":1,"summary":"` + strings.Repeat("界", 342) + `"}`,
		},
		{
			name:       "relationship reason is at most 512 UTF-8 bytes",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"reason","changes":[{"op":"relate","from":"fact:a","relation":"supports","to":"fact:b","reason":"` + strings.Repeat("界", 171) + `"}]}`,
		},
		{
			name:       "history records remain typed and closed",
			schemaName: "semanticHistory",
			value:      `{"schema":"semantic-history/v2","revision":2,"key":"fact:a","items":[{"version":1,"type":"fact","record":{"unknown":"storage leak"}}]}`,
		},
		{
			name:       "update clear list is type-specific",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"clear","changes":[{"op":"update","key":"entity:a","version":1,"type":"entity","record":{"name":"A"},"clear":["body"]}]}`,
		},
		{
			name:       "merge clear list is closed",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"merge-clear","changes":[{"op":"merge","source":"fact:a","source_version":1,"canonical":"fact:b","canonical_version":1,"clear":["storage_id"]}]}`,
		},
		{
			name:       "merge clear is never null",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"merge-null-clear","changes":[{"op":"merge","source":"fact:a","source_version":1,"canonical":"fact:b","canonical_version":1,"clear":null}]}`,
		},
		{
			name:       "merge clear entries are unique",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"merge-duplicate-clear","changes":[{"op":"merge","source":"fact:a","source_version":1,"canonical":"fact:b","canonical_version":1,"clear":["body","body"]}]}`,
		},
		{
			name:       "relationship version changes a supplied reason",
			schemaName: "changeBatch",
			value:      `{"schema":"semantic-change-batch/v2","idempotency_key":"relation-version","changes":[{"op":"relate","from":"fact:a","relation":"supports","to":"fact:b","version":1}]}`,
		},
		{
			name:       "empty snapshot groups are omitted",
			schemaName: "runtimeSnapshot",
			value:      `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":0,"work":{"objectives":{}},"knowledge":{},"relations":[]}`,
		},
		{
			name:       "verified snapshot Solution retains verification meaning",
			schemaName: "runtimeSnapshot",
			value:      `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":1,"work":{},"knowledge":{"solutions":{"solution:a":{"version":1,"status":"verified","kind":"flag","summary":"Candidate"}}},"relations":[]}`,
		},
		{
			name:       "confirmed snapshot Finding cannot remain CVSS pending",
			schemaName: "runtimeSnapshot",
			value:      `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":1,"work":{},"knowledge":{"findings":{"finding:a":{"version":1,"status":"confirmed","title":"Issue","cvss_pending":true}}},"relations":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := harness.Validate(tt.schemaName, []byte(tt.value)); err == nil {
				t.Fatalf("%s unexpectedly accepted forbidden wire shape", tt.schemaName)
			}
		})
	}

	valid := `{"idempotency_key":"checkpoint","key":"attempt:unicode","version":1,"summary":"` + strings.Repeat("界", 341) + `"}`
	if err := harness.Validate("checkpointAttemptRequest", []byte(valid)); err != nil {
		t.Fatalf("1023-byte semantic text rejected: %v", err)
	}
}

func TestInputExtensionErrorPathIsDeterministicWhenMultipleFieldsExceedByteLimits(t *testing.T) {
	value := map[string]any{
		"summary":       strings.Repeat("界", 342),
		"artifact_type": strings.Repeat("界", 171),
	}
	for replay := 0; replay < 100; replay++ {
		err := blackboardv2contract.ValidateInputExtensions(value)
		extensionErr, ok := err.(*blackboardv2contract.InputExtensionError)
		if !ok {
			t.Fatalf("replay %d extension error = %T %v", replay, err, err)
		}
		if extensionErr.Path != "$.artifact_type" {
			t.Fatalf("replay %d extension path = %q, want stable lexical first path", replay, extensionErr.Path)
		}
	}
}

func TestFixturesReturnStableCompactJSONBytes(t *testing.T) {
	harness := mustHarness(t)
	for _, name := range harness.FixtureNames() {
		first, err := harness.Fixture(name)
		if err != nil {
			t.Fatalf("load fixture %s: %v", name, err)
		}
		second, err := harness.Fixture(name)
		if err != nil {
			t.Fatalf("reload fixture %s: %v", name, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("fixture %s is not byte-stable", name)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, first); err != nil {
			t.Errorf("fixture %s is invalid JSON: %v", name, err)
			continue
		}
		if !bytes.Equal(first, compact.Bytes()) {
			t.Errorf("fixture %s is not exact compact JSON", name)
		}
	}
}

func TestSemanticErrorBodyAlwaysMatchesTheClosedV2Envelope(t *testing.T) {
	harness := mustHarness(t)
	raw, err := json.Marshal(blackboardv2.Error{
		Code: "semantic_validation", Message: "Attempt requires a tested target", Retryable: false,
	})
	if err != nil {
		t.Fatalf("marshal semantic error: %v", err)
	}
	if err := harness.Validate("errorBody", raw); err != nil {
		t.Fatalf("semantic error body violates v2 contract: %v\n%s", err, raw)
	}
}
