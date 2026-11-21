package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/store"
	"pentest/internal/task"

	_ "modernc.org/sqlite"
)

func newServices(t *testing.T) (*runtime.Harness, *task.Service, *project.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	harness := runtime.NewHarness(tasks)
	return harness, tasks, projects
}

// TestFakeRuntimeEmitsNormalizedEvents proves the tracer bullet: launching a
// fake runtime through the harness makes the task timeline receive normalized
// lifecycle and runtime-output events.
func TestFakeRuntimeEmitsNormalizedEvents(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "fake", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID:  created.ID,
		Adapter: runtime.NewFakeAdapter(),
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	// The fake runtime must emit a lifecycle-started event and at least one
	// runtime-output event, all normalized through the task timeline.
	kinds := map[task.EventKind]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
	}
	if !kinds[task.EventKindLifecycle] {
		t.Fatalf("expected a lifecycle event, got %#v", kinds)
	}
	if !kinds[task.EventKindRuntimeOutput] {
		t.Fatalf("expected a runtime_output event, got %#v", kinds)
	}
}

func TestRuntimeEventsAreBoundToTheLaunchedContinuation(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "fake", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, "fake", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if err := harness.Launch(context.Background(), runtime.LaunchRequest{
		TaskID: created.ID, ContinuationID: continuation.ID, Adapter: runtime.NewFakeAdapter(),
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("launch emitted no Events")
	}
	for _, event := range events {
		if event.ContinuationID != continuation.ID {
			t.Fatalf("Event %s Continuation = %q want %q", event.ID, event.ContinuationID, continuation.ID)
		}
	}
}

func TestHarnessRebindContinuationFinalizesReplacementTurn(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long task", RuntimeProfileID: "codex", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	first, err := tasks.CreateContinuation(created.ID, "codex", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID, ContinuationID: first.ID, Adapter: slowFakeAdapter{},
		})
	}()
	waitForActive(t, harness, created.ID)
	first, err = tasks.Continuation(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := tasks.CreateReplacementContinuation(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := harness.RebindContinuation(created.ID, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateContinuationStatus(first.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("replacement turn did not stop")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("launch error = %v, want canceled", err)
	}
	firstAfter, err := tasks.Continuation(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacementAfter, err := tasks.Continuation(replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAfter.Status != task.StatusCompleted {
		t.Fatalf("first status = %q, want completed", firstAfter.Status)
	}
	if replacementAfter.Status != task.StatusStopped {
		t.Fatalf("replacement status = %q, want stopped", replacementAfter.Status)
	}
}

func TestCommandRuntimeAdapterExecutesProviderProcessAndStreamsOutput(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "codex", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho provider-ready:$*\necho provider-warning >&2\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID: created.ID,
		Goal:   created.Goal,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    "codex",
			Program: binary,
			Args:    []string{"run", "--", created.Goal},
		}),
	}); err != nil {
		t.Fatalf("launch provider adapter: %v", err)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var sawAdapter bool
	var sawStdout bool
	var sawStderr bool
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["adapter"] == "codex" {
			sawAdapter = true
		}
		if event.Kind == task.EventKindRuntimeOutput {
			text, _ := event.Payload["text"].(string)
			stream, _ := event.Payload["stream"].(string)
			if stream == "stdout" && strings.Contains(text, "provider-ready:run -- enumerate example.com") {
				sawStdout = true
			}
			if stream == "stderr" && strings.Contains(text, "provider-warning") {
				sawStderr = true
			}
		}
	}
	if !sawAdapter {
		t.Fatalf("expected lifecycle adapter codex, got %#v", events)
	}
	if !sawStdout {
		t.Fatalf("expected stdout event from provider, got %#v", events)
	}
	if !sawStderr {
		t.Fatalf("expected stderr event from provider, got %#v", events)
	}
}

func TestCommandRuntimeOutputKeepsSecretShapedContent(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)

	const opaque = "opaque-launch-secret-value-42"
	binary := filepath.Join(t.TempDir(), "content-test")
	script := "#!/bin/sh\necho \"flag:$1 env:$LAUNCH_SECRET_TOKEN model:$ANTHROPIC_MODEL root:$PENTEST_WORKING_GRAPH_ROOT shape:bearer secret-echo-token-123456\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write content binary: %v", err)
	}

	adapter := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name:    "content-test",
		Program: binary,
		Args:    []string{opaque},
		Env: map[string]string{
			"LAUNCH_SECRET_TOKEN":        opaque,
			"ANTHROPIC_MODEL":            "claude-sonnet-4-5",
			"PENTEST_WORKING_GRAPH_ROOT": "/workspace/project-root",
		},
	})
	var events []task.EventPayload
	var kinds []task.EventKind
	emit := func(kind task.EventKind, payload task.EventPayload) {
		kinds = append(kinds, kind)
		events = append(events, payload)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := adapter.Run(ctx, "goal", emit); err != nil {
		t.Fatalf("run content adapter: %v", err)
	}

	var sawContent bool
	var sawLifecycle bool
	for i, event := range events {
		switch kinds[i] {
		case task.EventKindRuntimeOutput:
			text, _ := event["text"].(string)
			if strings.Contains(text, opaque) {
				t.Fatalf("runtime output leaked known launch secret: %#v", event)
			}
			if strings.Contains(text, "flag:[REDACTED] env:[REDACTED] model:claude-sonnet-4-5 root:/workspace/project-root shape:bearer secret-echo-token-123456") {
				sawContent = true
			}
		case task.EventKindLifecycle:
			if event["phase"] != "process_started" {
				continue
			}
			sawLifecycle = true
			if args, _ := event["args"].([]string); len(args) == 1 && args[0] == opaque {
				t.Fatalf("process_started args kept the launch secret: %#v", event)
			}
		}
	}
	if !sawContent {
		t.Fatalf("runtime output lost byte-for-byte LLM content: %#v", events)
	}
	if !sawLifecycle {
		t.Fatalf("expected process_started lifecycle event, got %#v", events)
	}
}

func TestCommandRuntimeAdapterRecordsNativeSessionFromClaudeInitOutput(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "claude", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, "claude", "claude_code", task.RunnerHost)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "claude-test")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-claude\"}'\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID:         created.ID,
		Goal:           created.Goal,
		ContinuationID: continuation.ID,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    "claude_code",
			Program: binary,
		}),
	}); err != nil {
		t.Fatalf("launch provider adapter: %v", err)
	}

	updated, err := tasks.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if updated == nil || updated.NativeSessionID != "sess-claude" {
		t.Fatalf("expected captured claude session id, got %#v", updated)
	}
}

func TestCommandRuntimeAdapterRecordsNativeSessionFromPiSessionHeader(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "pi", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, "pi", "pi", task.RunnerHost)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "pi-test")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"session\",\"version\":3,\"id\":\"sess-pi\",\"cwd\":\"/task/workdir\"}'\n" +
		"echo '{\"type\":\"message\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID:         created.ID,
		Goal:           created.Goal,
		ContinuationID: continuation.ID,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    "pi",
			Program: binary,
		}),
	}); err != nil {
		t.Fatalf("launch provider adapter: %v", err)
	}

	updated, err := tasks.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if updated == nil || updated.NativeSessionID != "sess-pi" {
		t.Fatalf("expected captured pi session id, got %#v", updated)
	}
}

// slowFakeAdapter cooperates with cancellation so Stop can be observed.
type slowFakeAdapter struct{}

func (slowFakeAdapter) Name() string { return "fake-slow" }
func (slowFakeAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	emit(task.EventKindRuntimeOutput, task.EventPayload{"text": "started long work"})
	<-ctx.Done()
	return ctx.Err()
}

// TestHarnessStopEndsActiveRun proves Stop cancels the active continuation and
// the task ends in the stopped status.

func waitForActive(t *testing.T, h *runtime.Harness, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsActive(taskID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not become active")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
