package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// A native steer delivery is a Harness Control Turn. The executor assigns the
// kind from request lineage for both owners so a steer can never be
// lineage-classified as operator work (ADR 0018).
func TestNativeSteerSendsHarnessControlTurnKind(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-kind","message":"focus on admin"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d, body=%s", response.Code, response.Body.String())
	}

	waitForProviderRequests(t, session, 1)
	for _, sent := range session.LastRequests() {
		if sent.TurnKind != runtime.RuntimeTurnKindControl {
			t.Fatalf("steer request %q TurnKind = %q, want %q", sent.RequestID, sent.TurnKind, runtime.RuntimeTurnKindControl)
		}
	}
}
