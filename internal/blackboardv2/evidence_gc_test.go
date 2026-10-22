package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestConcurrentRetainCannotClaimSharedPathDuringDurableGC(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Evidence Payload GC")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-concurrent-gc", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	for _, directory := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(fixture.workdir, directory), 0o700); err != nil {
			t.Fatalf("create shared-path source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, directory, "shared.txt"), []byte("shared content-addressed proof\n"), 0o600); err != nil {
			t.Fatalf("write shared-path source: %v", err)
		}
	}
	barrier := &evidenceGCClaimBarrier{
		service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID,
		claimed: make(chan struct{}), release: make(chan struct{}),
	}
	cleanupService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier,
	})
	cleanupRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-cleanup-gc-race", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("first", "shared.txt"), ArtifactType: "text", Summary: "Payload losing a semantic race",
	}
	cleanupResult := make(chan error, 1)
	go func() {
		_, err := cleanupService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, cleanupRequest)
		cleanupResult <- err
	}()
	select {
	case <-barrier.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not reach durable payload GC claim")
	}
	sharedRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-shared-after-gc", Key: "evidence:shared", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("second", "shared.txt"), ArtifactType: "text", Summary: "Concurrent shared payload",
	}
	_, concurrentErr := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, sharedRequest)
	var retryable *blackboardv2.Error
	if !errors.As(concurrentErr, &retryable) || retryable.Code != "evidence_payload_gc_in_progress" || !retryable.Retryable {
		t.Fatalf("concurrent shared-path retain error = %#v, want retryable payload GC", concurrentErr)
	}
	close(barrier.release)
	if err := <-cleanupResult; !isSemanticCode(err, "version_conflict") {
		t.Fatalf("cleanup retain error = %#v, want version_conflict", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, sharedRequest); err != nil {
		t.Fatalf("retry shared-path retain after GC: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, sharedRequest.Key)
	if err != nil {
		t.Fatalf("read shared-path Evidence: %v", err)
	}
	var internalPath string
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, sharedRequest.IdempotencyKey).Scan(&internalPath); err != nil {
		t.Fatalf("read shared-path managed location: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(internalPath)))
	if err != nil || string(payload) != "shared content-addressed proof\n" || detail.Record.SHA256 == "" {
		t.Fatalf("committed shared-path Evidence payload = %q, detail=%#v, err=%v", payload, detail, err)
	}
}

func TestReservedSharedPathPreventsConcurrentCleanupUnlink(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Reserved Shared Evidence Payload")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-shared-reservation", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	for _, directory := range []string{"owner", "waiter"} {
		if err := os.Mkdir(filepath.Join(fixture.workdir, directory), 0o700); err != nil {
			t.Fatalf("create reserved shared-path source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, directory, "shared.txt"), []byte("reserved shared proof\n"), 0o600); err != nil {
			t.Fatalf("write reserved shared-path source: %v", err)
		}
	}
	waiterBarrier := &evidenceBeforePublishBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	waiterService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: waiterBarrier,
	})
	waiterRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-reserved-shared-waiter", Key: "evidence:waiter", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("waiter", "shared.txt"), ArtifactType: "text", Summary: "Reserved shared payload waiter",
	}
	waiterResult := make(chan error, 1)
	go func() {
		_, err := waiterService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, waiterRequest)
		waiterResult <- err
	}()
	select {
	case <-waiterBarrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("shared-path waiter did not reach its durable reservation")
	}
	ownerInjector := &evidenceVersionRaceInjector{service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID}
	ownerService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: ownerInjector,
	})
	ownerRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-reserved-shared-owner", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("owner", "shared.txt"), ArtifactType: "text", Summary: "Shared payload owner losing semantic race",
	}
	if _, err := ownerService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, ownerRequest); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("shared-path owner error = %#v, want version_conflict", err)
	}
	var sharedPath string
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, waiterRequest.IdempotencyKey).Scan(&sharedPath); err != nil {
		t.Fatalf("read reserved shared managed path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(sharedPath))); err != nil {
		t.Fatalf("cleanup unlinked payload protected by reservation: %v", err)
	}
	close(waiterBarrier.release)
	if err := <-waiterResult; err != nil {
		t.Fatalf("reserved shared-path waiter commit: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, waiterRequest.Key)
	if err != nil || detail.Record.SHA256 == "" {
		t.Fatalf("read committed reserved shared Evidence: %#v, err=%v", detail, err)
	}
}

func TestSharedPayloadOwnershipTransfersAndLastReservationReclaimsAfterRestart(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Shared Evidence Ownership Transfer")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-owner-transfer", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	for _, directory := range []string{"owner-transfer", "last-reservation"} {
		if err := os.Mkdir(filepath.Join(fixture.workdir, directory), 0o700); err != nil {
			t.Fatalf("create ownership-transfer source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, directory, "shared.txt"), []byte("ownership-transfer-proof\n"), 0o600); err != nil {
			t.Fatalf("write ownership-transfer source: %v", err)
		}
	}
	waiterInjector := &evidenceTransferredOwnerBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	waiterService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: waiterInjector,
	})
	waiterRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-transferred-last", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("last-reservation", "shared.txt"), ArtifactType: "text", Summary: "Last shared reservation",
	}
	waiterResult := make(chan error, 1)
	go func() {
		_, err := waiterService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, waiterRequest)
		waiterResult <- err
	}()
	select {
	case <-waiterInjector.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("last shared reservation did not reach publication barrier")
	}
	ownerInjector := &evidenceVersionRaceInjector{service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID}
	ownerService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: ownerInjector,
	})
	ownerRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-departing-owner", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("owner-transfer", "shared.txt"), ArtifactType: "text", Summary: "Departing shared payload owner",
	}
	if _, err := ownerService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, ownerRequest); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("departing owner error = %#v, want version_conflict", err)
	}
	var sharedPath, payloadState string
	var waiterOwned, ownerCount int
	if err := fixture.db.QueryRow(`SELECT managed_internal_path,payload_owned FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, waiterRequest.IdempotencyKey).Scan(&sharedPath, &waiterOwned); err != nil {
		t.Fatalf("read transferred shared reservation: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, ownerRequest.IdempotencyKey).Scan(&ownerCount); err != nil {
		t.Fatalf("count departed owner request: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, fixture.projectID, sharedPath).Scan(&payloadState); err != nil {
		t.Fatalf("read transferred payload claim: %v", err)
	}
	if ownerCount != 0 || waiterOwned != 1 || payloadState != "active" {
		t.Fatalf("ownership transfer = owner rows %d waiter owned %d state %q", ownerCount, waiterOwned, payloadState)
	}
	close(waiterInjector.release)
	if err := <-waiterResult; !isSemanticCode(err, "version_conflict") {
		t.Fatalf("last shared reservation error = %#v, want version_conflict", err)
	}
	var gcContinuationID, gcKey string
	if err := fixture.db.QueryRow(`
		SELECT payload.state,payload.gc_continuation_id,payload.gc_idempotency_key
		FROM blackboard_v2_evidence_payloads payload
		JOIN blackboard_v2_evidence_requests request
		  ON request.project_id=payload.project_id AND request.managed_internal_path=payload.managed_internal_path
		WHERE request.project_id=? AND request.continuation_id=? AND request.idempotency_key=?`,
		fixture.projectID, fixture.continuationID, waiterRequest.IdempotencyKey,
	).Scan(&payloadState, &gcContinuationID, &gcKey); err != nil {
		t.Fatalf("read last reservation GC checkpoint: %v", err)
	}
	if payloadState != "gc" || gcContinuationID != fixture.continuationID || gcKey != waiterRequest.IdempotencyKey {
		t.Fatalf("last reservation GC checkpoint = %q/%q/%q", payloadState, gcContinuationID, gcKey)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close store after transferred-owner GC crash: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("restart transferred-owner GC: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
	if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, waiterRequest); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("restart last shared reservation = %#v, want version_conflict", err)
	}
	var requestCount, payloadCount int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND managed_internal_path=?`, fixture.projectID, sharedPath).Scan(&requestCount); err != nil {
		t.Fatalf("count transferred shared requests after recovery: %v", err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, fixture.projectID, sharedPath).Scan(&payloadCount); err != nil {
		t.Fatalf("count transferred payload claim after recovery: %v", err)
	}
	if requestCount != 0 || payloadCount != 0 {
		t.Fatalf("transferred-owner recovery rows = requests %d payloads %d, want zero", requestCount, payloadCount)
	}
	if _, err := os.Lstat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(sharedPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transferred-owner recovery left shared payload: %v", err)
	}
}
