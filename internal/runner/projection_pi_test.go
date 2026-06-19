package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectPiConfigWritesModelsAndAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	creds := credential.NewService(db)
	if _, err := creds.Upsert("anthropic-key", credential.ScopeGlobal, "", credential.Source{
		Kind:  credential.SourceEnv,
		Value: "ANTHROPIC_API_KEY",
	}, false); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model:          "claude-sonnet-4",
			Endpoint:       "https://proxy.example.test/anthropic",
			CredentialRefs: []string{"anthropic-key"},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:   "project-1",
		Credentials: creds,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	agentDir := filepath.Join(layout.ProviderHome, "agent")
	modelsPath := filepath.Join(agentDir, "models.json")
	authPath := filepath.Join(agentDir, "auth.json")
	if projection.ConfigPath != modelsPath {
		t.Fatalf("expected models.json path, got %q", projection.ConfigPath)
	}

	modelsRaw, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(modelsRaw, &models); err != nil {
		t.Fatalf("decode models.json: %v", err)
	}
	custom, ok := models.Providers["custom"]
	if !ok {
		t.Fatalf("expected custom provider, got %#v", models.Providers)
	}
	if custom.BaseURL != "https://proxy.example.test/anthropic" {
		t.Fatalf("unexpected baseUrl: %q", custom.BaseURL)
	}
	if custom.API != "anthropic-messages" {
		t.Fatalf("unexpected api: %q", custom.API)
	}
	if custom.APIKey != "$ANTHROPIC_API_KEY" {
		t.Fatalf("unexpected apiKey ref: %q", custom.APIKey)
	}
	if len(custom.Models) != 1 || custom.Models[0].ID != "claude-sonnet-4" {
		t.Fatalf("unexpected models: %#v", custom.Models)
	}

	authRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		t.Fatalf("decode auth.json: %v", err)
	}
	entry, ok := auth["custom"]
	if !ok {
		t.Fatalf("expected custom provider auth entry, got %#v", auth)
	}
	if entry.Type != "api_key" || entry.Key != "sk-ant-test-key" {
		t.Fatalf("unexpected custom provider auth: %#v", entry)
	}

	authPreview, ok := projection.Config["auth_json"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth_json preview, got %#v", projection.Config["auth_json"])
	}
	customPreview, ok := authPreview["custom"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom provider preview entry, got %#v", authPreview["custom"])
	}
	if customPreview["key"] != "[REDACTED]" {
		t.Fatalf("expected redacted key preview, got %#v", customPreview["key"])
	}
}
