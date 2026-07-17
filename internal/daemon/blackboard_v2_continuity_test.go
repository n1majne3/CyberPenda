package daemon

import (
	"bytes"
	"context"
	"encoding/json"
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
	"pentest/internal/runtime"
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
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect example.test", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
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
	profile, err := server.profiles.Create("Codex resume", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect resume.test", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
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
	found, resumeGoal, resumePlan, err := server.prepareHandoffResumeContinuation(createdTask)
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
	found, resumeGoal, resumePlan, err = server.prepareHandoffResumeContinuation(createdTask)
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
	found, secondGoal, secondPlan, err := server.prepareHandoffResumeContinuation(createdTask)
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
	for _, table := range []string{"task_summary_versions", "blackboard_graph_mutations", "blackboard_graph_operations"} {
		var count int
		if err := server.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count forbidden table %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("v2 finish/resume touched forbidden table %s (%d rows)", table, count)
		}
	}
}

func TestBlackboardV2ResumeProjectionFailureLeavesNoContinuationPinOrSteeringConsumption(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "resume-projection.db")
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start Blackboard v2 daemon: %v", err)
	}
	createdProject, err := server.projects.Create("Projection retry", "", project.Scope{Domains: []string{"retry.test"}}, project.Defaults{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex projection retry", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "continue projection safely", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		_ = server.Close()
		t.Fatalf("build first plan: %v", err)
	}
	first, _, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		_ = server.Close()
		t.Fatalf("create first Continuation: %v", err)
	}
	if _, err := server.blackboardV2.FinishContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: "finish-before-projection-retry"}); err != nil {
		_ = server.Close()
		t.Fatalf("Finish first Continuation: %v", err)
	}
	steering, err := server.tasks.AppendEvent(createdTask.ID, task.EventKindSteering, task.EventPayload{
		"phase": "steering_requested", "mode": "queue", "directive": "retry this directive exactly once",
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("queue steering: %v", err)
	}
	found, resumeGoal, resumePlan, err := server.prepareHandoffResumeContinuation(createdTask)
	if err != nil {
		_ = server.Close()
		t.Fatalf("prepare handoff resume: %v", err)
	}
	scopePath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", "scope.json")
	if err := os.Remove(scopePath); err != nil {
		_ = server.Close()
		t.Fatalf("remove projected Scope file: %v", err)
	}
	if err := os.Mkdir(scopePath, 0o700); err != nil {
		_ = server.Close()
		t.Fatalf("replace Scope file with directory: %v", err)
	}
	if _, _, err := server.prepareBlackboardV2ContinuationLaunch(found, resumePlan, resumeGoal); err == nil {
		_ = server.Close()
		t.Fatal("resume accepted a directory at .pentest/scope.json")
	}
	latest, err := server.tasks.LatestContinuation(createdTask.ID)
	if err != nil || latest == nil || latest.ID != first.ID {
		_ = server.Close()
		t.Fatalf("failed projection left durable active Continuation: %#v, %v", latest, err)
	}
	var continuationCount, pinCount int
	if err := server.db.QueryRow(`SELECT COUNT(*) FROM task_continuations WHERE task_id=?`, createdTask.ID).Scan(&continuationCount); err != nil {
		_ = server.Close()
		t.Fatalf("count Continuations after failed projection: %v", err)
	}
	if err := server.db.QueryRow(`
		SELECT COUNT(*) FROM blackboard_v2_continuation_pins pin
		JOIN task_continuations continuation ON continuation.id=pin.continuation_id
		WHERE continuation.task_id=?`, createdTask.ID).Scan(&pinCount); err != nil {
		_ = server.Close()
		t.Fatalf("count pins after failed projection: %v", err)
	}
	if continuationCount != 1 || pinCount != 1 {
		_ = server.Close()
		t.Fatalf("failed projection leaked Continuation/pin: continuations=%d pins=%d", continuationCount, pinCount)
	}
	unconsumed, err := server.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(unconsumed) != 1 || unconsumed[0].EventID != steering.ID {
		_ = server.Close()
		t.Fatalf("failed projection consumed steering: %#v, %v", unconsumed, err)
	}
	if err := os.RemoveAll(scopePath); err != nil {
		_ = server.Close()
		t.Fatalf("repair Scope path: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close before retry: %v", err)
	}

	restarted, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart after failed projection: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	found, err = restarted.tasks.Get(createdTask.ID)
	if err != nil {
		t.Fatalf("read Task after restart: %v", err)
	}
	found, resumeGoal, resumePlan, err = restarted.prepareHandoffResumeContinuation(found)
	if err != nil {
		t.Fatalf("retry resume preparation: %v", err)
	}
	resumed, _, err := restarted.prepareBlackboardV2ContinuationLaunch(found, resumePlan, resumeGoal)
	if err != nil {
		t.Fatalf("retry resume after restart: %v", err)
	}
	if resumed.Number != first.Number+1 {
		t.Fatalf("retry Continuation number = %d, want %d", resumed.Number, first.Number+1)
	}
	unconsumed, err = restarted.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(unconsumed) != 0 {
		t.Fatalf("successful retry did not consume steering once: %#v, %v", unconsumed, err)
	}
}

func TestBlackboardV2InterruptSteerUsesReconciledResumeContextAndAtomicSteeringCommit(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "interrupt-steer.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start Blackboard v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Interrupt steer", "", project.Scope{Domains: []string{"steer.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	binary := filepath.Join(root, "codex-interrupt")
	script := "#!/bin/sh\n" +
		"echo codex-provider:$*\n" +
		"case \"$*\" in *resume*) exit 0 ;; esac\n" +
		"exec sleep 20\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write Codex test binary: %v", err)
	}
	profile, err := server.profiles.Create("Codex interrupt", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: binary, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "map the Task Goal before continuing", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		t.Fatalf("build initial plan: %v", err)
	}
	if err := server.launchTaskInBackground(createdTask, plan, createdTask.Goal); err != nil {
		t.Fatalf("launch initial Continuation: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !server.harness.IsActive(createdTask.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !server.harness.IsActive(createdTask.ID) {
		t.Fatal("initial Runtime did not become active")
	}
	first, err := server.tasks.LatestContinuation(createdTask.ID)
	if err != nil || first == nil {
		t.Fatalf("read first Continuation: %#v, %v", first, err)
	}
	sessionPath := filepath.Join(runtimeRoot, createdTask.ID, "runtime-home", "codex", "sessions", "2026", "07", "17", "rollout-interrupt.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("prepare native session directory: %v", err)
	}
	sessionMeta := `{"timestamp":"2026-07-17T00:00:00Z","type":"session_meta","payload":{"session_id":"sess-interrupt","cwd":"` + runtimeRoot + `"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(sessionMeta), 0o600); err != nil {
		t.Fatalf("write native session: %v", err)
	}
	if _, err := server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "interrupt-steer-open-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:interrupt-steer", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Continue interrupted work"}},
			{Op: "create", Key: "attempt:interrupt-steer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Initial active work"}},
			{Op: "relate", From: "attempt:interrupt-steer", Relation: "tests", To: "objective:interrupt-steer"},
		},
	}); err != nil {
		t.Fatalf("create interrupted Attempt: %v", err)
	}
	const checkpointSummary = "Mapped the login boundary before interruption"
	if _, err := server.blackboardV2.CheckpointAttemptForContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "interrupt-steer-checkpoint", Key: "attempt:interrupt-steer", Version: 1, Summary: checkpointSummary,
	}); err != nil {
		t.Fatalf("checkpoint interrupted Attempt: %v", err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks/"+createdTask.ID+"/steer", strings.NewReader(`{"directive":"continue from the checkpoint"}`))
	request.SetPathValue("id", createdProject.ID)
	request.SetPathValue("task_id", createdTask.ID)
	server.handleSteerTask(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("interrupt steer status = %d, body=%s", response.Code, response.Body.String())
	}
	second, err := server.tasks.LatestContinuation(createdTask.ID)
	if err != nil || second == nil || second.ID == first.ID {
		t.Fatalf("read resumed Continuation: %#v, %v", second, err)
	}
	events, err := server.tasks.Events(createdTask.ID)
	if err != nil {
		t.Fatalf("read interrupt steer Events: %v", err)
	}
	appliedCount := 0
	for _, event := range events {
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_applied" {
			appliedCount++
			if event.ContinuationID != second.ID {
				t.Errorf("steering_applied is outside resumed Continuation transaction: %#v", event)
			}
		}
	}
	if appliedCount != 1 {
		t.Fatalf("steering_applied count = %d, want 1", appliedCount)
	}
	unconsumed, err := server.tasks.UnconsumedHarnessSteering(context.Background(), createdTask.ID)
	if err != nil || len(unconsumed) != 0 {
		t.Fatalf("successful interrupt resume left steering retryable: %#v, %v", unconsumed, err)
	}

	deadline = time.Now().Add(5 * time.Second)
	var resumedOutput string
	for time.Now().Before(deadline) {
		events, err = server.tasks.Events(createdTask.ID)
		if err != nil {
			t.Fatalf("read resumed output: %v", err)
		}
		resumedOutput = ""
		for _, event := range events {
			if event.ContinuationID != second.ID || event.Kind != task.EventKindRuntimeOutput {
				continue
			}
			text, _ := event.Payload["text"].(string)
			if strings.Contains(text, "codex-provider:") {
				resumedOutput += text
			}
		}
		if strings.Contains(resumedOutput, checkpointSummary) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, required := range []string{createdTask.Goal, "continue from the checkpoint", "attempt:interrupt-steer", checkpointSummary} {
		if !strings.Contains(resumedOutput, required) {
			t.Errorf("resumed Runtime omitted %q: %s", required, resumedOutput)
		}
	}
}

func TestBlackboardV2DaemonOwnsUnexpectedAttemptReconciliationAcrossRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "daemon-reconciliation.db")
	server, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start Blackboard v2 daemon: %v", err)
	}
	createdProject, err := server.projects.Create("Daemon reconciliation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex reconciliation", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "exercise daemon reconciliation", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		_ = server.Close()
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "daemon-reconciliation-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:daemon-reconciliation", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Prove daemon authority"}},
			{Op: "create", Key: "attempt:daemon-reconciliation", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Runtime work remained open"}},
			{Op: "relate", From: "attempt:daemon-reconciliation", Relation: "tests", To: "objective:daemon-reconciliation"},
		},
	}); err != nil {
		_ = server.Close()
		t.Fatalf("create owned Attempt: %v", err)
	}
	if _, err := server.blackboardV2.CheckpointAttemptForContinuation(context.Background(), createdProject.ID, launch.Continuation.ID, blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "daemon-reconciliation-checkpoint", Key: "attempt:daemon-reconciliation", Version: 1,
		Summary: "Checkpoint retained before daemon-observed failure",
	}); err != nil {
		_ = server.Close()
		t.Fatalf("checkpoint Attempt: %v", err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusFailed); err != nil {
		_ = server.Close()
		t.Fatalf("reconcile unexpected Continuation end: %v", err)
	}
	before, err := server.blackboardV2.ReadHistory(context.Background(), createdProject.ID, "attempt:daemon-reconciliation", blackboardv2.HistoryOptions{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("read reconciled Attempt: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close daemon before restart: %v", err)
	}

	restarted, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart Blackboard v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	after, err := restarted.blackboardV2.ReadHistory(context.Background(), createdProject.ID, "attempt:daemon-reconciliation", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read reconciled Attempt after restart: %v", err)
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("encode pre-restart history: %v", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("encode post-restart history: %v", err)
	}
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("restart changed reconciled Attempt history\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
	marked, err := restarted.tasks.Continuation(launch.Continuation.ID)
	if err != nil {
		t.Fatalf("read reconciliation marker after restart: %v", err)
	}
	if marked.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("reconciliation marker after restart = %q", marked.BlackboardReconciliationStatus)
	}
}

func TestCodexV2LaunchExcludesIdentityMetadataAndOperatorCredentialSurface(t *testing.T) {
	root := t.TempDir()
	const operatorToken = "operator-token-must-never-reach-codex"
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "metadata.db"), RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectA, err := server.projects.Create("A", "", project.Scope{Domains: []string{"a.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project A: %v", err)
	}
	projectB, err := server.projects.Create("B", "", project.Scope{Domains: []string{"b.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project B: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: "/usr/local/bin/codex", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectA.ID, Goal: "inspect a.example", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		t.Fatalf("build initial plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare bound plan: %v", err)
	}
	runtimeConfig, err := json.Marshal(bound.RuntimeConfig)
	if err != nil {
		t.Fatalf("encode runtime config: %v", err)
	}
	configTOML, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "runtime-home", "codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "workdir", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	visible := strings.Join([]string{bound.LaunchGoal, string(runtimeConfig), string(configTOML), string(agents)}, "\n")
	for _, forbidden := range []string{
		projectA.ID, projectB.ID, createdTask.ID, continuation.ID, profile.ID, operatorToken,
		"project_id", "task_id", "continuation_id", "runtime_profile_id", "runtime_plugin_id",
		"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_MCP_URL", "PENTEST_AUTH_TOKEN",
		"[mcp_servers.pentest]", "/mcp?token=", "projection_hash", "estimated_tokens", "protocol_rule_digest",
	} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("Codex v2 launch surface leaked %q:\n%s", forbidden, visible)
		}
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/projects", nil),
		httptest.NewRequest(http.MethodGet, "/api/projects/"+projectB.ID+"/tasks", nil),
		httptest.NewRequest(http.MethodPost, "/api/projects/"+projectB.ID+"/blackboard/mutations", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/mcp?token=continuation-capability-not-issued", nil),
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("networkless Codex request %s %s status = %d, want 401", request.Method, request.URL, response.Code)
		}
	}
}

func TestRealCodexAdapterHarnessRereadsAcknowledgedSnapshotAfterContextLoss(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "codex-shim")
	shimScript := `#!/bin/sh
set -eu
printf '%s\n' "$@" > .shim-args
env > .shim-env
cat .pentest/blackboard.json > .shim-discarded
: > .shim-discarded
: > .shim-ready
while [ ! -f .shim-continue ]; do sleep 0.05; done
cat .pentest/blackboard.json > .shim-reread
cat .pentest/blackboard.json
`
	if err := os.WriteFile(shim, []byte(shimScript), 0o700); err != nil {
		t.Fatalf("write Codex shim: %v", err)
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "harness.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Harness", "", project.Scope{Domains: []string{"harness.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex shim", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: shim, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "exercise long Codex continuation", RuntimeProfileID: profile.ID,
		Runner: task.RunnerHost, RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		t.Fatalf("build Codex plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare Codex Continuation: %v", err)
	}
	workdir := filepath.Join(root, "runs", createdTask.ID, "workdir")
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: createdTask.ID, Goal: bound.LaunchGoal, Adapter: bound.Adapter, ContinuationID: continuation.ID,
		})
	}()
	waitForLocalFile(t, filepath.Join(workdir, ".shim-ready"), 5*time.Second)
	_, err = server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "harness-acknowledged-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:harness-current", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Current after compaction", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("acknowledge Runtime write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".shim-continue"), []byte("continue"), 0o600); err != nil {
		t.Fatalf("release Codex shim: %v", err)
	}
	select {
	case err := <-launchDone:
		if err != nil {
			t.Fatalf("production Codex harness launch: %v", err)
		}
	case <-time.After(8 * time.Second):
		server.harness.Stop(createdTask.ID)
		t.Fatal("Codex shim did not finish after acknowledged replacement")
	}
	current, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		t.Fatalf("project current Snapshot: %v", err)
	}
	reread, err := os.ReadFile(filepath.Join(workdir, ".shim-reread"))
	if err != nil {
		t.Fatalf("read Codex shim reread: %v", err)
	}
	if !bytes.Equal(reread, current.Bytes) {
		t.Fatalf("Codex shim reread stale bytes\ngot=%s\nwant=%s", reread, current.Bytes)
	}
	discarded, err := os.ReadFile(filepath.Join(workdir, ".shim-discarded"))
	if err != nil || len(discarded) != 0 {
		t.Fatalf("context-loss simulation retained prior Snapshot: %q, %v", discarded, err)
	}
	args, err := os.ReadFile(filepath.Join(workdir, ".shim-args"))
	if err != nil {
		t.Fatalf("read Codex shim args: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(workdir, ".shim-env"))
	if err != nil {
		t.Fatalf("read Codex shim env: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read Codex checklist: %v", err)
	}
	for _, label := range []string{"Runner:", "Scope:", "Blackboard:", "Schema:", "Revision:"} {
		if strings.Count(string(args), label) != 1 {
			t.Fatalf("Codex argv header label %q count = %d:\n%s", label, strings.Count(string(args), label), args)
		}
	}
	if strings.Count(string(args)+string(agents), blackboardv2.CodexChecklist()) != 1 {
		t.Fatalf("checklist repeated across Codex argv/instructions\nargs=%s\nagents=%s", args, agents)
	}
	for _, forbidden := range []string{
		createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
		"PENTEST_PROJECT_ID=", "PENTEST_TASK_ID=", "PENTEST_MCP_URL=", "PENTEST_AUTH_TOKEN=", "/mcp?token=",
	} {
		if strings.Contains(string(args)+string(env), forbidden) {
			t.Fatalf("production Codex process received forbidden metadata %q\nargs=%s\nenv=%s", forbidden, args, env)
		}
	}
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
