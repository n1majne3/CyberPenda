package blackboardv2input

import (
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
)

const validChangeInput = `{"schema":"semantic-change-batch/v2","idempotency_key":"input-test","changes":[{"op":"create","key":"entity:host","type":"entity","record":{"status":"active","kind":"host","name":"Host","scope_status":"in_scope"}}]}`

func TestDecodeContractInputRejectsDuplicateFields(t *testing.T) {
	var valid blackboardv2.ChangeBatch
	if err := DecodeContractInput("changeBatch", false, json.RawMessage(validChangeInput), &valid); err != nil {
		t.Fatalf("DecodeContractInput() rejected valid input: %v", err)
	}
	for _, test := range []struct{ name, raw string }{
		{"root", `{"schema":"semantic-change-batch/v2",` + validChangeInput[1:]},
		{"nested record", strings.Replace(validChangeInput, `"kind":"host"`, `"kind":"invalid","kind":"host"`, 1)},
		{"array item", strings.Replace(validChangeInput, `"op":"create"`, `"op":"invalid","op":"create"`, 1)},
		{"escaped field", strings.Replace(validChangeInput, `"kind":"host"`, `"kind":"host","k\u0069nd":"host"`, 1)},
		{"case-folded field", strings.Replace(validChangeInput, `"kind":"host"`, `"kind":"host","Kind":"host"`, 1)},
		{"Unicode case-folded field", strings.Replace(validChangeInput, `"key":"entity:host"`, `"key":"entity:host","Key":"entity:host"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target blackboardv2.ChangeBatch
			err := DecodeContractInput("changeBatch", false, json.RawMessage(test.raw), &target)
			assertInvalidJSON(t, err)
			if target.Schema != "" || target.Changes != nil {
				t.Fatalf("DecodeContractInput() changed the target: %#v", target)
			}
		})
	}
}

func TestDecodeContractInputRejectsMalformedJSON(t *testing.T) {
	for _, raw := range []string{`{`, `{"schema":`, validChangeInput + `{}`, validChangeInput + ` true`, validChangeInput + ` !`} {
		var target blackboardv2.ChangeBatch
		assertInvalidJSON(t, DecodeContractInput("changeBatch", false, json.RawMessage(raw), &target))
	}
	var target blackboardv2.ChangeBatch
	err := DecodeContractInput("changeBatch", false, nil, &target)
	if err == nil || err.Code != "invalid_schema" || err.Details["reason"] == "invalid_json" {
		t.Fatalf("DecodeContractInput() empty input = %#v; want a Schema error for an empty object", err)
	}
}

func assertInvalidJSON(t *testing.T, err *blackboardv2.Error) {
	t.Helper()
	if err == nil || err.Code != "invalid_schema" || err.Path != "arguments" || err.Retryable || err.Details["reason"] != "invalid_json" {
		t.Fatalf("DecodeContractInput() error = %#v; want invalid_schema at arguments with reason invalid_json", err)
	}
}

func TestRejectDuplicateJSONFieldsAtAnyDepth(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: `{}`, want: true},
		{raw: `[]`, want: true},
		{raw: `null`, want: true},
		{raw: `1.0000000000000001`, want: true},
		{raw: `[{"key":1},{"key":2}]`, want: true},
		{raw: `{"items":[[{"key":1,"key":2}]]}`},
		{raw: `{"items":[[{"scope_status":1,"scope_ſtatus":2}]]}`},
		{raw: `{"items":[`},
		{raw: `{"items":{`},
	} {
		t.Run(test.raw, func(t *testing.T) {
			if err := rejectDuplicateJSONFields([]byte(test.raw)); (err == nil) != test.want {
				t.Fatalf("rejectDuplicateJSONFields() = %v; want valid %t", err, test.want)
			}
		})
	}
}
