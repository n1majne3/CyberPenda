package blackboardv2_test

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestMigration27StagingRecoversIntoFixedScope(t *testing.T) {
	t.Run("terminal replay adopts synced migration 27 temp", func(t *testing.T) {
		fixture := newEvidenceV2Fixture(t, "Migration 27 Evidence Recovery")
		payload := strings.Repeat("migration-27-terminal-proof-", 4096)
		fixture.writeSource(t, "migration-27.txt", payload)
		request := blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: "retain-migration-27", Key: "evidence:migration-27", Attempt: "attempt:evidence",
			SourcePath: "migration-27.txt", ArtifactType: "text", Summary: "Recover migration 27 private staging",
		}
		failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
			RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
			Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
		})
		if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
			t.Fatal("migration 27 fixture publication unexpectedly succeeded")
		}
		var currentTemp, finalPath, requestHash string
		if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path,request_hash FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&currentTemp, &finalPath, &requestHash); err != nil {
			t.Fatalf("read migration 27 fixture paths: %v", err)
		}
		projectRoot := strings.Split(finalPath, "/retained/")[0]
		migration27Temp := filepath.Join(projectRoot, ".evidence-staging", hex.EncodeToString([]byte(fixture.continuationID)), hex.EncodeToString([]byte(request.IdempotencyKey)), requestHash)
		absoluteMigration27 := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(migration27Temp))
		if err := os.MkdirAll(filepath.Dir(absoluteMigration27), 0o700); err != nil {
			t.Fatalf("create migration 27 staging hierarchy: %v", err)
		}
		if err := os.Rename(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(currentTemp)), absoluteMigration27); err != nil {
			t.Fatalf("move synced proof into migration 27 staging: %v", err)
		}
		if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path=?,migration27_temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, migration27Temp, fixture.projectID, fixture.continuationID, request.IdempotencyKey); err != nil {
			t.Fatalf("restore migration 27 request path: %v", err)
		}
		if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version=28`); err != nil {
			t.Fatalf("restore migration 27 ledger: %v", err)
		}
		if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-migration-27-attempt",
			Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "Attempt ended after migration 27 temp sync"}},
		}); err != nil {
			t.Fatalf("terminalize migration 27 Attempt: %v", err)
		}
		if err := os.Remove(filepath.Join(fixture.workdir, request.SourcePath)); err != nil {
			t.Fatalf("remove migration 27 source: %v", err)
		}
		if err := fixture.db.Close(); err != nil {
			t.Fatalf("close migration 27 fixture: %v", err)
		}
		reopened, err := store.Open(fixture.dbPath)
		if err != nil {
			t.Fatalf("upgrade migration 27 fixture: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		service := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
		if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
			t.Fatalf("terminal replay from migration 27 staging: %v", err)
		}
		var fixedTemp, recoveryTemp string
		if err := reopened.QueryRow(`SELECT temp_internal_path,migration27_temp_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&fixedTemp, &recoveryTemp); err != nil {
			t.Fatalf("read migration 28 staging paths: %v", err)
		}
		if recoveryTemp != migration27Temp || fixedTemp == migration27Temp || !strings.Contains(fixedTemp, "/.evidence-staging/") {
			t.Fatalf("migration 28 paths = fixed %q recovery %q", fixedTemp, recoveryTemp)
		}
		if _, err := os.Lstat(absoluteMigration27); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration 27 temp survived adoption: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath)))
		if err != nil || string(got) != payload {
			t.Fatalf("migration 27 recovered payload size=%d err=%v", len(got), err)
		}
	})

	t.Run("impossible migration 27 path falls back without poisoning reservation", func(t *testing.T) {
		fixture := newEvidenceV2Fixture(t, "Migration 27 Long Path Recovery")
		payload := strings.Repeat("migration-27-live-source-", 1024)
		fixture.writeSource(t, "migration-27-long.txt", payload)
		longKey := strings.Repeat("migration-27-long-key-", 128)
		request := blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: longKey, Key: "evidence:migration-27-long", Attempt: "attempt:evidence",
			SourcePath: "migration-27-long.txt", ArtifactType: "text", Summary: "Ignore impossible migration 27 staging path",
		}
		failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
			RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
			Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureBeforeTempCopy},
		})
		if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
			t.Fatal("long migration 27 fixture unexpectedly succeeded")
		}
		var currentTemp, finalPath, requestHash string
		if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path,request_hash FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, longKey).Scan(&currentTemp, &finalPath, &requestHash); err != nil {
			t.Fatalf("read long migration 27 fixture paths: %v", err)
		}
		projectRoot := strings.Split(finalPath, "/retained/")[0]
		impossibleTemp := filepath.Join(projectRoot, ".evidence-staging", hex.EncodeToString([]byte(fixture.continuationID)), hex.EncodeToString([]byte(longKey)), requestHash)
		if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path=?,migration27_temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, impossibleTemp, fixture.projectID, fixture.continuationID, longKey); err != nil {
			t.Fatalf("restore impossible migration 27 path: %v", err)
		}
		if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version=28`); err != nil {
			t.Fatalf("restore long migration 27 ledger: %v", err)
		}
		if err := fixture.db.Close(); err != nil {
			t.Fatalf("close long migration 27 fixture: %v", err)
		}
		reopened, err := store.Open(fixture.dbPath)
		if err != nil {
			t.Fatalf("upgrade long migration 27 fixture: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		service := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
		if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
			t.Fatalf("retry with impossible migration 27 path: %v", err)
		}
		var fixedTemp, recoveryTemp string
		if err := reopened.QueryRow(`SELECT temp_internal_path,migration27_temp_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, longKey).Scan(&fixedTemp, &recoveryTemp); err != nil {
			t.Fatalf("read long migration 28 staging paths: %v", err)
		}
		if fixedTemp != currentTemp || recoveryTemp != impossibleTemp {
			t.Fatalf("long migration paths = fixed %q recovery %q, want %q and impossible hint", fixedTemp, recoveryTemp, currentTemp)
		}
		for _, component := range strings.Split(filepath.ToSlash(fixedTemp), "/") {
			if len(component) > 64 {
				t.Fatalf("migrated fixed staging component length = %d in %q", len(component), fixedTemp)
			}
		}

		fixture.writeSource(t, "migration-27-cleanup.txt", payload)
		cleanupKey := strings.Repeat("migration-27-cleanup-key-", 128)
		cleanupRequest := blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: cleanupKey, Key: "evidence:migration-27-cleanup", Attempt: "attempt:evidence",
			SourcePath: "migration-27-cleanup.txt", ArtifactType: "text", Summary: "Cleanup ignores impossible migration 27 staging path",
		}
		cleanupFailure := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
			RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
			Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureBeforeTempCopy},
		})
		if _, err := cleanupFailure.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, cleanupRequest); err == nil {
			t.Fatal("migration 27 cleanup fixture unexpectedly succeeded")
		}
		var cleanupTemp, cleanupFinal, cleanupHash string
		if err := reopened.QueryRow(`SELECT temp_internal_path,managed_internal_path,request_hash FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, cleanupKey).Scan(&cleanupTemp, &cleanupFinal, &cleanupHash); err != nil {
			t.Fatalf("read migration 27 cleanup reservation: %v", err)
		}
		cleanupProjectRoot := strings.Split(cleanupFinal, "/retained/")[0]
		cleanupImpossible := filepath.Join(cleanupProjectRoot, ".evidence-staging", hex.EncodeToString([]byte(fixture.continuationID)), hex.EncodeToString([]byte(cleanupKey)), cleanupHash)
		if _, err := reopened.Exec(`UPDATE blackboard_v2_evidence_requests SET migration27_temp_internal_path=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, cleanupImpossible, fixture.projectID, fixture.continuationID, cleanupKey); err != nil {
			t.Fatalf("record impossible migration 27 cleanup hint: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, cleanupRequest.SourcePath), []byte("changed after reservation\n"), 0o600); err != nil {
			t.Fatalf("change migration 27 cleanup source: %v", err)
		}
		if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, cleanupRequest); !isSemanticCode(err, "evidence_source_changed") {
			t.Fatalf("migration 27 cleanup error = %#v, want evidence_source_changed", err)
		}
		var cleanupRequests int
		if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, cleanupKey).Scan(&cleanupRequests); err != nil {
			t.Fatalf("count migration 27 cleanup reservation: %v", err)
		}
		if cleanupRequests != 0 {
			t.Fatalf("migration 27 cleanup reservation count = %d, want zero", cleanupRequests)
		}
		if _, err := os.Lstat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(cleanupTemp))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration 27 cleanup left fixed temp: %v", err)
		}
	})
}
