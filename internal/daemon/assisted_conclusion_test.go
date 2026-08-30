package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// #163 primary acceptance seam: the daemon Task HTTP API, real Task and
// Blackboard v2 services, SQLite, and a deterministic fake ProviderSession.
func TestAssistedWorkTurnWithTerminalToolResultBecomesPending(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create assisted Task status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Task: %v", err)
	}
	waitForAssistedProviderRequests(t, session, 1)

	for _, observation := range []runtime.ProviderSessionObservation{
		{
			Kind:           runtime.ProviderSessionObservationToolUse,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell",
		},
		{
			Kind:           runtime.ProviderSessionObservationToolResult,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
		},
		{
			Kind:           runtime.ProviderSessionObservationTurnCompleted,
			ProviderTurnID: "work-turn-1", Status: "completed",
		},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatalf("emit observation %#v: %v", observation, err)
		}
	}

	waitForAssistedProviderRequests(t, session, 2)
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		t.Fatalf("stored conclusion mode = %q, want assisted", found.RunControls.BlackboardConclusionMode)
	}
	if found.BlackboardConclusion.Mode != task.BlackboardConclusionModeAssisted || found.BlackboardConclusion.SourceTurnID != "work-turn-1" {
		t.Fatalf("Blackboard conclusion view = %#v", found.BlackboardConclusion)
	}
	if found.Status != task.StatusRunning || found.RuntimeActivity.Liveness != "live" || found.RuntimeActivity.TurnActivity != "busy" {
		t.Fatalf("Task lifecycle/activity changed by pending conclusion: status=%q activity=%#v", found.Status, found.RuntimeActivity)
	}

	events := assistedTaskEvents(t, server, projectID, created.ID)
	pendingEvents := 0
	for _, event := range events {
		if event.Kind != task.EventKindBlackboardConclusion {
			continue
		}
		if event.Payload["phase"] != "pending_detected" {
			continue
		}
		pendingEvents++
		if event.ContinuationID == "" || event.Payload["source_turn_id"] != "work-turn-1" {
			t.Fatalf("pending Harness event = %#v", event)
		}
		for _, forbidden := range []string{"args", "input", "output", "text", "raw", "message", "reasoning"} {
			if _, ok := event.Payload[forbidden]; ok {
				t.Fatalf("pending Harness event leaked %q: %#v", forbidden, event.Payload)
			}
		}
	}
	if pendingEvents != 1 {
		t.Fatalf("pending Harness events = %d, want 1; events=%#v", pendingEvents, events)
	}

	timelineRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/timeline", nil)
	timelineResponse := httptest.NewRecorder()
	server.ServeHTTP(timelineResponse, timelineRequest)
	if timelineResponse.Code != http.StatusOK {
		t.Fatalf("get Timeline status = %d body %s", timelineResponse.Code, timelineResponse.Body.String())
	}
	var timelineBody struct {
		Items []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(timelineResponse.Body).Decode(&timelineBody); err != nil {
		t.Fatalf("decode Timeline: %v", err)
	}
	pendingHarnessItems := 0
	for _, item := range timelineBody.Items {
		if item.Type == "harness" && strings.Contains(item.Content, "Blackboard conclusion pending") {
			pendingHarnessItems++
		}
	}
	if pendingHarnessItems != 1 {
		t.Fatalf("pending Harness Timeline items = %d, want 1; items=%#v", pendingHarnessItems, timelineBody.Items)
	}

	transcriptRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/transcript", nil)
	transcriptResponse := httptest.NewRecorder()
	server.ServeHTTP(transcriptResponse, transcriptRequest)
	if transcriptResponse.Code != http.StatusOK {
		t.Fatalf("get transcript status = %d body %s", transcriptResponse.Code, transcriptResponse.Body.String())
	}
	if strings.Contains(strings.ToLower(transcriptResponse.Body.String()), "pending_detected") {
		t.Fatalf("Harness pending marker leaked into Task Conversation: %s", transcriptResponse.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 2 {
		t.Fatalf("provider requests = %d, want work and Conclude Turns", len(requests))
	}
}

func TestAssistedConclusionRejectsSeparatorAliasOfHistoricalAttempt(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	peer, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectID, Type: task.TypePentest, Goal: "record the completed challenge", RuntimeProfileID: profileID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: projectID, TaskID: peer.ID, RuntimeProfileID: profileID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.blackboardV2.ApplyForContinuation(context.Background(), projectID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "historical-attempt-with-slash-keys",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective/solve-nssctf-arena", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Solve the current NSSCTF challenge."}},
			{Op: "create", Key: "attempt/3121", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Solved challenge 3121."}},
			{Op: "relate", From: "attempt/3121", Relation: "tests", To: "objective/solve-nssctf-arena"},
			{Op: "transition", Key: "attempt/3121", Version: 1, Status: "inconclusive", Summary: "Completed challenge 3121 work."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-3124", ToolCallID: "tool-3124", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-3124", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil || receipt.BaseRevision == nil {
		t.Fatalf("load awaiting conclusion: receipt=%#v err=%v", receipt, err)
	}
	staleAlias := fmt.Sprintf(`{
		"schema":"runtime-attempt-result/v1","base_revision":%d,
		"attempt":{"key":"attempt:3121","create":true,"summary":"Solved challenge 3121.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:solve-nssctf-arena","create_objective":{"objective":"Solve the current NSSCTF challenge."}}],
		"produced_targets":[]
	}`, *receipt.BaseRevision)
	if err := emitAttemptResultAndComplete(t, session, []byte(staleAlias)); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.AppliedRevision != nil {
		t.Fatalf("stale conclusion was applied: %#v", found.BlackboardConclusion)
	}
	if _, err := server.blackboardV2.ReadCurrent(context.Background(), projectID, "attempt:3121"); err == nil {
		t.Fatal("stale conclusion created a separator-alias Attempt")
	}
}

func TestAssistedWorkTurnDispatchesControlTurnAndAppliesClosedResult(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	sourceContinuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}

	waitForAssistedProviderRequests(t, session, 2)
	requests := session.LastRequests()
	controlRequest := requests[1]
	if !strings.HasPrefix(controlRequest.RequestID, "conclude:v1:") || controlRequest.RequestID == workRequest.RequestID {
		t.Fatalf("control request identity = %q after work %q", controlRequest.RequestID, workRequest.RequestID)
	}
	if controlRequest.TurnKind != runtime.RuntimeTurnKindControl {
		t.Fatalf("control Turn kind = %q", controlRequest.TurnKind)
	}
	if controlRequest.ModelProviderID != workRequest.ModelProviderID || controlRequest.Model != workRequest.Model ||
		controlRequest.RequestedReasoningEffort != workRequest.RequestedReasoningEffort {
		t.Fatalf("control selection = %#v, want source selection %#v", controlRequest, workRequest)
	}
	if !strings.Contains(controlRequest.Message, "runtime-attempt-result/v1") || !strings.Contains(strings.ToLower(controlRequest.Message), "stop") {
		t.Fatalf("control directive does not require a bounded result: %q", controlRequest.Message)
	}
	lowerDirective := strings.ToLower(controlRequest.Message)
	if !strings.Contains(lowerDirective, "do not read files") || strings.Contains(lowerDirective, ".pentest/blackboard.json") || strings.Contains(lowerDirective, "reread") {
		t.Fatalf("control directive invites forbidden file access: %q", controlRequest.Message)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if found.Status != task.StatusRunning || found.RuntimeActivity.Liveness != "live" || found.RuntimeActivity.TurnActivity != "busy" {
		t.Fatalf("Conclude Turn changed Task lifecycle/activity: status=%q activity=%#v", found.Status, found.RuntimeActivity)
	}
	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil {
		t.Fatalf("load concluding receipt: %#v, %v", receipt, err)
	}
	if receipt.ControlTurnID == "" || receipt.ControlTurnID == receipt.SourceTurnID || receipt.BaseRevision == nil || *receipt.BaseRevision != 0 {
		t.Fatalf("Conclude Turn durable lineage = %#v", receipt)
	}

	result := `{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt:juice-shop-search-sqli","create":true,"summary":"Tested search SQL injection; exploitability remained unproven.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:juice-shop-search-sqli","create_objective":{"objective":"Determine whether search is vulnerable to SQL injection."}}],
		"produced_targets":[]
	}`
	if err := emitAttemptResultAndComplete(t, session, []byte(result)); err != nil {
		t.Fatal(err)
	}
	found = waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || *found.BlackboardConclusion.AppliedRevision == 0 {
		t.Fatalf("applied conclusion view = %#v", found.BlackboardConclusion)
	}
	snapshot, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != *found.BlackboardConclusion.AppliedRevision || snapshot.Work.Objectives["objective:juice-shop-search-sqli"].Status != "open" {
		t.Fatalf("applied Blackboard snapshot = %#v", snapshot)
	}
	history, err := server.blackboardV2.ReadHistory(context.Background(), projectID, "attempt:juice-shop-search-sqli", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, item := range history.Items {
		if item.Record != nil && item.Record.Status == "inconclusive" {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal Attempt history entries = %d, want 1; history=%#v", terminal, history)
	}

	beforeRevision := snapshot.Revision
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-1", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitAttemptResult([]byte(result)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := len(session.LastRequests()); got != 2 {
		t.Fatalf("provider requests after duplicate delivery = %d, want 2", got)
	}
	after, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != beforeRevision {
		t.Fatalf("duplicate result advanced revision from %d to %d", beforeRevision, after.Revision)
	}
	latestContinuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latestContinuation.ID != sourceContinuation.ID {
		t.Fatalf("Conclude Turn created a Runtime Continuation: before=%#v after=%#v err=%v", sourceContinuation, latestContinuation, err)
	}
	phases := map[string]int{}
	events := assistedTaskEvents(t, server, projectID, created.ID)
	encodedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedEvents), "runtime-attempt-result/v1") || strings.Contains(string(encodedEvents), "exploitability remained unproven") {
		t.Fatalf("structured directive/result leaked into Task Events: %s", encodedEvents)
	}
	for _, event := range events {
		if event.Kind == task.EventKindBlackboardConclusion {
			phases[fmt.Sprint(event.Payload["phase"])]++
		}
	}
	if phases["dispatch_requested"] != 1 || phases["applied"] != 1 {
		t.Fatalf("idempotent conclusion phases = %#v", phases)
	}
	transcriptRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/transcript", nil)
	transcriptResponse := httptest.NewRecorder()
	server.ServeHTTP(transcriptResponse, transcriptRequest)
	if transcriptResponse.Code != http.StatusOK || strings.Contains(transcriptResponse.Body.String(), "runtime-attempt-result/v1") || strings.Contains(transcriptResponse.Body.String(), "exploitability remained unproven") {
		t.Fatalf("structured directive/result leaked into Conversation: status=%d body=%s", transcriptResponse.Code, transcriptResponse.Body.String())
	}
}

func TestAssistedConclusionRegeneratesOnceAfterConcurrentContinuationAdvancesProject(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "work-turn-version-conflict", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "work-turn-version-conflict", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	concludeRequest := session.LastRequests()[1]

	peerRevision := advanceProjectFromPeerContinuation(t, server, projectID, profileID)
	initial := assistedAttemptResultJSON(0, "attempt:version-conflict", "objective:version-conflict")
	if err := emitAttemptResultAndComplete(t, session, []byte(initial)); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 3)

	regenerateRequest := session.LastRequests()[2]
	if regenerateRequest.RequestID == concludeRequest.RequestID || !strings.Contains(regenerateRequest.RequestID, "version") {
		t.Fatalf("regeneration request ID = %q after Conclude %q", regenerateRequest.RequestID, concludeRequest.RequestID)
	}
	if regenerateRequest.TurnKind != runtime.RuntimeTurnKindControl ||
		regenerateRequest.ModelProviderID != concludeRequest.ModelProviderID ||
		regenerateRequest.Model != concludeRequest.Model ||
		regenerateRequest.RequestedReasoningEffort != concludeRequest.RequestedReasoningEffort {
		t.Fatalf("regeneration selection = %#v, Conclude selection = %#v", regenerateRequest, concludeRequest)
	}
	directive := strings.ToLower(regenerateRequest.Message)
	for _, required := range []string{"regenerate", "do not read files", "do not call tools", fmt.Sprintf("base_revision %d", peerRevision)} {
		if !strings.Contains(directive, required) {
			t.Fatalf("regeneration directive missing %q: %s", required, regenerateRequest.Message)
		}
	}
	if strings.Contains(directive, ".pentest/blackboard.json") || strings.Contains(directive, "reread") {
		t.Fatalf("regeneration directive invites forbidden file access: %s", regenerateRequest.Message)
	}
	continuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	working, err := server.blackboardV2Continuity.ReadWorkingSnapshot(context.Background(), continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(server.runtimeRoot, created.ID, "workdir", ".pentest", "blackboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	if working.LastAcknowledgedRevision != peerRevision || !bytes.Equal(working.Bytes, onDisk) {
		t.Fatalf("synchronized working snapshot revision=%d disk_equal=%v want revision=%d", working.LastAcknowledgedRevision, bytes.Equal(working.Bytes, onDisk), peerRevision)
	}

	regenerated := assistedAttemptResultJSON(peerRevision, "attempt:version-conflict", "objective:version-conflict")
	if err := emitAttemptResultAndComplete(t, session, []byte(regenerated)); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || *found.BlackboardConclusion.AppliedRevision <= peerRevision {
		t.Fatalf("regenerated conclusion view = %#v", found.BlackboardConclusion)
	}
	if len(session.LastRequests()) != 3 {
		t.Fatalf("provider requests = %d, want work, Conclude, regeneration", len(session.LastRequests()))
	}
	snapshot, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Work.Objectives["objective:version-conflict"].Status != "open" {
		t.Fatalf("conclusion implicitly closed objective: %#v", snapshot.Work.Objectives["objective:version-conflict"])
	}
	for _, relation := range snapshot.Relations {
		if len(relation) > 1 && relation[1] == "satisfies" {
			t.Fatalf("conclusion inferred objective satisfaction: %#v", relation)
		}
	}
}

func TestAssistedConclusionSecondProjectRevisionConflictRequiresAction(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-second-version-conflict", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-second-version-conflict", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	firstPeerRevision := advanceProjectFromPeerContinuation(t, server, projectID, profileID)
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(0, "attempt:second-version-conflict", "objective:second-version-conflict"))); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 3)
	secondPeerRevision := advanceProjectFromPeerContinuation(t, server, projectID, profileID)
	if secondPeerRevision <= firstPeerRevision {
		t.Fatalf("second peer revision = %d after %d", secondPeerRevision, firstPeerRevision)
	}
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(firstPeerRevision, "attempt:second-version-conflict", "objective:second-version-conflict"))); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorVersionConflict || len(session.LastRequests()) != 3 {
		t.Fatalf("second version conflict = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionQueuesRegeneratedResultBeforeSendTurnReturns(t *testing.T) {
	regenerated := assistedAttemptResultJSON(1, "attempt:eager-version", "objective:eager-version")
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &eagerVersionRegenerationSession{FakeProviderSession: fake, raw: []byte(regenerated)}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-eager-version", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-eager-version", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	if revision := advanceProjectFromPeerContinuation(t, server, projectID, profileID); revision != 1 {
		t.Fatalf("peer revision = %d, want 1", revision)
	}
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(0, "attempt:eager-version", "objective:eager-version"))); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || *found.BlackboardConclusion.AppliedRevision <= 1 || len(session.LastRequests()) != 3 {
		t.Fatalf("eager regenerated conclusion = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionIgnoresOtherProjectRevisionProgress(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-other-project", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-other-project", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	otherProject, err := server.projects.Create("Other Project", "", project.Scope{Domains: []string{"other.example"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if revision := advanceProjectFromPeerContinuation(t, server, otherProject.ID, profileID); revision != 1 {
		t.Fatalf("other Project revision = %d, want 1", revision)
	}
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(0, "attempt:project-isolation", "objective:project-isolation"))); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || len(session.LastRequests()) != 2 {
		t.Fatalf("other Project progress affected conclusion = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionStartupReplaysCommittedValidatedApplyWithoutProviderTurn(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-startup-replay", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-startup-replay", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil || receipt.BaseRevision == nil {
		t.Fatalf("load awaiting receipt: %#v, %v", receipt, err)
	}
	decoded, err := blackboardconclusion.Decode([]byte(assistedAttemptResultJSON(*receipt.BaseRevision, "attempt:startup-replay", "objective:startup-replay")))
	if err != nil {
		t.Fatal(err)
	}
	validated, won, err := server.tasks.MarkBlackboardConclusionValidated(receipt.DispatchRequestID, decoded.CanonicalJSON)
	if err != nil || !won {
		t.Fatalf("persist validated apply intent: won=%v receipt=%#v err=%v", won, validated, err)
	}
	batch, err := blackboardconclusion.Compile(decoded.Result, validated.ApplyIdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := server.blackboardV2.ApplyForContinuationAtRevision(
		context.Background(), projectID, validated.ContinuationID, *validated.BaseRevision, batch,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	restartSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "restart-session", Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	restartAdapter := runtime.NewProviderSessionRunAdapter(restartSession, make(chan struct{}))
	restarted, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: &assistedConclusionSessionFactory{session: restartSession, adapter: restartAdapter, support: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	replayed, err := restarted.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || replayed == nil {
		t.Fatalf("load replayed receipt: %#v, %v", replayed, err)
	}
	if replayed.InternalState != task.BlackboardConclusionReceiptApplied || replayed.AppliedRevision == nil ||
		*replayed.AppliedRevision != committed.Revision || replayed.ApplyIdempotencyKey != validated.ApplyIdempotencyKey ||
		replayed.CanonicalResultSHA256 != validated.CanonicalResultSHA256 {
		t.Fatalf("startup replay receipt = %#v, committed revision=%d", replayed, committed.Revision)
	}
	if len(restartSession.LastRequests()) != 0 {
		t.Fatalf("startup replay dispatched %d provider Turn(s)", len(restartSession.LastRequests()))
	}
}

func TestAssistedConclusionReplaysValidatedIntentOnceAfterCommittedPublicationError(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-publication-replay", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-publication-replay", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	injections := 0
	server.blackboardV2.SetSnapshotPublicationInjector(func(point blackboardv2.SnapshotPublicationPoint, _ string) error {
		if point == blackboardv2.SnapshotPublicationAfterCommit {
			injections++
			return errors.New("injected committed publication loss")
		}
		return nil
	})
	defer server.blackboardV2.SetSnapshotPublicationInjector(nil)
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(0, "attempt:publication-replay", "objective:publication-replay"))); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if injections != 1 || found.BlackboardConclusion.AppliedRevision == nil || len(session.LastRequests()) != 2 {
		t.Fatalf("publication replay injections=%d conclusion=%#v requests=%d", injections, found.BlackboardConclusion, len(session.LastRequests()))
	}
	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil || receipt.CanonicalResultSHA256 == "" || receipt.ApplyIdempotencyKey == "" {
		t.Fatalf("publication replay durable lineage = %#v, %v", receipt, err)
	}
}

func TestAssistedConclusionStartupFailsClosedWhenValidatedBaseAdvancedWithoutLiveSession(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-startup-conflict", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-startup-conflict", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	receipt := waitForBlackboardConclusionReceiptState(t, server, created.ID, task.BlackboardConclusionReceiptAwaitingResult)
	if receipt.BaseRevision == nil {
		t.Fatalf("awaiting receipt has no base revision: %#v", receipt)
	}
	decoded, err := blackboardconclusion.Decode([]byte(assistedAttemptResultJSON(*receipt.BaseRevision, "attempt:startup-conflict", "objective:startup-conflict")))
	if err != nil {
		t.Fatal(err)
	}
	if _, won, err := server.tasks.MarkBlackboardConclusionValidated(receipt.DispatchRequestID, decoded.CanonicalJSON); err != nil || !won {
		t.Fatalf("persist validated conflict intent: won=%v err=%v", won, err)
	}
	if revision := advanceProjectFromPeerContinuation(t, server, projectID, profileID); revision != 1 {
		t.Fatalf("peer revision = %d, want 1", revision)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	restartSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "restart-conflict-session", Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	restarted, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: &assistedConclusionSessionFactory{
			session: restartSession, adapter: runtime.NewProviderSessionRunAdapter(restartSession, make(chan struct{})), support: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	failedClosed, err := restarted.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || failedClosed == nil {
		t.Fatalf("load startup conflict receipt: %#v, %v", failedClosed, err)
	}
	if failedClosed.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		failedClosed.ErrorCode != task.BlackboardConclusionErrorVersionConflict || len(restartSession.LastRequests()) != 0 {
		t.Fatalf("startup conflict recovery = %#v requests=%d", failedClosed, len(restartSession.LastRequests()))
	}
}

func TestAssistedConclusionSynchronizationFilesystemFailureSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-sync-filesystem", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-sync-filesystem", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	if revision := advanceProjectFromPeerContinuation(t, server, projectID, profileID); revision != 1 {
		t.Fatalf("peer revision = %d, want 1", revision)
	}
	workdir := filepath.Join(root, "runs", created.ID, "workdir")
	if err := os.RemoveAll(workdir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workdir, []byte("blocks working snapshot publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(0, "attempt:sync-filesystem", "objective:sync-filesystem"))); err != nil {
		t.Fatal(err)
	}
	var failed *task.BlackboardConclusionReceipt
	var pollErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		failed, pollErr = server.tasks.LatestBlackboardConclusion(created.ID)
		if pollErr == nil && failed != nil && failed.InternalState == task.BlackboardConclusionReceiptActionRequired {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if failed == nil || failed.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		failed.ErrorCode != task.BlackboardConclusionErrorInvalidResult || len(session.LastRequests()) != 2 {
		t.Fatalf("synchronization failure = %#v err=%v requests=%d", failed, pollErr, len(session.LastRequests()))
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workdir); err != nil {
		t.Fatal(err)
	}

	restartSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "restart-sync-session", Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	restarted, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: &assistedConclusionSessionFactory{
			session: restartSession, adapter: runtime.NewProviderSessionRunAdapter(restartSession, make(chan struct{})), support: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	persisted, err := restarted.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || persisted == nil || persisted.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		persisted.ErrorCode != task.BlackboardConclusionErrorInvalidResult || len(restartSession.LastRequests()) != 0 {
		t.Fatalf("restarted synchronization failure = %#v err=%v requests=%d", persisted, err, len(restartSession.LastRequests()))
	}
}

func TestAssistedConclusionIncompatibleSemanticResultRequiresActionWithoutRegeneration(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	seed, err := server.blackboardV2.Apply(context.Background(), projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-incompatible-objective",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "objective:incompatible", Type: "objective",
			Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Existing objective"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-incompatible", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-incompatible", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	if err := emitAttemptResultAndComplete(t, session, []byte(assistedAttemptResultJSON(seed.Revision, "attempt:incompatible", "objective:incompatible"))); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorInvalidResult || len(session.LastRequests()) != 2 {
		t.Fatalf("incompatible semantic result = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionRecordVersionMismatchRequiresActionWithoutOverwrite(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	continuation, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || continuation == nil {
		t.Fatalf("load source Continuation: %#v, %v", continuation, err)
	}
	seeded, err := server.blackboardV2.ApplyForContinuation(context.Background(), projectID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-record-version-mismatch",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:record-version", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Check record version handling"}},
			{Op: "create", Key: "attempt:record-version", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Initial attempt"}},
			{Op: "relate", From: "attempt:record-version", Relation: "tests", To: "objective:record-version"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 1)
	work := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: work.RequestID, ProviderTurnID: "work-turn-record-version", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: work.RequestID, ProviderTurnID: "work-turn-record-version", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	result := fmt.Sprintf(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":%d,
		"attempt":{"key":"attempt:record-version","expected_version":99,"summary":"Stale semantic update.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:record-version","expected_version":1}],
		"produced_targets":[]
	}`, seeded.Revision)
	if err := emitAttemptResultAndComplete(t, session, []byte(result)); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorVersionConflict || len(session.LastRequests()) != 2 {
		t.Fatalf("record-version mismatch = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
	detail, err := server.blackboardV2.ReadCurrent(context.Background(), projectID, "attempt:record-version")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Version != 1 || detail.Record.Status != "open" || detail.Record.Summary != "Initial attempt" {
		t.Fatalf("record-version mismatch overwrote Attempt: %#v", detail)
	}
}

func TestAssistedConclusionQueuesResultEmittedBeforeSendTurnReturns(t *testing.T) {
	raw := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:eager-result","create":true,"summary":"Recorded an eager control result.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:eager-result","create_objective":{"objective":"Exercise eager provider result delivery."}}],
		"produced_targets":[]
	}`)
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &eagerAttemptResultSession{FakeProviderSession: fake, raw: raw}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil {
		t.Fatalf("eager result was not applied: %#v", found.BlackboardConclusion)
	}
}

func TestAssistedConclusionQueuesInvalidResultEmittedBeforeSendTurnReturns(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &eagerAttemptResultSession{
				FakeProviderSession: fake,
				raw:                 []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`),
				ignoreResultError:   true,
			}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 3)
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRepairExhausted {
		t.Fatalf("eager invalid result state = %#v", found.BlackboardConclusion)
	}
}

func TestAssistedConclusionQueuesForbiddenToolUseEmittedBeforeSendTurnReturns(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &eagerControlToolSession{FakeProviderSession: fake}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || len(session.LastRequests()) != 2 {
		t.Fatalf("eager forbidden tool result = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionWaitsForBusyTaskControlWithoutLosingDispatch(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	if !server.acquireProviderTaskControl(created.ID) {
		t.Fatal("acquire competing provider task control")
	}
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStatePending)
	if got := len(session.LastRequests()); got != 1 {
		t.Fatalf("Conclude Turn ran through competing control: requests=%d", got)
	}
	server.releaseProviderTaskControl(created.ID)
	waitForAssistedProviderRequests(t, session, 2)
}

func TestAssistedConclusionWaitsForBusyTaskControlWithoutLosingResult(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	waitForProviderTaskControl(t, server, created.ID)
	result := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:queued-result","create":true,"summary":"Queued result under control contention.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:queued-result","create_objective":{"objective":"Exercise queued result delivery."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, session, result); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	found := getConclusionTask(t, server, projectID, created.ID)
	if found.BlackboardConclusion.State != task.BlackboardConclusionStateConcluding {
		t.Fatalf("result ignored competing control: %#v", found.BlackboardConclusion)
	}
	server.releaseProviderTaskControl(created.ID)
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
}

func waitForProviderTaskControl(t *testing.T, server *Server, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.acquireProviderTaskControl(taskID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("provider task control did not become available")
}

func getConclusionTask(t *testing.T, server *Server, projectID, taskID string) task.Task {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get Task status=%d body=%s", response.Code, response.Body.String())
	}
	var found task.Task
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatal(err)
	}
	return found
}

type eagerAttemptResultSession struct {
	*runtime.FakeProviderSession
	raw               []byte
	ignoreResultError bool
}

type eagerVersionRegenerationSession struct {
	*runtime.FakeProviderSession
	raw []byte
}

func (session *eagerVersionRegenerationSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	result, err := session.FakeProviderSession.SendTurn(ctx, request, emit)
	if err == nil && strings.Contains(request.RequestID, "version") {
		resultErr := session.EmitAttemptResult(session.raw)
		terminalErr := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: request.RequestID,
			ProviderTurnID: result.ProviderTurnID, Status: "completed",
		})
		if resultErr != nil {
			return runtime.ProviderSessionResult{}, resultErr
		}
		if terminalErr != nil {
			return runtime.ProviderSessionResult{}, terminalErr
		}
	}
	return result, err
}

func (session *eagerAttemptResultSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	result, err := session.FakeProviderSession.SendTurn(ctx, request, emit)
	if err == nil && request.TurnKind == runtime.RuntimeTurnKindControl {
		resultErr := session.EmitAttemptResult(session.raw)
		if completeErr := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: request.RequestID,
			ProviderTurnID: result.ProviderTurnID, Status: "completed",
		}); completeErr != nil {
			return result, completeErr
		}
		if !session.ignoreResultError {
			err = resultErr
		}
	}
	return result, err
}

type eagerControlToolSession struct {
	*runtime.FakeProviderSession
}

type failingConclusionDispatchSession struct {
	*runtime.FakeProviderSession
	requestKind string
}

func (session *failingConclusionDispatchSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	if request.TurnKind == runtime.RuntimeTurnKindControl && strings.Contains(request.RequestID, session.requestKind) {
		return runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{
			Mode: runtime.ProviderSessionModeSendTurn, Cause: errors.New("injected control dispatch failure"),
		}
	}
	return session.FakeProviderSession.SendTurn(ctx, request, emit)
}

func (session *eagerControlToolSession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	result, err := session.FakeProviderSession.SendTurn(ctx, request, emit)
	if err == nil && request.TurnKind == runtime.RuntimeTurnKindControl {
		err = session.EmitObservation(runtime.ProviderSessionObservation{
			Kind: runtime.ProviderSessionObservationToolUse, RequestID: request.RequestID,
			ProviderTurnID: "provider-spoofed-turn", ToolCallID: "eager-control-tool", ToolName: "shell",
		})
	}
	return result, err
}

func TestAssistedLaunchRejectsProviderWithoutConclusionObservations(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, false)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "assisted_conclusion_unsupported") {
		t.Fatalf("unsupported assisted launch status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("unsupported assisted launch reached provider: %#v", requests)
	}
}

func TestRuntimePluginAPIProjectsEffectiveAssistedConclusionCapability(t *testing.T) {
	for _, test := range []struct {
		name    string
		support bool
	}{
		{name: "supported", support: true},
		{name: "unsupported", support: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, _ := newAssistedConclusionFixture(t, test.support)
			request := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins/codex", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("get runtime plugin status = %d body %s", response.Code, response.Body.String())
			}
			var plugin runtimeplugin.Plugin
			if err := json.NewDecoder(response.Body).Decode(&plugin); err != nil {
				t.Fatal(err)
			}
			if plugin.Capabilities.AssistedConclusion != test.support {
				t.Fatalf("effective assisted_conclusion = %v, want %v", plugin.Capabilities.AssistedConclusion, test.support)
			}
		})
	}
}

func TestAssistedLaunchRejectsSessionThatCannotEmitConclusionObservations(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureWithCapabilities(t, true, false)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "assisted_conclusion_unsupported") {
		t.Fatalf("session capability mismatch status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("capability mismatch reached provider Turn: %#v", requests)
	}
}

func TestTaskLaunchRejectsUnknownBlackboardConclusionMode(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"automatic"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "interactive, assisted, or disabled") {
		t.Fatalf("invalid conclusion mode status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("invalid conclusion mode reached provider: %#v", requests)
	}
}

func TestAssistedWorkTurnWithoutToolResultStaysClean(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind:           runtime.ProviderSessionObservationTurnCompleted,
		ProviderTurnID: "work-turn-1", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.SourceTurnID != "" {
		t.Fatalf("clean conclusion retained source Turn: %#v", found.BlackboardConclusion)
	}
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			t.Fatalf("no-tool Turn created conclusion Event: %#v", event)
		}
	}
}

func TestInteractiveWorkTurnWithToolResultStaysClean(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, false)
	created := launchConclusionTask(t, server, projectID, profileID, "interactive")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive || found.BlackboardConclusion.Mode != task.BlackboardConclusionModeInteractive {
		t.Fatalf("interactive conclusion view = %#v", found.BlackboardConclusion)
	}
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			t.Fatalf("interactive Turn created conclusion Event: %#v", event)
		}
	}
}

func TestTaskLaunchWithoutConclusionModeDefaultsToInteractive(t *testing.T) {
	server, projectID, profileID, _ := newAssistedConclusionFixture(t, false)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("legacy Task launch status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive ||
		created.BlackboardConclusion != (task.BlackboardConclusion{Mode: task.BlackboardConclusionModeInteractive, State: task.BlackboardConclusionStateClean}) {
		t.Fatalf("legacy Task conclusion defaults = %#v / %#v", created.RunControls, created.BlackboardConclusion)
	}
}

func TestDuplicateAssistedTurnCompletionCreatesOnePendingMarker(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for replay := 0; replay < 2; replay++ {
		if err := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind:           runtime.ProviderSessionObservationToolResult,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
		}); err != nil {
			t.Fatal(err)
		}
		if err := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind:           runtime.ProviderSessionObservationTurnCompleted,
			ProviderTurnID: "work-turn-1", Status: "completed",
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	markers := 0
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion && event.Payload["phase"] == "pending_detected" {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("duplicate completion markers = %d, want 1", markers)
	}
}

func TestUncertainBlackboardConclusionRequiresActionAfterDaemonRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	before := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if before.BlackboardConclusion.SourceTurnID != "work-turn-1" || before.BlackboardConclusion.SourceWorkWatermark != 1 ||
		before.BlackboardConclusion.SemanticPersistenceWatermark != 0 {
		t.Fatalf("pending checkpoint before restart = %#v", before.BlackboardConclusion)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	found := waitForBlackboardConclusionState(t, reopened, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.SourceTurnID != "work-turn-1" || found.BlackboardConclusion.SourceWorkWatermark != 1 ||
		found.BlackboardConclusion.SemanticPersistenceWatermark != 0 ||
		found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("restarted conclusion view = %#v", found.BlackboardConclusion)
	}
	var pending task.Event
	for _, event := range assistedTaskEvents(t, reopened, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion && event.Payload["phase"] == "pending_detected" {
			pending = event
		}
	}
	if pending.Payload["source_work_watermark"] != float64(1) || pending.Payload["semantic_persistence_watermark"] != float64(0) {
		t.Fatalf("restarted semantic-debt projection = %#v", pending)
	}
}

func TestRecoveryGenerationAwaitingResultBecomesActionRequiredAfterRestart(t *testing.T) {
	for _, generation := range []string{"repair", "retry"} {
		t.Run(generation, func(t *testing.T) {
			root := t.TempDir()
			server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
			created := launchConclusionTask(t, server, projectID, profileID, "assisted")
			waitForAssistedProviderRequests(t, session, 1)
			workRequest := session.LastRequests()[0]
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
				ProviderTurnID: "work-turn-restart-" + generation, ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
				ProviderTurnID: "work-turn-restart-" + generation, Status: "completed",
			}); err != nil {
				t.Fatal(err)
			}
			waitForAssistedProviderRequests(t, session, 2)
			switch generation {
			case "repair":
				if err := emitAttemptResultAndComplete(t, session, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)); err == nil {
					t.Fatal("invalid result unexpectedly decoded")
				}
				waitForAssistedProviderRequests(t, session, 3)
			case "retry":
				if err := session.EmitObservation(runtime.ProviderSessionObservation{
					Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "force-restart-retry", ToolName: "shell",
				}); err != nil {
					t.Fatal(err)
				}
				waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
				retry := httptest.NewRequest(http.MethodPost,
					"/api/projects/"+projectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
				authorizeOperatorTestRequest(server, retry)
				retry.Header.Set("Idempotency-Key", "restart-awaiting-retry")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, retry)
				if response.Code != http.StatusAccepted {
					t.Fatalf("retry status = %d body %s", response.Code, response.Body.String())
				}
				waitForAssistedProviderRequests(t, session, 3)
			}
			if receipt, err := server.tasks.LatestBlackboardConclusion(created.ID); err != nil || receipt == nil || receipt.InternalState != task.BlackboardConclusionReceiptAwaitingResult {
				t.Fatalf("pre-restart %s receipt = %#v, %v", generation, receipt, err)
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := NewServer(Config{
				DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
				SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			found := waitForBlackboardConclusionState(t, reopened, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
			if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
				t.Fatalf("restarted %s conclusion = %#v", generation, found.BlackboardConclusion)
			}
		})
	}
}

func TestCleanBlackboardConclusionCheckpointSurvivesDaemonRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	requestID := session.LastRequests()[0].RequestID
	for index, observation := range []runtime.ProviderSessionObservation{
		{ToolName: "shell", Status: "failed"},
		{ToolName: "mcp__pentest__blackboard_change", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationChange},
	} {
		observation.Kind = runtime.ProviderSessionObservationToolResult
		observation.RequestID = requestID
		observation.ProviderTurnID = "work-turn-clean"
		observation.ToolCallID = fmt.Sprintf("tool-%d", index+1)
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
		ProviderTurnID: "work-turn-clean", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	before := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if before.BlackboardConclusion.SourceTurnID != "work-turn-clean" || before.BlackboardConclusion.SourceWorkWatermark != 1 ||
		before.BlackboardConclusion.SemanticPersistenceWatermark != 1 || len(session.LastRequests()) != 1 {
		t.Fatalf("clean checkpoint before restart = %#v; requests=%d", before.BlackboardConclusion, len(session.LastRequests()))
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after := waitForBlackboardConclusionState(t, reopened, projectID, created.ID, task.BlackboardConclusionStateClean)
	if after.BlackboardConclusion.SourceTurnID != "work-turn-clean" || after.BlackboardConclusion.SourceWorkWatermark != 1 ||
		after.BlackboardConclusion.SemanticPersistenceWatermark != 1 {
		t.Fatalf("clean checkpoint after restart = %#v", after.BlackboardConclusion)
	}
}

func TestAssistedConclusionIgnoresControlTurnsAndTrustedBlackboardTools(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	// Recording the request precedes completion of the asynchronous launch
	// dispatch. Replaying the same idempotency key waits for that dispatch, so
	// the control Turn below cannot race the provider's active control call.
	if _, err := session.SendTurn(context.Background(), workRequest, nil); err != nil {
		t.Fatal(err)
	}
	workRequestID := workRequest.RequestID
	controlResult, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "control-request-1", Message: "reconcile", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: "control-request-1", ProviderTurnID: controlResult.ProviderTurnID, ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "control-request-1", ProviderTurnID: controlResult.ProviderTurnID, Status: "completed"},
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequestID, ProviderTurnID: "work-turn-1", ToolCallID: "tool-2", ToolName: "mcp__pentest__blackboard_change", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationChange},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequestID, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion && event.Payload["phase"] != "persistence_current" {
			t.Fatalf("control or trusted Blackboard Tool created semantic debt: %#v", event)
		}
	}
}

func TestAssistedConclusionTracksSemanticPersistenceInToolResultOrder(t *testing.T) {
	tests := []struct {
		name        string
		results     []runtime.ProviderSessionObservation
		wantDebt    bool
		wantWork    int
		wantPersist int
	}{
		{
			name: "successful semantic mutations clear earlier work",
			results: []runtime.ProviderSessionObservation{
				{ToolName: "shell", Status: "failed"},
				{ToolName: "mcp__pentest__blackboard_change", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationChange},
				{ToolName: "mcp__pentest__blackboard_checkpoint_attempt", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationCheckpointAttempt},
				{ToolName: "mcp__pentest__blackboard_finish", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationFinish},
			},
			wantWork: 1, wantPersist: 1,
		},
		{
			name: "reads evidence retention and failed mutations do not clear work",
			results: []runtime.ProviderSessionObservation{
				{ToolName: "shell", Status: "succeeded"},
				{ToolName: "mcp__pentest__blackboard_read", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationRead},
				{ToolName: "mcp__pentest__blackboard_history", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationHistory},
				{ToolName: "mcp__pentest__blackboard_retain_evidence", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationRetainEvidence},
				{ToolName: "mcp__pentest__blackboard_change", Status: "failed", BlackboardOperation: runtime.BlackboardOperationChange},
				{ToolName: "mcp__pentest__blackboard_checkpoint_attempt", Status: "failed", BlackboardOperation: runtime.BlackboardOperationCheckpointAttempt},
				{ToolName: "mcp__pentest__blackboard_finish", Status: "failed", BlackboardOperation: runtime.BlackboardOperationFinish},
			},
			wantDebt: true, wantWork: 1, wantPersist: 0,
		},
		{
			name: "later failed non Blackboard result restores debt",
			results: []runtime.ProviderSessionObservation{
				{ToolName: "http", Status: "succeeded"},
				{ToolName: "mcp__pentest__blackboard_change", Status: "succeeded", BlackboardOperation: runtime.BlackboardOperationChange},
				{ToolName: "shell", Status: "failed"},
			},
			wantDebt: true, wantWork: 2, wantPersist: 1,
		},
		{
			name: "provider qualified spoofed server name never covers work",
			results: []runtime.ProviderSessionObservation{
				{ToolName: "shell", Status: "succeeded"},
				{ToolName: "mcp__evil__blackboard_change", Status: "succeeded"},
				{ToolName: "blackboard_finish", Status: "succeeded"},
			},
			wantDebt: true, wantWork: 3, wantPersist: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "assisted")
			waitForAssistedProviderRequests(t, session, 1)
			requestID := session.LastRequests()[0].RequestID
			for index, result := range test.results {
				result.Kind = runtime.ProviderSessionObservationToolResult
				result.RequestID = requestID
				result.ProviderTurnID = "work-turn-watermark"
				result.ToolCallID = fmt.Sprintf("tool-%d", index+1)
				if err := session.EmitObservation(result); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
				ProviderTurnID: "work-turn-watermark", Status: "completed",
			}); err != nil {
				t.Fatal(err)
			}

			if !test.wantDebt {
				found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
				if len(session.LastRequests()) != 1 || found.BlackboardConclusion.SourceTurnID != "work-turn-watermark" ||
					found.BlackboardConclusion.SourceWorkWatermark != test.wantWork ||
					found.BlackboardConclusion.SemanticPersistenceWatermark != test.wantPersist {
					t.Fatalf("persisted Work Turn scheduled conclusion: requests=%d view=%#v", len(session.LastRequests()), found.BlackboardConclusion)
				}
				return
			}

			waitForAssistedProviderRequests(t, session, 2)
			receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
			if err != nil || receipt == nil {
				t.Fatalf("load semantic-debt receipt: %#v, %v", receipt, err)
			}
			if receipt.SourceWorkWatermark != test.wantWork || receipt.SemanticPersistenceWatermark != test.wantPersist {
				t.Fatalf("receipt watermarks = work %d persistence %d, want %d/%d", receipt.SourceWorkWatermark, receipt.SemanticPersistenceWatermark, test.wantWork, test.wantPersist)
			}
		})
	}
}

func TestAssistedConclusionSchedulesOnlyAtCompletedWorkTurnBoundary(t *testing.T) {
	for _, terminalStatus := range []string{"failed", "interrupted"} {
		t.Run(terminalStatus, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "assisted")
			waitForAssistedProviderRequests(t, session, 1)
			requestID := session.LastRequests()[0].RequestID
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
				ProviderTurnID: "work-turn-boundary", ToolCallID: "tool-1", ToolName: "shell", Status: "failed",
			}); err != nil {
				t.Fatal(err)
			}
			if len(session.LastRequests()) != 1 {
				t.Fatalf("Tool Result scheduled before Turn boundary: requests=%d", len(session.LastRequests()))
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
				ProviderTurnID: "work-turn-boundary", Status: terminalStatus,
			}); err != nil {
				t.Fatal(err)
			}
			// Non-completed Work Turns with non-Blackboard debt surface durable attention
			// without dispatching an automatic Conclude Turn.
			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
			if len(session.LastRequests()) != 1 ||
				found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
				t.Fatalf("%s Work Turn mis-handled conclusion: requests=%d view=%#v", terminalStatus, len(session.LastRequests()), found.BlackboardConclusion)
			}
		})
	}
}

// #192: only the exact canonical Project Interface tool identity
// (mcp__pentest__<operation>) is trusted for semantic-debt accounting. A bare
// display name or a near-match from another server is never trusted.
func TestAssistedConclusionTrustsOnlyTheCanonicalProjectInterfaceToolIdentity(t *testing.T) {
	trusted := []struct {
		name string
		op   runtime.BlackboardOperation
	}{
		{name: "mcp__pentest__blackboard_change", op: runtime.BlackboardOperationChange},
		{name: "mcp__pentest__blackboard_read", op: runtime.BlackboardOperationRead},
		{name: "mcp__pentest__blackboard_history", op: runtime.BlackboardOperationHistory},
		{name: "mcp__pentest__blackboard_retain_evidence", op: runtime.BlackboardOperationRetainEvidence},
		{name: "mcp__pentest__blackboard_checkpoint_attempt", op: runtime.BlackboardOperationCheckpointAttempt},
		{name: "mcp__pentest__blackboard_finish", op: runtime.BlackboardOperationFinish},
	}
	for _, tool := range trusted {
		t.Run(tool.name, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "assisted")
			waitForAssistedProviderRequests(t, session, 1)
			requestID := session.LastRequests()[0].RequestID
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
				ProviderTurnID: "work-turn-trusted", ToolCallID: "tool-1", ToolName: tool.name,
				Status: "succeeded", BlackboardOperation: tool.op,
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
				ProviderTurnID: "work-turn-trusted", Status: "completed",
			}); err != nil {
				t.Fatal(err)
			}
			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
			if len(session.LastRequests()) != 1 || found.BlackboardConclusion.SourceTurnID != "work-turn-trusted" ||
				found.BlackboardConclusion.SourceWorkWatermark != 0 || found.BlackboardConclusion.SemanticPersistenceWatermark != 0 {
				t.Fatalf("trusted tool %q checkpoint = %#v; requests=%d", tool.name, found.BlackboardConclusion, len(session.LastRequests()))
			}
		})
	}

	for _, toolName := range []string{
		"blackboard_change", "blackboard_changes", "mcp__evil__blackboard_change", "mcp__pentest__blackboard_changes",
	} {
		t.Run(toolName, func(t *testing.T) {
			server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
			created := launchConclusionTask(t, server, projectID, profileID, "assisted")
			waitForAssistedProviderRequests(t, session, 1)
			requestID := session.LastRequests()[0].RequestID
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
				ProviderTurnID: "work-turn-untrusted", ToolCallID: "tool-1", ToolName: toolName, Status: "succeeded",
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.EmitObservation(runtime.ProviderSessionObservation{
				Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
				ProviderTurnID: "work-turn-untrusted", Status: "completed",
			}); err != nil {
				t.Fatal(err)
			}
			waitForAssistedProviderRequests(t, session, 2)
			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
			if found.BlackboardConclusion.SourceWorkWatermark != 1 || found.BlackboardConclusion.SemanticPersistenceWatermark != 0 {
				t.Fatalf("near-match tool %q checkpoint = %#v", toolName, found.BlackboardConclusion)
			}
		})
	}
}

// A fabricated canonical identity on an unregistered tool name is rejected at
// the bounded observation boundary and can never advance semantic persistence.
func TestAssistedConclusionFabricatedCanonicalIdentityIsRejected(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	requestID := session.LastRequests()[0].RequestID
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
		ProviderTurnID: "work-turn-spoofed", ToolCallID: "tool-1",
		ToolName: "mcp__evil__blackboard_change", Status: "succeeded",
		BlackboardOperation: runtime.BlackboardOperationChange,
	}); !errors.Is(err, runtime.ErrInvalidProviderSessionObservation) {
		t.Fatalf("fabricated identity observation error = %v", err)
	}
	if len(session.LastRequests()) != 1 {
		t.Fatalf("rejected observation dispatched a request: %d", len(session.LastRequests()))
	}
}

func TestAssistedConclusionPromptsOnceAfterManyResultsAndCompletedBoundary(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	requestID := session.LastRequests()[0].RequestID
	for index := 0; index < 100; index++ {
		if err := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind: runtime.ProviderSessionObservationToolResult, RequestID: requestID,
			ProviderTurnID: "work-turn-many", ToolCallID: fmt.Sprintf("tool-%d", index), ToolName: "shell", Status: "succeeded",
		}); err != nil {
			t.Fatal(err)
		}
		if len(session.LastRequests()) != 1 {
			t.Fatalf("result %d triggered a mid-Turn prompt: requests=%d", index, len(session.LastRequests()))
		}
	}
	completed := runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: requestID,
		ProviderTurnID: "work-turn-many", Status: "completed",
	}
	if err := session.EmitObservation(completed); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if found.BlackboardConclusion.SourceWorkWatermark != 100 || found.BlackboardConclusion.SemanticPersistenceWatermark != 0 {
		t.Fatalf("many-result checkpoint = %#v", found.BlackboardConclusion)
	}
	if err := session.EmitObservation(completed); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if len(session.LastRequests()) != 2 {
		t.Fatalf("duplicate completed prompts = %d, want work plus one Conclude Turn", len(session.LastRequests()))
	}
}

func TestAssistedConclusionInvalidResultDispatchesOneBoundedRepairTurn(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]

	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-repair", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-repair", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	concludeRequest := session.LastRequests()[1]

	if err := emitAttemptResultAndComplete(t, session, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)); err == nil {
		t.Fatal("invalid semantic result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, session, 3)

	requests := session.LastRequests()
	repairRequest := requests[2]
	if repairRequest.TurnKind != runtime.RuntimeTurnKindControl {
		t.Fatalf("repair Turn kind = %q", repairRequest.TurnKind)
	}
	if repairRequest.RequestID == concludeRequest.RequestID || !strings.Contains(repairRequest.RequestID, "repair") {
		t.Fatalf("repair request ID = %q after Conclude %q", repairRequest.RequestID, concludeRequest.RequestID)
	}
	if repairRequest.ModelProviderID != concludeRequest.ModelProviderID || repairRequest.Model != concludeRequest.Model ||
		repairRequest.RequestedReasoningEffort != concludeRequest.RequestedReasoningEffort {
		t.Fatalf("repair selection = %#v, Conclude selection = %#v", repairRequest, concludeRequest)
	}
	directive := strings.ToLower(repairRequest.Message)
	for _, required := range []string{"previous", "invalid", "runtime-attempt-result/v1", "do not call tools"} {
		if !strings.Contains(directive, required) {
			t.Fatalf("repair directive missing %q: %s", required, repairRequest.Message)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if found.Status != task.StatusRunning || found.RuntimeActivity.Liveness != "live" {
		t.Fatalf("repair changed Task lifecycle/activity: status=%q activity=%#v", found.Status, found.RuntimeActivity)
	}
}

func TestAssistedConclusionSecondInvalidResultRequiresActionWithoutAnotherTurn(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-exhausted", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-exhausted", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	invalid := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid Conclude result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid repair result unexpectedly decoded")
	}

	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRepairExhausted {
		t.Fatalf("action-required code = %q", found.BlackboardConclusion.ErrorCode)
	}
	if len(session.LastRequests()) != 3 {
		t.Fatalf("automatic provider requests = %d, want work, Conclude, repair", len(session.LastRequests()))
	}
	if found.Status != task.StatusRunning || found.RuntimeActivity.Liveness != "live" {
		t.Fatalf("action-required changed Task lifecycle/activity: status=%q activity=%#v", found.Status, found.RuntimeActivity)
	}
	manualRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer/queue", bytes.NewBufferString(`{
		"directive":"I will provide recovery context manually."
	}`))
	manualRequest.Header.Set("Content-Type", "application/json")
	manualResponse := httptest.NewRecorder()
	server.ServeHTTP(manualResponse, manualRequest)
	if manualResponse.Code != http.StatusOK {
		t.Fatalf("manual recovery message status = %d body %s", manualResponse.Code, manualResponse.Body.String())
	}
	if len(session.LastRequests()) != 3 {
		t.Fatalf("queued manual recovery unexpectedly dispatched: requests=%d", len(session.LastRequests()))
	}

	transcriptRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/transcript", nil)
	transcriptResponse := httptest.NewRecorder()
	server.ServeHTTP(transcriptResponse, transcriptRequest)
	for _, hidden := range []string{"unexpected", "previous Blackboard conclusion result", "runtime-attempt-result/v1"} {
		if strings.Contains(transcriptResponse.Body.String(), hidden) {
			t.Fatalf("hidden repair content leaked into Conversation: %s", transcriptResponse.Body.String())
		}
	}
}

func TestAssistedConclusionControlToolResultRequiresActionWithoutRecursion(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-forbidden", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-forbidden", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)

	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "control-tool-1", ToolName: "shell",
	}); err != nil {
		t.Fatal(err)
	}

	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden {
		t.Fatalf("forbidden control tool code = %q", found.BlackboardConclusion.ErrorCode)
	}
	if len(session.LastRequests()) != 2 {
		t.Fatalf("forbidden control tool dispatched recursively: requests=%d", len(session.LastRequests()))
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, ToolCallID: "control-tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion && event.Payload["phase"] == "pending_detected" {
			receipts++
		}
		if event.Payload["tool_name"] != nil || event.Payload["output"] != nil {
			t.Fatalf("forbidden tool detail leaked into Task Event: %#v", event)
		}
	}
	if receipts != 1 {
		t.Fatalf("pending receipts = %d, want only source Work Turn receipt", receipts)
	}
}

func TestAssistedConclusionControlTurnCannotUseTrustedBlackboardTool(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-trusted-control", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-trusted-control", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	before, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "control-blackboard-1", ToolName: "blackboard_change",
	}); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || len(session.LastRequests()) != 2 {
		t.Fatalf("trusted control tool result = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
	after, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("control Blackboard tool mutated revision: before=%d after=%d", before.Revision, after.Revision)
	}
}

func TestAssistedConclusionForbiddenFailureDominatesInvalidInEitherCallbackOrder(t *testing.T) {
	for _, test := range []struct {
		name            string
		invalidThenTool bool
	}{
		{name: "invalid then forbidden tool", invalidThenTool: true},
		{name: "forbidden tool then invalid", invalidThenTool: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
			waitForProviderTaskControl(t, server, created.ID)
			invalid := func() {
				if err := session.EmitAttemptResult([]byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)); err == nil {
					t.Error("invalid result unexpectedly decoded")
				}
			}
			forbidden := func() {
				if err := session.EmitObservation(runtime.ProviderSessionObservation{
					Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "forbidden-order", ToolName: "shell",
				}); err != nil {
					t.Error(err)
				}
			}
			if test.invalidThenTool {
				invalid()
				forbidden()
			} else {
				forbidden()
				invalid()
			}
			server.releaseProviderTaskControl(created.ID)

			found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
			if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || len(session.LastRequests()) != 2 {
				t.Fatalf("ordered failures = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
			}
		})
	}
}

func TestAssistedConclusionForbiddenDominatesAfterInvalidDrainOpportunity(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	if err := session.EmitAttemptResult([]byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)); err == nil {
		t.Fatal("invalid result unexpectedly decoded")
	}
	server.providerControlWG.Wait()
	if len(session.LastRequests()) != 2 {
		t.Fatalf("invalid candidate dispatched before terminal boundary: requests=%d", len(session.LastRequests()))
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "late-forbidden", ToolName: "shell",
	}); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || len(session.LastRequests()) != 2 {
		t.Fatalf("late forbidden arbitration = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionForbiddenSuppressesValidResultBeforeTerminal(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	requestID := session.LastRequests()[1].RequestID
	before, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:suppressed-valid","create":true,"summary":"Must be suppressed by forbidden tool use.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:suppressed-valid","create_objective":{"objective":"Must not be applied."}}],
		"produced_targets":[]
	}`)
	if err := session.EmitAttemptResult(valid); err != nil {
		t.Fatal(err)
	}
	server.providerControlWG.Wait()
	if found := getConclusionTask(t, server, projectID, created.ID); found.BlackboardConclusion.State != task.BlackboardConclusionStateConcluding {
		t.Fatalf("valid result applied before terminal: %#v", found.BlackboardConclusion)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "forbidden-after-valid", ToolName: "shell",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	after, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || after.Revision != before.Revision || len(session.LastRequests()) != 2 {
		t.Fatalf("forbidden/valid arbitration = %#v revisions=%d/%d requests=%d", found.BlackboardConclusion, before.Revision, after.Revision, len(session.LastRequests()))
	}
	if server.blackboardConclusions.HasRequest(created.ID, requestID) {
		t.Fatalf("resolved callback state retained for request %s", requestID)
	}
}

func TestAssistedConclusionAppliesValidResultAfterTerminalArrivesFirst(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	server.providerControlWG.Wait()
	if found := getConclusionTask(t, server, projectID, created.ID); found.BlackboardConclusion.State != task.BlackboardConclusionStateConcluding {
		t.Fatalf("terminal without result changed conclusion: %#v", found.BlackboardConclusion)
	}
	valid := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:terminal-first","create":true,"summary":"Apply after the canonical terminal arrived first.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:terminal-first","create_objective":{"objective":"Accept either canonical callback order."}}],
		"produced_targets":[]
	}`)
	if err := session.EmitAttemptResult(valid); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || found.BlackboardConclusion.ErrorCode != "" {
		t.Fatalf("terminal-first conclusion = %#v", found.BlackboardConclusion)
	}
}

func TestAssistedConclusionMismatchedDuplicateCannotDisplaceCanonicalValidResult(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	if !server.acquireProviderTaskControl(created.ID) {
		t.Fatal("acquire provider task control")
	}
	canonicalRaw := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:canonical-first","create":true,"summary":"Canonical first result.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:canonical-first","create_objective":{"objective":"Keep the canonical first callback."}}],
		"produced_targets":[]
	}`)
	if err := session.EmitAttemptResult(canonicalRaw); err != nil {
		t.Fatal(err)
	}
	duplicate, err := blackboardconclusion.Decode([]byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:spoofed-second","create":true,"summary":"Spoofed second result.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:spoofed-second","create_objective":{"objective":"Must not displace canonical callback."}}],
		"produced_targets":[]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request := session.LastRequests()[1]
	server.acceptBlackboardConclusionResult(created.ID, runtime.ProviderSessionAttemptResult{
		RequestID: request.RequestID, SessionID: session.SessionID(), ProviderTurnID: "spoofed-provider-turn", Validated: duplicate,
	})
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	server.releaseProviderTaskControl(created.ID)

	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	snapshot, err := server.blackboardV2.RuntimeSnapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("objective:canonical-first")) || bytes.Contains(encoded, []byte("objective:spoofed-second")) {
		t.Fatalf("Blackboard accepted displaced callback: %s", encoded)
	}
}

func TestAssistedConclusionWrongBaseRevisionUsesBoundedRepairBudget(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	wrongBase := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":99,
		"attempt":{"key":"attempt:wrong-base","create":true,"summary":"Wrong base revision.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:wrong-base","create_objective":{"objective":"Reject wrong base revision."}}],
		"produced_targets":[]
	}`)
	if err := emitAttemptResultAndComplete(t, session, wrongBase); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, wrongBase); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRepairExhausted || len(session.LastRequests()) != 3 {
		t.Fatalf("wrong-base repair budget = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionWrongBaseRevisionAfterExplicitRetryRequiresAction(t *testing.T) {
	server, projectID, created, session := prepareAssistedConclusionAwaiting(t)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "force-retry", ToolName: "shell",
	}); err != nil {
		t.Fatal(err)
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	retry := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+projectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, retry)
	retry.Header.Set("Idempotency-Key", "wrong-base-retry")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, retry)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body %s", response.Code, response.Body.String())
	}
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":99,
		"attempt":{"key":"attempt:retry-wrong-base","create":true,"summary":"Retry wrong base.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:retry-wrong-base","create_objective":{"objective":"Reject retry wrong base."}}],
		"produced_targets":[]
	}`)); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if len(session.LastRequests()) != 3 || found.BlackboardConclusion.ErrorCode == "" {
		t.Fatalf("wrong-base retry = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionRepairDispatchFailureBecomesActionRequired(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &failingConclusionDispatchSession{FakeProviderSession: fake, requestKind: "repair"}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-dispatch-failure", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-dispatch-failure", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	if err := emitAttemptResultAndComplete(t, session, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)); err == nil {
		t.Fatal("invalid result unexpectedly decoded")
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired || len(session.LastRequests()) != 2 {
		t.Fatalf("dispatch recovery = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
	recoveryEvents := waitForAssistedConclusionEventCount(t, server, projectID, created.ID, func(event task.Event) bool {
		return event.Kind == task.EventKindBlackboardConclusion && event.Payload["phase"] == "action_required" && event.Payload["reason"] == "dispatch_recovery"
	})
	if recoveryEvents != 1 || found.Status != task.StatusRunning {
		t.Fatalf("dispatch recovery events=%d task status=%q", recoveryEvents, found.Status)
	}
}

func TestAssistedConclusionRetryDispatchFailureBecomesActionRequired(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			return &failingConclusionDispatchSession{FakeProviderSession: fake, requestKind: "retry"}
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-retry-failure", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-retry-failure", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "force-retry-failure", ToolName: "shell",
	}); err != nil {
		t.Fatal(err)
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	retry := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+projectID+"/tasks/"+created.ID+"/blackboard-conclusion/retry", bytes.NewBufferString(`{}`))
	authorizeOperatorTestRequest(server, retry)
	retry.Header.Set("Idempotency-Key", "dispatch-failure-retry")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, retry)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body %s", response.Code, response.Body.String())
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	if found.BlackboardConclusion.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired || len(session.LastRequests()) != 2 {
		t.Fatalf("retry dispatch recovery = %#v requests=%d", found.BlackboardConclusion, len(session.LastRequests()))
	}
}

func TestAssistedConclusionRetryIsIdempotentAndValidResultRecovers(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-retry", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-retry", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	invalid := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid Conclude result unexpectedly decoded")
	}
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid repair result unexpectedly decoded")
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)

	retryURL := "/api/projects/" + projectID + "/tasks/" + created.ID + "/blackboard-conclusion/retry"
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, retryURL, bytes.NewBufferString(`{}`))
		authorizeOperatorTestRequest(server, request)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "operator-retry-1")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("retry attempt %d status = %d body %s", attempt+1, response.Code, response.Body.String())
		}
	}
	waitForAssistedProviderRequests(t, session, 4)
	if len(session.LastRequests()) != 4 {
		t.Fatalf("duplicate Retry provider requests = %d, want one retry", len(session.LastRequests()))
	}
	retryRequest := session.LastRequests()[3]
	if retryRequest.TurnKind != runtime.RuntimeTurnKindControl || !strings.Contains(retryRequest.RequestID, "retry") {
		t.Fatalf("retry request = %#v", retryRequest)
	}

	receipt, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || receipt == nil || receipt.BaseRevision == nil {
		t.Fatalf("load retry receipt: %#v, %v", receipt, err)
	}
	result := fmt.Sprintf(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":%d,
		"attempt":{"key":"attempt:retry","create":true,"summary":"Retried semantic conclusion.","outcome":"inconclusive"},
		"tested_targets":[{"key":"objective:retry","create_objective":{"objective":"Retain retry result."}}],
		"produced_targets":[]
	}`, *receipt.BaseRevision)
	if err := emitAttemptResultAndComplete(t, session, []byte(result)); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil || found.BlackboardConclusion.ErrorCode != "" {
		t.Fatalf("recovered conclusion view = %#v", found.BlackboardConclusion)
	}
}

func TestBlackboardConclusionEligibilityUsesInjectedClockAndCancellation(t *testing.T) {
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	eligible := now.Add(-time.Second)
	if err := waitForBlackboardConclusionEligibility(context.Background(), &eligible, func() time.Time { return now }); err != nil {
		t.Fatalf("already eligible: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eligible = now.Add(time.Hour)
	if err := waitForBlackboardConclusionEligibility(ctx, &eligible, func() time.Time { return now }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled eligibility wait = %v", err)
	}
}

func TestBlackboardConclusionRegenerationRecognizesOnlyBaseRevisionConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "exact wrapped conflict", err: fmt.Errorf("apply: %w", &blackboardv2.Error{Code: "version_conflict", Path: "base_revision"}), want: true},
		{name: "record version conflict", err: &blackboardv2.Error{Code: "version_conflict", Path: "changes[0].version"}},
		{name: "different semantic error", err: &blackboardv2.Error{Code: "key_conflict", Path: "changes[0].key"}},
		{name: "matching text is not typed", err: errors.New("version_conflict at base_revision")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isBlackboardConclusionBaseRevisionConflict(test.err); got != test.want {
				t.Fatalf("isBlackboardConclusionBaseRevisionConflict(%#v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func launchConclusionTask(t *testing.T, server *Server, projectID, profileID, mode string) task.Task {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest","goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"`+mode+`"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create %s Task status = %d body %s", mode, response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func advanceProjectFromPeerContinuation(t *testing.T, server *Server, projectID, profileID string) int {
	t.Helper()
	peer, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectID, Type: task.TypePentest, Goal: "advance the shared Project Blackboard", RuntimeProfileID: profileID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: projectID, TaskID: peer.ID, RuntimeProfileID: profileID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.blackboardV2.ApplyForContinuation(context.Background(), projectID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "peer-version-conflict-advance-" + peer.ID,
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:peer-progress-" + peer.ID, Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Peer progress", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Revision
}

func assistedAttemptResultJSON(baseRevision int, attemptKey, objectiveKey string) string {
	return fmt.Sprintf(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":%d,
		"attempt":{"key":%q,"create":true,"summary":"Tested the target after synchronizing concurrent Project progress.","outcome":"inconclusive"},
		"tested_targets":[{"key":%q,"create_objective":{"objective":"Determine whether the synchronized target is vulnerable."}}],
		"produced_targets":[]
	}`, baseRevision, attemptKey, objectiveKey)
}

type assistedConclusionSessionFactory struct {
	session runtime.ProviderSession
	adapter *runtime.ProviderSessionRunAdapter
	support bool
}

func (factory *assistedConclusionSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if binder, ok := factory.session.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(request.Continuation.ID); err != nil {
			return ProviderSessionBinding{}, err
		}
	}
	factory.adapter.BindContinuation(request.Continuation.ID)
	return ProviderSessionBinding{Session: factory.session, Adapter: factory.adapter}, nil
}

func (factory *assistedConclusionSessionFactory) SupportsAssistedConclusion(provider runtimeprofile.Provider) bool {
	return factory.support && provider == runtimeprofile.ProviderCodex
}

func prepareAssistedConclusionAwaiting(t *testing.T) (*Server, string, task.Task, *runtime.FakeProviderSession) {
	t.Helper()
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-callback-order", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID,
		ProviderTurnID: "work-turn-callback-order", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	waitForAssistedProviderRequests(t, session, 2)
	server.providerControlWG.Wait()
	return server, projectID, created, session
}

func emitAttemptResultAndComplete(t *testing.T, session *runtime.FakeProviderSession, raw []byte) error {
	t.Helper()
	resultErr := session.EmitAttemptResult(raw)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	return resultErr
}

func newAssistedConclusionFixture(t *testing.T, support bool) (*Server, string, string, *runtime.FakeProviderSession) {
	return newAssistedConclusionFixtureWithCapabilities(t, support, support)
}

func newAssistedConclusionFixtureWithCapabilities(t *testing.T, reporterSupport, sessionSupport bool) (*Server, string, string, *runtime.FakeProviderSession) {
	t.Helper()
	return newAssistedConclusionFixtureAt(t, t.TempDir(), reporterSupport, sessionSupport)
}

func newAssistedConclusionFixtureAt(t *testing.T, root string, reporterSupport, sessionSupport bool) (*Server, string, string, *runtime.FakeProviderSession) {
	return newAssistedConclusionFixtureAtWithDecorator(t, root, reporterSupport, sessionSupport, nil)
}

func newAssistedConclusionFixtureAtWithDecorator(t *testing.T, root string, reporterSupport, sessionSupport bool, decorate func(*runtime.FakeProviderSession) runtime.ProviderSession) (*Server, string, string, *runtime.FakeProviderSession) {
	t.Helper()
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "assisted-session", ActiveTurnID: "work-turn-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: sessionSupport,
		},
	})
	var session runtime.ProviderSession = fake
	if decorate != nil {
		session = decorate(fake)
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))
	factory := &assistedConclusionSessionFactory{session: session, adapter: adapter, support: reporterSupport}
	dockerCLI := filepath.Join(root, "fake-docker")
	if err := writeExecutable(dockerCLI, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true, ProviderSessionFactory: factory,
		ContainerCLI: dockerCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	binary := filepath.Join(root, "codex")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test", BinaryPath: binary, SandboxImage: "cyberpenda:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, createdProject.ID, profile.ID, fake
}

// assistedConclusionWaitBudget is the shared poll ceiling for the
// assisted-conclusion wait helpers below. The conclusion chain
// (observe → pending → dispatch → conclude turn → drain → validate → apply)
// is multi-hop, asynchronous, and traverses the serialized provider-control
// queue, so its latency is bounded by scheduler availability, not by any
// fixed work cost. When goroutines get scheduled the chain settles in tens to
// hundreds of milliseconds; the failures seen in #228 and #244 were loaded-CI
// scheduler stalls exceeding first a 5s then a 10s wall-clock budget (the
// last flake blew 10s at ~11.2s). Because the chain is correct and fast when
// scheduled, the helpers assert on reaching the durable state, not on a tight
// latency bound, and use a generous ceiling that absorbs pathological CI
// scheduling while still failing a genuinely stuck chain. The ceiling is not
// a performance assertion; local runs return as soon as the state appears.
const assistedConclusionWaitBudget = 60 * time.Second

func waitForAssistedProviderRequests(t *testing.T, session *runtime.FakeProviderSession, count int) {
	t.Helper()
	// See assistedConclusionWaitBudget: assert dispatch order, not latency.
	deadline := time.Now().Add(assistedConclusionWaitBudget)
	for time.Now().Before(deadline) {
		if len(session.LastRequests()) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("provider requests = %d, want at least %d", len(session.LastRequests()), count)
}

func waitForBlackboardConclusionState(t *testing.T, server *Server, projectID, taskID string, state task.BlackboardConclusionState) task.Task {
	t.Helper()
	// See assistedConclusionWaitBudget: assert reaching the durable state.
	deadline := time.Now().Add(assistedConclusionWaitBudget)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var found task.Task
			if err := json.NewDecoder(response.Body).Decode(&found); err == nil && found.BlackboardConclusion.State == state {
				return found
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach Blackboard conclusion state %q", taskID, state)
	return task.Task{}
}

// waitForBlackboardConclusionReceiptState waits until the newest durable
// Blackboard Conclusion obligation reaches the given internal receipt state.
// The provider-request signal can fire before the daemon durably persists
// that state, so tests that act on a receipt must wait for it here instead of
// assuming request ordering implies receipt persistence.
func waitForBlackboardConclusionReceiptState(t *testing.T, server *Server, taskID string, state task.BlackboardConclusionReceiptState) *task.BlackboardConclusionReceipt {
	t.Helper()
	// See assistedConclusionWaitBudget: assert reaching the durable state.
	deadline := time.Now().Add(assistedConclusionWaitBudget)
	for time.Now().Before(deadline) {
		receipt, err := server.tasks.LatestBlackboardConclusion(taskID)
		if err != nil {
			t.Fatalf("load Blackboard conclusion receipt: %v", err)
		}
		if receipt != nil && receipt.InternalState == state {
			return receipt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach Blackboard conclusion receipt state %q", taskID, state)
	return nil
}

func assistedTaskEvents(t *testing.T, server *Server, projectID, taskID string) []task.Event {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/events", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get Task events status = %d body %s", response.Code, response.Body.String())
	}
	var body struct {
		Events []task.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Task events: %v", err)
	}
	return body.Events
}

func waitForAssistedConclusionEventCount(t *testing.T, server *Server, projectID, taskID string, match func(task.Event) bool) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, event := range assistedTaskEvents(t, server, projectID, taskID) {
			if match(event) {
				count++
			}
		}
		if count > 0 {
			return count
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0
}
