package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// #203 / ADR 0021: a late result that resolves to a superseded Conclusion
// Dispatch is durably recorded as a late terminal delivery outcome and can
// never settle or corrupt the current obligation. Only the ACTIVE dispatch may
// validate or apply a result.
func TestAssistedConclusionLateResultFromSupersededDispatchCannotSettle(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	projectRecord, err := server.projects.Create("Late", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Late Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "late result", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: projectRecord.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.UpdateContinuationRuntimeMetadata(launch.Continuation.ID, "", "assisted-session", "/sessions/assisted-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	obligation, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, continuation.ID, "work-request-late", "assisted-session", "work-turn-late",
		task.TurnSelection{ModelProviderID: "provider-late", Model: "model-late"},
		task.SemanticDebtWatermarks{SourceWork: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, won, err := server.tasks.ClaimBlackboardConclusionDispatch(obligation.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", initial, won, err)
	}
	if _, _, err := server.tasks.MarkBlackboardConclusionAwaiting(initial.DispatchRequestID, "control-late"); err != nil {
		t.Fatal(err)
	}
	// The initial result is invalid: the failed dispatch is superseded and a
	// NEW repair dispatch becomes the active delivery.
	repair, won, err := server.tasks.HandleBlackboardConclusionFailure(
		initial.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, time.Now().UTC(), 0,
	)
	if err != nil || !won || repair.DispatchRequestID == initial.DispatchRequestID {
		t.Fatalf("claim repair dispatch: %#v won=%v err=%v", repair, won, err)
	}

	// A late result for the OLD dispatch arrives. The lookup must record the
	// late terminal outcome on the superseded dispatch and reject the callback;
	// the obligation stays repair_dispatch_requested and nothing is applied.
	view, lookupErr := server.tasks.BlackboardConclusionByDispatchRequestID(initial.DispatchRequestID)
	if !isDispatchInactiveErr(lookupErr) {
		t.Fatalf("late result resolved active dispatch: %#v err=%v", view, lookupErr)
	}
	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation: %#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptRepairDispatchRequested ||
		latest.DispatchRequestID != repair.DispatchRequestID {
		t.Fatalf("late result corrupted the obligation: %#v", latest)
	}
	dispatches, err := server.tasks.ConclusionDispatches(obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range dispatches {
		if dispatches[i].DispatchRequestID == initial.DispatchRequestID &&
			dispatches[i].DeliveryState != task.ConclusionDispatchLateTerminal {
			t.Fatalf("late result not recorded on superseded dispatch: %#v", dispatches[i])
		}
	}
}

// #203 / ADR 0021: restart recovery creates a NEW Conclusion Dispatch bound to
// the current active continuation + session when the obligation's active
// dispatch is bound to a dead continuation but live ownership of the current
// Task-scoped Runtime is proven. The old dispatch is superseded, never
// rewritten, and its request id fails closed.
func TestAssistedConclusionRestartRecoveryCreatesNewDispatchWithProvenOwnership(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	dispatched, won, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", dispatched, won, err)
	}
	// Simulate a steer-like stale binding: repoint the obligation's active
	// dispatch to a dead continuation so restart recovery must create a new
	// dispatch instead of resuming it.
	if _, err := seed.server.db.Exec(`UPDATE conclusion_dispatches
		SET continuation_id='dead-continuation', source_session_id='dead-session' WHERE id=?`,
		dispatched.ActiveDispatchID); err != nil {
		t.Fatal(err)
	}
	closeSeedServer(t, seed.server)

	session := newConclusionRecoverySession()
	factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
	restarted := openConclusionRecoveryServer(t, root, factory)
	restarted.providerControlWG.Wait()

	latest, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation after restart: %#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptAwaitingResult {
		t.Fatalf("recovered obligation state = %s, want awaiting_result", latest.InternalState)
	}
	if latest.DispatchRequestID == dispatched.DispatchRequestID {
		t.Fatalf("recovery reused the stale dispatch request id %s", latest.DispatchRequestID)
	}
	requests := session.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != latest.DispatchRequestID {
		t.Fatalf("recovered provider requests = %#v, want one request for %q", requests, latest.DispatchRequestID)
	}
	history, err := restarted.tasks.ConclusionDispatches(seed.receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("dispatch history = %d rows, want 2 (stale + recovery)", len(history))
	}
	for i := range history {
		if history[i].DispatchRequestID == dispatched.DispatchRequestID &&
			history[i].DeliveryState != task.ConclusionDispatchSuperseded {
			t.Fatalf("stale dispatch not superseded: %#v", history[i])
		}
	}
	assertConclusionRecoveryDidNotLaunch(t, restarted, factory, seed.task.ID)
}

// #203 / ADR 0021: when live Runtime ownership cannot be proven, restart
// recovery fails closed with an operator-visible recovery reason on the
// obligation; it never invents a replacement dispatch.
func TestAssistedConclusionRestartWithoutOwnershipFailsClosedWithReason(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	closeSeedServer(t, seed.server)

	factory := &conclusionRecoveryFactory{liveness: ProviderSessionRecoveryOrphaned}
	restarted := openConclusionRecoveryServer(t, root, factory)
	latest, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation: %#v err=%v", latest, err)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		latest.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		latest.RecoveryReason != string(task.ConclusionRecoveryRuntimeOwnershipNotProven) {
		t.Fatalf("fail-closed recovery = %#v", latest)
	}
	// The ownership probe runs (Recover is how liveness is learned), but the
	// daemon must never launch a replacement Runtime when ownership cannot be
	// proven.
	if opens, recovers := factory.counts(); opens != 0 || recovers != 1 {
		t.Fatalf("fail-closed recovery launched/probed wrongly: opens=%d recovers=%d", opens, recovers)
	}
	assertConclusionAPIState(t, restarted, seed.projectID, seed.task.ID, task.BlackboardConclusionStateActionRequired)
}

// #203 / ADR 0021: recovery dispatches are delivered through the daemon's
// provider control queue and settle the obligation (dropped response window is
// closed by the send-start fence; a pre-send dispatch is replayed with the SAME
// deterministic request id, a post-fence dispatch is never replayed).
func TestAssistedConclusionRestartResumesPreSendDispatchWithSameRequestID(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	dispatched, won, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", dispatched, won, err)
	}
	closeSeedServer(t, seed.server)

	session := newConclusionRecoverySession()
	factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
	restarted := openConclusionRecoveryServer(t, root, factory)
	restarted.providerControlWG.Wait()

	latest, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation: %#v err=%v", latest, err)
	}
	if latest.DispatchRequestID != dispatched.DispatchRequestID {
		t.Fatalf("pre-send resume changed the request id: %s != %s", latest.DispatchRequestID, dispatched.DispatchRequestID)
	}
	requests := session.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != dispatched.DispatchRequestID {
		t.Fatalf("pre-send resume requests = %#v", requests)
	}
}

func isDispatchInactiveErr(err error) bool {
	return err == task.ErrBlackboardConclusionDispatchInactive
}

var _ = runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true}
var _ = runtime.FakeProviderSessionConfig{}
var _ = httptest.NewRecorder
var _ = http.MethodGet
var _ sync.Mutex
