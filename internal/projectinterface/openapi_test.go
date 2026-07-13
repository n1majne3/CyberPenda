package projectinterface_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProjectInterfaceOpenAPICoversVersionedRoutesAndErrors(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi/project-interface-v1.json")
	if err != nil {
		t.Fatalf("read project-interface OpenAPI: %v", err)
	}
	var document struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			Responses map[string]struct {
				Headers map[string]any `json:"headers"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode project-interface OpenAPI: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q want 3.1.0", document.OpenAPI)
	}
	tests := []struct {
		path     string
		method   string
		statuses []string
	}{
		{"/api/projects/{project_id}/blackboard/mutations", "post", []string{"200", "400", "401", "403", "404", "409", "422", "500", "503"}},
		{"/api/projects/{project_id}/blackboard/records:resolve", "post", []string{"200", "400", "401", "403", "404", "500", "503"}},
		{"/api/projects/{project_id}/blackboard/evidence:retain", "post", []string{"200", "400", "401", "403", "404", "409", "422", "500", "503"}},
		{"/api/projects/{project_id}/blackboard/attempts:checkpoint", "post", []string{"200", "400", "401", "403", "404", "409", "422", "500", "503"}},
		{"/api/projects/{project_id}/tasks/{task_id}/continuations/{continuation_id}:finish", "post", []string{"200", "400", "401", "403", "404", "409", "422", "500", "503"}},
		{"/api/projects/{project_id}/blackboard/runtime-graph", "get", []string{"200", "304", "400", "401", "403", "404", "500", "503"}},
	}
	for _, tt := range tests {
		operation, ok := document.Paths[tt.path][tt.method]
		if !ok {
			t.Fatalf("OpenAPI missing %s %s", tt.method, tt.path)
		}
		for _, status := range tt.statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("OpenAPI %s %s missing status %s", tt.method, tt.path, status)
			}
		}
	}
}

func TestCompatibilityOpenAPIDocumentsDeprecationConcurrencyAndErrors(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi/project-interface-v1.json")
	if err != nil {
		t.Fatalf("read project-interface OpenAPI: %v", err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Headers map[string]any `json:"headers"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode project-interface OpenAPI: %v", err)
	}

	routes := []struct {
		path     string
		method   string
		statuses []string
	}{
		{"/api/projects/{project_id}/facts/{fact_key}", "put", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/facts/{fact_key}/relations", "put", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/facts/merge", "post", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/findings/{finding_key}", "put", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/findings/merge", "post", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/evidence", "post", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/tasks/{task_id}/summary", "put", []string{"200", "400", "404", "409", "422", "500"}},
		{"/api/projects/{project_id}/report", "post", []string{"200", "400", "404", "409", "422", "500"}},
	}
	for _, route := range routes {
		operation, ok := document.Paths[route.path][route.method]
		if !ok {
			t.Fatalf("OpenAPI missing compatibility route %s %s", route.method, route.path)
		}
		for _, status := range route.statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("OpenAPI %s %s missing status %s", route.method, route.path, status)
			}
		}
		for _, header := range []string{"Deprecation", "Link", "CyberPenda-Compatibility"} {
			if _, ok := operation.Responses["200"].Headers[header]; !ok {
				t.Errorf("OpenAPI %s %s missing %s response header", route.method, route.path, header)
			}
		}
	}

	for schemaName, fields := range map[string][]string{
		"LegacyFactWriteV1":        {"expected_version", "idempotency_key"},
		"LegacyRelationWriteV1":    {"expected_version", "idempotency_key"},
		"LegacyFindingWriteV1":     {"expected_version", "idempotency_key"},
		"LegacyEvidenceWriteV1":    {"expected_version", "idempotency_key", "produced_by_attempt"},
		"LegacyTaskSummaryWriteV1": {"idempotency_key"},
		"LegacyReportRequestV1":    {"idempotency_key"},
	} {
		schema, ok := document.Components.Schemas[schemaName]
		if !ok {
			t.Errorf("OpenAPI missing schema %s", schemaName)
			continue
		}
		for _, field := range fields {
			if _, ok := schema.Properties[field]; !ok {
				t.Errorf("OpenAPI schema %s missing additive field %s", schemaName, field)
			}
		}
	}
}
