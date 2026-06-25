package modelprovidermigrate_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/modelprovidermigrate"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestPreviewLegacyCodexProfile(t *testing.T) {
	svc := newServices(t)
	profile, err := svc.Profiles.Create("Codex CN", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Endpoint: "https://api.example.test/v1/",
		Model:    "gpt-5",
		APIKeys:  map[string]string{"OPENAI_API_KEY": "sk-test"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	preview, err := svc.Migrator.Preview(profile.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Eligible {
		t.Fatalf("expected eligible preview, reason=%q", preview.Reason)
	}
	if preview.Proposed.BaseURL != "https://api.example.test/v1" {
		t.Fatalf("base URL = %q", preview.Proposed.BaseURL)
	}
	if preview.Proposed.Model != "gpt-5" {
		t.Fatalf("model = %q", preview.Proposed.Model)
	}
	if preview.Proposed.SuggestedProtocol != modelprovider.ProtocolOpenAIResponses {
		t.Fatalf("protocol = %q", preview.Proposed.SuggestedProtocol)
	}
	if len(preview.APIKeySources) == 0 || preview.APIKeySources[0].Kind != "inline_api_key" {
		t.Fatalf("api key sources = %#v", preview.APIKeySources)
	}
}

func TestPreviewShowsExistingProviderMatch(t *testing.T) {
	svc := newServices(t)
	if _, err := svc.Providers.Create(modelprovider.CreateRequest{
		Name:      "Shared",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	profile, err := svc.Profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Endpoint: "https://api.example.test/v1",
		Model:    "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	preview, err := svc.Migrator.Preview(profile.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Matches) != 1 || preview.Matches[0].Provider.Name != "Shared" {
		t.Fatalf("matches = %#v", preview.Matches)
	}
}

func TestApplyCreateClearsLegacyFieldsAndMigratesAPIKey(t *testing.T) {
	svc := newServices(t)
	profile, err := svc.Profiles.Create("Pi Local", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Endpoint: "https://api.mimo.test/v1",
		Model:    "mimo-v2",
		APIKeys:  map[string]string{"ANTHROPIC_API_KEY": "sk-pi-secret"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result, err := svc.Migrator.Apply(modelprovidermigrate.ApplyRequest{
		ProfileID:     profile.ID,
		Action:        modelprovidermigrate.ActionCreate,
		MigrateAPIKey: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Provider.ID == "" || result.Provider.APIKeyEnv == "" {
		t.Fatalf("provider = %#v", result.Provider)
	}
	if result.Profile.Fields.ModelProviderID != result.Provider.ID {
		t.Fatalf("profile provider id = %q", result.Profile.Fields.ModelProviderID)
	}
	if result.Profile.Fields.Endpoint != "" || result.Profile.Fields.Model != "" {
		t.Fatalf("legacy fields not cleared: %#v", result.Profile.Fields)
	}
	if len(result.Profile.Fields.APIKeys) != 0 {
		t.Fatalf("inline api keys not cleared: %#v", result.Profile.Fields.APIKeys)
	}
	if result.Provider.Catalog.DefaultModel != "mimo-v2" {
		t.Fatalf("default model = %q", result.Provider.Catalog.DefaultModel)
	}

	resolution, err := svc.Creds.Resolve(result.Provider.APIKeyEnv, "")
	if err != nil || !resolution.Found {
		t.Fatalf("credential binding missing: %#v err=%v", resolution, err)
	}
}

func TestApplyReuseExistingProvider(t *testing.T) {
	svc := newServices(t)
	provider, err := svc.Providers.Create(modelprovider.CreateRequest{
		Name:      "Existing",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-5"}, DefaultModel: "gpt-5"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	profile, err := svc.Profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Endpoint: "https://api.example.test/v1",
		Model:    "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result, err := svc.Migrator.Apply(modelprovidermigrate.ApplyRequest{
		ProfileID:  profile.ID,
		Action:     modelprovidermigrate.ActionReuse,
		ProviderID: provider.ID,
	})
	if err != nil {
		t.Fatalf("apply reuse: %v", err)
	}
	if result.Provider.ID != provider.ID {
		t.Fatalf("provider id = %q", result.Provider.ID)
	}
	if result.Profile.Fields.ModelProviderID != provider.ID {
		t.Fatalf("profile provider id = %q", result.Profile.Fields.ModelProviderID)
	}
}

func TestPreviewNotEligibleWhenAlreadyMigrated(t *testing.T) {
	svc := newServices(t)
	provider, err := svc.Providers.Create(modelprovider.CreateRequest{
		Name:    "Existing",
		BaseURL: "https://api.example.test/v1",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	profile, err := svc.Profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
		Endpoint:        "https://legacy.example.test/v1",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	preview, err := svc.Migrator.Preview(profile.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Eligible {
		t.Fatal("expected ineligible preview")
	}
}

type testServices struct {
	Profiles  *runtimeprofile.Service
	Providers *modelprovider.Service
	Creds     *credential.Service
	Migrator  *modelprovidermigrate.Service
}

func newServices(t *testing.T) testServices {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	profiles := runtimeprofile.NewService(db)
	providers := modelprovider.NewService(db)
	creds := credential.NewService(db)
	plugins := runtimeplugin.MustBuiltinRegistry()
	migrator := modelprovidermigrate.NewService(profiles, providers, creds, plugins)
	return testServices{Profiles: profiles, Providers: providers, Creds: creds, Migrator: migrator}
}

func TestClearLegacyEnvKeys(t *testing.T) {
	fields := modelprovidermigrate.ClearLegacyModelFields(runtimeprofile.Fields{
		Endpoint: "https://api.example.test/v1",
		Model:    "gpt-5",
		Env: map[string]string{
			"OPENAI_BASE_URL":      "https://api.example.test/v1",
			"CODEX_MODEL_PROVIDER": "custom",
			"KEEP_ME":              "1",
		},
		APIKeys: map[string]string{"OPENAI_API_KEY": "sk-test"},
	}, runtimeprofile.ProviderCodex)

	if fields.Endpoint != "" || fields.Model != "" {
		t.Fatalf("endpoint/model not cleared")
	}
	if fields.Env["OPENAI_BASE_URL"] != "" || fields.Env["CODEX_MODEL_PROVIDER"] != "" {
		t.Fatalf("legacy env not cleared: %#v", fields.Env)
	}
	if fields.Env["KEEP_ME"] != "1" {
		t.Fatalf("unrelated env removed: %#v", fields.Env)
	}
	if !reflect.DeepEqual(fields.APIKeys, map[string]string{}) && fields.APIKeys != nil {
		t.Fatalf("api keys not cleared: %#v", fields.APIKeys)
	}
}