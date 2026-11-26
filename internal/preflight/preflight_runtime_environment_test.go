package preflight_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/preflight"
	"pentest/internal/runtimeprofile"
)

func TestRunRejectsConflictingCustomArgs(t *testing.T) {
	svc := newTestServices(t)

	profileID := insertLegacyProfile(t, svc.db, "codex-conflict", "codex", `{"model":"gpt-5","custom_args":["--model","other","--strict"]}`)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profileID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail for conflicting custom args")
	}
	if !checkFailed(result, "custom_args") {
		t.Fatalf("expected custom_args check to fail, got %#v", result.Checks)
	}
	detail := checkDetail(result, "custom_args")
	if !strings.Contains(detail, "--model") {
		t.Fatalf("detail must name offending argument --model, got %q", detail)
	}
	if !strings.Contains(detail, "model") {
		t.Fatalf("detail must name structured field model, got %q", detail)
	}
}

func TestRunPassesNonConflictingCustomArgs(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.createProfile(t, "codex-ok", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model:      "gpt-5",
		CustomArgs: []string{"--strict", "--skip-git-repo-check"},
		APIKeys:    map[string]string{"OPENAI_API_KEY": "sk-test"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if !result.Pass {
		t.Fatalf("expected preflight to pass, got %#v", result.Checks)
	}
	if !checkPassed(result, "custom_args") {
		t.Fatalf("expected custom_args check to pass, got %#v", result.Checks)
	}
}

func TestSandboxPreflightProbesContainerEngineAndVPNTun(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.createProfile(t, "codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] != "info" {
			t.Fatalf("unexpected args %v", args)
		}
		return []byte("Server:\n Name: OrbStack\n Operating System: OrbStack\n"), nil
	}
	svc.preflight = svc.preflight.WithContainerRunner(run)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		Runner:           "sandbox",
		ContainerCLI:     "docker",
		SandboxVPNTun:    true,
	})
	if !result.Pass {
		t.Fatalf("expected pass, got %#v", result.Checks)
	}
	if !checkPassed(result, "container_engine") {
		t.Fatalf("container_engine check missing: %#v", result.Checks)
	}
	if !checkPassed(result, "container_engines") {
		t.Fatalf("container_engines check missing: %#v", result.Checks)
	}
	if !checkPassed(result, "sandbox_vpn_tun") {
		t.Fatalf("sandbox_vpn_tun check missing: %#v", result.Checks)
	}
	if !strings.Contains(checkDetail(result, "container_engine"), "OrbStack") {
		t.Fatalf("detail = %q", checkDetail(result, "container_engine"))
	}
	if !strings.Contains(checkDetail(result, "container_engines"), "docker:") {
		t.Fatalf("availability = %q", checkDetail(result, "container_engines"))
	}
}

func TestSandboxPreflightRejectsRootlessPodmanVPNTun(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.createProfile(t, "codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("host:\n  security:\n    rootless: true\npodman version\n"), nil
	}
	svc.preflight = svc.preflight.WithContainerRunner(run)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		Runner:           "sandbox",
		ContainerCLI:     "podman",
		SandboxVPNTun:    true,
	})
	if result.Pass {
		t.Fatalf("expected fail for rootless VPN TUN, got %#v", result.Checks)
	}
	if !checkFailed(result, "sandbox_vpn_tun") {
		t.Fatalf("expected sandbox_vpn_tun fail: %#v", result.Checks)
	}
}

func TestSandboxPreflightRuntimeRootMustBeWritable(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.createProfile(t, "codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	root := t.TempDir()
	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		Runner:           "sandbox",
		ContainerCLI:     "docker",
		RuntimeRoot:      root,
	})
	if !result.Pass {
		t.Fatalf("expected pass, got %#v", result.Checks)
	}
	if !checkPassed(result, "sandbox_runtime_root") {
		t.Fatalf("sandbox_runtime_root missing: %#v", result.Checks)
	}
	if !strings.Contains(checkDetail(result, "sandbox_runtime_root"), "writable") {
		t.Fatalf("detail = %q", checkDetail(result, "sandbox_runtime_root"))
	}

	// A file path cannot be a runtime root directory.
	fileRoot := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		Runner:           "sandbox",
		RuntimeRoot:      fileRoot,
	})
	if result.Pass {
		t.Fatal("expected fail when runtime root is a file")
	}
	if !checkFailed(result, "sandbox_runtime_root") {
		t.Fatalf("expected sandbox_runtime_root fail: %#v", result.Checks)
	}
}

func TestSandboxPreflightRejectsVPNTunWithHostProxyOnly(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.createProfile(t, "codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-5",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Server:\n Name: OrbStack\n"), nil
	}
	svc.preflight = svc.preflight.WithContainerRunner(run)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		Runner:           "sandbox",
		ContainerCLI:     "docker",
		SandboxVPNTun:    true,
		SandboxNetwork:   "host_proxy_only",
	})
	if result.Pass {
		t.Fatal("expected conflict fail")
	}
	if !checkFailed(result, "sandbox_vpn_tun") {
		t.Fatalf("expected sandbox_vpn_tun fail: %#v", result.Checks)
	}
}
