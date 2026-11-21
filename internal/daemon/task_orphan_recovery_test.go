package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/task"
)

func TestStopSettlesRunningTaskWhenHarnessAndSessionAlreadyGone(t *testing.T) {
	server, created, _ := newFinishTaskFixture(t, nil)
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	// No harness activity, no bound session — Stop must still write stopped.
	resp := postStop(server, created.ProjectID, created.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("stop status = %d body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "stopped" {
		t.Fatalf("stop body status = %q, want stopped", body.Status)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusStopped {
		t.Fatalf("durable status = %q, want stopped", found.Status)
	}
}

func TestOrphanOwnershipResolvedBeforeReplacementLaunch(t *testing.T) {
	session, adapter := newFinishSessionPair("orphan-cleanup")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)
	if !server.harness.IsActive(created.ID) {
		t.Fatal("setup: harness should be active")
	}
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("setup: session should be bound")
	}

	// ensureRuntimeAbsentBeforeLaunch must stop harness and unbind session.
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		t.Fatalf("orphan resolve: %v", err)
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("harness still active after ensureRuntimeAbsentBeforeLaunch")
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("provider session still bound after ensureRuntimeAbsentBeforeLaunch")
	}
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		t.Fatalf("second orphan resolve: %v", err)
	}

	replacement, repAdapter := newFinishSessionPair("orphan-replacement")
	factory.session, factory.adapter = replacement, repAdapter
	// Mark prior cont terminal so CreateContinuation can proceed.
	if cont, err := server.tasks.LatestContinuation(created.ID); err == nil && cont != nil {
		if cont.Status == task.StatusRunning || cont.Status == task.StatusPending {
			_, _ = server.tasks.UpdateContinuationStatus(cont.ID, task.StatusStopped)
		}
		if cont.BlackboardReconciliationStatus != task.ReconciliationCompleted {
			_, _ = server.tasks.MarkContinuationReconciliation(context.Background(), cont.ID, task.ReconciliationCompleted, "orphan", time.Now().UTC())
		}
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, goal, plan, err := server.prepareResumeContinuation(found, "")
	if err != nil {
		t.Fatal(err)
	}
	opensBefore := factory.openCount()
	if err := server.launchTaskInBackground(found, plan, goal); err != nil {
		t.Fatal(err)
	}
	waitForTaskRunning(t, server, created.ID)
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("replacement opens before=%d after=%d", opensBefore, factory.openCount())
	}
}

func TestBlackboardFinishDoesNotCompleteTask(t *testing.T) {
	session, adapter := newFinishSessionPair("bb-finish-guard")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	if server.blackboardV2 != nil {
		// Blackboard Finish may succeed or fail on open work; either way Task must stay active.
		_, _ = server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
			IdempotencyKey: "bb-only-finish",
		})
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Blackboard Finish must not complete the Task")
	}
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("Blackboard Finish must not close Runtime ownership by itself")
	}
}
