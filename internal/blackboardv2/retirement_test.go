package blackboardv2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBlackboardV2HasNoCurrentGraphV1ReadOrAuditProjection is the repository
// boundary for #126. Historical decoding is an offline migration concern; no
// current daemon, runtime, transport, or CLI package may register or invoke it.
func TestBlackboardV2HasNoCurrentGraphV1ReadOrAuditProjection(t *testing.T) {
	root := repositoryRoot(t)
	for _, relativePath := range []string{
		"internal/blackboard/read_service.go",
		"internal/blackboard/read_legacy.go",
		"internal/blackboard/read_provenance.go",
		"internal/blackboard/read_traversal.go",
		"internal/blackboard/read_health.go",
		"internal/blackboard/read_graph_explorer.go",
		"internal/blackboard/read_current_frontier.go",
		"internal/blackboard/read_detail.go",
		"internal/blackboard/read_entities.go",
		"internal/blackboard/read_reports.go",
		"internal/blackboard/read_summary.go",
		"internal/blackboard/graph_health.go",
	} {
		if _, err := os.Stat(filepath.Join(root, relativePath)); err == nil {
			t.Errorf("retired Blackboard v1 projection remains at %s", relativePath)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", relativePath, err)
		}
	}
	for _, relativePath := range []string{
		"internal/daemon/server.go",
		"internal/daemon/task_handlers.go",
		"internal/mcpserver/v2.go",
		"internal/pentestctl/blackboard_v2.go",
		"internal/runner/mcp.go",
		"internal/runner/projection.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		for _, forbidden := range []string{
			"NewBlackboardReadService", "ReadKind", "BlackboardReadService",
		} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("current Blackboard path %s still references retired %q", relativePath, forbidden)
			}
		}
	}

	server, err := os.ReadFile(filepath.Join(root, "internal/daemon/server.go"))
	if err != nil {
		t.Fatalf("read daemon server: %v", err)
	}
	if strings.Contains(string(server), "NewGraphService(") {
		t.Fatal("daemon still creates a graph-v1 service")
	}
	if !strings.Contains(string(server), "offline migration inputs") {
		t.Fatal("daemon no longer makes graph-v1 stores offline-only")
	}
	migration, err := os.ReadFile(filepath.Join(root, "internal/blackboardmigration/service.go"))
	if err != nil {
		t.Fatalf("read migration service: %v", err)
	}
	if strings.Contains(string(migration), ".RunHealth(") {
		t.Fatal("offline migration verification still runs retired graph audit health")
	}
	importer, err := os.ReadFile(filepath.Join(root, "internal/blackboardmigration/import.go"))
	if err != nil {
		t.Fatalf("read migration importer: %v", err)
	}
	for _, forbidden := range []string{"BlackboardReadService", "NewBlackboardReadService", "ReadKind"} {
		if strings.Contains(string(importer), forbidden) {
			t.Errorf("offline migration decoder still imports retired Blackboard read API %q", forbidden)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("find repository root")
		}
		directory = parent
	}
}
