package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// The configured daemon token authorizes the full HTTP API. The generated
// loopback token authorizes Blackboard operator actions. A Runtime environment
// is not trusted with either authority, so neither token may reach a launch
// plan, a projected config file, or a Runtime process environment. Only a
// Continuation Interface Grant may authenticate Runtime traffic.
func TestLegacyLaunchPlanNeverProjectsOperatorToken(t *testing.T) {
	for _, test := range []struct {
		name      string
		authToken string
	}{
		{name: "configured", authToken: "operator-secret-must-never-project"},
		{name: "generated-loopback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot := filepath.Join(root, "runs")
			server, err := NewServer(Config{
				DBPath:               filepath.Join(root, "db.sqlite"),
				RuntimeRoot:          runtimeRoot,
				AuthToken:            test.authToken,
				DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			operatorToken := server.operatorToken
			if operatorToken == "" {
				t.Fatal("daemon omitted operator authority")
			}

			proj, err := server.projects.CreateWithKind("Token boundary", "", project.KindPentest, project.Scope{}, project.Defaults{})
			if err != nil {
				t.Fatal(err)
			}
			profile, err := server.profiles.Create("Claude legacy boundary", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{Model: "test-model"})
			if err != nil {
				t.Fatal(err)
			}
			created, err := server.tasks.Create(task.CreateRequest{
				ProjectID: proj.ID, Type: task.TypePentest, Goal: "inspect boundary",
				RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
			})
			if err != nil {
				t.Fatal(err)
			}

			// A nil binding forces the legacy non-v2 projection branch, the one path
			// that historically read the daemon operator token.
			plan, err := server.buildTaskLaunchPlanWithBinding(created, "inspect boundary", "", "", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			if raw, err := json.Marshal(plan.RuntimeConfig); err == nil && strings.Contains(string(raw), operatorToken) {
				t.Fatalf("launch plan runtime config leaked operator token: %s", raw)
			}
			if raw, err := json.Marshal(plan.CapturedRuntimeConfig); err == nil && strings.Contains(string(raw), operatorToken) {
				t.Fatalf("captured runtime config leaked operator token: %s", raw)
			}

			layoutRoot := filepath.Join(runtimeRoot, created.ID)
			walkErr := filepath.WalkDir(layoutRoot, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return walkErr
				}
				content, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(content), operatorToken) {
					t.Errorf("projected file %s leaked the daemon operator token", path)
				}
				return nil
			})
			if walkErr != nil {
				t.Fatalf("scan projected layout: %v", walkErr)
			}
		})
	}
}
