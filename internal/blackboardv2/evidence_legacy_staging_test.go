package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestPrivateStagingCannotCollideWithClaimedStageLikeOrRetainFinalNames(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Namespace Collision")
	payload := strings.Repeat("namespace-collision-proof-", 4096)
	fixture.writeSource(t, "foo", payload)
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-namespace-owner", Key: "evidence:namespace-owner", Attempt: "attempt:evidence",
		SourcePath: "foo", ArtifactType: "text", Summary: "Private staging cannot collide with final names",
	}
	failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureBeforeTempCopy},
	})
	if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("namespace owner staging crash unexpectedly succeeded")
	}
	var privateFixtureTemp, ownerFinalPath, requestHash string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path,request_hash FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&privateFixtureTemp, &ownerFinalPath, &requestHash); err != nil {
		t.Fatalf("read pre-upgrade namespace paths: %v", err)
	}
	oldTempPath := ownerFinalPath + ".stage-" + requestHash[:24]
	if err := os.Remove(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(privateFixtureTemp))); err != nil {
		t.Fatalf("remove abandoned pre-upgrade stage: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path=?,previous_temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, oldTempPath, fixture.projectID, fixture.continuationID, request.IdempotencyKey); err != nil {
		t.Fatalf("restore v26 namespace reservation: %v", err)
	}
	stageLikeName := filepath.Base(oldTempPath)
	legalRetainName := ".retain-0123456789abcdef01234567"
	for directory, name := range map[string]string{"stage-final": stageLikeName, "retain-final": legalRetainName} {
		sourceDirectory := filepath.Join(fixture.workdir, directory)
		if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
			t.Fatalf("create adversarial source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sourceDirectory, name), []byte(payload), 0o600); err != nil {
			t.Fatalf("write adversarial source %q: %v", name, err)
		}
	}
	stageRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-legal-stage-final", Key: "evidence:legal-stage-final", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("stage-final", stageLikeName), ArtifactType: "text", Summary: "Legal stage-like final basename",
	}
	retainRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-legal-retain-final", Key: "evidence:legal-retain-final", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("retain-final", legalRetainName), ArtifactType: "text", Summary: "Legal retain-like final basename",
	}
	for _, claimed := range []blackboardv2.RetainEvidenceRequest{stageRequest, retainRequest} {
		if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, claimed); err != nil {
			t.Fatalf("retain adversarial claimed final %q: %v", claimed.SourcePath, err)
		}
	}
	var stageFinalPath, retainFinalPath string
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, stageRequest.IdempotencyKey).Scan(&stageFinalPath); err != nil {
		t.Fatalf("read stage-like final path: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, retainRequest.IdempotencyKey).Scan(&retainFinalPath); err != nil {
		t.Fatalf("read retain-like final path: %v", err)
	}
	if stageFinalPath != oldTempPath {
		t.Fatalf("adversarial final %q does not reproduce old stage collision %q", stageFinalPath, oldTempPath)
	}
	legacyOrphan := filepath.Join(filepath.Dir(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(ownerFinalPath))), ".retain-fedcba9876543210fedcba98")
	if err := os.WriteFile(legacyOrphan, []byte("partial historical proof"), 0o600); err != nil {
		t.Fatalf("write unclaimed historical temp: %v", err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version=27`); err != nil {
		t.Fatalf("restore pre-private-staging migration ledger: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close pre-private-staging store: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("upgrade namespace collision store: %v", err)
	}
	crashing := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempDirectorySync},
	})
	if _, err := crashing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("private staging restart crash unexpectedly succeeded")
	}
	for path, label := range map[string]string{stageFinalPath: "stage-like", retainFinalPath: "retain-like"} {
		got, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(path)))
		if err != nil || string(got) != payload {
			t.Fatalf("claimed %s final changed during retry: size=%d err=%v", label, len(got), err)
		}
	}
	if _, err := os.Lstat(legacyOrphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unclaimed historical temp survived guarded sweep: %v", err)
	}
	var privateTempPath, previousTempPath string
	if err := reopened.QueryRow(`SELECT temp_internal_path,previous_temp_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&privateTempPath, &previousTempPath); err != nil {
		t.Fatalf("read migrated private staging paths: %v", err)
	}
	if previousTempPath != oldTempPath || strings.Contains(privateTempPath, "/retained/") || !strings.Contains(privateTempPath, "/.evidence-staging/") {
		t.Fatalf("migrated staging paths = private %q previous %q", privateTempPath, previousTempPath)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close store after private staging crash: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.workdir, "foo")); err != nil {
		t.Fatalf("remove owner source before private staging recovery: %v", err)
	}
	reopened, err = store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("restart private staging recovery: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
	if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
		t.Fatalf("recover private staging without source: %v", err)
	}
	for path, label := range map[string]string{ownerFinalPath: "owner", stageFinalPath: "stage-like", retainFinalPath: "retain-like"} {
		got, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(path)))
		if err != nil || string(got) != payload {
			t.Fatalf("final %s payload after restart: size=%d err=%v", label, len(got), err)
		}
	}
}
