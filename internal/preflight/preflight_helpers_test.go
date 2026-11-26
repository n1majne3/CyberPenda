package preflight_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

type services struct {
	preflight      *preflight.Service
	db             *store.DB
	profiles       *runtimeprofile.Service
	creds          *credential.Service
	modelProviders *modelprovider.Service
}

func newTestServices(t *testing.T) services {
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
	profiles := runtimeprofile.NewService(db)
	creds := credential.NewService(db)
	providers := modelprovider.NewService(db)
	svc := services{db: db, profiles: profiles, creds: creds, modelProviders: providers}
	svc.preflight = svc.newPreflight()
	return svc
}

func (svc services) newPreflight(skills ...preflight.SkillGetter) *preflight.Service {
	return preflight.NewService(svc.profiles, svc.creds, skills...).
		WithModelProviders(svc.modelProviders, runtimeplugin.MustBuiltinRegistry()).
		WithContainerRunner(func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Server:\n Name: test-engine\n Operating System: test\n"), nil
		})
}

func newPiExtensionRegistry(t *testing.T, source string) *runtimeextension.Registry {
	t.Helper()
	loaded, errs := runtimeextension.LoadDirectory(writePiExtensionManifest(t, source))
	if len(errs) > 0 {
		t.Fatalf("load extensions: %v", errs)
	}
	registry, err := runtimeextension.NewRegistry(loaded)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func writePiExtensionManifest(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	extension := runtimeextension.Extension{
		SchemaVersion: runtimeextension.SchemaVersion,
		ID:            "pi_browser_tools",
		Name:          "Pi Browser Tools",
		CompatibleRuntimePlugins: []string{
			"pi",
		},
		Source:     runtimeextension.Source{Type: "local_dir", Path: source},
		Projection: runtimeextension.Projection{Location: "provider_home", Path: "extensions/browser-tools"},
	}
	raw, err := json.Marshal(extension)
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi_browser_tools.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func checkPassed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckPass
		}
	}
	return false
}

func checkFailed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckFail
		}
	}
	return false
}

func insertLegacyProfile(t *testing.T, db *store.DB, name, provider, fieldsJSON string) string {
	t.Helper()
	id := "legacy-" + name
	_, err := db.Exec(
		`INSERT INTO runtime_profiles (id, name, provider, fields_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, provider, fieldsJSON, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert legacy profile: %v", err)
	}
	return id
}

func checkDetail(result preflight.Result, name string) string {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Detail
		}
	}
	return ""
}

func (svc services) createProfile(t *testing.T, name string, provider runtimeprofile.Provider, fields runtimeprofile.Fields) (runtimeprofile.Profile, error) {
	t.Helper()
	if provider == runtimeprofile.ProviderFake || strings.TrimSpace(fields.ModelProviderID) != "" {
		return svc.profiles.Create(name, provider, fields)
	}
	model := strings.TrimSpace(fields.ModelOverride)
	if model == "" {
		model = strings.TrimSpace(fields.Model)
	}
	if model == "" {
		model = "gpt-test"
	}
	modelProvider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name: name + " provider",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/v1"},
			{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "https://api.example.test/v1"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/anthropic"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{model}, DefaultModel: model},
	})
	if err != nil {
		t.Fatalf("create model provider fixture: %v", err)
	}
	t.Setenv(modelProvider.APIKeyEnv, "sk-test")
	fields.Model = ""
	fields.Endpoint = ""
	if fields.ModelOverride == "" {
		fields.ModelOverride = model
	}
	fields.ModelProviderID = modelProvider.ID
	return svc.profiles.Create(name, provider, fields)
}
