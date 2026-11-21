package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestBuildSandboxCommandRejectsSymlinkReadOnlyTaskFileInBindMountMode(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-bind-symlink", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	pentestDir := filepath.Join(layout.Workdir, ".pentest")
	if err := os.MkdirAll(pentestDir, 0o700); err != nil {
		t.Fatalf("mkdir .pentest: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(pentestDir, "scope.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err = runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:            layout,
		Provider:          runtimeprofile.ProviderCodex,
		RuntimeCommand:    []string{"codex", "exec"},
		ReadOnlyTaskFiles: []string{"workdir/.pentest/scope.json"},
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected bind-mount symbolic link rejection, got %v", err)
	}
}

func TestBuildSandboxCommandPassesProcessEnvViaCommandEnvWithoutArgvSecrets(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-env-argv", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	req := runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderCodex,
		RuntimeCommand: []string{"codex", "exec"},
		ProcessEnv: map[string]string{
			"Z_TOKEN":        "secret-z-value",
			"OPENAI_API_KEY": "sk-secret-openai",
			"A_FLAG":         "value-a",
		},
	}
	cmd1, err := runner.BuildSandboxCommand(req)
	if err != nil {
		t.Fatalf("build sandbox command: %v", err)
	}
	cmd2, err := runner.BuildSandboxCommand(req)
	if err != nil {
		t.Fatalf("build sandbox command second time: %v", err)
	}
	if !reflect.DeepEqual(cmd1.Args, cmd2.Args) {
		t.Fatalf("sandbox args are non-deterministic:\n%v\n%v", cmd1.Args, cmd2.Args)
	}
	joined := strings.Join(cmd1.Args, "\n")
	for _, secret := range []string{"secret-z-value", "sk-secret-openai", "value-a"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("sandbox argv leaked env value %q:\n%s", secret, joined)
		}
	}
	for _, key := range []string{"A_FLAG", "OPENAI_API_KEY", "Z_TOKEN"} {
		if cmd1.Env[key] != req.ProcessEnv[key] {
			t.Fatalf("Command.Env[%q] = %q, want %q", key, cmd1.Env[key], req.ProcessEnv[key])
		}
	}
	encoded, err := json.Marshal(cmd1)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	if strings.Contains(string(encoded), "sk-secret-openai") {
		t.Fatalf("Command JSON leaked secret env: %s", encoded)
	}
}

func TestProjectCodexConfigOnlyWritesOpenAIAPIKeyToAuthJSON(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-codex-auth", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID:       "codex-1",
		Name:     "Codex",
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model: "gpt-5",
			APIKeys: map[string]string{
				"AWS_ACCESS_KEY_ID": "AKIA-UNRELATED-SECRET",
				"GITHUB_TOKEN":      "ghp-unrelated-secret",
				"OPENAI_API_KEY":    "sk-codex-openai-key",
			},
		},
	}
	_, err = runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil)})
	if err != nil {
		t.Fatalf("project codex config: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "auth.json"))
	if err != nil {
		t.Fatalf("read codex auth.json: %v", err)
	}
	var auth map[string]string
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatalf("unmarshal codex auth.json: %v", err)
	}
	if auth["OPENAI_API_KEY"] != "sk-codex-openai-key" {
		t.Fatalf("OPENAI_API_KEY = %q, want sk-codex-openai-key", auth["OPENAI_API_KEY"])
	}
	if len(auth) != 1 {
		t.Fatalf("codex auth.json leaked unrelated credentials: %#v", auth)
	}
}

func TestProjectPiConfigPrefersProviderAPIKeyOverAlphabeticalCredentials(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-key", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID:       "pi-1",
		Name:     "Pi",
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Endpoint: "https://api.openai.com/v1",
			Model:    "gpt-5",
			APIKeys: map[string]string{
				"AWS_ACCESS_KEY_ID": "AKIA-SHOULD-NOT-WIN",
				"OPENAI_API_KEY":    "sk-pi-openai-winner",
			},
		},
	}
	_, err = runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil)})
	if err != nil {
		t.Fatalf("project pi config: %v", err)
	}
	authRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "auth.json"))
	if err != nil {
		t.Fatalf("read pi auth.json: %v", err)
	}
	var auth map[string]map[string]string
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		t.Fatalf("unmarshal pi auth.json: %v", err)
	}
	if got := auth["custom"]["key"]; got != "sk-pi-openai-winner" {
		t.Fatalf("pi auth key = %q, want sk-pi-openai-winner", got)
	}
	modelsRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read pi models.json: %v", err)
	}
	if !strings.Contains(string(modelsRaw), `"$OPENAI_API_KEY"`) {
		t.Fatalf("pi models.json did not reference $OPENAI_API_KEY: %s", modelsRaw)
	}
}
