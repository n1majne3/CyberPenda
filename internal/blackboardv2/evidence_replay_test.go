package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestReservedAndPublishedRetainResumeAfterProducingAttemptTerminal(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeFilePublish,
		blackboardv2.EvidenceFailureAfterFileRename,
		blackboardv2.EvidenceFailureBeforePublishStore,
		blackboardv2.EvidenceFailureAfterFilePublish,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Terminal Resume "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			fixture.writeSource(t, "terminal-resume.txt", "durably reserved proof\n")
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "terminal-resume-" + string(point), Key: "evidence:terminal-resume-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "terminal-resume.txt", ArtifactType: "text", Summary: "Proof reserved before the Attempt became terminal",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected durable retention failure unexpectedly succeeded")
			}
			var journalStatus, internalPath string
			var payloadOwned int
			if err := fixture.db.QueryRow(`SELECT status,managed_internal_path,payload_owned FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&journalStatus, &internalPath, &payloadOwned); err != nil {
				t.Fatalf("read durable Evidence journal: %v", err)
			}
			if internalPath == "" || strings.Contains(internalPath, fixture.taskID) {
				t.Fatalf("journal planned path = %q, want private Project-owned path without Task ID", internalPath)
			}
			if point == blackboardv2.EvidenceFailureBeforeFilePublish && (journalStatus != "reserved" || payloadOwned != 0) {
				t.Fatalf("pre-publish journal = status %q owned %d, want reserved/0", journalStatus, payloadOwned)
			}
			if (point == blackboardv2.EvidenceFailureAfterFileRename || point == blackboardv2.EvidenceFailureBeforePublishStore) && (journalStatus != "reserved" || payloadOwned != 1) {
				t.Fatalf("post-rename journal = status %q owned %d, want reserved/1", journalStatus, payloadOwned)
			}
			if point == blackboardv2.EvidenceFailureAfterFilePublish && (journalStatus != "published" || payloadOwned != 1) {
				t.Fatalf("published journal = status %q owned %d, want published/1", journalStatus, payloadOwned)
			}
			if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-" + string(point),
				Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "The producing Attempt ended after durable Evidence reservation"}},
			}); err != nil {
				t.Fatalf("terminalize producing Attempt: %v", err)
			}
			if point == blackboardv2.EvidenceFailureAfterFileRename || point == blackboardv2.EvidenceFailureBeforePublishStore || point == blackboardv2.EvidenceFailureAfterFilePublish {
				if err := os.Remove(filepath.Join(fixture.workdir, "terminal-resume.txt")); err != nil {
					t.Fatalf("remove source after durable publication: %v", err)
				}
			}
			result, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("resume durable Retain Evidence after terminal Attempt: %v", err)
			}
			if got, want := mustTupleJSON(t, result.Records), [][]any{{request.Key, float64(1)}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("resumed Evidence records = %#v, want %#v", got, want)
			}
			history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, request.Key, blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read resumed Evidence history: %v", err)
			}
			foundProduced := false
			for _, item := range history.Items {
				foundProduced = foundProduced || (item.Kind == "relationship" && item.From == "attempt:evidence" && item.Relation == "produced" && item.To == request.Key)
			}
			if !foundProduced {
				t.Fatalf("resumed Evidence history lacks terminal Attempt production: %#v", history.Items)
			}
		})
	}
}

func TestRetainEvidenceRestartRecoversDeterministicJournaledTempAtEveryPublicationBoundary(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeTempCopy,
		blackboardv2.EvidenceFailureMidTempCopy,
		blackboardv2.EvidenceFailureAfterTempSync,
		blackboardv2.EvidenceFailureBeforeFileRename,
		blackboardv2.EvidenceFailureAfterFileRename,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Restart Temp Recovery "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			payload := strings.Repeat("restart-safe-proof-", 8192)
			fixture.writeSource(t, "restart.txt", payload)
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "restart-" + string(point), Key: "evidence:restart-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "restart.txt", ArtifactType: "text", Summary: "Proof recovered after a publication crash",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected publication crash unexpectedly succeeded")
			}
			var requestHash, tempPath, finalPath, status string
			if err := fixture.db.QueryRow(`SELECT request_hash,temp_internal_path,managed_internal_path,status FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&requestHash, &tempPath, &finalPath, &status); err != nil {
				t.Fatalf("read restart journal: %v", err)
			}
			if filepath.Base(tempPath) != requestHash || filepath.Dir(tempPath) == filepath.Dir(finalPath) || strings.Contains(tempPath, "/retained/") || !strings.Contains(tempPath, "/.evidence-staging/") || status != "reserved" {
				t.Fatalf("restart journal temp=%q final=%q status=%q", tempPath, finalPath, status)
			}
			trackedTemp := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))
			temps := globEvidenceTemps(t, filepath.Dir(trackedTemp), "*")
			for _, candidate := range temps {
				if candidate != trackedTemp {
					t.Fatalf("raw unjournaled temp survived crash: tracked %q candidates %v", trackedTemp, temps)
				}
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close store for restart: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("reopen store after publication crash: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			service := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			if point == blackboardv2.EvidenceFailureAfterTempSync || point == blackboardv2.EvidenceFailureBeforeFileRename || point == blackboardv2.EvidenceFailureAfterFileRename {
				if err := os.Remove(filepath.Join(fixture.workdir, "restart.txt")); err != nil {
					t.Fatalf("remove source before restart recovery: %v", err)
				}
			}
			if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
				t.Fatalf("recover Retain Evidence after restart: %v", err)
			}
			if _, err := os.Lstat(trackedTemp); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journaled temp remained after recovery: err=%v path=%q", err, trackedTemp)
			}
			files := retainedEvidenceFiles(t, fixture.runtimeRoot)
			if len(files) != 1 {
				t.Fatalf("restart recovery final payloads = %v, want one", files)
			}
			got, err := os.ReadFile(files[0])
			if err != nil || string(got) != payload {
				t.Fatalf("recovered payload size=%d err=%v", len(got), err)
			}
		})
	}
}

func TestEvidenceDirectoryHierarchyCreationFsyncsEachParentAcrossRestart(t *testing.T) {
	for _, crashPoint := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureAfterDirectoryCreate,
		blackboardv2.EvidenceFailureAfterDirectorySync,
	} {
		t.Run(string(crashPoint), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Evidence Directory Durability "+string(crashPoint))
			fixture.writeSource(t, "directory-proof.txt", "proof requiring durable parent dentries\n")
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-directory-" + string(crashPoint), Key: "evidence:directory-" + string(crashPoint), Attempt: "attempt:evidence",
				SourcePath: "directory-proof.txt", ArtifactType: "text", Summary: "Every managed directory parent is durable",
			}
			crash := &evidenceDirectoryDurabilityInjector{failPoint: crashPoint}
			service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: crash,
			})
			if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("directory creation durability crash unexpectedly succeeded")
			}
			wantEvents := []blackboardv2.EvidenceFailurePoint{blackboardv2.EvidenceFailureAfterDirectoryCreate}
			if crashPoint == blackboardv2.EvidenceFailureAfterDirectorySync {
				wantEvents = append(wantEvents, blackboardv2.EvidenceFailureAfterDirectorySync)
			}
			if !reflect.DeepEqual(crash.events, wantEvents) {
				t.Fatalf("directory crash events = %v, want %v", crash.events, wantEvents)
			}
			if _, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, request.Key); !isSemanticCode(err, "not_found") {
				t.Fatalf("directory crash committed semantic Evidence: %#v", err)
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close store after directory creation crash: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("restart after directory creation crash: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			recovery := &evidenceDirectoryDurabilityInjector{}
			restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: recovery,
			})
			if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
				t.Fatalf("recover durable Evidence directory hierarchy: %v", err)
			}
			if len(recovery.events) == 0 || recovery.events[0] != blackboardv2.EvidenceFailureAfterDirectorySync {
				t.Fatalf("restart directory events begin %v, want parent sync of pre-crash component", recovery.events)
			}
			unsyncedCreates := 0
			foundSemanticCommit := false
			for _, point := range recovery.events {
				switch point {
				case blackboardv2.EvidenceFailureAfterDirectoryCreate:
					if unsyncedCreates != 0 {
						t.Fatalf("directory create occurred before prior parent sync: %v", recovery.events)
					}
					unsyncedCreates++
				case blackboardv2.EvidenceFailureAfterDirectorySync:
					unsyncedCreates = 0
				case blackboardv2.EvidenceFailureAfterGraphCommit:
					foundSemanticCommit = true
					if unsyncedCreates != 0 {
						t.Fatalf("semantic commit preceded directory parent sync: %v", recovery.events)
					}
				}
			}
			if !foundSemanticCommit {
				t.Fatalf("directory durability events lack semantic commit: %v", recovery.events)
			}
		})
	}
}

func TestRetainEvidenceRestartAfterTempFileAndDirectorySync(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Temp Directory Sync")
	payload := strings.Repeat("temp-and-directory-durable-proof-", 4096)
	fixture.writeSource(t, "directory-synced.txt", payload)
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-temp-directory-sync", Key: "evidence:temp-directory-sync", Attempt: "attempt:evidence",
		SourcePath: "directory-synced.txt", ArtifactType: "text", Summary: "Staging inode and directory are durable",
	}
	service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempDirectorySync},
	})
	if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("post-temp-directory-sync crash unexpectedly succeeded")
	}
	var tempPath string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND status='reserved'`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath); err != nil {
		t.Fatalf("read directory-synced temp reservation: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close store after temp directory sync: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.workdir, "directory-synced.txt")); err != nil {
		t.Fatalf("remove source after temp directory sync: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("restart after temp directory sync: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
	if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
		t.Fatalf("recover after temp file and directory sync: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory-synced temp remained after recovery: %v", err)
	}
	files := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(files) != 1 {
		t.Fatalf("directory-sync recovery payloads = %v, want one final", files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil || string(got) != payload {
		t.Fatalf("directory-sync recovery final size=%d err=%v", len(got), err)
	}
}
