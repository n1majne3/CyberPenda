package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// P1 #1: Claude/Pi post-grant projection must not leave an active Continuation,
// grant, pin, working state, or grant-bearing MCP config when it fails after
// CreateContinuation would otherwise have committed.

func TestClaudeAndPiV2PostGrantProjectionFailureRollsBackDurableLaunchAndGrantConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider runtimeprofile.Provider
		binary   string
		mcpPath  func(runtimeRoot, taskID string) string
	}{
		{
			name: "claude", provider: runtimeprofile.ProviderClaudeCode, binary: "claude",
			mcpPath: func(runtimeRoot, taskID string) string {
				return filepath.Join(runtimeRoot, taskID, "workdir", ".mcp.json")
			},
		},
		{
			name: "pi", provider: runtimeprofile.ProviderPi, binary: "pi",
			mcpPath: func(runtimeRoot, taskID string) string {
				return filepath.Join(runtimeRoot, taskID, "runtime-home", "pi", "agent", "mcp.json")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot := filepath.Join(root, "runs")
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(root, "post-grant.db"),
				RuntimeRoot: runtimeRoot, AuthToken: "operator-secret", DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatalf("start daemon: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })

			createdProject, err := server.projects.Create(tc.name+" atomic", "", project.Scope{Domains: []string{"atomic.example"}}, project.Defaults{})
			if err != nil {
				t.Fatalf("create Project: %v", err)
			}
			profile, err := server.profiles.Create(tc.name+" profile", tc.provider, runtimeprofile.Fields{
				BinaryPath: "/usr/bin/" + tc.binary, Model: "test-model",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			createdTask, err := server.tasks.Create(task.CreateRequest{
				ProjectID: createdProject.ID, Goal: "inspect atomic.example",
				RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
			})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
			if err != nil {
				t.Fatalf("build launch plan: %v", err)
			}
			if !plan.BlackboardV2 {
				t.Fatal("expected Blackboard v2 launch plan")
			}

			injected := errors.New("injected post-grant projection failure")
			server.blackboardV2Continuity.SetFailureInjector(func(point blackboardv2.ContinuityFailurePoint) error {
				if point == blackboardv2.ContinuityFailureAfterBindGrant {
					// Prove BindGrant already wrote a grant-bearing config before abort.
					if raw, readErr := os.ReadFile(tc.mcpPath(runtimeRoot, createdTask.ID)); readErr == nil {
						if !strings.Contains(string(raw), "/mcp?token=") {
							t.Fatalf("expected grant-bearing MCP config before abort injection: %s", raw)
						}
					}
					return injected
				}
				return nil
			})

			_, _, err = server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if !errors.Is(err, injected) {
				t.Fatalf("post-grant failure error = %v, want injected failure", err)
			}

			for _, table := range []string{
				"task_continuations",
				"task_runtime_config_versions",
				"blackboard_v2_continuation_pins",
				"blackboard_v2_continuation_state",
				"blackboard_continuation_grants",
			} {
				var count int
				if err := server.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				if count != 0 {
					t.Fatalf("%s retained %d rows after failed post-grant projection", table, count)
				}
			}

			workingPath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", "blackboard.json")
			if _, err := os.Stat(workingPath); !os.IsNotExist(err) {
				t.Fatalf("Working Snapshot survived failed post-grant projection: %v", err)
			}
			if raw, err := os.ReadFile(tc.mcpPath(runtimeRoot, createdTask.ID)); err == nil {
				if strings.Contains(string(raw), "/mcp?token=") || strings.Contains(string(raw), "token=") {
					t.Fatalf("grant-bearing MCP config survived failed post-grant projection: %s", raw)
				}
			}

			// Retry must not hit ErrActiveContinuation.
			server.blackboardV2Continuity.SetFailureInjector(nil)
			continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if err != nil {
				t.Fatalf("retry after rolled-back post-grant failure: %v", err)
			}
			if continuation.ID == "" || bound.Adapter == nil {
				t.Fatalf("retry returned empty launch: %#v %#v", continuation, bound)
			}
			raw, err := os.ReadFile(tc.mcpPath(runtimeRoot, createdTask.ID))
			if err != nil {
				t.Fatalf("read grant-bearing MCP after successful retry: %v", err)
			}
			if !strings.Contains(string(raw), "/mcp?token=") {
				t.Fatalf("successful retry missing grant token in MCP config: %s", raw)
			}
		})
	}
}

func TestCodexV2LaunchStillSucceedsWithoutBindGrantSideEffects(t *testing.T) {
	// Preserve existing Codex networkless behavior: no BindGrant projection path
	// is required, and launch remains atomic under the shared Continuity API.
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "codex-bind.db"),
		RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Codex bind", "", project.Scope{Domains: []string{"codex.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect codex.example",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	if _, err := server.blackboardV2.Apply(context.Background(), createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "codex-bind-seed",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:codex-bind", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Codex host", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("Codex v2 launch: %v", err)
	}
	if continuation.ID == "" || bound.Adapter == nil {
		t.Fatalf("Codex launch incomplete: %#v %#v", continuation, bound)
	}
	// Codex must not project grant-bearing MCP config.
	for _, path := range []string{
		filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".mcp.json"),
		filepath.Join(runtimeRoot, createdTask.ID, "runtime-home", "codex", "config.toml"),
	} {
		if raw, err := os.ReadFile(path); err == nil && strings.Contains(string(raw), "/mcp?token=") {
			t.Fatalf("Codex projected grant-bearing MCP at %s: %s", path, raw)
		}
	}
}

func TestClaudeAndPiV2SandboxArgvAndEnvOmitIdentityAndHostTaskRoots(t *testing.T) {
	// P2: the Runtime process argv and process env (what Claude/Pi see inside
	// the sandbox) must not carry project/task/continuation/profile IDs or
	// host TaskRoot absolute paths. Host-side docker flags (--cidfile, -v
	// sources) necessarily reference the TaskRoot and are out of scope.
	for _, tc := range []struct {
		name     string
		provider runtimeprofile.Provider
		binary   string
	}{
		{"claude", runtimeprofile.ProviderClaudeCode, "claude"},
		{"pi", runtimeprofile.ProviderPi, "pi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot := filepath.Join(root, "runs")
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(root, "sandbox-leak.db"),
				RuntimeRoot: runtimeRoot, AuthToken: "operator-secret",
				SandboxImage: "pentest-kali:test", DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatalf("start daemon: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })

			createdProject, err := server.projects.Create(tc.name+" sandbox", "", project.Scope{Domains: []string{"sandbox.example"}}, project.Defaults{})
			if err != nil {
				t.Fatalf("create Project: %v", err)
			}
			profile, err := server.profiles.Create(tc.name, tc.provider, runtimeprofile.Fields{
				BinaryPath: "/usr/bin/" + tc.binary, Model: "test-model",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			createdTask, err := server.tasks.Create(task.CreateRequest{
				ProjectID: createdProject.ID, Goal: "inspect sandbox.example",
				RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
			})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
			if err != nil {
				t.Fatalf("build plan: %v", err)
			}
			continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if err != nil {
				t.Fatalf("prepare Continuation: %v", err)
			}
			createArgs, ok := runtime.DockerSandboxCreateArgs(bound.Adapter)
			if !ok {
				t.Fatal("expected Docker sandbox adapter with inspectable create argv")
			}
			taskRoot := filepath.Join(runtimeRoot, createdTask.ID)

			// Process env (-e KEY=VALUE) visible to the Runtime.
			var envPairs []string
			for i := 0; i < len(createArgs)-1; i++ {
				if createArgs[i] == "-e" {
					envPairs = append(envPairs, createArgs[i+1])
					i++
				}
			}
			envJoined := strings.Join(envPairs, "\n")
			for _, forbidden := range []string{
				createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
				taskRoot, runtimeRoot,
				"PENTEST_PROJECT_ID=", "PENTEST_TASK_ID=", "PENTEST_CONTINUATION_ID=",
				"PENTEST_AUTH_TOKEN=", "PENTEST_INTERFACE_TOKEN=", "PENTEST_MCP_URL=",
				"PENTEST_API_URL=",
			} {
				if forbidden != "" && strings.Contains(envJoined, forbidden) {
					t.Fatalf("sandbox process env leaked %q:\n%s", forbidden, envJoined)
				}
			}

			// Runtime command (argv after the image) is what Claude/Pi exec.
			imageIdx := -1
			for i, arg := range createArgs {
				if arg == "pentest-kali:test" {
					imageIdx = i
					break
				}
			}
			if imageIdx < 0 {
				t.Fatalf("sandbox image not found in create args: %v", createArgs)
			}
			runtimeArgv := strings.Join(createArgs[imageIdx+1:], "\n")
			for _, forbidden := range []string{
				createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
				taskRoot, runtimeRoot,
			} {
				if forbidden != "" && strings.Contains(runtimeArgv, forbidden) {
					t.Fatalf("sandbox runtime argv leaked %q:\n%s", forbidden, runtimeArgv)
				}
			}
			// Config paths inside the container must be task-relative (/task/...),
			// never host absolute paths.
			for _, arg := range createArgs[imageIdx+1:] {
				if strings.HasPrefix(arg, "/") && !strings.HasPrefix(arg, "/task/") && !strings.HasPrefix(arg, "/usr/") && !strings.HasPrefix(arg, "/opt/") {
					// Allow absolute binary paths under /usr; reject other host paths.
					if strings.Contains(arg, runtimeRoot) || strings.Contains(arg, taskRoot) {
						t.Fatalf("sandbox runtime argv used host path %q", arg)
					}
				}
			}
		})
	}
}
