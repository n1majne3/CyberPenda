package blackboardv2_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestBlackboardV2ReplacementRetiresEveryIndependentProductionV1Path(t *testing.T) {
	root := repositoryRoot(t)

	for _, retired := range []string{
		"internal/blackboard/evidence.go",
		"internal/blackboard/facts.go",
		"internal/blackboard/findings.go",
		"internal/blackboard/graph_types.go",
		"internal/blackboard/retired_graph_compat.go",
		"internal/blackboard/sources.go",
	} {
		if _, err := os.Stat(filepath.Join(root, retired)); err == nil {
			t.Errorf("retired Blackboard v1 production file remains: %s", retired)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", retired, err)
		}
	}

	// The offline migration package is the only production caller allowed to
	// decode graph-v1 types.
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			switch relative {
			case ".git", ".qoder", "runs", "web/node_modules", "web/dist", "internal/daemon/webfs/dist", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(relative, "_test.go") || strings.HasSuffix(relative, ".test.ts") ||
			strings.HasSuffix(relative, ".test.tsx") || strings.HasPrefix(relative, "internal/testsupport/") {
			return nil
		}
		if strings.HasPrefix(relative, "internal/blackboardmigration/") || relative == "internal/store/store.go" {
			return nil
		}
		if filepath.Ext(relative) != ".go" && filepath.Ext(relative) != ".ts" &&
			filepath.Ext(relative) != ".tsx" && filepath.Ext(relative) != ".md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, forbidden := range []string{
			`"pentest/internal/blackboard"`,
			"CanonicalStoreGraphV1", "CanonicalStoreLegacyV1", "GraphService",
			"blackboard_graph_", "graph_v1", "legacy_v1",
			"ProjectInterfaceErrorV1", "Task Summary Version", "Task Summary",
			"graph reconciliation audit", "graph normal-audit",
			"runtime protocol", "graph contract", "graph-native", "graph cutover",
			"GoalProjector", "ProjectTaskGoal", "submit_task_summary",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("production path %s contains retired Blackboard v1 reference %q", relative, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production replacement boundary: %v", err)
	}
}

func TestBlackboardV2AcceptedContractsAndUserDocsAgree(t *testing.T) {
	root := repositoryRoot(t)

	var openAPI struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	decodeRepositoryJSON(t, filepath.Join(root, "internal/blackboardv2contract/contractdata/openapi.json"), &openAPI)
	if openAPI.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", openAPI.OpenAPI)
	}
	wantPaths := []string{
		"/api/v2/projects/{project_id}/blackboard/attempts/{key}:checkpoint",
		"/api/v2/projects/{project_id}/blackboard/changes",
		"/api/v2/projects/{project_id}/blackboard/evidence:retain",
		"/api/v2/projects/{project_id}/blackboard/health",
		"/api/v2/projects/{project_id}/blackboard/records/{key}",
		"/api/v2/projects/{project_id}/blackboard/records/{key}/history",
		"/api/v2/projects/{project_id}/blackboard/snapshot",
		"/api/v2/projects/{project_id}/continuation:finish",
		"/api/v2/projects/{project_id}/reports/ctf-solution",
		"/api/v2/projects/{project_id}/reports/pentest",
	}
	gotPaths := make([]string, 0, len(openAPI.Paths))
	for path := range openAPI.Paths {
		gotPaths = append(gotPaths, path)
	}
	sort.Strings(gotPaths)
	if strings.Join(gotPaths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("OpenAPI v2 paths =\n%s\nwant\n%s", strings.Join(gotPaths, "\n"), strings.Join(wantPaths, "\n"))
	}

	var tools struct {
		Schema string `json:"schema"`
		Tools  []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	decodeRepositoryJSON(t, filepath.Join(root, "internal/blackboardv2contract/contractdata/trusted-tools.json"), &tools)
	wantTools := []string{
		"blackboard_change", "blackboard_read", "blackboard_history",
		"blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish",
	}
	if tools.Schema != "trusted-blackboard-tools/v2" || len(tools.Tools) != len(wantTools) {
		t.Fatalf("trusted tool catalog schema/count = %q/%d", tools.Schema, len(tools.Tools))
	}
	spec := readRepositoryFile(t, root, "docs/specs/blackboard-v2-spec.md")
	adr := readRepositoryFile(t, root, "docs/adr/0014-use-one-versioned-blackboard-v2-project-interface.md")
	readme := readRepositoryFile(t, root, "README.md")
	proofPlan := readRepositoryFile(t, root, "docs/specs/blackboard-v2-tdd-plan.md")
	for index, name := range wantTools {
		if tools.Tools[index].Name != name || strings.TrimSpace(tools.Tools[index].Description) == "" {
			t.Errorf("trusted tool %d = %#v, want named %q with a description", index, tools.Tools[index], name)
		}
		if !strings.Contains(spec, "`"+name+"`") {
			t.Errorf("accepted v2 spec omits trusted tool %q", name)
		}
	}
	for _, document := range []struct {
		name string
		body string
	}{
		{"ADR 0014", adr},
		{"README", readme},
	} {
		if !strings.Contains(document.body, "six") && !strings.Contains(document.body, "Exactly six") {
			t.Errorf("%s does not state the six-tool contract", document.name)
		}
		if strings.Contains(document.body, "/api/v1/") || strings.Contains(document.body, "get_current_graph") {
			t.Errorf("%s advertises a retired public v1 interface", document.name)
		}
	}
	for _, marker := range []string{
		"### 6.1 T30 executable proof matrix",
		"go test ./...",
		"go test -race",
		"npm test",
		"npm run build",
		"make check-ui-sync",
		"TestBlackboardChangeMCPSchemaAdvertisesObjectiveAndAttemptCreateEnvelope",
		"TestClaudeV2RuntimeConfigPreservesTrustedMCPAllowlistWithoutIdentityContext",
	} {
		if !strings.Contains(proofPlan, marker) {
			t.Errorf("accepted TDD issue graph omits final proof marker %q", marker)
		}
	}
}

func TestBlackboardV1SpecificationsAreVisiblyHistorical(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"docs/specs/blackboard-graph-contract.md",
		"docs/specs/blackboard-graph-refactor.md",
		"docs/specs/blackboard-graph-storage.md",
		"docs/specs/blackboard-legacy-migration.md",
		"docs/specs/blackboard-read-projections.md",
		"docs/specs/blackboard-runtime-protocol.md",
		"docs/specs/blackboard-tdd-acceptance-and-slices.md",
	} {
		body := readRepositoryFile(t, root, path)
		for _, marker := range []string{
			"historical reference only; do not implement",
			"**STOP:** This document specifies Blackboard v1.",
			"[Blackboard v2](./blackboard-v2-spec.md)",
		} {
			if !strings.Contains(body, marker) {
				t.Errorf("historical specification %s omits marker %q", path, marker)
			}
		}
	}
}

func decodeRepositoryJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func readRepositoryFile(t *testing.T, root, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(raw)
}
