package blackboardv2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBlackboardV2RuntimeHasNoGraphV1Ledger is the repository boundary for
// #127. Historical v1 decoding is deliberately isolated to the offline
// migration package; the active daemon/runtime path must be v2-only.
func TestBlackboardV2RuntimeHasNoGraphV1Ledger(t *testing.T) {
	root := repositoryRoot(t)
	for _, relativePath := range []string{
		"internal/blackboard/graph.go",
		"internal/blackboard/graph_integrity.go",
		"internal/blackboard/graph_reconstruction.go",
		"internal/blackboard/graph_compaction.go",
		"internal/blackboard/graph_projection.go",
		"internal/blackboard/graph_disposition.go",
		"internal/blackboard/legacy_import.go",
		"internal/blackboard/graph_budget.go",
		"internal/blackboard/graph_result_decode.go",
		"internal/blackboardmigration/import.go",
	} {
		if _, err := os.Stat(filepath.Join(root, relativePath)); err == nil {
			t.Errorf("retired Blackboard v1 ledger implementation remains at %s", relativePath)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", relativePath, err)
		}
	}

	allowedProductionPaths := map[string]bool{
		// Historical migrations must retain exact SQL/checksums, and this seam
		// classifies v1 files without activating them.
		"internal/store/store.go": true,
	}
	forbidden := []string{
		"GraphService", "blackboard_graph_", "graph_v1", "legacy_v1",
		"CanonicalStoreGraphV1", "CanonicalStoreLegacyV1", "CanonicalStoreGraphV1Finalized",
		"history_head_hash", "current_semantic_state_hash", "current_main_projection_hash",
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		if entry.IsDir() {
			if relativePath == "internal/blackboardmigration" || relativePath == "internal/testsupport" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || allowedProductionPaths[relativePath] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, term := range forbidden {
			if strings.Contains(string(body), term) {
				t.Errorf("production path %s references retired %q", relativePath, term)
			}
		}
		if strings.Contains(string(body), `"pentest/internal/blackboardmigration"`) && relativePath != "internal/pentestctl/pentestctl.go" {
			t.Errorf("production path %s imports the offline-only v1 decoder", relativePath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Blackboard boundary: %v", err)
	}
}

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
	if strings.Contains(string(server), "CanonicalStoreGraphV1") {
		t.Fatal("daemon still recognizes a graph-v1 epoch")
	}
	migration, err := os.ReadFile(filepath.Join(root, "internal/blackboardmigration/service.go"))
	if err != nil {
		t.Fatalf("read migration service: %v", err)
	}
	if strings.Contains(string(migration), ".RunHealth(") {
		t.Fatal("offline migration verification still runs retired graph audit health")
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
