package daemon

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"pentest/internal/runtime"
)

func TestResumeWaitsForTerminalProviderRelease(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair("resume-provider-release-" + strconv.Itoa(open))
	}
	seed, adapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: adapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)
	server.runtimeStopTimeout = time.Second
	release := make(chan struct{})
	_, launchDone := forceTerminalWithActiveHarness(t, server, created, release)
	close(release)
	if err := <-launchDone; err != nil {
		t.Fatal(err)
	}
	if err := server.providerSessions.bind(created.ID, seed); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- postTaskResume(server, created.ProjectID, created.ID).Code }()
	select {
	case status := <-done:
		t.Fatalf("Resume returned %d before the terminal Provider session was released", status)
	case <-time.After(30 * time.Millisecond):
	}
	server.providerSessions.remove(created.ID)
	_ = seed.Close(context.Background())
	select {
	case status := <-done:
		if status != http.StatusAccepted {
			t.Fatalf("Resume returned %d after Provider release, want 202", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resume did not settle after Provider release")
	}
	if factory.openCount() != 1 {
		t.Fatalf("replacement Runtime opens = %d, want 1", factory.openCount())
	}
	waitForTaskRunning(t, server, created.ID)
}
