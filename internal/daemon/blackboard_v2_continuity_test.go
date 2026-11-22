package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestCodexV2ContinuationLaunchAndRestartConformanceKeepsSnapshotRereadable(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "codex-v2.db")
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	createdProject, err := server.projects.Create("Codex v2", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Project: %v", err)
	}
	if _, err := server.db.Exec(`UPDATE projects SET blackboard_protocol='legacy' WHERE id=?`, createdProject.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "inspect example.test", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Task: %v", err)
	}
	_, err = server.blackboardV2.Apply(context.Background(), createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-codex-conformance",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:conformance", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Conformance host", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("seed Blackboard v2: %v", err)
	}
	want, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		_ = server.Close()
		t.Fatalf("project expected Snapshot: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
	if err != nil {
		_ = server.Close()
		t.Fatalf("build Codex launch plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		_ = server.Close()
		t.Fatalf("prepare Codex v2 Continuation: %v", err)
	}

	workingPath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", "blackboard.json")
	onDisk, err := os.ReadFile(workingPath)
	if err != nil {
		_ = server.Close()
		t.Fatalf("reread Working Snapshot after launch: %v", err)
	}
	if !bytes.Equal(onDisk, want.Bytes) {
		_ = server.Close()
		t.Fatalf("Codex Working Snapshot differs from exact pin\ngot=%s\nwant=%s", onDisk, want.Bytes)
	}
	header := blackboardv2.RenderLaunchHeader(blackboardv2.LaunchHeader{
		Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		Schema: "runtime-blackboard/v2", Revision: want.Snapshot.Revision,
	})
	if !strings.HasPrefix(bound.LaunchGoal, header+"\n\nTASK GOAL:\n") {
		_ = server.Close()
		t.Fatalf("Codex launch content does not start with compact header: %s", bound.LaunchGoal)
	}
	for _, forbidden := range []string{
		createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
		"http://", "https://", "hash", "bytes", "tokens", "digest", "Trusted tools:", string(want.Bytes),
	} {
		if forbidden != "" && strings.Contains(bound.LaunchGoal, forbidden) {
			_ = server.Close()
			t.Fatalf("Codex launch content leaked %q: %s", forbidden, bound.LaunchGoal)
		}
	}
	agents, err := os.ReadFile(filepath.Join(runtimeRoot, createdTask.ID, "workdir", "AGENTS.md"))
	if err != nil {
		_ = server.Close()
		t.Fatalf("read persistent Codex checklist: %v", err)
	}
	if strings.Count(string(agents)+bound.LaunchGoal, blackboardv2.CodexChecklist()) != 1 {
		_ = server.Close()
		t.Fatalf("checklist is not projected exactly once\nAGENTS=%s\nLAUNCH=%s", agents, bound.LaunchGoal)
	}

	if err := os.Remove(workingPath); err != nil {
		_ = server.Close()
		t.Fatalf("remove Working Snapshot before restart: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close daemon: %v", err)
	}
	restarted, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	recovered, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("reread Working Snapshot after restart/context compaction: %v", err)
	}
	if !bytes.Equal(recovered, want.Bytes) {
		t.Fatalf("restart changed exact reread bytes\ngot=%s\nwant=%s", recovered, want.Bytes)
	}
}

func TestBlackboardV2FinishThenResumeUsesFreshPinAndOnlyUnconsumedHarnessSteering(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "finish-resume.db"),
		RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Finish resume", "", project.Scope{Domains: []string{"resume.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := server.db.Exec(`UPDATE projects SET blackboard_protocol='legacy' WHERE id=?`, createdProject.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex resume", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "inspect resume.test", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
	if err != nil {
		t.Fatalf("build first plan: %v", err)
	}
	first, _, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare first Continuation: %v", err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(first.ID, task.StatusRunning); err != nil {
		t.Fatalf("start first Continuation: %v", err)
	}
	if _, err := server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finish-resume-state",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:finish-resume", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Fresh resume state", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("write before Finish: %v", err)
	}
	if _, err := server.blackboardV2.FinishContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "daemon-finish"}); err != nil {
		t.Fatalf("Finish first Continuation: %v", err)
	}
	if _, err := server.tasks.UpdateStatus(createdTask.ID, task.StatusCompleted); err != nil {
		t.Fatalf("record normal terminal Task state: %v", err)
	}
	conversation, err := server.tasks.AppendEvent(createdTask.ID, task.EventKindConversation, task.EventPayload{"message": "normal Task Conversation"})
	if err != nil {
		t.Fatalf("append Task Conversation: %v", err)
	}
	consumed, err := server.tasks.AppendEvent(createdTask.ID, task.EventKindSteering, task.EventPayload{"phase": "steering_requested", "directive": "already consumed"})
	if err != nil {
		t.Fatalf("append consumed steering: %v", err)
	}
	if _, err := server.tasks.AppendEvent(createdTask.ID, task.EventKindSteering, task.EventPayload{"phase": "steering_applied", "directive": "already consumed", "requested_event_id": consumed.ID}); err != nil {
		t.Fatalf("mark steering consumed: %v", err)
	}
	var unconsumedIDs []string
	for _, directive := range []string{"first unconsumed", "second unconsumed"} {
		event, err := server.tasks.AppendEvent(createdTask.ID, task.EventKindSteering, task.EventPayload{"phase": "steering_requested", "directive": directive})
		if err != nil {
			t.Fatalf("append unconsumed steering: %v", err)
		}
		unconsumedIDs = append(unconsumedIDs, event.ID)
	}

	if !server.acquireTaskControl(createdTask.ID) {
		t.Fatal("acquire resume task control")
	}
	queueWhileSelected := httptest.NewRecorder()
	queueRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks/"+createdTask.ID+"/steer/queue", strings.NewReader(`{"directive":"must wait for next resume"}`))
	queueRequest.SetPathValue("id", createdProject.ID)
	queueRequest.SetPathValue("task_id", createdTask.ID)
	server.handleQueueSteerTask(queueWhileSelected, queueRequest)
	if queueWhileSelected.Code != http.StatusConflict {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("queue during resume selection status = %d, body=%s", queueWhileSelected.Code, queueWhileSelected.Body.String())
	}
	found, resumeGoal, resumePlan, err := server.prepareFreshResumeContinuation(createdTask)
	if err != nil {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("prepare v2 resume: %v", err)
	}
	for _, required := range []string{createdTask.Goal, "first unconsumed", "second unconsumed"} {
		if !strings.Contains(resumeGoal, required) {
			t.Errorf("v2 resume omitted %q: %s", required, resumeGoal)
		}
	}
	if strings.Index(resumeGoal, "first unconsumed") > strings.Index(resumeGoal, "second unconsumed") {
		t.Errorf("v2 resume changed steering order: %s", resumeGoal)
	}
	for _, forbidden := range []string{"already consumed", "task summary", "objective outcome", "mechanical handoff", "conclusion"} {
		if strings.Contains(strings.ToLower(resumeGoal), forbidden) {
			t.Errorf("v2 resume copied forbidden %q: %s", forbidden, resumeGoal)
		}
	}
	current, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("project current resume state: %v", err)
	}
	publicationFailure := errors.New("injected resumed publication failure")
	failedOnce := false
	server.blackboardV2Continuity.SetFailureInjector(func(point blackboardv2.ContinuityFailurePoint) error {
		if point == blackboardv2.ContinuityFailureBeforeWorkingSnapshotPublication && !failedOnce {
			failedOnce = true
			return publicationFailure
		}
		return nil
	})
	if _, _, err := server.prepareBlackboardV2ContinuationLaunch(found, resumePlan, resumeGoal); !errors.Is(err, publicationFailure) {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("failed resumed launch error = %v", err)
	}
	server.releaseTaskControl(createdTask.ID)
	stillUnconsumed, err := server.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(stillUnconsumed) != 2 {
		t.Fatalf("failed launch consumed steering: %#v, %v", stillUnconsumed, err)
	}

	if !server.acquireTaskControl(createdTask.ID) {
		t.Fatal("reacquire resume task control")
	}
	found, resumeGoal, resumePlan, err = server.prepareFreshResumeContinuation(createdTask)
	if err != nil {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("retry prepare v2 resume: %v", err)
	}
	resumed, _, err := server.prepareBlackboardV2ContinuationLaunch(found, resumePlan, resumeGoal)
	server.releaseTaskControl(createdTask.ID)
	if err != nil {
		t.Fatalf("retry create resumed Continuation: %v", err)
	}
	resumedPin, err := server.blackboardV2Continuity.ReadLaunchPin(context.Background(), resumed.ID)
	if err != nil {
		t.Fatalf("read resumed pin: %v", err)
	}
	if resumed.ID == first.ID || resumed.Number != first.Number+1 || !bytes.Equal(resumedPin.Bytes, current.Bytes) {
		t.Fatalf("resume did not create fresh current pin: first=%#v resumed=%#v", first, resumed)
	}
	terminalTask, err := server.tasks.Get(createdTask.ID)
	if err != nil || terminalTask.Status != task.StatusCompleted {
		t.Fatalf("resume preparation changed normal terminal Task state: %#v, %v", terminalTask, err)
	}
	events, err := server.tasks.Events(createdTask.ID)
	if err != nil {
		t.Fatalf("read Task surfaces after resume: %v", err)
	}
	if len(events) != 7 || events[0].ID != conversation.ID {
		t.Fatalf("resume changed Task Conversation or steering events: %#v", events)
	}
	for index, requestedID := range unconsumedIDs {
		applied := events[5+index]
		if applied.Kind != task.EventKindSteering || applied.ContinuationID != resumed.ID || applied.Payload["phase"] != "steering_applied" || applied.Payload["requested_event_id"] != requestedID {
			t.Errorf("applied steering %d = %#v", index, applied)
		}
	}
	remaining, err := server.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("successful resume left duplicate steering: %#v, %v", remaining, err)
	}
	queueNext := httptest.NewRecorder()
	queueNextRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks/"+createdTask.ID+"/steer/queue", strings.NewReader(`{"directive":"next resume only"}`))
	queueNextRequest.SetPathValue("id", createdProject.ID)
	queueNextRequest.SetPathValue("task_id", createdTask.ID)
	server.handleQueueSteerTask(queueNext, queueNextRequest)
	if queueNext.Code != http.StatusOK {
		t.Fatalf("queue after successful resume status = %d, body=%s", queueNext.Code, queueNext.Body.String())
	}
	if _, err := server.blackboardV2.FinishContinuation(context.Background(), createdProject.ID, resumed.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-second-resume"}); err != nil {
		t.Fatalf("Finish resumed Continuation: %v", err)
	}
	if !server.acquireTaskControl(createdTask.ID) {
		t.Fatal("acquire second resume task control")
	}
	found, secondGoal, secondPlan, err := server.prepareFreshResumeContinuation(createdTask)
	if err != nil {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("prepare second resume: %v", err)
	}
	if !strings.Contains(secondGoal, "next resume only") || strings.Contains(secondGoal, "first unconsumed") || strings.Contains(secondGoal, "second unconsumed") {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("second resume steering selection = %s", secondGoal)
	}
	if _, _, err := server.prepareBlackboardV2ContinuationLaunch(found, secondPlan, secondGoal); err != nil {
		server.releaseTaskControl(createdTask.ID)
		t.Fatalf("create second resumed Continuation: %v", err)
	}
	server.releaseTaskControl(createdTask.ID)
	remaining, err = server.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("second successful resume left steering: %#v, %v", remaining, err)
	}
	for _, table := range []string{"blackboard_graph_mutations", "blackboard_graph_operations"} {
		var exists int
		if err := server.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatalf("inspect forbidden table %s: %v", table, err)
		}
		if exists != 0 {
			t.Errorf("v2 finish/resume retained forbidden table %s", table)
		}
	}
}

type testProviderSessionFactoryFunc func(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error)

func (f testProviderSessionFactoryFunc) Open(ctx context.Context, req ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	return f(ctx, req)
}

func waitForLocalFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
