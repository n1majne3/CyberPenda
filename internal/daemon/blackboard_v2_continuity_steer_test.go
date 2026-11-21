package daemon

import (
	"context"
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
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

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
	if _, err := server.db.Exec(`UPDATE projects SET blackboard_protocol='legacy' WHERE id=?`, createdProject.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex projection retry", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Runtime Profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "continue projection safely", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
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
	found, resumeGoal, resumePlan, err := server.prepareFreshResumeContinuation(createdTask)
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
	found, resumeGoal, resumePlan, err = restarted.prepareFreshResumeContinuation(found)
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
	factory := testProviderSessionFactoryFunc(func(_ context.Context, req ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
		args := []string{req.LaunchGoal}
		if req.Continuation.NativeSessionID != "" {
			args = []string{"resume", req.Continuation.NativeSessionID, req.LaunchGoal}
		}
		session := fakeprovider.New(fakeprovider.Config{
			Capabilities: runtimeplugin.Capabilities{},
		})
		adapter := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    string(req.Provider),
			Program: req.Facts.ProviderBinary,
			Args:    args,
			Workdir: req.Facts.Workdir,
			Env:     req.Facts.Env,
		})
		return ProviderSessionBinding{Session: session, Adapter: adapter}, nil
	})
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "interrupt-steer.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true, ProviderSessionFactory: factory})
	if err != nil {
		t.Fatalf("start Blackboard v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Interrupt steer", "", project.Scope{Domains: []string{"steer.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := server.db.Exec(`UPDATE projects SET blackboard_protocol='legacy' WHERE id=?`, createdProject.ID); err != nil {
		t.Fatal(err)
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
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "map the Task Goal before continuing", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerHost),
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
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

	_ = server.providerSessions.remove(createdTask.ID)
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
