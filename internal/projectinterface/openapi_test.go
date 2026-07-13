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
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
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
