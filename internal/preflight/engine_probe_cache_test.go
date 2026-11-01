package preflight_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pentest/internal/preflight"
	"pentest/internal/runtimeprofile"
)

type engineProbeCounter struct {
	mu    sync.Mutex
	calls map[string]int
}

func (c *engineProbeCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *engineProbeCounter) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[name]++
}

func sandboxRequest(t *testing.T, svc services) preflight.Request {
	t.Helper()
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return preflight.Request{RuntimeProfileID: profile.ID, Runner: "sandbox", ContainerCLI: "docker"}
}

func TestEngineProbeReusesSuccessfulProbeWithinTTL(t *testing.T) {
	svc := newTestServices(t)
	request := sandboxRequest(t, svc)
	now := time.Unix(1_700_000_000, 0)
	var counter engineProbeCounter
	svc.preflight = svc.preflight.WithContainerRunner(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		counter.record(name)
		return []byte("Server:\n Name: Docker\n"), nil
	})
	preflight.SetEngineProbeClock(svc.preflight, func() time.Time { return now })

	for range 3 {
		if result := svc.preflight.Run(context.Background(), request); !result.Pass {
			t.Fatalf("expected pass, got %#v", result.Checks)
		}
	}
	if counter.count("docker") != 1 || counter.count("podman") != 1 {
		t.Fatalf("probes within TTL = docker:%d podman:%d, want 1 each", counter.count("docker"), counter.count("podman"))
	}

	now = now.Add(preflight.EngineProbeTTL)
	if result := svc.preflight.Run(context.Background(), request); !result.Pass {
		t.Fatalf("expected pass after expiry, got %#v", result.Checks)
	}
	if counter.count("docker") != 2 || counter.count("podman") != 2 {
		t.Fatalf("probes after TTL = docker:%d podman:%d, want 2 each", counter.count("docker"), counter.count("podman"))
	}
}

func TestEngineProbeNeverCachesFailures(t *testing.T) {
	svc := newTestServices(t)
	request := sandboxRequest(t, svc)
	var counter engineProbeCounter
	ready := false
	svc.preflight = svc.preflight.WithContainerRunner(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		counter.record(name)
		if name == "docker" && ready {
			return []byte("Server:\n Name: Docker\n"), nil
		}
		return nil, errors.New("engine down")
	})

	var details []string
	for range 2 {
		result := svc.preflight.Run(context.Background(), request)
		if result.Pass || checkPassed(result, "container_engine") {
			t.Fatalf("expected container_engine failure, got %#v", result.Checks)
		}
		details = append(details, checkDetail(result, "container_engine"))
	}
	if details[0] != details[1] || details[0] != `selected docker is not ready: container CLI "docker" is not ready: engine down: ` {
		t.Fatalf("failure details = %q", details)
	}
	if counter.count("docker") != 2 || counter.count("podman") != 2 {
		t.Fatalf("failed probes = docker:%d podman:%d, want every launch to re-probe", counter.count("docker"), counter.count("podman"))
	}

	// A newly started engine is seen on the very next launch.
	ready = true
	if result := svc.preflight.Run(context.Background(), request); !result.Pass {
		t.Fatalf("expected pass once docker is ready, got %#v", result.Checks)
	}
}

func TestEngineProbeCacheResetsWhenRunnerChanges(t *testing.T) {
	svc := newTestServices(t)
	request := sandboxRequest(t, svc)
	svc.preflight = svc.preflight.WithContainerRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Server:\n Name: Docker\n"), nil
	})
	if result := svc.preflight.Run(context.Background(), request); !result.Pass {
		t.Fatalf("expected pass, got %#v", result.Checks)
	}
	svc.preflight = svc.preflight.WithContainerRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("engine down")
	})
	if result := svc.preflight.Run(context.Background(), request); result.Pass {
		t.Fatalf("stale cached probe survived a runner change: %#v", result.Checks)
	}
}

func TestEngineProbesRunConcurrently(t *testing.T) {
	svc := newTestServices(t)
	request := sandboxRequest(t, svc)
	var started sync.WaitGroup
	started.Add(2)
	svc.preflight = svc.preflight.WithContainerRunner(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		started.Done()
		done := make(chan struct{})
		go func() { started.Wait(); close(done) }()
		select {
		case <-done:
			return []byte("Server:\n Name: Docker\n"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("docker and podman probes ran sequentially")
		}
	})
	if result := svc.preflight.Run(context.Background(), request); !result.Pass {
		t.Fatalf("expected concurrent probes to pass, got %#v", result.Checks)
	}
}
