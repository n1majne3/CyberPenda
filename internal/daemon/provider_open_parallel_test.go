package daemon

import (
	"context"
	"fmt"
	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"sync"
	"testing"
	"time"
)

type parallelFactoryStarter struct {
	mu       sync.Mutex
	starters []*productionFactoryHostStarter
}

func (s *parallelFactoryStarter) Start(ctx context.Context, spec runtime.HostProcessSpec) (runtime.HostProcessHandle, error) {
	starter := newProductionFactoryHostStarter()
	s.mu.Lock()
	s.starters = append(s.starters, starter)
	s.mu.Unlock()
	return starter.Start(ctx, spec)
}

type heldSetupAssembler struct {
	fakeAssembler
	entered, release chan struct{}
}

func (a *heldSetupAssembler) Setup(ctx context.Context, b productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error) {
	if providerSessionOwnerID(assembly.request) == "held" {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return providerSessionSetup{}, ctx.Err()
		}
	}
	return a.fakeAssembler.Setup(ctx, b, assembly)
}

func TestProviderFactoryOpensUnrelatedOwnersDuringHeldSetup(t *testing.T) {
	starter := &parallelFactoryStarter{}
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	asm := &heldSetupAssembler{fakeAssembler: fakeAssembler{program: "fake"}, entered: make(chan struct{}), release: make(chan struct{})}
	factory.assemblers[runtimeprofile.Provider("fake")] = asm
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	makeRequest := func(id string) ProviderSessionLaunchRequest {
		return ProviderSessionLaunchRequest{Owner: owner.NewTaskContract(id, "project", ""), Continuation: owner.Continuation{ID: id + "-c1", OwnerID: id}, Provider: runtimeprofile.Provider("fake"), Runner: task.RunnerHost, Facts: ProviderSessionLaunchFacts{Workdir: "/work"}}
	}
	held := make(chan error, 1)
	go func() {
		b, e := factory.Open(ctx, makeRequest("held"))
		if e == nil {
			defer b.Session.Close(context.Background())
		}
		held <- e
	}()
	select {
	case <-asm.entered:
	case <-ctx.Done():
		t.Fatal("held owner did not enter Setup")
	}
	done := make(chan error, 1)
	go func() {
		b, e := factory.Open(ctx, makeRequest("other"))
		if e == nil {
			defer b.Session.Close(context.Background())
		}
		done <- e
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("unrelated owner blocked behind held Setup")
	}
	close(asm.release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
}

func TestProviderFactoryRetriesOwnerAfterSetupFailure(t *testing.T) {
	starter := &parallelFactoryStarter{}
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	asm := &fakeAssembler{program: "fake", setupErr: fmt.Errorf("setup failed")}
	factory.assemblers[runtimeprofile.Provider("fake")] = asm
	request := ProviderSessionLaunchRequest{Owner: owner.NewTaskContract("retry", "project", ""), Continuation: owner.Continuation{ID: "retry-c1", OwnerID: "retry"}, Provider: runtimeprofile.Provider("fake"), Runner: task.RunnerHost, Facts: ProviderSessionLaunchFacts{Workdir: "/work"}}
	if _, err := factory.Open(context.Background(), request); err == nil {
		t.Fatal("setup failure was ignored")
	}
	if _, ok := factory.hostBridges.Get("retry"); ok {
		t.Fatal("failed setup retained bridge")
	}
	asm.setupErr = nil
	binding, err := factory.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	rebound, err := factory.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Session != binding.Session {
		t.Fatal("same owner created another session")
	}
	starter.mu.Lock()
	count := len(starter.starters)
	starter.mu.Unlock()
	if count != 2 {
		t.Fatalf("starts=%d, want failed start plus one successful start", count)
	}
}

func TestProviderFactoryConcurrentOwnerStartsOnce(t *testing.T) {
	starter := &parallelFactoryStarter{}
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	asm := &heldSetupAssembler{fakeAssembler: fakeAssembler{program: "fake"}, entered: make(chan struct{}), release: make(chan struct{})}
	factory.assemblers[runtimeprofile.Provider("fake")] = asm
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := ProviderSessionLaunchRequest{Owner: owner.NewTaskContract("held", "project", ""), Continuation: owner.Continuation{ID: "held-c1", OwnerID: "held"}, Provider: runtimeprofile.Provider("fake"), Runner: task.RunnerHost, Facts: ProviderSessionLaunchFacts{Workdir: "/work"}}
	type result struct {
		binding ProviderSessionBinding
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { b, e := factory.Open(ctx, request); first <- result{b, e} }()
	select {
	case <-asm.entered:
	case <-ctx.Done():
		t.Fatal("first owner did not enter Setup")
	}
	go func() { b, e := factory.Open(ctx, request); second <- result{b, e} }()
	close(asm.release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("opens failed: %v %v", a.err, b.err)
	}
	defer a.binding.Session.Close(context.Background())
	if a.binding.Session != b.binding.Session {
		t.Fatal("concurrent owner created two sessions")
	}
	starter.mu.Lock()
	count := len(starter.starters)
	starter.mu.Unlock()
	if count != 1 {
		t.Fatalf("starts=%d, want 1", count)
	}
}
