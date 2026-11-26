package blackboardv2contract_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
		"/api/v2/projects/{project_id}/blackboard/health":                    "get",
		"/api/v2/projects/{project_id}/blackboard/records/{key}":             "get",
		"/api/v2/projects/{project_id}/blackboard/records/{key}/history":     "get",
		"/api/v2/projects/{project_id}/blackboard/evidence:retain":           "post",
		"/api/v2/projects/{project_id}/blackboard/attempts/{key}:checkpoint": "post",
		"/api/v2/projects/{project_id}/continuation:finish":                  "post",
		"/api/v2/projects/{project_id}/reports/pentest":                      "get",
		"/api/v2/projects/{project_id}/reports/ctf-solution":                 "get",
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
	wantTools := []string{"blackboard_change", "blackboard_record_attempt_result", "blackboard_read", "blackboard_history", "blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish"}
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

func TestToolInputSchemaIncludesOnlyTransitiveRootDefs(t *testing.T) {
	harness := mustHarness(t)
	tools, err := harness.TrustedTools()
	if err != nil {
		t.Fatalf("trusted tools: %v", err)
	}
	if len(tools) != 7 {
		t.Fatalf("trusted tools = %d, want 7", len(tools))
	}
	unrelated := []string{
		"migrationPlan", "migrationProjectPlan", "migrationDecision", "migrationBlocker",
		"migrationMapping", "migrationResult", "migrationSource", "migrationValidation",
		"migrationProjectResult", "runtimeSnapshot", "snapshotKnowledge", "snapshotWork",
		"errorEnvelope", "errorBody", "unauthenticatedErrorEnvelope", "syncAttachment",
		"currentDetail", "semanticHistory", "changeResult", "finishResult",
	}
	for _, tool := range tools {
		schema, err := harness.ToolInputSchema(tool.InputSchema)
		if err != nil {
			t.Fatalf("ToolInputSchema(%s): %v", tool.InputSchema, err)
		}
		if schema.Type != "object" {
			t.Fatalf("%s schema type = %q, want object", tool.Name, schema.Type)
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}
		var encoded map[string]any
		if err := json.Unmarshal(raw, &encoded); err != nil {
			t.Fatalf("decode %s schema: %v", tool.Name, err)
		}
		defs, _ := encoded["$defs"].(map[string]any)
		if defs == nil {
			defs = map[string]any{}
		}
		// Root DTO is the schema body; it must not re-embed the entire frozen $defs set.
		if len(defs) > 40 {
			t.Fatalf("%s $defs count = %d, want a pruned transitive set", tool.Name, len(defs))
		}
		for _, name := range unrelated {
			if _, ok := defs[name]; ok {
				t.Errorf("%s $defs includes unrelated %q", tool.Name, name)
			}
		}
		// Every advertised $ref must resolve inside the pruned $defs.
		const prefix = `#/$defs/`
		blob := string(raw)
		for {
			index := strings.Index(blob, prefix)
			if index < 0 {
				break
			}
			blob = blob[index+len(prefix):]
			end := 0
			for end < len(blob) {
				c := blob[end]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
					end++
					continue
				}
				break
			}
			name := blob[:end]
			blob = blob[end:]
			if name == "" {
				continue
			}
			if _, ok := defs[name]; !ok {
				t.Errorf("%s unresolved $ref %q", tool.Name, name)
			}
		}
		if !bytes.Contains(raw, []byte(`"additionalProperties":false`)) && !bytes.Contains(raw, []byte(`"additionalProperties": false`)) {
			t.Errorf("%s schema is not closed: %s", tool.Name, raw)
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
