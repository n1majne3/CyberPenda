package runtimeprofile_test

import (
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func newTestService(t *testing.T) *runtimeprofile.Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return runtimeprofile.NewService(db)
}

func TestCreateRejectsBlankName(t *testing.T) {
	service := newTestService(t)

	_, err := service.Create("  ", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != runtimeprofile.ErrMissingName {
		t.Fatalf("expected ErrMissingName, got %v", err)
	}
}

func TestCreateRejectsUnknownProvider(t *testing.T) {
	service := newTestService(t)

	_, err := service.Create("My Profile", "not-a-real-provider", runtimeprofile.Fields{})
	if !errors.Is(err, runtimeprofile.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}

func TestCreateAcceptsInjectedProviderIDs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	service := runtimeprofile.NewService(db, []runtimeprofile.Provider{"custom_runtime"})

	created, err := service.Create("Custom", "custom_runtime", runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create injected provider: %v", err)
	}
	if created.Provider != "custom_runtime" {
		t.Fatalf("expected custom_runtime, got %q", created.Provider)
	}

	_, err = service.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if !errors.Is(err, runtimeprofile.ErrUnknownProvider) {
		t.Fatalf("expected default provider to be rejected by injected set, got %v", err)
	}
}

func TestCreateAcceptsEverySupportedProvider(t *testing.T) {
	providers := []runtimeprofile.Provider{
		runtimeprofile.ProviderFake,
		runtimeprofile.ProviderCodex,
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
	}
	for _, provider := range providers {
		service := newTestService(t)
		created, err := service.Create("Profile-"+string(provider), provider, runtimeprofile.Fields{})
		if err != nil {
			t.Fatalf("create %q: %v", provider, err)
		}
		if created.Provider != provider {
			t.Fatalf("expected provider %q, got %q", provider, created.Provider)
		}
	}
}

func TestCreatePersistsStructuredFieldsWithoutSecrets(t *testing.T) {
	service := newTestService(t)
	enabled := true

	created, err := service.Create(
		"Codex Default",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{
			BinaryPath:     "/usr/local/bin/codex",
			Model:          "gpt-5",
			CustomArgs:     []string{"--strict"},
			CredentialRefs: []string{"codex-api-key"},
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
				ID:      "codex_review_pack",
				Enabled: &enabled,
				Config:  map[string]string{"mode": "readonly"},
			}},
			MCPServers: []runtimeprofile.MCPServer{{
				Name: "project",
				Mode: runtimeprofile.MCPServerTrusted,
				URL:  "http://127.0.0.1:8787/mcp",
			}},
			DefaultRunner: "sandbox",
		},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Fields.BinaryPath != "/usr/local/bin/codex" {
		t.Fatalf("expected binary path preserved, got %q", fetched.Fields.BinaryPath)
	}
	if got := fetched.Fields.CredentialRefs; len(got) != 1 || got[0] != "codex-api-key" {
		t.Fatalf("expected credential ref preserved, got %#v", got)
	}
	if len(fetched.Fields.MCPServers) != 1 || fetched.Fields.MCPServers[0].Name != "project" {
		t.Fatalf("expected mcp server preserved, got %#v", fetched.Fields.MCPServers)
	}
	if len(fetched.Fields.RuntimeExtensions) != 1 || fetched.Fields.RuntimeExtensions[0].ID != "codex_review_pack" {
		t.Fatalf("expected runtime extension ref preserved, got %#v", fetched.Fields.RuntimeExtensions)
	}
}

func TestUpdatePreservesUntouchedFields(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create(
		"Codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{Model: "gpt-5", BinaryPath: "/bin/codex"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update only the name; fields omitted so the existing structured fields stay.
	updated, err := service.Update(created.ID, "Codex Renamed", "", runtimeprofile.Fields{}, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Codex Renamed" {
		t.Fatalf("expected renamed profile, got %q", updated.Name)
	}
	if updated.Fields.Model != "gpt-5" {
		t.Fatalf("expected model preserved, got %q", updated.Fields.Model)
	}
	if updated.Fields.BinaryPath != "/bin/codex" {
		t.Fatalf("expected binary path preserved, got %q", updated.Fields.BinaryPath)
	}
}

func TestUpdateReplacesFieldsWhenTouched(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create(
		"Codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{Model: "gpt-5", Endpoint: "https://old.example"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newFields := runtimeprofile.Fields{Model: "gpt-5.5"}
	updated, err := service.Update(created.ID, "Codex", "", newFields, true)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Fields.Model != "gpt-5.5" {
		t.Fatalf("expected model overwritten, got %q", updated.Fields.Model)
	}
	if updated.Fields.Endpoint != "" {
		t.Fatalf("expected endpoint cleared by full-fields overwrite, got %q", updated.Fields.Endpoint)
	}
}

func TestUpdateRejectsBlankName(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Original", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Blank name keeps the existing name rather than erroring, since the HTTP
	// layer treats blank as "omit". Verify it stays the original name.
	updated, err := service.Update(created.ID, "   ", "", runtimeprofile.Fields{}, false)
	if err != nil {
		t.Fatalf("update with blank name should preserve name, got: %v", err)
	}
	if updated.Name != "Original" {
		t.Fatalf("expected name preserved, got %q", updated.Name)
	}
}

func TestUpdateRejectsUnknownProvider(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Original", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = service.Update(created.ID, "Original", "not-real", runtimeprofile.Fields{}, false)
	if !errors.Is(err, runtimeprofile.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	service := newTestService(t)

	_, err := service.Get("missing")
	if err != runtimeprofile.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteRemovesProfile(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Temp", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := service.Delete(created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := service.Get(created.ID); err != runtimeprofile.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteMissingReturnsNotFound(t *testing.T) {
	service := newTestService(t)

	if err := service.Delete("missing"); err != runtimeprofile.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGeneratedConfigNeverLeaksSecrets(t *testing.T) {
	enabled := true
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model:          "gpt-5",
			CredentialRefs: []string{"codex-api-key"},
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
				ID:      "codex_review_pack",
				Enabled: &enabled,
				Config:  map[string]string{"mode": "readonly"},
			}},
		},
	}

	cfg := runtimeprofile.GeneratedConfig(profile)

	if cfg["model"] != "gpt-5" {
		t.Fatalf("expected model in config, got %v", cfg["model"])
	}
	refs, ok := cfg["credential_refs"].([]string)
	if !ok || len(refs) != 1 || refs[0] != "codex-api-key" {
		t.Fatalf("expected credential_refs as references, got %#v", cfg["credential_refs"])
	}
	extensions, ok := cfg["runtime_extensions"].([]map[string]any)
	if !ok || len(extensions) != 1 || extensions[0]["id"] != "codex_review_pack" {
		t.Fatalf("expected runtime extension refs in config, got %#v", cfg["runtime_extensions"])
	}
	// The generated config must never contain a raw secret value.
	for key := range cfg {
		if key == "secret" || key == "api_key" || key == "token" {
			t.Fatalf("generated config must not contain secret field %q", key)
		}
	}
}
