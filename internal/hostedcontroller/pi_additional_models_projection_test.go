package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/hostedcontroller"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

// hostedRecorderResponse returns a fresh recorder with JSON headers for the
// fake daemon transport used by projection tests.
func hostedRecorderResponse() *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "application/json")
	return response
}

// TestHostedPIAdditionalModelsReachTheProjectedPiRegistry covers the real
// Model Provider service and Pi Config Projection path: bootstrap creates the
// planned providers, and the existing Global Model Projection writes each
// distinct model into task-local models.json and auth.json. No live model or
// TSecBench platform is contacted.
func TestHostedPIAdditionalModelsReachTheProjectedPiRegistry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	credentials := credential.NewService(db)

	var capturedProfile runtimeprofile.Profile
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := hostedRecorderResponse()
		switch request.Method + " " + request.URL.Path {
		case "POST /api/model-providers":
			var input modelprovider.CreateRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			provider, err := providers.Create(input)
			if err != nil {
				t.Fatalf("create planned provider %q: %v", input.Name, err)
			}
			_ = json.NewEncoder(response).Encode(provider)
		case "POST /api/runtime-profiles":
			if err := json.NewDecoder(request.Body).Decode(&capturedProfile); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(response, `{"id":"profile-1"}`)
		case "POST /api/projects":
			_, _ = io.WriteString(response, `{"id":"project-1"}`)
		case "POST /api/projects/project-1/tasks":
			_, _ = io.WriteString(response, `{"id":"task-1"}`)
		case "PUT /api/skills/ctf-orchestrator":
			_, _ = io.WriteString(response, `{}`)
		case "PUT /api/projects/project-1/credential-bindings":
			var body struct {
				CredentialRef string            `json:"credential_ref"`
				Source        credential.Source `json:"source"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, err := credentials.Upsert(body.CredentialRef, credential.ScopeProject, "project-1", body.Source, false); err != nil {
				t.Fatalf("bind %s: %v", body.CredentialRef, err)
			}
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		return response.Result(), nil
	})}

	env := validPiEnv()
	env["CYBERPENDA_MODEL"] = "parent-model"
	env["CYBERPENDA_MODEL_API_KEY"] = "parent-key"
	env["CYBERPENDA_CONTEXT_WINDOW"] = "1048576"
	env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "393216"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "inherited-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2"] = "second-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL"] = "http://second.tsecbench.gw/v1"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY"] = "second-key"

	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatal(err)
	}
	created, err := providers.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created providers = %d, want 2", len(created))
	}
	if got := created[0].Catalog.Manual; len(got) != 2 || !containsAll(got, "parent-model", "inherited-model") {
		t.Fatalf("parent catalog manual = %#v, want parent-model and inherited-model", got)
	}
	if got := created[1].Catalog.Manual; len(got) != 1 || got[0] != "second-model" {
		t.Fatalf("additional catalog manual = %#v, want second-model", got)
	}
	if capturedProfile.Fields.ModelProviderID != created[0].ID || capturedProfile.Fields.ModelOverride != "parent-model" {
		t.Fatalf("parent selection changed: provider %q model override %q", capturedProfile.Fields.ModelProviderID, capturedProfile.Fields.ModelOverride)
	}
	// The daemon resolves ModelOverride into Fields.Model through the Model
	// Provider Snapshot; apply the same effect at this seam.
	if capturedProfile.Fields.Model == "" {
		capturedProfile.Fields.Model = capturedProfile.Fields.ModelOverride
	}

	// Mirror the daemon launch sequence: the real Project Credential Bindings
	// feed the pre-transaction materialized snapshot, which then drives Config
	// Projection. No environment-variable fallbacks are involved.
	capturedProfile.Fields.CredentialRefs = nil
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-additional-models", capturedProfile.Provider)
	if err != nil {
		t.Fatal(err)
	}
	ownerContract := owner.NewTaskContract("task-additional-models", "project-1", layout.Workdir)
	capabilityCache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"second-model": {ContextWindow: 200000, MaxOutputTokens: 32000},
	}, "", nil)
	materialized, err := runner.MaterializeLaunchCredentials(capturedProfile, runner.ProjectionRequest{
		Owner:           ownerContract,
		Credentials:     credentials,
		ModelProviders:  providers,
		CapabilityCache: capabilityCache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if materialized[created[0].APIKeyEnv] != "parent-key" || materialized[created[1].APIKeyEnv] != "second-key" {
		t.Fatalf("materialized launch credentials = %#v, want both model keys", materialized)
	}
	request := runner.ProjectionRequest{
		Owner:                       ownerContract,
		Credentials:                 credentials,
		ModelProviders:              providers,
		MaterializedCredentials:     materialized,
		GlobalModelProviderSnapshot: &runner.GlobalModelProviderSnapshot{Providers: created},
		CapabilityCache:             capabilityCache,
	}
	projection, err := runner.ProjectRuntimeConfig(layout, capturedProfile, request)
	if err != nil {
		t.Fatal(err)
	}

	rawModels, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var modelsDoc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID            string
				ContextWindow int
				MaxTokens     int
			}
		} `json:"providers"`
	}
	if err := json.Unmarshal(rawModels, &modelsDoc); err != nil {
		t.Fatal(err)
	}
	if len(modelsDoc.Providers) != 2 {
		t.Fatalf("projected providers = %d, want 2: %s", len(modelsDoc.Providers), rawModels)
	}
	parentEntry, parentProjected := modelsDoc.Providers[created[0].ID]
	secondEntry, secondProjected := modelsDoc.Providers[created[1].ID]
	if !parentProjected || !secondProjected {
		t.Fatalf("projected providers = %#v, want both the parent and additional provider", modelsDoc.Providers)
	}
	if parentEntry.BaseURL != "http://model.tsecbench.gw/v1" || parentEntry.API != "openai-completions" ||
		parentEntry.APIKey != "$"+created[0].APIKeyEnv || len(parentEntry.Models) != 2 ||
		!containsAll(modelIDs(parentEntry.Models), "parent-model", "inherited-model") {
		t.Fatalf("parent models.json entry = %#v", parentEntry)
	}
	if secondEntry.BaseURL != "http://second.tsecbench.gw/v1" || secondEntry.API != "openai-completions" ||
		secondEntry.APIKey != "$"+created[1].APIKeyEnv || len(secondEntry.Models) != 1 ||
		secondEntry.Models[0].ID != "second-model" {
		t.Fatalf("additional models.json entry = %#v", secondEntry)
	}
	for _, entry := range []struct {
		name   string
		models []struct {
			ID            string
			ContextWindow int
			MaxTokens     int
		}
	}{{"parent", parentEntry.Models}, {"additional", secondEntry.Models}} {
		for _, model := range entry.models {
			// Explicit Hosted limits beat the Model Capability Cache entry.
			if model.ContextWindow != 1048576 || model.MaxTokens != 393216 {
				t.Fatalf("%s model %s limits = %d/%d, want 1048576/393216", entry.name, model.ID, model.ContextWindow, model.MaxTokens)
			}
		}
	}

	rawAuth, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var authDoc map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(rawAuth, &authDoc); err != nil {
		t.Fatal(err)
	}
	if len(authDoc) != 2 {
		t.Fatalf("projected auth = %#v, want both providers", authDoc)
	}
	if authDoc[created[0].ID].Key != "parent-key" || authDoc[created[1].ID].Key != "second-key" {
		t.Fatalf("projected auth = %#v", authDoc)
	}

	previewRaw, err := json.Marshal(projection.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"parent-key", "second-key"} {
		if strings.Contains(string(previewRaw), secret) {
			t.Fatalf("config preview disclosed the API key %q: %s", secret, previewRaw)
		}
	}
	authPreview, _ := projection.Config["auth_json"].(map[string]any)
	if len(authPreview) != 2 {
		t.Fatalf("redacted auth preview = %#v, want both providers", authPreview)
	}
	for providerID, redacted := range authPreview {
		entry, _ := redacted.(map[string]any)
		if entry["key"] != "[REDACTED]" {
			t.Fatalf("auth preview for %s = %#v, want a redacted key", providerID, entry)
		}
	}
}

// containsAll reports whether got contains every wanted value.
func containsAll(got []string, wanted ...string) bool {
	set := make(map[string]bool, len(got))
	for _, value := range got {
		set[value] = true
	}
	for _, value := range wanted {
		if !set[value] {
			return false
		}
	}
	return true
}

func modelIDs(models []struct {
	ID            string
	ContextWindow int
	MaxTokens     int
}) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}
