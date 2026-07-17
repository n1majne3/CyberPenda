package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
)

func TestBlackboardV2WireDecodersRejectInvalidUTF8BeforeSemanticRewrite(t *testing.T) {
	for _, test := range []struct {
		name   string
		prefix string
		suffix string
		value  any
	}{
		{
			name:   "ChangeBatch",
			prefix: `{"schema":"semantic-change-batch/v2","idempotency_key":"invalid-utf8","changes":[{"op":"create","key":"fact:utf8","type":"fact","record":{"category":"wire","summary":"`,
			suffix: `","confidence":"tentative","scope_status":"in_scope"}}]}`,
			value:  &blackboardv2.ChangeBatch{},
		},
		{
			name:   "Change",
			prefix: `{"op":"create","key":"fact:utf8","type":"fact","record":{"category":"wire","summary":"`,
			suffix: `","confidence":"tentative","scope_status":"in_scope"}}`,
			value:  &blackboardv2.Change{},
		},
		{
			name:   "RetainEvidenceRequest",
			prefix: `{"idempotency_key":"invalid-utf8","key":"evidence:utf8","attempt":"attempt:utf8","source_path":"wire.txt","artifact_type":"text","summary":"`,
			suffix: `"}`,
			value:  &blackboardv2.RetainEvidenceRequest{},
		},
		{
			name:   "EvidenceLink",
			prefix: `["about","entity:`,
			suffix: `"]`,
			value:  &blackboardv2.EvidenceLink{},
		},
		{
			name:   "RecordVersionTuple",
			prefix: `["fact:`,
			suffix: `",1]`,
			value:  &blackboardv2.RecordVersionTuple{},
		},
		{
			name:   "RelationVersionTuple",
			prefix: `["fact:a","supports","fact:`,
			suffix: `",1]`,
			value:  &blackboardv2.RelationVersionTuple{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, invalidByte := range []byte{0xfe, 0xff} {
				raw := invalidUTF8JSON(test.prefix, invalidByte, test.suffix)
				err := json.Unmarshal(raw, test.value)
				assertInvalidUTF8Error(t, err)
			}
		})
	}
}

func TestInvalidUTF8ChangeBatchesCannotCollapseIntoOneIdempotentMeaning(t *testing.T) {
	fixture := newProjectionFixture(t, project.KindPentest)
	for _, invalidByte := range []byte{0xfe, 0xff} {
		raw := invalidUTF8JSON(
			`{"schema":"semantic-change-batch/v2","idempotency_key":"same-malformed-bytes","changes":[{"op":"create","key":"fact:malformed","type":"fact","record":{"category":"wire","summary":"`,
			invalidByte,
			`","confidence":"tentative","scope_status":"in_scope"}}]}`,
		)
		var batch blackboardv2.ChangeBatch
		err := json.Unmarshal(raw, &batch)
		assertInvalidUTF8Error(t, err)
	}
	projection, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("project after malformed batches: %v", err)
	}
	if projection.Snapshot.Revision != 0 || len(projection.Snapshot.Knowledge.Facts) != 0 {
		t.Fatalf("malformed bytes committed rewritten semantics: %#v", projection.Snapshot)
	}

	var valid blackboardv2.ChangeBatch
	if err := json.Unmarshal([]byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"same-malformed-bytes","changes":[{"op":"create","key":"fact:malformed","type":"fact","record":{"category":"wire","summary":"Valid UTF-8 remains distinct","confidence":"tentative","scope_status":"in_scope"}}]}`), &valid); err != nil {
		t.Fatalf("decode valid batch after malformed bytes: %v", err)
	}
	result, err := fixture.service.Apply(context.Background(), fixture.projectID, valid)
	if err != nil {
		t.Fatalf("apply valid batch after malformed bytes: %v", err)
	}
	if result.Revision != 1 {
		t.Fatalf("valid batch revision = %d, want 1", result.Revision)
	}
}

func TestInvalidUTF8EvidenceRequestsCommitNoReservationOrSemanticState(t *testing.T) {
	fixture := newProjectionFixture(t, project.KindPentest)
	prepareEvidenceAttempt(t, fixture)
	if err := os.WriteFile(filepath.Join(fixture.workdir, "wire.txt"), []byte("wire proof"), 0o600); err != nil {
		t.Fatalf("write Evidence wire source: %v", err)
	}
	before, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("project before malformed Evidence: %v", err)
	}
	for _, invalidByte := range []byte{0xfe, 0xff} {
		raw := invalidUTF8JSON(
			`{"idempotency_key":"same-malformed-evidence","key":"evidence:malformed","attempt":"attempt:evidence-limit","source_path":"wire.txt","artifact_type":"text","summary":"`,
			invalidByte,
			`"}`,
		)
		var request blackboardv2.RetainEvidenceRequest
		err := json.Unmarshal(raw, &request)
		assertInvalidUTF8Error(t, err)
	}
	after, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("project after malformed Evidence: %v", err)
	}
	if after.Snapshot.Revision != before.Snapshot.Revision || len(after.Snapshot.Knowledge.Evidence) != 0 {
		t.Fatalf("malformed Evidence changed semantic state: before=%d after=%d evidence=%d", before.Snapshot.Revision, after.Snapshot.Revision, len(after.Snapshot.Knowledge.Evidence))
	}

	var valid blackboardv2.RetainEvidenceRequest
	if err := json.Unmarshal([]byte(`{"idempotency_key":"same-malformed-evidence","key":"evidence:malformed","attempt":"attempt:evidence-limit","source_path":"wire.txt","artifact_type":"text","summary":"Valid retained proof"}`), &valid); err != nil {
		t.Fatalf("decode valid Evidence after malformed bytes: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, valid); err != nil {
		t.Fatalf("retain valid Evidence after malformed bytes: %v", err)
	}
}

func invalidUTF8JSON(prefix string, invalidByte byte, suffix string) []byte {
	raw := make([]byte, 0, len(prefix)+1+len(suffix))
	raw = append(raw, prefix...)
	raw = append(raw, invalidByte)
	raw = append(raw, suffix...)
	return raw
}

func assertInvalidUTF8Error(t *testing.T, err error) {
	t.Helper()
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Message != "JSON input must be valid UTF-8" || semanticErr.Path != "" || semanticErr.Details["reason"] != "invalid_utf8" {
		t.Fatalf("invalid UTF-8 error = %#v", err)
	}
}
