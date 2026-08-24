package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type conclusionRecoveryFactory struct {
	mu       sync.Mutex
	session  *runtime.FakeProviderSession
	liveness ProviderSessionRecoveryLiveness
	opens    int
	recovers int
}

func (f *conclusionRecoveryFactory) Open(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	return ProviderSessionBinding{}, errString("restart recovery must not open a Runtime")
}

func (f *conclusionRecoveryFactory) Recover(context.Context, ProviderSessionRecoveryRequest) (ProviderSessionRecoveryResult, error) {
	f.mu.Lock()
	f.recovers++
	f.mu.Unlock()
	if f.liveness != ProviderSessionRecoveryLive {
		return ProviderSessionRecoveryResult{Liveness: f.liveness}, nil
	}
	return ProviderSessionRecoveryResult{
		Liveness: ProviderSessionRecoveryLive,
		Binding: ProviderSessionBinding{
			Session: f.session,
			Adapter: runtime.NewProviderSessionRunAdapter(f.session, make(chan struct{})),
		},
	}, nil
}

func (f *conclusionRecoveryFactory) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.recovers
}

func TestAssistedConclusionRestartSendsOnlyDurablePreSendIntentWithProvenOwner(t *testing.T) {
	for _, state := range []string{"pending", "dispatch_requested"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			seed := seedConclusionRecoveryReceipt(t, root)
			wantRequestID := ""
			if state == "dispatch_requested" {
				dispatched, won, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
				if err != nil || !won {
					t.Fatalf("claim pre-send dispatch: %#v won=%v err=%v", dispatched, won, err)
				}
				wantRequestID = dispatched.DispatchRequestID
			}
			closeSeedServer(t, seed.server)

			session := newConclusionRecoverySession()
			factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
			restarted := openConclusionRecoveryServer(t, root, factory)
			restarted.providerControlWG.Wait()

			receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
			if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
				receipt.SendAttemptCount != 1 || receipt.SendStartedAt == nil {
				t.Fatalf("recovered pre-send receipt = %#v, err=%v", receipt, err)
			}
			requests := session.LastRequests()
			if len(requests) != 1 || requests[0].RequestID != receipt.DispatchRequestID ||
				(wantRequestID != "" && requests[0].RequestID != wantRequestID) {
				t.Fatalf("recovered provider requests = %#v, original=%q", requests, wantRequestID)
			}
			assertConclusionRecoveryDidNotLaunch(t, restarted, factory, seed.task.ID)
			assertConclusionAPIState(t, restarted, seed.projectID, seed.task.ID, task.BlackboardConclusionStateConcluding)
		})
	}
}

func TestAssistedConclusionRestartRejectsLegacySourceCorrelationBeforeOwnershipProbe(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	if _, err := seed.server.db.Exec(`UPDATE pending_blackboard_conclusions
		SET source_request_id=?,source_request_correlation_exact=0 WHERE id=?`,
		"legacy:"+seed.receipt.SourceSessionID+":"+seed.receipt.SourceTurnID, seed.receipt.ID); err != nil {
		t.Fatal(err)
	}
	closeSeedServer(t, seed.server)

	session := newConclusionRecoverySession()
	factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
	restarted := openConclusionRecoveryServer(t, root, factory)
	receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		receipt.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("legacy recovery receipt = %#v, err=%v", receipt, err)
	}
	if opens, recovers := factory.counts(); opens != 0 || recovers != 0 {
		t.Fatalf("legacy correlation reached provider factory: opens=%d recovers=%d", opens, recovers)
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("legacy correlation sent %d provider Turn(s)", len(requests))
	}
}

func TestAssistedConclusionRestartFailsClosedAfterSendStartedOrAcceptance(t *testing.T) {
	for _, state := range []string{"send_started", "awaiting_result"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			seed := seedConclusionRecoveryReceipt(t, root)
			dispatched, _, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			sending, _, err := seed.server.tasks.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if state == "awaiting_result" {
				if _, _, err := seed.server.tasks.MarkBlackboardConclusionAwaiting(sending.DispatchRequestID, "control-accepted"); err != nil {
					t.Fatal(err)
				}
			}
			closeSeedServer(t, seed.server)

			session := newConclusionRecoverySession()
			factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
			restarted := openConclusionRecoveryServer(t, root, factory)
			receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
			if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptActionRequired ||
				receipt.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired || receipt.NextEligibleAt == nil {
				t.Fatalf("ambiguous restart receipt = %#v, err=%v", receipt, err)
			}
			if len(session.LastRequests()) != 0 {
				t.Fatalf("ambiguous restart resent %d provider Turn(s)", len(session.LastRequests()))
			}
			assertConclusionRecoveryDidNotLaunch(t, restarted, factory, seed.task.ID)
			assertConclusionAPIState(t, restarted, seed.projectID, seed.task.ID, task.BlackboardConclusionStateActionRequired)

			cooldown := *receipt.NextEligibleAt
			updatedAt := receipt.UpdatedAt
			if err := restarted.Close(); err != nil {
				t.Fatal(err)
			}
			againFactory := &conclusionRecoveryFactory{liveness: ProviderSessionRecoveryUnknown}
			again := openConclusionRecoveryServer(t, root, againFactory)
			replayed, err := again.tasks.LatestBlackboardConclusion(seed.task.ID)
			if err != nil || replayed == nil || replayed.NextEligibleAt == nil || !replayed.NextEligibleAt.Equal(cooldown) ||
				!replayed.UpdatedAt.Equal(updatedAt) || replayed.SendAttemptCount != 1 || replayed.AutomaticTurnCount != 1 {
				t.Fatalf("repeated restart changed cooldown or counters: %#v, err=%v", replayed, err)
			}
			if opens, recovers := againFactory.counts(); opens != 0 || recovers != 0 {
				t.Fatalf("completed recovery probed/launched again: opens=%d recovers=%d", opens, recovers)
			}
		})
	}
}

func TestAssistedConclusionRestartAppliesPersistedCanonicalResultWithoutProviderTurn(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	dispatched, _, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := seed.server.tasks.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := seed.server.tasks.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-validated"); err != nil {
		t.Fatal(err)
	}
	decoded, err := blackboardconclusion.Decode([]byte(assistedAttemptResultJSON(0, "attempt:validated-restart", "objective:validated-restart")))
	if err != nil {
		t.Fatal(err)
	}
	validated, won, err := seed.server.tasks.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, decoded.CanonicalJSON)
	if err != nil || !won {
		t.Fatalf("persist canonical result: %#v won=%v err=%v", validated, won, err)
	}
	closeSeedServer(t, seed.server)

	factory := &conclusionRecoveryFactory{liveness: ProviderSessionRecoveryUnknown}
	restarted := openConclusionRecoveryServer(t, root, factory)
	receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptApplied ||
		receipt.AppliedRevision == nil || *receipt.AppliedRevision <= 0 || receipt.CanonicalResultSHA256 != validated.CanonicalResultSHA256 {
		t.Fatalf("validated restart receipt = %#v, err=%v", receipt, err)
	}
	snapshot, err := restarted.blackboardV2.RuntimeSnapshot(context.Background(), seed.projectID)
	if err != nil || snapshot.Revision < *receipt.AppliedRevision {
		t.Fatalf("validated restart snapshot revision = %d, applied=%d, err=%v", snapshot.Revision, *receipt.AppliedRevision, err)
	}
	if opens, recovers := factory.counts(); opens != 0 || recovers != 0 {
		t.Fatalf("validated replay touched provider: opens=%d recovers=%d", opens, recovers)
	}
	assertConclusionAPIState(t, restarted, seed.projectID, seed.task.ID, task.BlackboardConclusionStateClean)
	appliedRevision := *receipt.AppliedRevision
	resultHash := receipt.CanonicalResultSHA256
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	againFactory := &conclusionRecoveryFactory{liveness: ProviderSessionRecoveryUnknown}
	again := openConclusionRecoveryServer(t, root, againFactory)
	replayed, err := again.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || replayed == nil || replayed.InternalState != task.BlackboardConclusionReceiptApplied ||
		replayed.AppliedRevision == nil || *replayed.AppliedRevision != appliedRevision || replayed.CanonicalResultSHA256 != resultHash {
		t.Fatalf("repeated applied restart = %#v, err=%v", replayed, err)
	}
	if opens, recovers := againFactory.counts(); opens != 0 || recovers != 0 {
		t.Fatalf("repeated applied restart touched provider: opens=%d recovers=%d", opens, recovers)
	}
	assertConclusionAPIState(t, again, seed.projectID, seed.task.ID, task.BlackboardConclusionStateClean)
}

func TestAssistedConclusionPendingRecoveryRetryDispatchesInitialTurnIdempotently(t *testing.T) {
	root := t.TempDir()
	seed := seedConclusionRecoveryReceipt(t, root)
	closeSeedServer(t, seed.server)

	factory := &conclusionRecoveryFactory{liveness: ProviderSessionRecoveryOrphaned}
	restarted := openConclusionRecoveryServer(t, root, factory)
	action, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || action == nil || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.DispatchRequestID != "" || action.ApplyIdempotencyKey != "" {
		t.Fatalf("pending recovery action = %#v, err=%v", action, err)
	}
	original, err := restarted.tasks.LatestContinuation(seed.task.ID)
	if err != nil || original == nil {
		t.Fatalf("load interrupted Continuation: %#v err=%v", original, err)
	}
	replacement, err := restarted.tasks.CreateReplacementContinuation(*original)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.blackboardV2Continuity.RebindContinuationForNativeSteer(
		context.Background(), original.ID, replacement.ID,
	); err != nil {
		t.Fatal(err)
	}
	replacement, err = restarted.tasks.UpdateContinuationRuntimeMetadata(
		replacement.ID, "", "assisted-session", "/sessions/assisted-session.jsonl",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	session := newConclusionRecoverySession()
	if err := restarted.BindProviderSession(seed.task.ID, session); err != nil {
		t.Fatal(err)
	}
	retryURL := "/api/projects/" + seed.projectID + "/tasks/" + seed.task.ID + "/blackboard-conclusion/retry"
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, retryURL, bytes.NewBufferString(`{}`))
		request.Header.Set("Idempotency-Key", "pending-recovery-retry")
		response := httptest.NewRecorder()
		restarted.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("retry %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	restarted.providerControlWG.Wait()
	receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
	if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
		receipt.BaseRevision == nil || receipt.ApplyIdempotencyKey == "" || receipt.ExplicitRetryCount != 1 {
		t.Fatalf("pending retry receipt = %#v, err=%v", receipt, err)
	}
	requests := session.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != receipt.DispatchRequestID ||
		requests[0].TurnKind != runtime.RuntimeTurnKindControl {
		t.Fatalf("pending retry provider requests = %#v", requests)
	}
	opens, recovers := factory.counts()
	if opens != 0 || recovers != 1 || restarted.harness.IsActive(seed.task.ID) {
		t.Fatalf("restart launched Runtime: opens=%d recovers=%d harness_active=%v", opens, recovers, restarted.harness.IsActive(seed.task.ID))
	}
	var continuations int
	if err := restarted.db.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, seed.task.ID).Scan(&continuations); err != nil || continuations != 2 {
		t.Fatalf("replacement continuation count=%d err=%v", continuations, err)
	}
}

func TestAssistedConclusionRestartRecoversUnsentFollowupGenerationOnce(t *testing.T) {
	for _, generation := range []string{"repair", "retry", "version_regeneration"} {
		t.Run(generation, func(t *testing.T) {
			root := t.TempDir()
			seed := seedConclusionRecoveryReceipt(t, root)
			dispatched, _, err := seed.server.tasks.ClaimBlackboardConclusionDispatch(seed.receipt.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := seed.server.tasks.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := seed.server.tasks.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-source"); err != nil {
				t.Fatal(err)
			}
			switch generation {
			case "repair":
				followup, won, err := seed.server.tasks.HandleBlackboardConclusionFailure(
					dispatched.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, time.Now().UTC(), 0,
				)
				if err != nil || !won {
					t.Fatalf("claim repair: %#v won=%v err=%v", followup, won, err)
				}
			case "retry":
				if _, _, err := seed.server.tasks.HandleBlackboardConclusionFailure(
					dispatched.DispatchRequestID, task.BlackboardConclusionErrorToolUseForbidden, task.ConclusionValidationDetail{}, time.Now().UTC(), 0,
				); err != nil {
					t.Fatal(err)
				}
				followup, won, err := seed.server.tasks.RetryBlackboardConclusion(seed.receipt.ID, "restart-retry", time.Now().UTC())
				if err != nil || !won {
					t.Fatalf("claim retry: %#v won=%v err=%v", followup, won, err)
				}
			case "version_regeneration":
				decoded, err := blackboardconclusion.Decode([]byte(assistedAttemptResultJSON(0, "attempt:version-restart", "objective:version-restart")))
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := seed.server.tasks.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, decoded.CanonicalJSON); err != nil {
					t.Fatal(err)
				}
				if revision := advanceProjectFromPeerContinuation(t, seed.server, seed.projectID, seed.profileID); revision != 1 {
					t.Fatalf("peer revision=%d, want 1", revision)
				}
				if _, won, err := seed.server.tasks.ClaimBlackboardConclusionVersionSync(dispatched.DispatchRequestID); err != nil || !won {
					t.Fatalf("claim version sync: won=%v err=%v", won, err)
				}
			}
			closeSeedServer(t, seed.server)

			session := newConclusionRecoverySession()
			factory := &conclusionRecoveryFactory{session: session, liveness: ProviderSessionRecoveryLive}
			restarted := openConclusionRecoveryServer(t, root, factory)
			restarted.providerControlWG.Wait()
			receipt, err := restarted.tasks.LatestBlackboardConclusion(seed.task.ID)
			if err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptAwaitingResult ||
				receipt.SendAttemptCount != 1 || len(session.LastRequests()) != 1 {
				t.Fatalf("recovered %s receipt=%#v requests=%#v err=%v", generation, receipt, session.LastRequests(), err)
			}
			if generation == "repair" && (receipt.RepairCount != 1 || receipt.AutomaticTurnCount != 2) {
				t.Fatalf("repair counters changed: %#v", receipt)
			}
			if generation == "retry" && receipt.ExplicitRetryCount != 1 {
				t.Fatalf("retry counters changed: %#v", receipt)
			}
			if generation == "version_regeneration" && (receipt.VersionRegenerationCount != 1 || receipt.SynchronizedRevision == nil || *receipt.SynchronizedRevision != 1) {
				t.Fatalf("version counters changed: %#v", receipt)
			}
			assertConclusionRecoveryDidNotLaunch(t, restarted, factory, seed.task.ID)
		})
	}
}

type conclusionRecoverySeed struct {
	server    *Server
	projectID string
	profileID string
	task      task.Task
	receipt   task.BlackboardConclusionReceipt
}

func seedConclusionRecoveryReceipt(t *testing.T, root string) conclusionRecoverySeed {
	t.Helper()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRecord, err := server.projects.Create("Recovery", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Recovery Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "recover conclusion", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: projectRecord.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.UpdateContinuationRuntimeMetadata(
		launch.Continuation.ID, "", "assisted-session", "/sessions/assisted-session.jsonl",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	receipt, inserted, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, continuation.ID, "work-request-recovery", "assisted-session", "work-turn-recovery",
		task.TurnSelection{ModelProviderID: "provider-recovery", Model: "model-recovery"},
		task.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil || !inserted {
		t.Fatalf("seed recovery receipt: %#v inserted=%v err=%v", receipt, inserted, err)
	}
	return conclusionRecoverySeed{server: server, projectID: projectRecord.ID, profileID: profile.ID, task: created, receipt: receipt}
}

func newConclusionRecoverySession() *runtime.FakeProviderSession {
	return runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "assisted-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
}

func openConclusionRecoveryServer(t *testing.T, root string, factory ProviderSessionFactory) *Server {
	t.Helper()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func closeSeedServer(t *testing.T, server *Server) {
	t.Helper()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertConclusionRecoveryDidNotLaunch(t *testing.T, server *Server, factory *conclusionRecoveryFactory, taskID string) {
	t.Helper()
	opens, recovers := factory.counts()
	if opens != 0 || recovers != 1 || server.harness.IsActive(taskID) {
		t.Fatalf("restart launched Runtime: opens=%d recovers=%d harness_active=%v", opens, recovers, server.harness.IsActive(taskID))
	}
	var continuations int
	err := server.db.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, taskID).Scan(&continuations)
	if err != nil || continuations != 1 {
		t.Fatalf("restart continuation count = %d, err=%v", continuations, err)
	}
}

func assertConclusionAPIState(t *testing.T, server *Server, projectID, taskID string, want task.BlackboardConclusionState) {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Task API status = %d body=%s", response.Code, response.Body.String())
	}
	found, err := server.taskDetail(taskID)
	if err != nil || found.BlackboardConclusion.State != want {
		t.Fatalf("Task API conclusion = %#v, err=%v, want=%q", found.BlackboardConclusion, err, want)
	}
}
