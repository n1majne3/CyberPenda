package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestRetainEvidenceConvergesAcrossPublicationSemanticAndLostResponseFailures(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeFilePublish,
		blackboardv2.EvidenceFailureAfterFilePublish,
		blackboardv2.EvidenceFailureAfterGraphCommit,
		blackboardv2.EvidenceFailureAfterResultStore,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Evidence Failure "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			fixture.writeSource(t, "failure.txt", "one durable payload\n")
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "failure-" + string(point), Key: "evidence:failure-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "failure.txt", ArtifactType: "text", Summary: "Evidence retained across an uncertain boundary",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected Evidence failure unexpectedly succeeded")
			}
			if point == blackboardv2.EvidenceFailureBeforeFilePublish {
				assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)
			}
			first, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("retry Retain Evidence: %v", err)
			}
			replay, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("replay Retain Evidence: %v", err)
			}
			if !reflect.DeepEqual(first, replay) {
				t.Fatalf("Retain Evidence replay drifted:\nfirst %#v\nreplay %#v", first, replay)
			}
			detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, request.Key)
			if err != nil || detail.Version != 1 || detail.Record.Status != "available" {
				t.Fatalf("converged Evidence = %#v, err = %v", detail, err)
			}
			matches := retainedEvidenceFiles(t, fixture.runtimeRoot)
			if len(matches) != 1 {
				t.Fatalf("managed Evidence files = %v", matches)
			}
			payload, err := os.ReadFile(matches[0])
			if err != nil || string(payload) != "one durable payload\n" {
				t.Fatalf("managed Evidence payload = %q, err = %v", payload, err)
			}
		})
	}
}

func TestRetainEvidenceReplacementUsesCurrentVersionWithoutPublishingStalePayload(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Replacement")
	fixture.writeSource(t, "first.txt", "first payload\n")
	firstRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-first", Key: "evidence:replace", Attempt: "attempt:evidence",
		SourcePath: "first.txt", ArtifactType: "text", Summary: "First retained payload",
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, firstRequest); err != nil {
		t.Fatalf("retain first Evidence version: %v", err)
	}
	fixture.writeSource(t, "second.txt", "second payload\n")
	secondRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-second", Key: "evidence:replace", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "second.txt", ArtifactType: "text", Summary: "Second retained payload",
	}
	second, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, secondRequest)
	if err != nil {
		t.Fatalf("replace retained Evidence: %v", err)
	}
	if got, want := mustTupleJSON(t, second.Records), [][]any{{"evidence:replace", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement records = %#v, want %#v", got, want)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:replace")
	if err != nil || detail.Version != 2 || detail.Record.Summary != "Second retained payload" || detail.Record.SourcePath != "second.txt" {
		t.Fatalf("replacement detail = %#v, err = %v", detail, err)
	}
	history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, "evidence:replace", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read replacement history: %v", err)
	}
	if len(history.Items) != 1 || history.Items[0].Record == nil || history.Items[0].Record.SourcePath != "first.txt" || history.Items[0].Record.SHA256 == detail.Record.SHA256 {
		t.Fatalf("replacement history = %#v", history)
	}
	matchesBefore := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(matchesBefore) != 2 {
		t.Fatalf("managed replacement files before stale write = %v", matchesBefore)
	}
	noOp, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-second-no-op", Key: "evidence:replace", Version: 2, Attempt: "attempt:evidence",
		SourcePath: "second.txt", ArtifactType: "text", Summary: "Second retained payload",
	})
	if err != nil {
		t.Fatalf("repeat semantic replacement: %v", err)
	}
	if len(noOp.Records) != 0 || len(noOp.Relations) != 0 || noOp.Revision != second.Revision {
		t.Fatalf("semantic replacement no-op = %#v, want unchanged revision %d", noOp, second.Revision)
	}

	fixture.writeSource(t, "stale.txt", "stale payload must not publish\n")
	_, err = fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-stale", Key: "evidence:replace", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "stale.txt", ArtifactType: "text", Summary: "Stale replacement payload",
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence replacement error = %#v, want version_conflict", err)
	}
	matchesAfter := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if !reflect.DeepEqual(matchesAfter, matchesBefore) {
		t.Fatalf("stale replacement changed managed files:\nbefore %v\nafter %v", matchesBefore, matchesAfter)
	}
}

func TestDefinitiveSemanticFailureCleansOnlyRequestOwnedPayloadAndReservation(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Definitive Cleanup")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-cleanup", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	fixture.writeSource(t, "raced.txt", "raced replacement payload\n")
	injector := &evidenceVersionRaceInjector{
		service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID,
	}
	tracingService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
	})
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-raced-cleanup", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "raced.txt", ArtifactType: "text", Summary: "Raced replacement payload",
	}
	if _, err := tracingService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("raced replacement error = %#v, want version_conflict", err)
	}
	files := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(files) != 1 {
		t.Fatalf("definitive failure left an orphan or removed shared payload: %v", files)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:cleanup")
	if err != nil || detail.Version != 2 || detail.Record.Summary != "Concurrent semantic update" {
		t.Fatalf("definitive failure left partial semantic state: %#v, err = %v", detail, err)
	}
	request.Version = 2
	result, err := tracingService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("reuse cleaned reservation against current version: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{"evidence:cleanup", float64(3)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement after cleanup records = %#v, want %#v", got, want)
	}
}

func TestDefinitiveEvidenceCleanupRecoversDurableGCCheckpointAfterRestart(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureAfterPayloadGCClaim,
		blackboardv2.EvidenceFailureAfterPayloadUnlink,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Evidence GC Restart "+string(point))
			fixture.writeSource(t, "current.txt", "current payload\n")
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-current-gc", Key: "evidence:cleanup", Attempt: "attempt:evidence",
				SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
			}); err != nil {
				t.Fatalf("retain current Evidence: %v", err)
			}
			fixture.writeSource(t, "raced.txt", "payload requiring durable cleanup\n")
			injector := &evidenceCleanupCrashInjector{
				service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID, crashPoint: point,
			}
			service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-raced-gc", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
				SourcePath: "raced.txt", ArtifactType: "text", Summary: "Raced replacement payload",
			}
			if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
				t.Fatalf("raced replacement error = %#v, want version_conflict", err)
			}
			var state, gcContinuationID, gcKey, finalPath, tempPath string
			if err := fixture.db.QueryRow(`
				SELECT payload.state,payload.gc_continuation_id,payload.gc_idempotency_key,request.managed_internal_path,request.temp_internal_path
				FROM blackboard_v2_evidence_requests request
				JOIN blackboard_v2_evidence_payloads payload
				  ON payload.project_id=request.project_id AND payload.managed_internal_path=request.managed_internal_path
				WHERE request.project_id=? AND request.continuation_id=? AND request.idempotency_key=?`,
				fixture.projectID, fixture.continuationID, request.IdempotencyKey,
			).Scan(&state, &gcContinuationID, &gcKey, &finalPath, &tempPath); err != nil {
				t.Fatalf("read durable GC checkpoint: %v", err)
			}
			if state != "gc" || gcContinuationID != fixture.continuationID || gcKey != request.IdempotencyKey {
				t.Fatalf("durable GC checkpoint = %q/%q/%q", state, gcContinuationID, gcKey)
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close store for GC restart: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("reopen store after GC crash: %v", err)
			}
			syncCrash := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
				Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterPayloadUnlink},
			})
			if _, err := syncCrash.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil || isSemanticCode(err, "version_conflict") {
				t.Fatalf("GC recovery sync crash = %#v, want injected durability failure", err)
			}
			var durableRequestCount, durablePayloadCount int
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&durableRequestCount); err != nil {
				t.Fatalf("count GC request after recovery sync crash: %v", err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=? AND state='gc'`, fixture.projectID, finalPath).Scan(&durablePayloadCount); err != nil {
				t.Fatalf("count GC claim after recovery sync crash: %v", err)
			}
			if durableRequestCount != 1 || durablePayloadCount != 1 {
				t.Fatalf("GC durability rows after sync crash = request %d payload %d, want one each", durableRequestCount, durablePayloadCount)
			}
			if err := reopened.Close(); err != nil {
				t.Fatalf("close store after GC recovery sync crash: %v", err)
			}
			reopened, err = store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("restart after GC recovery sync crash: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
				t.Fatalf("second restart after durable GC checkpoint = %#v, want version_conflict", err)
			}
			var requestCount, payloadCount int
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&requestCount); err != nil {
				t.Fatalf("count recovered GC request: %v", err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, fixture.projectID, finalPath).Scan(&payloadCount); err != nil {
				t.Fatalf("count recovered GC payload claim: %v", err)
			}
			if requestCount != 0 || payloadCount != 0 {
				t.Fatalf("recovered GC rows = request %d payload %d, want zero", requestCount, payloadCount)
			}
			for _, path := range []string{finalPath, tempPath} {
				absolute := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(path))
				if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovered GC left managed file %q: %v", absolute, err)
				}
			}
		})
	}
}
