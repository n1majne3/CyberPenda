package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type cleanupFailureDocker struct {
	mu        sync.Mutex
	instances map[string]*productionFactoryDocker
	readers   sync.WaitGroup
	closeErr  error
}

func (d *cleanupFailureDocker) Create(ctx context.Context, args []string) (string, error) {
	instance := newProductionFactoryDocker()
	_, _ = instance.Create(ctx, args)
	d.mu.Lock()
	id := fmt.Sprintf("bridge-%d", len(d.instances)+1)
	d.instances[id] = instance
	d.mu.Unlock()
	d.readers.Add(1)
	go func() {
		defer d.readers.Done()
		scanner := bufio.NewScanner(instance.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				return
			}
			response := runtime.SandboxBridgeResponse{ID: request.ID, Result: json.RawMessage(`{"session_id":"pi-session","session_path":"/task/pi-session.jsonl"}`)}
			raw, _ := json.Marshal(response)
			if _, err := io.WriteString(instance.outputW, string(raw)+"\n"); err != nil {
				return
			}
		}
	}()
	return id, nil
}

func (d *cleanupFailureDocker) Start(ctx context.Context, id string) (runtime.SandboxBridgeIO, error) {
	d.mu.Lock()
	instance := d.instances[id]
	d.mu.Unlock()
	return instance.Start(ctx, id)
}

func (d *cleanupFailureDocker) Stop(ctx context.Context, id string) error {
	d.mu.Lock()
	instance := d.instances[id]
	d.mu.Unlock()
	_ = instance.Stop(ctx, id)
	if id == "bridge-1" {
		return d.closeErr
	}
	return nil
}

func (*cleanupFailureDocker) Remove(context.Context, string) error { return nil }

func TestClosedPiCleanupFailureDoesNotKeepOwnerBindings(t *testing.T) {
	cleanupErr := errors.New("container cleanup failed")
	for _, tc := range []struct {
		name     string
		closeErr error
		wantErr  error
	}{
		{name: "cleanup failure", closeErr: cleanupErr, wantErr: cleanupErr},
		{name: "wrapped already closed", closeErr: fmt.Errorf("transport: %w", runtime.ErrProviderSessionClosed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docker := &cleanupFailureDocker{instances: map[string]*productionFactoryDocker{}, closeErr: tc.closeErr}
			t.Cleanup(func() {
				for id := range docker.instances {
					_ = docker.Stop(context.Background(), id)
				}
				docker.readers.Wait()
			})
			factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
			legacy := testProviderLaunch{Name: "pi", Image: "sandbox:test", CreateArgs: []string{"create", "-i", "sandbox:test", "pi"}}
			request := ProviderSessionLaunchRequest{
				Owner: owner.NewSessionContract("session-close", ""), Continuation: owner.Continuation{ID: "continuation-1", OwnerID: "session-close"},
				Provider: runtimeprofile.ProviderPi, Runner: task.RunnerSandbox,
				Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
			}
			binding, err := factory.Open(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			registry := newProviderSessionRegistry()
			if err := registry.bind("session-close", binding.Session); err != nil {
				t.Fatal(err)
			}
			if err := registry.closeTask(context.Background(), "session-close"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("cleanup result = %v, want %v", err, tc.wantErr)
			}
			if !binding.Session.(*productionBoundSession).SessionClosed() {
				t.Fatal("precondition: Pi session did not close")
			}
			if _, found := registry.get("session-close"); found {
				t.Error("closed Pi session remained in the owner registry")
			}
			request.Continuation.ID = "continuation-2"
			request.Continuation.NativeSessionID = "pi-session"
			request.Continuation.NativeSessionPath = "/task/pi-session.jsonl"
			replacement, err := factory.Open(context.Background(), request)
			if err != nil {
				t.Fatalf("replacement after closed Pi cleanup failure: %v", err)
			}
			defer replacement.Session.Close(context.Background())
			if replacement.Session == binding.Session || replacement.Session.SessionID() != "pi-session" {
				t.Fatal("replacement must use a new binding and retain native identity")
			}
			docker.mu.Lock()
			creates := len(docker.instances)
			docker.mu.Unlock()
			if creates != 2 {
				t.Fatalf("provider process creations = %d, want 2", creates)
			}
		})
	}
}

type closeCallbackSession struct {
	runtime.ProviderSession
	close func() error
}

func (s *closeCallbackSession) Close(context.Context) error { return s.close() }

func TestLateProviderCloseDoesNotRemoveReplacementWithSameNativeID(t *testing.T) {
	registry := newProviderSessionRegistry()
	replacement := runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{SessionID: "same-native"})
	old := &closeCallbackSession{
		ProviderSession: runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{SessionID: "same-native"}),
		close:           func() error { return registry.bind("owner", replacement) },
	}
	if err := registry.bind("owner", old); err != nil {
		t.Fatal(err)
	}
	if err := registry.closeTask(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	if current, found := registry.get("owner"); !found || current != replacement {
		t.Fatal("late close removed the replacement binding that reused the native identity")
	}
}

func TestProviderCloseConflictKeepsLiveOwnerBinding(t *testing.T) {
	registry := newProviderSessionRegistry()
	live := &closeCallbackSession{
		ProviderSession: runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{SessionID: "native"}),
		close:           func() error { return runtime.ErrProviderSessionControlConflict },
	}
	if err := registry.bind("owner", live); err != nil {
		t.Fatal(err)
	}
	if err := registry.closeTask(context.Background(), "owner"); !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
		t.Fatalf("close result = %v", err)
	}
	if current, found := registry.get("owner"); !found || current != live {
		t.Fatal("a close conflict must retain the live binding")
	}
}

type valueProviderSession struct {
	runtime.ProviderSession
	data []string
}

func TestProviderCloseSupportsNonComparableSessionValues(t *testing.T) {
	registry := newProviderSessionRegistry()
	bound := valueProviderSession{
		ProviderSession: runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{SessionID: "native"}),
		data:            []string{"provider state"},
	}
	if err := registry.bind("owner", bound); err != nil {
		t.Fatal(err)
	}
	if err := registry.closeTask(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.get("owner"); found {
		t.Fatal("closed value session remained bound")
	}
}
