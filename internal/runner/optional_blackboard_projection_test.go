package runner_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestRuntimeProvidersRejectRequiredArtifactsWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "required-then-omitted-" + string(provider)
			layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare reused Runtime layout: %v", err)
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
				Model:   "ordinary-model",
				Env:     map[string]string{"ORDINARY_RUNTIME_SETTING": "preserve-me"},
				APIKeys: map[string]string{"OPENAI_API_KEY": "ordinary-model-credential"},
				MCPServers: []runtimeprofile.MCPServer{
					{Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted, URL: "http://stale.example.test/mcp?token=stale-token"},
					{Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp"},
				},
			}}
			required := runner.ProjectionRequest{
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot:               project.Scope{Domains: []string{"scope.example.test"}},
				DaemonAddr:                  "127.0.0.1:8787",
				AuthToken:                   "required-token",
			}
			if _, err := runner.ProjectBlackboardV2RuntimeConfig(layout, profile, required); err != nil {
				t.Fatalf("project required Runtime config: %v", err)
			}
			if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: 9,
			}, required.ScopeSnapshot); err != nil {
				t.Fatalf("project required Blackboard files: %v", err)
			}
			legacyProjection, err := runner.ProjectRuntimeConfig(layout, profile, required)
			if err != nil {
				t.Fatalf("project legacy required Runtime config: %v", err)
			}
			blackboardPath := filepath.Join(layout.Workdir, ".pentest", "blackboard.json")
			if err := os.WriteFile(blackboardPath, []byte(`{"schema":"runtime-blackboard/v2","revision":9}`), 0o600); err != nil {
				t.Fatalf("materialize Working Blackboard Snapshot: %v", err)
			}
			operatorFile := filepath.Join(layout.Workdir, "operator-notes.md")
			if err := os.WriteFile(operatorFile, []byte("preserve operator data"), 0o600); err != nil {
				t.Fatalf("write operator file: %v", err)
			}
			for _, path := range []string{
				filepath.Join(layout.Workdir, "AGENTS.md"),
				filepath.Join(layout.Workdir, ".pentest", "context.json"),
				blackboardPath,
				providerMCPConfigPath(layout, provider, legacyProjection),
				operatorFile,
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("required projection artifact %s is missing: %v", path, err)
				}
			}
			if provider == runtimeprofile.ProviderClaudeCode {
				if _, err := os.Stat(filepath.Join(layout.Workdir, "CLAUDE.md")); err != nil {
					t.Fatalf("required Claude checklist is missing: %v", err)
				}
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			omitted := required
			omitted.BlackboardProjection = runner.BlackboardProjectionOmitted
			if _, err := runner.ProjectRuntimeConfig(layout, profile, omitted); err == nil {
				t.Fatal("omitted projection accepted a reused layout with required Blackboard artifacts")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectModifiedGeneratedInstructionsWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "modified-generated-instructions-" + string(provider)
			layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			scope := project.Scope{Domains: []string{"scope.example.test"}}
			if err := runner.ProjectBlackboardV2Files(layout, provider, blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: 9,
			}, scope); err != nil {
				t.Fatalf("project required Blackboard files: %v", err)
			}
			instructionPath := filepath.Join(layout.Workdir, "AGENTS.md")
			if provider == runtimeprofile.ProviderClaudeCode {
				instructionPath = filepath.Join(layout.Workdir, "CLAUDE.md")
			}
			generated, err := os.ReadFile(instructionPath)
			if err != nil {
				t.Fatalf("read generated instructions: %v", err)
			}
			modified := append([]byte(nil), generated...)
			for _, marker := range [][]byte{
				[]byte("Reread Scope and the Blackboard snapshot"),
				[]byte("Write with Blackboard Keys and current versions"),
				[]byte("Blackboard scope labels never grant authorization"),
			} {
				changedAt := bytes.Index(modified, marker)
				if changedAt < 0 {
					t.Fatalf("generated checklist does not contain marker %q", marker)
				}
				modified[changedAt] += 'a' - 'A'
			}
			if err := os.WriteFile(instructionPath, modified, 0o600); err != nil {
				t.Fatalf("modify generated instructions: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
			}, runner.ProjectionRequest{
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot:               scope,
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted Runtime-modified Blackboard instructions")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectGeneratedInstructionHeaderWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "generated-instruction-header-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			instructionPath := filepath.Join(layout.Workdir, "AGENTS.md")
			if provider == runtimeprofile.ProviderClaudeCode {
				instructionPath = filepath.Join(layout.Workdir, "CLAUDE.md")
			}
			if err := os.WriteFile(instructionPath, []byte("# Blackboard workflow\n\n"), 0o600); err != nil {
				t.Fatalf("write generated instruction header: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
			}, runner.ProjectionRequest{
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted the generated Blackboard instruction header")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersSupportFreshAndRepeatedOmittedProjection(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		for _, externalMCP := range []bool{false, true} {
			name := "zero-external-mcp"
			if externalMCP {
				name = "external-mcp"
			}
			t.Run(string(provider)+"/"+name, func(t *testing.T) {
				taskID := "repeated-" + string(provider) + "-" + name
				layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
				if err != nil {
					t.Fatalf("prepare Runtime layout: %v", err)
				}
				profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{Model: "ordinary-model"}}
				if externalMCP {
					profile.Fields.MCPServers = []runtimeprofile.MCPServer{{
						Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp?token=external-token",
					}}
				}
				userFiles := map[string][]byte{
					filepath.Join(layout.Workdir, "AGENTS.md"):         []byte("# Operator notes\n\nBlackboard workflow is discussed here, but this is not generated instructions.\n"),
					filepath.Join(layout.Workdir, "CLAUDE.md"):         []byte("Claude operator preferences; no Project Interface authority.\n"),
					filepath.Join(layout.Workdir, "operator-notes.md"): []byte("Trusted MCP is pre-configured\n## Required workflow\nblackboard_finish\n.pentest/context.json\nPENTEST_INTERFACE_TOKEN=operator-example\n"),
				}
				for path, raw := range userFiles {
					if err := os.WriteFile(path, raw, 0o600); err != nil {
						t.Fatalf("write user file %s: %v", path, err)
					}
				}
				request := runner.ProjectionRequest{
					GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
					Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
					ScopeSnapshot:               project.Scope{Domains: []string{"scope.example.test"}},
					BlackboardProjection:        runner.BlackboardProjectionOmitted,
				}
				first, err := runner.ProjectRuntimeConfig(layout, profile, request)
				if err != nil {
					t.Fatalf("project fresh omitted Runtime config: %v", err)
				}
				mcpPath := providerMCPConfigPath(layout, provider, first)
				var firstMCP []byte
				if externalMCP {
					firstMCP, err = os.ReadFile(mcpPath)
					if err != nil || !strings.Contains(string(firstMCP), "external-docs") {
						t.Fatalf("External MCP Server projection = %s, err=%v", firstMCP, err)
					}
				}
				scopePath := filepath.Join(layout.Workdir, ".pentest", "scope.json")
				firstScope, err := os.ReadFile(scopePath)
				if err != nil || !strings.Contains(string(firstScope), "scope.example.test") {
					t.Fatalf("Scope projection = %s, err=%v", firstScope, err)
				}
				if _, err := runner.ProjectRuntimeConfig(layout, profile, request); err != nil {
					t.Fatalf("repeat omitted Runtime projection: %v", err)
				}
				for path, want := range userFiles {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Errorf("user file %s changed: %q, err=%v", path, got, err)
					}
				}
				if externalMCP {
					secondMCP, err := os.ReadFile(mcpPath)
					if err != nil || !bytes.Equal(secondMCP, firstMCP) {
						t.Errorf("repeated Omitted projection changed External MCP config: %s, err=%v", secondMCP, err)
					}
				}
				if secondScope, err := os.ReadFile(scopePath); err != nil || !bytes.Equal(secondScope, firstScope) {
					t.Errorf("repeated Omitted projection changed Scope: %s, err=%v", secondScope, err)
				}
			})
		}
	}
}

func TestRuntimeProvidersSupportRepeatedOmittedProjectionWithExternalPentestServer(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("USERPROFILE", t.TempDir())
			taskID := "external-pentest-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
				Model: "ordinary-model",
				MCPServers: []runtimeprofile.MCPServer{{
					Name: "pentest", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp?token=external-token",
				}},
			}}
			request := runner.ProjectionRequest{
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot:               project.Scope{Domains: []string{"scope.example.test"}},
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
			}
			first, err := runner.ProjectRuntimeConfig(layout, profile, request)
			if err != nil {
				t.Fatalf("project fresh Omitted Runtime config: %v", err)
			}
			mcpPath := providerMCPConfigPath(layout, provider, first)
			firstMCP, err := os.ReadFile(mcpPath)
			if err != nil || !strings.Contains(string(firstMCP), "pentest") || !strings.Contains(string(firstMCP), "external-token") {
				t.Fatalf("External MCP Server projection = %s, err=%v", firstMCP, err)
			}
			if _, err := runner.ProjectRuntimeConfig(layout, profile, request); err != nil {
				t.Fatalf("repeat Omitted Runtime projection: %v", err)
			}
			secondMCP, err := os.ReadFile(mcpPath)
			if err != nil || !bytes.Equal(secondMCP, firstMCP) {
				t.Fatalf("repeat Omitted projection changed External MCP config: %s, err=%v", secondMCP, err)
			}
		})
	}
}

func optionalProjectionProviders() []runtimeprofile.Provider {
	return []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex,
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
	}
}

func providerMCPConfigPath(layout runner.Layout, provider runtimeprofile.Provider, projection runner.ConfigProjection) string {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		return filepath.Join(layout.Workdir, ".mcp.json")
	case runtimeprofile.ProviderPi:
		return filepath.Join(layout.ProviderHome, "agent", "mcp.json")
	default:
		return projection.ConfigPath
	}
}
