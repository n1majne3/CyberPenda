package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectRuntimeConfigUsesModelProviderSnapshotWithoutLeakingKey(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	provider, err := providers.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"mimo-v2.5-pro", "mimo-v2-pro"},
			DefaultModel: "mimo-v2.5-pro",
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-secret-value")

	taskRoot := t.TempDir()
	layout := runner.Layout{
		TaskRoot:     taskRoot,
		RuntimeHome:  filepath.Join(taskRoot, "runtime-home"),
		ProviderHome: filepath.Join(taskRoot, "runtime-home", "codex"),
		Workdir:      filepath.Join(taskRoot, "workdir"),
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ModelProviders:      providers,
		LaunchModelOverride: "mimo-v2-pro",
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}
	if projection.ModelSnapshot == nil || projection.ModelSnapshot.APIKeyEnv != "MIMO_API_KEY" {
		t.Fatalf("missing model snapshot: %#v", projection.ModelSnapshot)
	}
	if projection.ResolvedProfile.Fields.Model != "mimo-v2-pro" {
		t.Fatalf("resolved model = %q", projection.ResolvedProfile.Fields.Model)
	}
	if projection.ModelSnapshot == nil || projection.ModelSnapshot.Model != "mimo-v2-pro" {
		t.Fatalf("snapshot model = %#v", projection.ModelSnapshot)
	}
	raw, err := os.ReadFile(projection.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "sk-secret-value") {
		t.Fatalf("config leaked API key: %s", raw)
	}
}
