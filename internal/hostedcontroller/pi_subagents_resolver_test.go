package hostedcontroller_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/credential"
	"pentest/internal/hostedcontroller"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

// The subagent model-selection regression runs the actual
// @tintinweb/pi-subagents model resolver used by the TSecBench Hosted Image
// against registry/auth inputs derived from the real CyberPenda projection.
// The plugin's bare-model fallback ignores case and treats dots and dashes as
// equal, so only exact "providerID/modelID" selectors discovered from the
// projected registry are guaranteed to select the intended model.
const (
	piSubagentsPackage    = "@tintinweb/pi-subagents"
	piSubagentsVersion    = "0.19.0"
	piSubagentsTarballURL = "https://registry.npmjs.org/@tintinweb/pi-subagents/-/pi-subagents-" + piSubagentsVersion + ".tgz"
)

func TestPiSubagentsModelResolverSelectsProjectedModelsByExactSelector(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; the plugin resolver regression cannot run")
	}
	resolverPath := fetchPiSubagentsModelResolver(t)

	modelsPath, authPath := projectResolverFixture(t)

	// The driver imports the resolver with a relative path, so it must live in
	// the same directory as the extracted module.
	driver := filepath.Join(filepath.Dir(resolverPath), "resolver-driver.mjs")
	if err := os.WriteFile(driver, []byte(piSubagentsDriverJS), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, driver, resolverPath, modelsPath, authPath)
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("resolver driver failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var resolutions []struct {
		Selector   string `json:"selector"`
		Provider   string `json:"provider"`
		Model      string `json:"id"`
		BaseURL    string `json:"baseUrl"`
		APIKeyRef  string `json:"apiKey"`
		Error      string `json:"error"`
		OrderIndex int    `json:"orderIndex"`
	}
	if err := json.Unmarshal(output, &resolutions); err != nil {
		t.Fatalf("decode resolver driver output %q: %v", output, err)
	}
	if len(resolutions) == 0 {
		t.Fatal("resolver driver produced no resolutions")
	}
	type expected struct {
		provider, baseURL, apiKeyRef string
	}
	want := map[string]expected{}
	var rawModels struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	modelsRaw, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(modelsRaw, &rawModels); err != nil {
		t.Fatal(err)
	}
	for providerID, provider := range rawModels.Providers {
		for _, model := range provider.Models {
			want[providerID+"/"+model.ID] = expected{provider: providerID, baseURL: provider.BaseURL, apiKeyRef: provider.APIKey}
		}
	}
	if len(want) != 4 {
		t.Fatalf("projected registry = %#v, want the four fixture models", rawModels.Providers)
	}
	for _, resolution := range resolutions {
		intended, ok := want[resolution.Selector]
		if !ok {
			t.Fatalf("driver resolved an unknown selector %q", resolution.Selector)
		}
		if resolution.Error != "" {
			t.Fatalf("selector %q failed under registry order %d: %s", resolution.Selector, resolution.OrderIndex, resolution.Error)
		}
		if resolution.Provider != intended.provider || resolution.Model != strings.SplitN(resolution.Selector, "/", 2)[1] {
			t.Fatalf("selector %q resolved to %s/%s under order %d, want the exact registry entry",
				resolution.Selector, resolution.Provider, resolution.Model, resolution.OrderIndex)
		}
		if resolution.BaseURL != intended.baseURL || resolution.APIKeyRef != intended.apiKeyRef {
			t.Fatalf("selector %q lost its provider association: baseUrl %q apiKey %q, want %q / %q",
				resolution.Selector, resolution.BaseURL, resolution.APIKeyRef, intended.baseURL, intended.apiKeyRef)
		}
	}
}

// fetchPiSubagentsModelResolver downloads the pinned plugin tarball and
// extracts the compiled model resolver. The test skips when the registry is
// unreachable so offline development still builds; the version pin keeps the
// behavior under test stable.
func fetchPiSubagentsModelResolver(t *testing.T) string {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, piSubagentsTarballURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Skipf("npm registry unavailable; cannot verify %s@%s: %v", piSubagentsPackage, piSubagentsVersion, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Skipf("npm registry returned HTTP %d for %s@%s", response.StatusCode, piSubagentsPackage, piSubagentsVersion)
	}
	archive, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("read %s@%s tarball: %v", piSubagentsPackage, piSubagentsVersion, err)
	}
	defer archive.Close()
	dir := t.TempDir()
	reader := tar.NewReader(archive)
	var resolverPath string
	var packageJSON bytes.Buffer
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read %s@%s archive: %v", piSubagentsPackage, piSubagentsVersion, err)
		}
		switch header.Name {
		case "package/dist/model-resolver.js":
			// Node only treats .js next to a package.json without "type":
			// "module" as CommonJS, so rehome the ESM file under .mjs.
			resolverPath = filepath.Join(dir, "model-resolver.mjs")
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resolverPath, content, 0o600); err != nil {
				t.Fatal(err)
			}
		case "package/package.json":
			if _, err := io.Copy(&packageJSON, reader); err != nil {
				t.Fatal(err)
			}
		}
	}
	if resolverPath == "" {
		t.Fatalf("%s@%s tarball has no dist/model-resolver.js", piSubagentsPackage, piSubagentsVersion)
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageJSON.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != piSubagentsPackage || manifest.Version != piSubagentsVersion {
		t.Fatalf("tarball identifies itself as %s@%s, want %s@%s", manifest.Name, manifest.Version, piSubagentsPackage, piSubagentsVersion)
	}
	return resolverPath
}

// projectResolverFixture runs the real Hosted bootstrap and Pi Config
// Projection for a registry with the two known ambiguity hazards: model ids
// that differ only by case inside one provider, and a dot/dash pair
// (model-4.5 versus model-4-5) across two providers.
func projectResolverFixture(t *testing.T) (string, string) {
	t.Helper()
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
				t.Fatal(err)
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
				t.Fatal(err)
			}
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		return response.Result(), nil
	})}

	env := validPiEnv()
	env["CYBERPENDA_MODEL"] = "model-4.5"
	env["CYBERPENDA_MODEL_API_KEY"] = "parent-key"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_1"] = "Scout-Model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_2"] = "scout-model"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_3"] = "model-4-5"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_3_BASE_URL"] = "http://second-model.tsecbench.gw/v1"
	env["CYBERPENDA_PI_ADDITIONAL_MODEL_3_API_KEY"] = "second-key"

	config, err := hostedcontroller.ConfigFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
	if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
		t.Fatal(err)
	}

	capturedProfile.Fields.CredentialRefs = nil
	if capturedProfile.Fields.Model == "" {
		capturedProfile.Fields.Model = capturedProfile.Fields.ModelOverride
	}
	created, err := providers.List()
	if err != nil {
		t.Fatal(err)
	}
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-resolver-fixture", capturedProfile.Provider)
	if err != nil {
		t.Fatal(err)
	}
	ownerContract := owner.NewTaskContract("task-resolver-fixture", "project-1", layout.Workdir)
	materialized, err := runner.MaterializeLaunchCredentials(capturedProfile, runner.ProjectionRequest{
		Owner:          ownerContract,
		Credentials:    credentials,
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := runner.ProjectRuntimeConfig(layout, capturedProfile, runner.ProjectionRequest{
		Owner:                       ownerContract,
		Credentials:                 credentials,
		ModelProviders:              providers,
		MaterializedCredentials:     materialized,
		GlobalModelProviderSnapshot: &runner.GlobalModelProviderSnapshot{Providers: created},
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw, marshalErr := json.Marshal(projection.Config); marshalErr == nil && strings.Contains(string(raw), "parent-key") {
		t.Fatalf("projection preview disclosed an API key: %s", raw)
	}
	modelsPath := filepath.Join(layout.ProviderHome, "agent", "models.json")
	authPath := filepath.Join(layout.ProviderHome, "agent", "auth.json")
	for _, path := range []string{modelsPath, authPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("projection output %s missing: %v", path, err)
		}
	}
	return modelsPath, authPath
}

// piSubagentsDriverJS loads the real plugin resolver, builds a registry with
// Pi coding-agent semantics (exact case-sensitive find; availability from
// auth.json), and resolves every discovered providerID/modelID selector under
// several registry orderings.
const piSubagentsDriverJS = `
import { readFileSync } from "node:fs";
import { resolveModel } from "./model-resolver.mjs";

const [resolverArg, modelsPath, authPath] = process.argv.slice(2);
const models = JSON.parse(readFileSync(modelsPath, "utf8"));
const auth = JSON.parse(readFileSync(authPath, "utf8"));

function buildRegistry(order) {
  const byProvider = new Map();
  const all = [];
  for (const [providerID, provider] of Object.entries(models.providers)) {
    const modelMap = new Map();
    for (const model of provider.models ?? []) {
      const entry = {
        provider: providerID,
        id: model.id,
        name: model.id,
        baseUrl: provider.baseUrl,
        apiKey: provider.apiKey,
      };
      modelMap.set(model.id, entry);
      all.push(entry);
    }
    byProvider.set(providerID, modelMap);
  }
  const ordered = order(all.slice());
  const available = ordered.filter((entry) => auth[entry.provider]);
  return {
    find: (provider, id) => byProvider.get(provider)?.get(id),
    getAll: () => ordered,
    getAvailable: () => available,
  };
}

const selectors = [];
for (const [providerID, provider] of Object.entries(models.providers)) {
  for (const model of provider.models ?? []) {
    selectors.push(providerID + "/" + model.id);
  }
}

const orders = [
  ["file", (all) => all],
  ["reverse", (all) => all.reverse()],
  ["sorted", (all) => all.sort((a, b) => (a.provider + a.id).localeCompare(b.provider + b.id))],
];

const results = [];
orders.forEach(([label, order], index) => {
  const registry = buildRegistry(order);
  for (const selector of selectors) {
    const resolved = resolveModel(selector, registry);
    if (typeof resolved === "string") {
      results.push({ selector, error: resolved, orderIndex: index, order: label });
    } else {
      results.push({
        selector,
        provider: resolved.provider,
        id: resolved.id,
        baseUrl: resolved.baseUrl,
        apiKey: resolved.apiKey,
        orderIndex: index,
        order: label,
      });
    }
  }
});
console.log(JSON.stringify(results));
`
