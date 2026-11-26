package task_test

import (
	"context"
	"errors"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

type queuedSteeringAdapter struct {
	check func()
}

func (queuedSteeringAdapter) Name() string { return "queued-steering" }

func (a queuedSteeringAdapter) Run(context.Context, string, func(task.EventKind, task.EventPayload)) error {
	a.check()
	return nil
}

func TestQueuedHarnessSteeringSurvivesFailedPreparation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	service := task.NewService(db, projects)
	createdProject, err := projects.Create("Queued input", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Read a local fixture",
		RuntimeProfileID: "profile", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardMode: task.BlackboardModeDisabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.AppendEvent(created.ID, task.EventKindSteering, task.EventPayload{
		"phase": "steering_requested", "directive": "Keep this queued message",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := task.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: created.ID, RuntimeProfileID: "profile",
		RuntimeProvider: "pi", Runner: task.RunnerSandbox, RuntimeConfig: map[string]any{},
		SteeringEventIDs: []string{queued.ID},
	}
	_, failed, err := service.CreateContinuationLaunchWithoutBlackboard(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertQueued := func(want int) {
		t.Helper()
		pending, err := service.UnconsumedHarnessSteering(context.Background(), created.ID)
		if err != nil || len(pending) != want {
			t.Fatalf("pending input = %#v, %v; want %d", pending, err, want)
		}
		if want != 0 && pending[0].EventID != queued.ID {
			t.Fatalf("queued identity changed: %#v", pending)
		}
	}
	assertQueued(1)
	if _, err := service.UpdateContinuationStatus(failed.ID, task.StatusFailed); err != nil {
		t.Fatal(err)
	}
	assertQueued(1)
	_, retried, err := service.CreateContinuationLaunchWithoutBlackboard(context.Background(), request)
	if err != nil {
		t.Fatalf("retry lost the saved queue selection: %v", err)
	}
	assertQueued(1)
	entered := false
	if err := runtime.NewHarness(service).Launch(context.Background(), runtime.LaunchRequest{
		TaskID: created.ID, ContinuationID: retried.ID, Goal: "Keep this queued message",
		Adapter: queuedSteeringAdapter{check: func() {
			entered = true
			assertQueued(0)
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if !entered {
		t.Fatal("retry did not enter the Runtime")
	}
	assertQueued(0)
	events, err := service.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, event := range events {
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_applied" {
			applied++
			if event.ContinuationID != retried.ID || event.Payload["requested_event_id"] != queued.ID {
				t.Fatalf("queue was consumed by the failed preparation: %#v", event)
			}
		}
	}
	if applied != 1 {
		t.Fatalf("queue consumption count = %d, want 1", applied)
	}
	stoppedInput, err := service.AppendEvent(created.ID, task.EventKindSteering, task.EventPayload{
		"phase": "steering_requested", "directive": "Keep input stopped before handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	request.SteeringEventIDs = []string{stoppedInput.ID}
	_, stopped, err := service.CreateContinuationLaunchWithoutBlackboard(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runtime.NewHarness(service).Launch(ctx, runtime.LaunchRequest{
		TaskID: created.ID, ContinuationID: stopped.ID, Goal: "Keep input stopped before handoff",
		Adapter: queuedSteeringAdapter{check: func() { t.Error("stopped input entered the Runtime") }},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped launch = %v, want cancellation", err)
	}
	pending, err := service.UnconsumedHarnessSteering(context.Background(), created.ID)
	if err != nil || len(pending) != 1 || pending[0].EventID != stoppedInput.ID {
		t.Fatalf("Stop consumed input before handoff: %#v, %v", pending, err)
	}
}
