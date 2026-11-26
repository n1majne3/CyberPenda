package blackboardv2contract_test

import (
	"encoding/json"
	"testing"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
)

func TestRecordWireSchemasExposeLegalPositiveTransitionsAndRequireRetainedEvidence(t *testing.T) {
	harness := mustHarness(t)

	accepted := []string{
		`{"schema":"semantic-change-batch/v2","idempotency_key":"confirm-fact","changes":[{"op":"transition","key":"fact:a","version":1,"status":"confirmed"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"confirm-finding","changes":[{"op":"transition","key":"finding:a","version":1,"status":"confirmed"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"verify-solution","changes":[{"op":"transition","key":"solution:a","version":1,"status":"verified","verification_summary":"Accepted by the challenge"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"merge-facts","changes":[{"op":"merge","source":"fact:duplicate","source_version":2,"canonical":"fact:canonical","canonical_version":3,"canonical_record":{"summary":"Approved consolidated fact"},"clear":["body"]}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"unrelate-old-key","changes":[{"op":"unrelate","from":"entity:old","relation":"part_of","to":"entity:parent","version":2}]}`,
	}
	for _, raw := range accepted {
		if err := harness.Validate("changeBatch", []byte(raw)); err != nil {
			t.Errorf("legal record change rejected: %v\n%s", err, raw)
		}
	}

	rejected := []string{
		`{"schema":"semantic-change-batch/v2","idempotency_key":"create-evidence","changes":[{"op":"create","key":"evidence:a","type":"evidence","record":{"status":"available","artifact_type":"file","summary":"Captured response","source_path":"captures/response.txt"}}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"invalid-entity-kind","changes":[{"op":"create","key":"entity:a","type":"entity","record":{"status":"active","kind":"application","name":"API","scope_status":"in_scope"}}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"incomplete-finding","changes":[{"op":"create","key":"finding:a","type":"finding","record":{"status":"confirmed","title":"Incomplete"}}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"incomplete-solution","changes":[{"op":"create","key":"solution:a","type":"solution","record":{"status":"verified","kind":"flag","summary":"Recovered a candidate"}}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"smuggle-evidence-derived","changes":[{"op":"create","key":"evidence:a","type":"evidence","record":{"status":"available","artifact_type":"file","summary":"Captured response","source_path":"captures/response.txt","managed_path":"evidence/response.txt","sha256":"0000000000000000000000000000000000000000000000000000000000000000","size":10}}]}`,
	}
	for _, raw := range rejected {
		if err := harness.Validate("changeBatch", []byte(raw)); err == nil {
			t.Errorf("invalid or caller-derived record change was accepted:\n%s", raw)
		}
	}
}

func TestAcceptedWriteContractsAlwaysDecodeIntoTheirGoDTOs(t *testing.T) {
	harness := mustHarness(t)
	tests := []struct {
		name        string
		schemaName  string
		raw         string
		destination func() any
	}{
		{
			name: "semantic change batch", schemaName: "changeBatch",
			raw:         `{"schema":"semantic-change-batch/v2","idempotency_key":"dto-change","changes":[{"op":"create","key":"objective:dto","type":"objective","record":{"status":"open","objective":"Keep Contract and DTO aligned."}}]}`,
			destination: func() any { return &blackboardv2.ChangeBatch{} },
		},
		{
			name: "retained Evidence", schemaName: "retainEvidenceRequest",
			raw:         `{"idempotency_key":"dto-evidence","key":"evidence:dto","attempt":"attempt:dto","source_path":"captures/dto.txt","artifact_type":"text","summary":"DTO parity sample"}`,
			destination: func() any { return &blackboardv2.RetainEvidenceRequest{} },
		},
		{
			name: "Attempt checkpoint", schemaName: "checkpointAttemptRequest",
			raw:         `{"idempotency_key":"dto-checkpoint","key":"attempt:dto","version":1,"summary":"Contract and DTO checkpoint parity."}`,
			destination: func() any { return &blackboardv2.CheckpointAttemptRequest{} },
		},
		{
			name: "Continuation finish", schemaName: "finishRequest",
			raw:         `{"idempotency_key":"dto-finish"}`,
			destination: func() any { return &blackboardv2.FinishContinuationRequest{} },
		},
		{
			name: "Attempt result", schemaName: "attemptResultRequest",
			raw: `{"idempotency_key":"dto-attempt","result":{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:dto","create":true,"summary":"Tested DTO parity.","outcome":"failed"},"tested_targets":[{"key":"objective:dto","create_objective":{"objective":"Keep Attempt Result aligned."}}],"produced_targets":[]}}`,
			destination: func() any {
				return &struct {
					IdempotencyKey string                                    `json:"idempotency_key"`
					Result         blackboardconclusion.RuntimeAttemptResult `json:"result"`
				}{}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			if err := harness.Validate(tt.schemaName, raw); err != nil {
				t.Fatalf("contract rejected parity sample: %v", err)
			}
			if err := json.Unmarshal(raw, tt.destination()); err != nil {
				t.Fatalf("DTO rejected Contract-accepted input: %v", err)
			}
		})
	}
}

func TestContractIntegerFormsDecodeAcrossEveryVersionedWriteDTO(t *testing.T) {
	harness := mustHarness(t)
	tests := []struct {
		name       string
		schemaName string
		raw        string
		decode     func([]byte) error
	}{
		{
			name: "change version", schemaName: "changeBatch",
			raw:    `{"schema":"semantic-change-batch/v2","idempotency_key":"decimal-change","changes":[{"op":"transition","key":"attempt:decimal","version":1.0,"status":"failed","summary":"Decimal integer form."}]}`,
			decode: func(raw []byte) error { return json.Unmarshal(raw, &blackboardv2.ChangeBatch{}) },
		},
		{
			name: "Evidence version", schemaName: "retainEvidenceRequest",
			raw:    `{"idempotency_key":"decimal-evidence","key":"evidence:decimal","version":1.0,"attempt":"attempt:decimal","source_path":"captures/decimal.txt","artifact_type":"text","summary":"Decimal integer form."}`,
			decode: func(raw []byte) error { return json.Unmarshal(raw, &blackboardv2.RetainEvidenceRequest{}) },
		},
		{
			name: "Attempt result revisions", schemaName: "attemptResultRequest",
			raw: `{"idempotency_key":"decimal-attempt","result":{"schema":"runtime-attempt-result/v1","base_revision":1.0,"attempt":{"key":"attempt:decimal","expected_version":1.0,"summary":"Decimal integer form.","outcome":"failed"},"tested_targets":[{"key":"objective:decimal","expected_version":1.0}],"produced_targets":[]}}`,
			decode: func(raw []byte) error {
				var envelope struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(raw, &envelope); err != nil {
					return err
				}
				_, err := blackboardconclusion.Decode(envelope.Result)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			if err := harness.Validate(tt.schemaName, raw); err != nil {
				t.Fatalf("Contract rejected a valid JSON Schema integer: %v", err)
			}
			if err := tt.decode(raw); err != nil {
				t.Fatalf("DTO rejected a Contract-accepted integer form: %v", err)
			}
		})
	}
}

func TestContractRejectsVersionsBeyondTheExactJSONIntegerRange(t *testing.T) {
	harness := mustHarness(t)
	raw := []byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"oversized-version","changes":[{"op":"transition","key":"attempt:oversized","version":9007199254740992,"status":"failed","summary":"Too large."}]}`)
	if err := harness.Validate("changeBatch", raw); err == nil {
		t.Fatalf("Contract accepted a version that adapters cannot preserve exactly: %s", raw)
	}
}

func TestAttemptResultSchemaRequiresExactlyOneAttemptIdentityMode(t *testing.T) {
	harness := mustHarness(t)
	invalid := [][]byte{
		[]byte(`{"idempotency_key":"attempt-result-both","result":{"schema":"runtime-attempt-result/v1","base_revision":1,"attempt":{"key":"attempt:a","create":true,"expected_version":1,"summary":"Tested target","outcome":"failed"},"tested_targets":[{"key":"entity:a","expected_version":1}],"produced_targets":[]}}`),
		[]byte(`{"idempotency_key":"attempt-result-neither","result":{"schema":"runtime-attempt-result/v1","base_revision":1,"attempt":{"key":"attempt:a","summary":"Tested target","outcome":"failed"},"tested_targets":[{"key":"entity:a","expected_version":1}],"produced_targets":[]}}`),
	}
	for _, raw := range invalid {
		if err := harness.Validate("attemptResultRequest", raw); err == nil {
			t.Fatalf("Attempt result accepted an ambiguous identity mode: %s", raw)
		}
	}
}

func TestAttemptResultSchemaRequiresProducedTargetForSuccess(t *testing.T) {
	harness := mustHarness(t)
	raw := []byte(`{"idempotency_key":"attempt-result-empty-success","result":{"schema":"runtime-attempt-result/v1","base_revision":1,"attempt":{"key":"attempt:a","expected_version":1,"summary":"Recovered flag","outcome":"succeeded"},"tested_targets":[{"key":"entity:a","expected_version":1}],"produced_targets":[]}}`)
	if err := harness.Validate("attemptResultRequest", raw); err == nil {
		t.Fatalf("succeeded Attempt result accepted no produced target: %s", raw)
	}
}

func TestMigrationPlanClosesActionsByRemovedSourceType(t *testing.T) {
	harness := mustHarness(t)
	if err := harness.ValidateFixture("migration.plan"); err != nil {
		t.Fatalf("accepted migration plan fixture rejected: %v", err)
	}

	invalidDecisions := []string{
		`{"source":{"project":"p","type":"hypothesis","key":"hypothesis:a"},"allowed_actions":["objective"],"decision":"tentative_fact"}`,
		`{"source":{"project":"p","type":"project_directive","key":"directive:a"},"allowed_actions":["confirmed_fact"],"decision":"confirmed_fact"}`,
		`{"source":{"project":"p","type":"observation","key":"observation:a"},"allowed_actions":["objective"],"decision":"objective"}`,
	}
	for index, decision := range invalidDecisions {
		plan := `{"schema":"blackboard-v2-migration-plan/v1","source_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","projects":[],"validation_blockers":[],"required_decisions":[` + decision + `]}`
		if err := harness.Validate("migrationPlan", []byte(plan)); err == nil {
			t.Errorf("invalid migration decision %d was accepted: %s", index, decision)
		}
	}
}

func TestSemanticHealthRejectsExecutableProposals(t *testing.T) {
	harness := mustHarness(t)
	raw, err := harness.Fixture("semantic_health.healthy_empty")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]json.RawMessage
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatal(err)
	}
	health["proposals"] = json.RawMessage(`[{}]`)
	raw, err = json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.Validate("semanticHealth", raw); err == nil {
		t.Fatal("health schema accepted a non-empty proposal array")
	}
}
