package blackboardv2contract_test

import "testing"

func TestHarnessRejectsAmbiguousJSON(t *testing.T) {
	harness := mustHarness(t)
	valid := `{"schema":"semantic-change-batch/v2","idempotency_key":"strict-json","changes":[{"op":"create","key":"entity:host","type":"entity","record":{"status":"active","kind":"host","name":"Host","scope_status":"in_scope"}}]}`
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "one document", raw: valid, want: true},
		{name: "trailing space", raw: valid + " \n\t", want: true},
		{name: "second object", raw: valid + `{}`},
		{name: "second scalar", raw: valid + ` true`},
		{name: "trailing invalid JSON", raw: valid + ` !`},
		{name: "empty input"},
		{name: "incomplete object", raw: `{"schema":`},
		{name: "duplicate root field", raw: `{"schema":"semantic-change-batch/v2",` + valid[1:]},
		{name: "duplicate nested field", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"strict-json","changes":[{"op":"create","key":"entity:host","type":"entity","record":{"status":"active","kind":"host","name":"First","name":"Host","scope_status":"in_scope"}}]}`},
		{name: "escaped duplicate field", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"strict-json","changes":[{"op":"create","key":"entity:host","type":"entity","record":{"status":"active","kind":"host","name":"First","n\u0061me":"Host","scope_status":"in_scope"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := harness.Validate("changeBatch", []byte(test.raw))
			if (err == nil) != test.want {
				t.Fatalf("Validate() = %v; want valid %t", err, test.want)
			}
		})
	}
}
