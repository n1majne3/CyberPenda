package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
)

// The daemon does not inject a built-in Blackboard MCP server: Blackboard
// access is the pentestctl CLI plus the projected Mode Skill. Generated
// AGENTS.md instructions must not describe MCP tools that do not exist.
var forbiddenBlackboardMCPText = []string{
	"MCP",
	"mcp_url",
	"blackboard_change",
	"blackboard_read",
	"blackboard_history",
	"blackboard_record_attempt_result",
	"blackboard_retain_evidence",
	"blackboard_checkpoint_attempt",
	"blackboard_finish",
}

func TestRuntimeSmokeInstructionsDescribeCLIBlackboardInterface(t *testing.T) {
	tests := []struct {
		name    string
		ctx     RuntimeOwnerContext
		wantIDs []string
	}{
		{
			name: "task",
			ctx: RuntimeOwnerContext{
				Owner:     owner.NewTaskContract("task-1", "project-1", "/task/workdir"),
				Provider:  runtimeprofile.ProviderClaudeCode,
				Sandbox:   true,
				ScopeSnapshot: project.Scope{
					Domains: []string{"example.test"},
				},
			},
			wantIDs: []string{"project_id: `project-1`", "task_id: `task-1`"},
		},
		{
			name: "session",
			ctx: RuntimeOwnerContext{
				Owner:    owner.NewSessionContract("session-1", "/session/workdir"),
				Provider: runtimeprofile.ProviderClaudeCode,
			},
			wantIDs: []string{"session_id: `session-1`"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			if err := writeRuntimeSmokeInstructions(workdir, test.ctx); err != nil {
				t.Fatalf("writeRuntimeSmokeInstructions: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md"))
			if err != nil {
				t.Fatalf("read generated AGENTS.md: %v", err)
			}
			generated := string(raw)
			for _, forbidden := range forbiddenBlackboardMCPText {
				if strings.Contains(generated, forbidden) {
					t.Fatalf("generated AGENTS.md still describes the removed Blackboard MCP interface %q:\n%s", forbidden, generated)
				}
			}
			for _, want := range append([]string{
				"pentestctl blackboard",
				"only Blackboard interface",
				"Mode Skill",
				"do not rely on chat alone",
			}, test.wantIDs...) {
				if !strings.Contains(generated, want) {
					t.Fatalf("generated AGENTS.md is missing %q:\n%s", want, generated)
				}
			}
			if test.ctx.Owner.IsSession() {
				if !strings.Contains(generated, "Keep Session Facts and Attempts self-contained") {
					t.Fatalf("generated AGENTS.md is missing the Session self-contained note:\n%s", generated)
				}
			}
		})
	}
}

func TestWriteTaskContextFilesOmitMCPURL(t *testing.T) {
	workdir := t.TempDir()
	ctx := RuntimeOwnerContext{
		Owner:         owner.NewTaskContract("task-1", "project-1", workdir),
		MCPURL:        "http://daemon.test/mcp",
		ScopeSnapshot: project.Scope{Domains: []string{"example.test"}},
	}
	if err := writeTaskContextFiles(Layout{Workdir: workdir}, ctx); err != nil {
		t.Fatalf("writeTaskContextFiles: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workdir, ".pentest", "context.json"))
	if err != nil {
		t.Fatalf("read context.json: %v", err)
	}
	payload := string(raw)
	if strings.Contains(payload, "mcp_url") {
		t.Fatalf("context.json still carries the removed MCP endpoint: %s", payload)
	}
	for _, want := range []string{`"project_id"`, `"task_id"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("context.json is missing owner identifier %s: %s", want, payload)
		}
	}
}

func TestContainsGeneratedBlackboardInstructionsDetectsCLIFormat(t *testing.T) {
	workdir := t.TempDir()
	if err := writeRuntimeSmokeInstructions(workdir, RuntimeOwnerContext{
		Owner: owner.NewSessionContract("session-1", workdir),
	}); err != nil {
		t.Fatalf("writeRuntimeSmokeInstructions: %v", err)
	}
	current, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read generated AGENTS.md: %v", err)
	}
	if !containsGeneratedBlackboardInstructions(current) {
		t.Fatalf("stale-projection preflight no longer detects current generated AGENTS.md:\n%s", current)
	}

	legacy := []byte("# Non-Project Session context\n\nTrusted MCP is pre-configured\n## Required workflow\nblackboard_finish\n.pentest/context.json\n")
	if !containsGeneratedBlackboardInstructions(legacy) {
		t.Fatal("stale-projection preflight no longer detects legacy generated AGENTS.md")
	}

	operator := []byte("# Operator notes\n\nWe prefer pentestctl blackboard today; the Mode Skill may change. This is operator content, not generated instructions.\n")
	if containsGeneratedBlackboardInstructions(operator) {
		t.Fatal("stale-projection preflight misclassifies operator notes as generated instructions")
	}
}
