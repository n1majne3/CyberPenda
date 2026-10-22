package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type evidenceMergeRaceInjector struct {
	service                         *blackboardv2.Service
	projectID, idempotencyKey       string
	source, canonical               string
	sourceVersion, canonicalVersion int
	fired                           bool
	err                             error
}

func (injector *evidenceMergeRaceInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureAfterFilePublish || injector.fired {
		return nil
	}
	injector.fired = true
	_, injector.err = injector.service.Apply(context.Background(), injector.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: injector.idempotencyKey,
		Changes: []blackboardv2.Change{{Op: "merge", Source: injector.source, SourceVersion: injector.sourceVersion, Canonical: injector.canonical, CanonicalVersion: injector.canonicalVersion}},
	})
	return injector.err
}

func assertNoRetainedEvidenceFiles(t *testing.T, runtimeRoot string) {
	t.Helper()
	matches := retainedEvidenceFiles(t, runtimeRoot)
	if len(matches) != 0 {
		t.Fatalf("rejected retain published managed files: %v", matches)
	}
}

func retainedEvidenceFiles(t *testing.T, artifactRoot string) []string {
	t.Helper()
	patterns := []string{
		filepath.Join(artifactRoot, "projects", "*", "retained", "*", "*"),
		filepath.Join(artifactRoot, "*", "artifacts", "retained", "*", "*"),
	}
	matches := []string{}
	for _, pattern := range patterns {
		found, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob retained Evidence: %v", err)
		}
		matches = append(matches, found...)
	}
	return matches
}

func globEvidenceTemps(t *testing.T, directory, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, pattern))
	if err != nil {
		t.Fatalf("glob publication temps: %v", err)
	}
	return matches
}

type evidenceDirectoryDurabilityInjector struct {
	failPoint blackboardv2.EvidenceFailurePoint
	failed    bool
	events    []blackboardv2.EvidenceFailurePoint
}

func (injector *evidenceDirectoryDurabilityInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureAfterDirectoryCreate && point != blackboardv2.EvidenceFailureAfterDirectorySync && point != blackboardv2.EvidenceFailureAfterGraphCommit {
		return nil
	}
	injector.events = append(injector.events, point)
	if point == injector.failPoint && injector.failPoint != "" && !injector.failed {
		injector.failed = true
		return errors.New("injected Evidence directory durability crash")
	}
	return nil
}

type evidenceMidCopyBarrier struct {
	mu      sync.Mutex
	reached chan struct{}
	release chan struct{}
	blocked bool
}

func (barrier *evidenceMidCopyBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureMidTempCopy {
		return nil
	}
	barrier.mu.Lock()
	if barrier.blocked {
		barrier.mu.Unlock()
		return nil
	}
	barrier.blocked = true
	close(barrier.reached)
	barrier.mu.Unlock()
	<-barrier.release
	return nil
}

type evidenceReservationBarrier struct {
	want    int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newEvidenceReservationBarrier(want int) *evidenceReservationBarrier {
	return &evidenceReservationBarrier{want: want, release: make(chan struct{})}
}

func (barrier *evidenceReservationBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureBeforeReservation {
		return nil
	}
	barrier.mu.Lock()
	barrier.arrived++
	if barrier.arrived == barrier.want {
		close(barrier.release)
	}
	barrier.mu.Unlock()
	<-barrier.release
	return nil
}

type evidenceTransferredOwnerBarrier struct {
	reached   chan struct{}
	release   chan struct{}
	blocked   bool
	gcCrashed bool
}

func (barrier *evidenceTransferredOwnerBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureBeforeFilePublish && !barrier.blocked {
		barrier.blocked = true
		close(barrier.reached)
		<-barrier.release
	}
	if point == blackboardv2.EvidenceFailureAfterPayloadGCClaim && !barrier.gcCrashed {
		barrier.gcCrashed = true
		return errors.New("injected crash after transferred-owner GC claim")
	}
	return nil
}

type evidenceBeforePublishBarrier struct {
	reached chan struct{}
	release chan struct{}
}

func (barrier *evidenceBeforePublishBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureBeforeFilePublish {
		close(barrier.reached)
		<-barrier.release
	}
	return nil
}

type evidenceGCClaimBarrier struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	claimed        chan struct{}
	release        chan struct{}
	updated        bool
}

func (barrier *evidenceGCClaimBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureAfterFilePublish && !barrier.updated {
		barrier.updated = true
		_, err := barrier.service.ApplyForContinuation(context.Background(), barrier.projectID, barrier.continuationID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-update-before-payload-gc",
			Changes: []blackboardv2.Change{{
				Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
				Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
			}},
		})
		return err
	}
	if point == blackboardv2.EvidenceFailureAfterPayloadGCClaim {
		close(barrier.claimed)
		<-barrier.release
	}
	return nil
}

type evidenceCleanupCrashInjector struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	crashPoint     blackboardv2.EvidenceFailurePoint
	updated        bool
	crashed        bool
}

func (injector *evidenceCleanupCrashInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureAfterFilePublish && !injector.updated {
		injector.updated = true
		_, err := injector.service.ApplyForContinuation(context.Background(), injector.projectID, injector.continuationID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-evidence-update-" + string(injector.crashPoint),
			Changes: []blackboardv2.Change{{
				Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
				Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
			}},
		})
		return err
	}
	if point == injector.crashPoint && !injector.crashed {
		injector.crashed = true
		return errors.New("injected Evidence cleanup crash")
	}
	return nil
}

type evidenceVersionRaceInjector struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	fired          bool
}

func (injector *evidenceVersionRaceInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureAfterFilePublish || injector.fired {
		return nil
	}
	injector.fired = true
	_, err := injector.service.ApplyForContinuation(context.Background(), injector.projectID, injector.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-evidence-update",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
			Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
		}},
	})
	return err
}

type replaceEvidenceV2Source struct{ path string }

func (replacement replaceEvidenceV2Source) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureBeforeFilePublish {
		return nil
	}
	if err := os.Rename(replacement.path, replacement.path+".original"); err != nil {
		return err
	}
	return os.WriteFile(replacement.path, []byte("replacement bytes\n"), 0o600)
}

type failEvidenceV2Once struct {
	point  blackboardv2.EvidenceFailurePoint
	failed bool
}

func (failure *failEvidenceV2Once) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != failure.point || failure.failed {
		return nil
	}
	failure.failed = true
	return errors.New("injected Evidence retention failure")
}

type evidenceV2Fixture struct {
	db             *store.DB
	dbPath         string
	service        *blackboardv2.Service
	projectID      string
	taskID         string
	continuationID string
	workdir        string
	runtimeRoot    string
}

func newEvidenceV2Fixture(t *testing.T, name string) evidenceV2Fixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create(name, "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	runtimeSnapshot := map[string]any{
		"snapshot_version": 1, "runtime_plugin_id": "codex", "runner": "sandbox",
		"model_provider_snapshot": map[string]any{"model_provider_id": "fixture-provider", "model": "fixture-model"},
		"runtime_turn_selection":  map[string]any{"model_provider_id": "fixture-provider", "model": "fixture-model"},
		"settings":                map[string]any{}, "enabled_skill_ids": []string{},
		"config_projection": map[string]any{"primitive": "codex_home"},
	}
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: name, Runner: task.RunnerSandbox, RuntimeConfig: runtimeSnapshot})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	_, continuation, err := tasks.CreateContinuationLaunchWithoutBlackboard(context.Background(), task.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: "profile-evidence",
		RuntimeProvider: "codex", Runner: task.RunnerSandbox, RuntimeConfig: runtimeSnapshot,
	})
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(context.Background(), createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-evidence-fixture",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:evidence", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain fixture Evidence"}},
			{Op: "create", Key: "attempt:evidence", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain fixture Evidence"}},
			{Op: "relate", From: "attempt:evidence", Relation: "tests", To: "objective:evidence"},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence fixture: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create Evidence workdir: %v", err)
	}
	return evidenceV2Fixture{db: db, dbPath: dbPath, service: service, projectID: createdProject.ID, taskID: createdTask.ID, continuationID: continuation.ID, workdir: workdir, runtimeRoot: runtimeRoot}
}

func (fixture evidenceV2Fixture) writeSource(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.workdir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}
}
