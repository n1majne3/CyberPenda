package runner_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"pentest/internal/runner"
)

func TestDetectEngineClassifiesOrbStackDockerInfo(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" || len(args) != 1 || args[0] != "info" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("Client:\n Context: default\nServer:\n Name: OrbStack\n Operating System: OrbStack\n OSType: linux\n"), nil
	}
	info, err := runner.DetectEngine(context.Background(), "docker", run)
	if err != nil {
		t.Fatalf("DetectEngine: %v", err)
	}
	if info.Kind != runner.EngineDocker || info.Name != "OrbStack" {
		t.Fatalf("info = %#v, want docker/OrbStack", info)
	}
	if info.Rootless {
		t.Fatal("OrbStack must not be treated as rootless")
	}
}

func TestDetectEngineClassifiesPodmanRootless(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "podman" {
			t.Fatalf("cli = %q", name)
		}
		return []byte("host:\n  security:\n    rootless: true\n  os: linux\nregistries:\n"), nil
	}
	info, err := runner.DetectEngine(context.Background(), "podman", run)
	if err != nil {
		t.Fatalf("DetectEngine: %v", err)
	}
	if info.Kind != runner.EnginePodman {
		t.Fatalf("kind = %q, want podman", info.Kind)
	}
	if !info.Rootless {
		t.Fatal("expected rootless podman")
	}
	ok, detail := info.SupportsVPNTun()
	if ok {
		t.Fatalf("rootless podman must fail VPN TUN support, detail=%q", detail)
	}
}

func TestDetectEngineFailsWhenCLIOffline(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
	}
	_, err := runner.DetectEngine(context.Background(), "docker", run)
	if err == nil {
		t.Fatal("expected offline CLI to fail")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("error = %v, want not ready", err)
	}
}

func TestDetectEngineAllowsAbsoluteTestCLIWithoutInfo(t *testing.T) {
	dir := t.TempDir()
	cli := dir + "/fake-docker"
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("unsupported"), errors.New("exit status 1")
	}
	info, err := runner.DetectEngine(context.Background(), cli, run)
	if err != nil {
		t.Fatalf("DetectEngine: %v", err)
	}
	if info.Name != "test-container-cli" {
		t.Fatalf("info = %#v", info)
	}
}

func TestHostGatewayAddHostsAlwaysIncludesDockerInternal(t *testing.T) {
	for _, kind := range []runner.EngineKind{runner.EngineDocker, runner.EnginePodman, runner.EngineUnknown} {
		hosts := runner.HostGatewayAddHosts(kind)
		joined := strings.Join(hosts, " ")
		if !strings.Contains(joined, "host.docker.internal:host-gateway") {
			t.Fatalf("kind %s missing host.docker.internal: %v", kind, hosts)
		}
		if kind == runner.EnginePodman || kind == runner.EngineUnknown {
			if !strings.Contains(joined, "host.containers.internal:host-gateway") {
				t.Fatalf("kind %s missing host.containers.internal: %v", kind, hosts)
			}
		}
	}
}

func TestSupportsVPNTunRootfulDocker(t *testing.T) {
	info := runner.EngineInfo{Kind: runner.EngineDocker, Name: "OrbStack", Rootless: false}
	ok, detail := info.SupportsVPNTun()
	if !ok {
		t.Fatalf("expected VPN TUN support, detail=%q", detail)
	}
}
