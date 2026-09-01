package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
)

// Issue #204 / ADR 0022: in assisted mode blackboard_finish records a
// Blackboard Finish Intent and does not close the Runtime Continuation while the
// Work Runtime Turn can still produce work. Later same-Turn source work
// invalidates the intent and advances the conclusion watermarks. The Blackboard
// closes only when the Work Runtime Turn settles and its required Pending
// Blackboard Conclusion obligations have a valid terminal result.

// recordTaskFinishIntent drives the daemon-owned finish intent decision for a
// Task, mirroring what the trusted MCP blackboard_finish tool does through the
// Deps policy.
func recordTaskFinishIntent(t *testing.T, server *Server, projectID, taskID, continuationID, idempotencyKey string) blackboardv2.FinishContinuationResult {
	t.Helper()
	decision, err := server.resolveBlackboardFinishIntentPolicy(false, taskID, continuationID)
	if err != nil {
		t.Fatalf("resolve finish decision: %v", err)
	}
	if !decision.RecordIntent {
		t.Fatalf("finish decision = %#v, want RecordIntent for assisted Task", decision)
	}
	result, err := server.blackboardV2.RecordFinishIntent(context.Background(), projectID, continuationID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: idempotencyKey}, decision.Provenance)
	if err != nil {
		t.Fatalf("record finish intent: %v", err)
	}
	return result
}

// emitTaskWorkToolResult emits a non-Blackboard source-work Tool Result for the
// active Work Turn of an assisted Task, then returns the active continuation.
func emitTaskWorkToolResult(t *testing.T, server *Server, session *runtime.FakeProviderSession, requestID, turnID, toolCallID, toolName string) {
	t.Helper()
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
		ProviderTurnID: turnID, ToolCallID: toolCallID, ToolName: toolName, Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAssistedFinishIntentRecordsIntentAndKeepsContinuationWritable proves the
// core ADR 0022 behavior: in assisted mode blackboard_finish records an intent
// (not a close), the Continuation stays writable, and the public conclusion
// state is non-clean while the intent is unsettled.
func TestAssistedFinishIntentRecordsIntentAndKeepsContinuationWritable(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, t.TempDir(), true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	// Emit one source-work tool result so the Work Turn has provenance.
	emitTaskWorkToolResult(t, server, session, work.RequestID, "work-turn-finish-intent", "tool-1", "shell")

	intent := recordTaskFinishIntent(t, server, projectID, created.ID, cont.ID, "finish-intent-1")
	if intent.Status != blackboardv2.FinishStatusIntentRecorded {
		t.Fatalf("finish result status = %q, want intent_recorded", intent.Status)
	}
	// The Continuation stays writable: a Blackboard write succeeds.
	if _, err := server.blackboardV2.ApplyForContinuation(context.Background(), projectID, cont.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "post-intent-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:post-intent", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Post intent", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("write after finish intent: %v", err)
	}
	// The public conclusion state is non-clean while the intent is unsettled.
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStatePending)
	if found.BlackboardConclusion.State != task.BlackboardConclusionStatePending {
		t.Fatalf("public conclusion state = %q, want pending", found.BlackboardConclusion.State)
	}
}

// TestAssistedFinishIntentInvalidatedByLaterSameTurnSourceWork proves that a
// non-Blackboard Tool Result after a finish intent invalidates the intent, so a
// new finish call is required before the Blackboard can close.
func TestAssistedFinishIntentInvalidatedByLaterSameTurnSourceWork(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, t.TempDir(), true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	emitTaskWorkToolResult(t, server, session, work.RequestID, "work-turn-invalidate", "tool-1", "shell")
	recordTaskFinishIntent(t, server, projectID, created.ID, cont.ID, "finish-intent-invalidate-1")

	// A later non-Blackboard source-work tool result in the same Work Turn
	// invalidates the intent.
	emitTaskWorkToolResult(t, server, session, work.RequestID, "work-turn-invalidate", "tool-2", "shell")
	recorded, found, err := server.blackboardV2.FinishIntentForContinuation(context.Background(), projectID, cont.ID)
	if err != nil || !found {
		t.Fatalf("read finish intent after later work: found=%v err=%v", found, err)
	}
	if recorded.Valid {
		t.Fatalf("finish intent still valid after later source work: %#v", recorded)
	}
	// A new finish call records a fresh intent.
	if _, err := server.blackboardV2.RecordFinishIntent(context.Background(), projectID, cont.ID,
		blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-intent-invalidate-2"},
		blackboardv2.FinishIntentProvenance{SourceTurnID: "work-turn-invalidate", SourceWorkWatermark: 2}); err != nil {
		t.Fatalf("record fresh finish intent: %v", err)
	}
}

// TestAssistedFinishIntentSettlesAtWorkTurnClose closes the Continuation only
// when the Work Turn settles with a valid intent and covered semantic debt.
func TestAssistedFinishIntentSettlesAtWorkTurnClose(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, t.TempDir(), true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	// Record an intent with no outstanding source work.
	intent := recordTaskFinishIntent(t, server, projectID, created.ID, cont.ID, "finish-intent-settle")
	if intent.Status != blackboardv2.FinishStatusIntentRecorded {
		t.Fatalf("finish result status = %q, want intent_recorded", intent.Status)
	}
	// Work-Turn completion settles a valid intent with covered debt. The
	// WorkCompleted hook runs settlement when SourceWork <= SemanticPersistence.
	if _, err := server.blackboardV2.SettleFinishIntent(context.Background(), projectID, cont.ID); err != nil {
		t.Fatalf("settle finish intent: %v", err)
	}
	// After settlement the Continuation is closed.
	if active, err := server.tasks.ActiveContinuation(created.ID); err != nil || active != nil {
		t.Fatalf("continuation still active after settlement: %v %#v", err, active)
	}
}

// TestAssistedFinishIntentKeepsStatusNonCleanWhilePending proves criterion 4:
// the Runtime Owner Workspace status never reports clean while conclusion work
// is pending (a recorded-but-unsettled intent).
func TestAssistedFinishIntentKeepsStatusNonCleanWhilePending(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, t.TempDir(), true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	emitTaskWorkToolResult(t, server, session, work.RequestID, "work-turn-status", "tool-1", "shell")
	recordTaskFinishIntent(t, server, projectID, created.ID, cont.ID, "finish-intent-status")
	// The public state stays pending (not clean) while the intent is unsettled.
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("task detail status = %d body %s", response.Code, response.Body.String())
	}
	var found task.Task
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatal(err)
	}
	if found.BlackboardConclusion.State == task.BlackboardConclusionStateClean {
		t.Fatalf("public conclusion state = clean while finish intent pending: %#v", found.BlackboardConclusion)
	}
}

// TestInteractiveBlackboardFinishStillClosesImmediately proves the deferral is
// assisted-only: an interactive Task's finish closes the Continuation at once
// (the #190 behavior is unchanged).
func TestInteractiveBlackboardFinishStillClosesImmediately(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, t.TempDir(), true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "interactive")
	waitForAssistedProviderRequests(t, session, 1)
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	// Interactive mode: the daemon policy returns no deferred intent.
	decision, err := server.resolveBlackboardFinishIntentPolicy(false, created.ID, cont.ID)
	if err != nil {
		t.Fatalf("resolve finish decision: %v", err)
	}
	if decision.RecordIntent {
		t.Fatalf("interactive finish decision = %#v, want immediate close", decision)
	}
	finished, err := server.blackboardV2.FinishContinuation(context.Background(), projectID, cont.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "interactive-finish"})
	if err != nil {
		t.Fatalf("interactive finish: %v", err)
	}
	if finished.Status != blackboardv2.FinishStatusFinished {
		t.Fatalf("interactive finish status = %q, want finished", finished.Status)
	}
	if active, err := server.tasks.ActiveContinuation(created.ID); err != nil || active != nil {
		t.Fatalf("continuation still active after interactive finish: %v %#v", err, active)
	}
}

// TestTrustedBlackboardOperationRejectsSpoofedDisplayName proves criteria 5/7:
// an External MCP Server cannot gain trusted Blackboard authority by copying the
// "pentest" display name. The canonical identity classifier is the source of
// trust, never the provider-visible tool name.
func TestTrustedBlackboardOperationRejectsSpoofedDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		op       runtime.BlackboardOperation
		want     bool
	}{
		{name: "canonical finish", toolName: "mcp__pentest__blackboard_finish", op: runtime.BlackboardOperationFinish, want: true},
		{name: "spoofed finish bare name", toolName: "blackboard_finish", op: runtime.BlackboardOperationFinish, want: false},
		{name: "spoofed external server named pentest", toolName: "mcp__pentest_external__blackboard_finish", op: runtime.BlackboardOperationFinish, want: false},
		{name: "spoofed similar prefix", toolName: "mcp__pentest__blackboard_finishx", op: runtime.BlackboardOperationFinish, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classified, trusted := runtime.ClassifyTrustedBlackboardTool(tc.toolName)
			if trusted != tc.want {
				t.Fatalf("ClassifyTrustedBlackboardTool(%q) trusted = %v, want %v (classified=%v)", tc.toolName, trusted, tc.want, classified)
			}
			if tc.want && classified != tc.op {
				t.Fatalf("canonical op = %v, want %v", classified, tc.op)
			}
		})
	}
}

// launchAssistedSession creates an assisted Non-Project Session and returns it.
func launchAssistedSession(t *testing.T, server *Server, profileID string) session.Session {
	t.Helper()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", jsonBody(map[string]any{
		"input": "Inspect the standalone target",
		"run_controls": map[string]any{
			"blackboard_mode": "working_graph",
		},
		"runtime_profile_id": profileID,
		"runner":             "sandbox",
	}))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create assisted Session status = %d body %s", createResponse.Code, createResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Session: %v", err)
	}
	return created
}

func jsonBody(values map[string]any) *bytes.Reader {
	raw, _ := json.Marshal(values)
	return bytes.NewReader(raw)
}

// TestAssistedSessionFinishIntentRecordsIntentAndKeepsWritable proves the
// Session mirror of the ADR 0022 deferral: a Session blackboard_finish records
// an intent, the Session Continuation stays writable, and the public conclusion
// state is non-clean while the intent is unsettled.
func TestAssistedSessionFinishIntentRecordsIntentAndKeepsWritable(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := launchAssistedSession(t, server, profileID)
	waitForAssistedProviderRequests(t, provider, 1)
	work := provider.LastRequests()[0]
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil {
		t.Fatalf("active Session continuation: %v %#v", err, active)
	}
	emitTaskWorkToolResult(t, server, provider, work.RequestID, "session-work-intent", "tool-1", "shell")
	decision, err := server.resolveBlackboardFinishIntentPolicy(true, created.ID, active.ID)
	if err != nil {
		t.Fatalf("resolve Session finish decision: %v", err)
	}
	if !decision.RecordIntent {
		t.Fatalf("Session finish decision = %#v, want RecordIntent", decision)
	}
	intent, err := server.blackboardV2.RecordSessionFinishIntent(context.Background(), created.ID, active.ID, "session-intent-1", decision.Provenance)
	if err != nil {
		t.Fatalf("record Session finish intent: %v", err)
	}
	if intent.Status != blackboardv2.FinishStatusIntentRecorded {
		t.Fatalf("Session finish result status = %q, want intent_recorded", intent.Status)
	}
	// The Session Continuation stays writable.
	if _, err := server.blackboardV2.ApplyForSessionContinuation(context.Background(), created.ID, active.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "session-post-intent-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:session-post-intent", Type: "entity",
			Record: blackboardv2.SessionEntityRecord{Status: "active", Kind: "host", Name: "Session post intent"},
		}},
	}); err != nil {
		t.Fatalf("write after Session finish intent: %v", err)
	}
}

// TestAssistedSessionFinishIntentSettlesAtWorkTurnClose proves the Session
// Continuation closes only at Work-Turn settlement with a valid intent.
func TestAssistedSessionFinishIntentSettlesAtWorkTurnClose(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := launchAssistedSession(t, server, profileID)
	waitForAssistedProviderRequests(t, provider, 1)
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil {
		t.Fatalf("active Session continuation: %v %#v", err, active)
	}
	decision, err := server.resolveBlackboardFinishIntentPolicy(true, created.ID, active.ID)
	if err != nil {
		t.Fatalf("resolve Session finish decision: %v", err)
	}
	if _, err := server.blackboardV2.RecordSessionFinishIntent(context.Background(), created.ID, active.ID, "session-intent-settle", decision.Provenance); err != nil {
		t.Fatalf("record Session finish intent: %v", err)
	}
	settled, err := server.blackboardV2.SettleSessionFinishIntent(context.Background(), created.ID, active.ID)
	if err != nil {
		t.Fatalf("settle Session finish intent: %v", err)
	}
	if !settled {
		t.Fatalf("Session finish intent did not settle, want close")
	}
	if stillActive, err := server.sessions.ActiveContinuation(created.ID); err != nil || stillActive != nil {
		t.Fatalf("Session continuation still active after settlement: %v %#v", err, stillActive)
	}
}

// TestAssistedSessionFinishIntentInvalidatedByLaterSameTurnSourceWork proves the
// Session mirror of criterion 2: later non-Blackboard source work invalidates a
// recorded Session finish intent, and a new finish call records a fresh intent.
func TestAssistedSessionFinishIntentInvalidatedByLaterSameTurnSourceWork(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := launchAssistedSession(t, server, profileID)
	waitForAssistedProviderRequests(t, provider, 1)
	work := provider.LastRequests()[0]
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil {
		t.Fatalf("active Session continuation: %v %#v", err, active)
	}
	emitTaskWorkToolResult(t, server, provider, work.RequestID, "session-work-invalidate", "tool-1", "shell")
	decision, err := server.resolveBlackboardFinishIntentPolicy(true, created.ID, active.ID)
	if err != nil {
		t.Fatalf("resolve Session finish decision: %v", err)
	}
	if _, err := server.blackboardV2.RecordSessionFinishIntent(context.Background(), created.ID, active.ID, "session-intent-invalidate-1", decision.Provenance); err != nil {
		t.Fatalf("record Session finish intent: %v", err)
	}
	// A later non-Blackboard source-work tool result invalidates the intent.
	emitTaskWorkToolResult(t, server, provider, work.RequestID, "session-work-invalidate", "tool-2", "shell")
	recorded, found, err := server.blackboardV2.SessionFinishIntentForContinuation(context.Background(), created.ID, active.ID)
	if err != nil || !found {
		t.Fatalf("read Session finish intent after later work: found=%v err=%v", found, err)
	}
	if recorded.Valid {
		t.Fatalf("Session finish intent still valid after later source work: %#v", recorded)
	}
	// A new finish call records a fresh intent.
	if _, err := server.blackboardV2.RecordSessionFinishIntent(context.Background(), created.ID, active.ID, "session-intent-invalidate-2", decision.Provenance); err != nil {
		t.Fatalf("record fresh Session finish intent: %v", err)
	}
}

// TestAssistedSessionFinishIntentKeepsStatusNonCleanWhilePending proves the
// Session mirror of criterion 4: the Runtime Owner Workspace status never
// reports clean while a finish intent is pending.
func TestAssistedSessionFinishIntentKeepsStatusNonCleanWhilePending(t *testing.T) {
	server, _, profileID, provider := newAssistedConclusionFixture(t, true)
	created := launchAssistedSession(t, server, profileID)
	waitForAssistedProviderRequests(t, provider, 1)
	work := provider.LastRequests()[0]
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil {
		t.Fatalf("active Session continuation: %v %#v", err, active)
	}
	emitTaskWorkToolResult(t, server, provider, work.RequestID, "session-work-status", "tool-1", "shell")
	decision, err := server.resolveBlackboardFinishIntentPolicy(true, created.ID, active.ID)
	if err != nil {
		t.Fatalf("resolve Session finish decision: %v", err)
	}
	if _, err := server.blackboardV2.RecordSessionFinishIntent(context.Background(), created.ID, active.ID, "session-intent-status", decision.Provenance); err != nil {
		t.Fatalf("record Session finish intent: %v", err)
	}
	if !server.hasUnsettledSessionFinishIntent(created.ID) {
		t.Fatalf("Session finish intent not reported as unsettled while pending")
	}
}

// TestAssistedTaskFinishIntentSurvivesDaemonRestart proves a recorded-but-
// unsettled Blackboard Finish Intent is durable: after a daemon restart the
// recorded intent is still present and the public conclusion state stays
// non-clean (it cannot silently vanish). The obligation recovery path settles
// the intent when the Work Turn later completes.
func TestAssistedTaskFinishIntentSurvivesDaemonRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "working_graph")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	emitTaskWorkToolResult(t, server, session, work.RequestID, "work-turn-restart", "tool-1", "shell")
	recordTaskFinishIntent(t, server, projectID, created.ID, cont.ID, "finish-intent-restart")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	// A new daemon opens the same database. The recorded intent is durable.
	restartFactory := &assistedConclusionSessionFactory{
		session: session, adapter: runtime.NewProviderSessionRunAdapter(session, make(chan struct{})), support: true,
	}
	server2, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true, ProviderSessionFactory: restartFactory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server2.Close() })

	intent, found, err := server2.blackboardV2.FinishIntentForContinuation(context.Background(), projectID, cont.ID)
	if err != nil || !found {
		t.Fatalf("finish intent after restart: found=%v err=%v", found, err)
	}
	if !intent.Valid {
		t.Fatalf("finish intent invalidated by restart: %#v", intent)
	}
	// The Task's public conclusion state stays non-clean while the intent is
	// unsettled. The Task is interrupted after restart (orphaned runtime).
	found2, _ := server2.tasks.Get(created.ID)
	attached, err := server2.attachBlackboardConclusion(found2)
	if err != nil {
		t.Fatalf("attach conclusion after restart: %v", err)
	}
	if attached.BlackboardConclusion.State == task.BlackboardConclusionStateClean {
		t.Fatalf("public conclusion state = clean after restart while intent pending: %#v", attached.BlackboardConclusion)
	}
}
