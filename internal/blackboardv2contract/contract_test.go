package blackboardv2contract_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

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

func TestEmptyRuntimeSnapshotFixtureIsExactAndConformant(t *testing.T) {
	harness := mustHarness(t)

	got, err := harness.Fixture("runtime_snapshot.empty")
	if err != nil {
		t.Fatalf("load empty Runtime Snapshot fixture: %v", err)
	}
	want := []byte(`{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":0,"work":{},"knowledge":{},"relations":[]}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("empty Runtime Snapshot fixture = %s, want %s", got, want)
	}
	if err := harness.ValidateFixture("runtime_snapshot.empty"); err != nil {
		t.Fatalf("empty Runtime Snapshot fixture violates v2 contract: %v", err)
	}
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

func TestBaselineSeparatesExistingFailuresFromTheIntentionalV2Red(t *testing.T) {
	harness := mustHarness(t)
	baseline, err := harness.Baseline()
	if err != nil {
		t.Fatalf("load implementation baseline: %v", err)
	}
	if baseline.FixedPoint != "61d07e44a71e25d8392d95afb998d7171b4fe37b" {
		t.Fatalf("baseline fixed point = %q", baseline.FixedPoint)
	}
	if baseline.Command != "rtk go test ./..." || baseline.Passed != 944 || baseline.Failed != 3 {
		t.Fatalf("baseline result = command %q, %d passed, %d failed", baseline.Command, baseline.Passed, baseline.Failed)
	}
	if len(baseline.PreExistingFailures) != 3 {
		t.Fatalf("pre-existing failures = %d, want 3", len(baseline.PreExistingFailures))
	}
	for _, failure := range baseline.PreExistingFailures {
		if failure.Package != "pentest/internal/blackboardcompat" {
			t.Errorf("pre-existing failure package = %q", failure.Package)
		}
	}
	if baseline.IntentionalV2Red.Test != "TestEmptyRuntimeSnapshotFixtureIsExactAndConformant" || baseline.IntentionalV2Red.Failure != "package has no non-test Go files" {
		t.Fatalf("intentional v2 red = %+v", baseline.IntentionalV2Red)
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

func TestSyncAttachmentIsSharedByEveryAuthenticatedTrustedResponse(t *testing.T) {
	harness := mustHarness(t)
	syncFixture, err := harness.Fixture("response.sync")
	if err != nil {
		t.Fatalf("load sync fixture: %v", err)
	}
	var syncEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(syncFixture, &syncEnvelope); err != nil {
		t.Fatalf("decode sync fixture: %v", err)
	}

	for _, tt := range []struct {
		fixture    string
		schemaName string
	}{
		{"read.current", "currentDetail"},
		{"read.history.page", "semanticHistory"},
		{"change.result", "changeResult"},
		{"continuation.finish.result", "finishResult"},
		{"response.error", "errorEnvelope"},
	} {
		raw, err := harness.Fixture(tt.fixture)
		if err != nil {
			t.Fatalf("load fixture %s: %v", tt.fixture, err)
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode fixture %s: %v", tt.fixture, err)
		}
		response["sync"] = syncEnvelope["sync"]
		withSync, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("encode fixture %s with sync: %v", tt.fixture, err)
		}
		if err := harness.Validate(tt.schemaName, withSync); err != nil {
			t.Errorf("%s does not accept shared sync attachment: %v", tt.fixture, err)
		}
	}
}

func TestUnauthenticatedErrorCanNeverExposeSynchronizationState(t *testing.T) {
	harness := mustHarness(t)
	errorFixture, err := harness.Fixture("response.error")
	if err != nil {
		t.Fatalf("load error fixture: %v", err)
	}
	if err := harness.Validate("unauthenticatedErrorEnvelope", errorFixture); err != nil {
		t.Fatalf("plain unauthenticated error rejected: %v", err)
	}

	syncFixture, err := harness.Fixture("response.sync")
	if err != nil {
		t.Fatalf("load sync fixture: %v", err)
	}
	var errorResponse, syncResponse map[string]json.RawMessage
	if err := json.Unmarshal(errorFixture, &errorResponse); err != nil {
		t.Fatalf("decode error fixture: %v", err)
	}
	if err := json.Unmarshal(syncFixture, &syncResponse); err != nil {
		t.Fatalf("decode sync fixture: %v", err)
	}
	errorResponse["sync"] = syncResponse["sync"]
	withSync, err := json.Marshal(errorResponse)
	if err != nil {
		t.Fatalf("encode unauthenticated error with sync: %v", err)
	}
	if err := harness.Validate("unauthenticatedErrorEnvelope", withSync); err == nil {
		t.Fatal("unauthenticated error schema accepted synchronization state")
	}

	openAPI, err := harness.OpenAPI()
	if err != nil {
		t.Fatalf("load v2 OpenAPI: %v", err)
	}
	if !bytes.Contains(openAPI, []byte(`blackboard-v2.schema.json#/$defs/unauthenticatedErrorEnvelope`)) {
		t.Fatal("OpenAPI 401 response does not bind the no-sync unauthenticated envelope")
	}
}

func TestRecordWireSchemasExposeLegalPositiveTransitionsAndServerOwnedEvidence(t *testing.T) {
	harness := mustHarness(t)

	accepted := []string{
		`{"schema":"semantic-change-batch/v2","idempotency_key":"confirm-fact","changes":[{"op":"transition","key":"fact:a","version":1,"status":"confirmed"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"confirm-finding","changes":[{"op":"transition","key":"finding:a","version":1,"status":"confirmed"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"verify-solution","changes":[{"op":"transition","key":"solution:a","version":1,"status":"verified","verification_summary":"Accepted by the challenge"}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"create-evidence","changes":[{"op":"create","key":"evidence:a","type":"evidence","record":{"status":"available","artifact_type":"file","summary":"Captured response","source_path":"captures/response.txt"}}]}`,
		`{"schema":"semantic-change-batch/v2","idempotency_key":"merge-facts","changes":[{"op":"merge","source":"fact:duplicate","source_version":2,"canonical":"fact:canonical","canonical_version":3,"canonical_record":{"summary":"Approved consolidated fact"},"clear":["body"]}]}`,
	}
	for _, raw := range accepted {
		if err := harness.Validate("changeBatch", []byte(raw)); err != nil {
			t.Errorf("legal record change rejected: %v\n%s", err, raw)
		}
	}

	rejected := []string{
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

func TestSemanticHistoryFixtureCarriesRelationshipIdentityAndVersion(t *testing.T) {
	harness := mustHarness(t)
	raw, err := harness.Fixture("read.history.relationship_page")
	if err != nil {
		t.Fatalf("load relationship history fixture: %v", err)
	}
	if err := harness.ValidateFixture("read.history.relationship_page"); err != nil {
		t.Fatalf("relationship history fixture violates v2 contract: %v", err)
	}
	var page struct {
		Items []struct {
			Kind     string `json:"kind"`
			Version  int    `json:"version"`
			From     string `json:"from"`
			Relation string `json:"relation"`
			To       string `json:"to"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode relationship history fixture: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "relationship" || page.Items[0].Version != 2 || page.Items[0].From != "fact:login" || page.Items[0].Relation != "supports" || page.Items[0].To != "finding:sqli" {
		t.Fatalf("relationship history item = %+v", page.Items)
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

func TestOpenAPIAndTrustedToolsFreezeTheAcceptedV2Adapters(t *testing.T) {
	harness := mustHarness(t)

	openAPIRaw, err := harness.OpenAPI()
	if err != nil {
		t.Fatalf("load v2 OpenAPI: %v", err)
	}
	var openAPI struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
				Schema   struct {
					Default any      `json:"default"`
					Maximum *float64 `json:"maximum"`
				} `json:"schema"`
			} `json:"parameters"`
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
		Components struct {
			SecuritySchemes map[string]any `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIRaw, &openAPI); err != nil {
		t.Fatalf("decode v2 OpenAPI: %v", err)
	}
	if openAPI.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", openAPI.OpenAPI)
	}
	if len(openAPI.Components.SecuritySchemes) != 2 || openAPI.Components.SecuritySchemes["continuationGrant"] == nil || openAPI.Components.SecuritySchemes["daemonAuth"] == nil {
		t.Fatalf("OpenAPI security schemes = %v, want continuation grant and daemon auth", openAPI.Components.SecuritySchemes)
	}

	wantOperations := map[string]string{
		"/api/v2/projects/{project_id}/blackboard/changes":                   "post",
		"/api/v2/projects/{project_id}/blackboard/snapshot":                  "get",
		"/api/v2/projects/{project_id}/blackboard/records/{key}":             "get",
		"/api/v2/projects/{project_id}/blackboard/records/{key}/history":     "get",
		"/api/v2/projects/{project_id}/blackboard/evidence:retain":           "post",
		"/api/v2/projects/{project_id}/blackboard/attempts/{key}:checkpoint": "post",
		"/api/v2/projects/{project_id}/continuation:finish":                  "post",
	}
	if len(openAPI.Paths) != len(wantOperations) {
		t.Fatalf("OpenAPI paths = %d, want exactly %d v2 paths", len(openAPI.Paths), len(wantOperations))
	}
	for path, method := range wantOperations {
		operation, ok := openAPI.Paths[path][method]
		if !ok {
			t.Errorf("OpenAPI missing %s %s", method, path)
			continue
		}
		for _, status := range []string{"400", "401", "403", "404", "410", "500", "503"} {
			if operation.Responses[status] == nil {
				t.Errorf("OpenAPI %s %s missing honest status %s", method, path, status)
			}
		}
		if method == "post" {
			if !hasRequiredParameter(operation.Parameters, "Idempotency-Key", "header") {
				t.Errorf("OpenAPI %s %s does not require Idempotency-Key", method, path)
			}
			for _, status := range []string{"409", "422"} {
				if operation.Responses[status] == nil {
					t.Errorf("OpenAPI %s %s missing semantic status %s", method, path, status)
				}
			}
		}
	}
	history := openAPI.Paths["/api/v2/projects/{project_id}/blackboard/records/{key}/history"]["get"]
	if !hasHistoryPagination(history.Parameters) {
		t.Error("OpenAPI history operation does not freeze opaque cursor pagination with default 20 and maximum 100")
	}

	tools, err := harness.TrustedTools()
	if err != nil {
		t.Fatalf("load trusted-tool schemas: %v", err)
	}
	wantTools := []string{"blackboard_change", "blackboard_read", "blackboard_history", "blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish"}
	if len(tools) != len(wantTools) {
		t.Fatalf("trusted tools = %d, want %d", len(tools), len(wantTools))
	}
	for index, tool := range tools {
		if tool.Name != wantTools[index] {
			t.Errorf("trusted tool %d = %q, want %q", index, tool.Name, wantTools[index])
		}
		if tool.Authentication != "continuation_interface_grant" || tool.Authority != "server_owned" {
			t.Errorf("trusted tool %s auth/authority = %q/%q", tool.Name, tool.Authentication, tool.Authority)
		}
		if tool.InputSchema == "" || tool.ResultSchema == "" {
			t.Errorf("trusted tool %s omitted input/result schema", tool.Name)
		}
	}
}

func hasRequiredParameter(parameters []struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
	Schema   struct {
		Default any      `json:"default"`
		Maximum *float64 `json:"maximum"`
	} `json:"schema"`
}, name, in string) bool {
	for _, parameter := range parameters {
		if parameter.Name == name && parameter.In == in && parameter.Required {
			return true
		}
	}
	return false
}

func hasHistoryPagination(parameters []struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
	Schema   struct {
		Default any      `json:"default"`
		Maximum *float64 `json:"maximum"`
	} `json:"schema"`
}) bool {
	cursor := false
	limit := false
	for _, parameter := range parameters {
		switch parameter.Name {
		case "cursor":
			cursor = parameter.In == "query" && !parameter.Required
		case "limit":
			defaultLimit, ok := parameter.Schema.Default.(float64)
			limit = parameter.In == "query" && !parameter.Required && ok && defaultLimit == 20 && parameter.Schema.Maximum != nil && *parameter.Schema.Maximum == 100
		}
	}
	return cursor && limit
}

func TestFixtureCorpusCoversEveryFrozenV2WireShape(t *testing.T) {
	harness := mustHarness(t)

	want := []string{
		"attempt.checkpoint",
		"change.create",
		"change.merge",
		"change.relate",
		"change.result",
		"change.supersede",
		"change.transition",
		"change.unrelate",
		"change.update",
		"continuation.finish.request",
		"continuation.finish.result",
		"evidence.retain",
		"migration.plan",
		"migration.result",
		"read.current",
		"read.history.page",
		"read.history.relationship_page",
		"record.attempt",
		"record.entity",
		"record.evidence",
		"record.fact",
		"record.finding",
		"record.objective",
		"record.solution",
		"relationship.about",
		"relationship.contradicts",
		"relationship.depends_on",
		"relationship.derived_from",
		"relationship.evidences",
		"relationship.part_of",
		"relationship.produced",
		"relationship.satisfies",
		"relationship.supersedes",
		"relationship.supports",
		"relationship.tests",
		"response.error",
		"response.sync",
		"runtime_snapshot.empty",
		"runtime_snapshot.pentest",
		"runtime_snapshot.solution",
	}
	if got := harness.FixtureNames(); !slices.Equal(got, want) {
		t.Fatalf("fixture names = %v, want %v", got, want)
	}
	for _, name := range want {
		if err := harness.ValidateFixture(name); err != nil {
			t.Errorf("fixture %s violates v2 contract: %v", name, err)
		}
	}
}

func TestRelationshipTableEnumeratesEveryEndpointAndGraphPolicy(t *testing.T) {
	harness := mustHarness(t)
	cases, err := harness.RelationshipCases()
	if err != nil {
		t.Fatalf("load relationship cases: %v", err)
	}
	if got, want := len(cases), 11*7*7; got != want {
		t.Fatalf("relationship cases = %d, want %d", got, want)
	}

	wantCycles := map[string]string{
		"about":        "unrestricted",
		"part_of":      "acyclic_per_endpoint_family",
		"tests":        "unrestricted",
		"produced":     "unrestricted",
		"evidences":    "unrestricted",
		"supports":     "project_fact_to_project_fact_acyclic",
		"contradicts":  "reciprocal_allowed",
		"derived_from": "acyclic",
		"depends_on":   "acyclic",
		"satisfies":    "unrestricted",
		"supersedes":   "acyclic_single_current_replacement",
	}
	seen := map[string]bool{}
	allowed := 0
	for _, relationshipCase := range cases {
		identity := relationshipCase.Relation + ":" + relationshipCase.From + ":" + relationshipCase.To
		if seen[identity] {
			t.Fatalf("duplicate relationship case %s", identity)
		}
		seen[identity] = true
		if relationshipCase.Allowed {
			allowed++
		}
		if relationshipCase.SelfLinkPolicy != "reject" {
			t.Errorf("%s self-link policy = %q, want reject", identity, relationshipCase.SelfLinkPolicy)
		}
		if want := wantCycles[relationshipCase.Relation]; relationshipCase.CyclePolicy != want {
			t.Errorf("%s cycle policy = %q, want %q", identity, relationshipCase.CyclePolicy, want)
		}
		wantReason := "forbidden"
		if relationshipCase.Relation == "supports" || relationshipCase.Relation == "contradicts" || relationshipCase.Relation == "depends_on" {
			wantReason = "optional"
		}
		if relationshipCase.ReasonPolicy != wantReason {
			t.Errorf("%s reason policy = %q, want %q", identity, relationshipCase.ReasonPolicy, wantReason)
		}
	}
	if allowed != 44 {
		t.Fatalf("allowed endpoint cases = %d, want 44 (and 495 explicit rejections)", allowed)
	}
}
