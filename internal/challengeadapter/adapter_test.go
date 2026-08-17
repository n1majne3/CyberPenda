package challengeadapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/challengeadapter"
)

func TestLoadPrefersOverlayDirectoryOverBuiltin(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"id":"tsecbench","base_url_env":"CUSTOM_BASE","token_env":"CUSTOM_TOKEN","token_header":"X-Token"}`)
	if err := os.WriteFile(filepath.Join(dir, "tsecbench.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := challengeadapter.Load("tsecbench", []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BaseURLEnv != "CUSTOM_BASE" || manifest.TokenHeader != "X-Token" {
		t.Fatalf("overlay not used: %#v", manifest)
	}
}

func TestLoadFallsBackToBuiltinTSecBench(t *testing.T) {
	manifest, err := challengeadapter.Load("tsecbench", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "tsecbench" || manifest.TokenHeader != "BENCHMARK_TOKEN" {
		t.Fatalf("builtin = %#v", manifest)
	}
	if manifest.Operations["list"].Path != "/openapi/v1/challenges" {
		t.Fatalf("list path = %#v", manifest.Operations["list"])
	}
	if manifest.MaxActive != 3 || manifest.Budgets["hard"] != 40 {
		t.Fatalf("limits = %#v %#v", manifest.MaxActive, manifest.Budgets)
	}
}

func TestLoadRejectsUnknownAdapter(t *testing.T) {
	if _, err := challengeadapter.Load("missing", []string{t.TempDir()}); err == nil {
		t.Fatal("expected unknown adapter error")
	}
}

func TestSearchDirsPutsDataOverlayBeforeBuiltin(t *testing.T) {
	dirs := challengeadapter.SearchDirs("/data/adapters", "/opt/cyberpenda/adapters")
	if len(dirs) < 2 || dirs[0] != "/data/adapters" {
		t.Fatalf("search dirs = %#v", dirs)
	}
}
