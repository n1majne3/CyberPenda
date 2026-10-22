package blackboardv2_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestV24UpgradeRecoversLegacyTempBeforeChangedOrMissingSourceValidation(t *testing.T) {
	for index, sourceState := range []string{"changed", "missing", "changed-invalid-legacy", "partial-valid-source", "partial-terminal-missing"} {
		t.Run(sourceState, func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "V24 Source Recovery "+sourceState)
			payload := strings.Repeat("v24-source-independent-proof-", 2048)
			fixture.writeSource(t, "source.txt", payload)
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-v24-source-" + sourceState, Key: "evidence:v24-source-" + sourceState, Attempt: "attempt:evidence",
				SourcePath: "source.txt", ArtifactType: "text", Summary: "Recover v24 proof before Runtime source validation",
			}
			failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
				Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
			})
			if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("v24 source fixture publication crash unexpectedly succeeded")
			}
			var tempPath, finalPath string
			if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath, &finalPath); err != nil {
				t.Fatalf("read v24 source fixture paths: %v", err)
			}
			absoluteTemp := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))
			legacyTemp := filepath.Join(filepath.Dir(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath))), fmt.Sprintf(".retain-%024x", index+1))
			if err := os.Rename(absoluteTemp, legacyTemp); err != nil {
				t.Fatalf("convert source fixture to v24 temp: %v", err)
			}
			if sourceState == "changed-invalid-legacy" || strings.HasPrefix(sourceState, "partial-") {
				if err := os.Chmod(legacyTemp, 0o600); err != nil {
					t.Fatalf("make corrupt v24 temp writable: %v", err)
				}
				if sourceState == "changed-invalid-legacy" {
					if err := os.WriteFile(legacyTemp, []byte("corrupt legacy proof bytes\n"), 0o600); err != nil {
						t.Fatalf("corrupt v24 legacy temp: %v", err)
					}
				} else if err := os.Truncate(legacyTemp, int64(len(payload)/2)); err != nil {
					t.Fatalf("truncate v24 legacy temp: %v", err)
				}
			}
			if sourceState == "partial-terminal-missing" {
				if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
					Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-partial-v24-source",
					Changes: []blackboardv2.Change{{
						Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "Attempt ended with only a partial v24 temp",
					}},
				}); err != nil {
					t.Fatalf("terminalize partial v24 source Attempt: %v", err)
				}
			}
			if _, err := fixture.db.Exec(`DROP TABLE blackboard_v2_evidence_payloads`); err != nil {
				t.Fatalf("remove post-v24 payload claims: %v", err)
			}
			if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey); err != nil {
				t.Fatalf("restore v24 source request shape: %v", err)
			}
			if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version>=25`); err != nil {
				t.Fatalf("restore v24 source migration ledger: %v", err)
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close v24 source fixture: %v", err)
			}
			sourcePath := filepath.Join(fixture.workdir, "source.txt")
			if strings.HasPrefix(sourceState, "changed") {
				if err := os.WriteFile(sourcePath, []byte("changed Runtime source bytes\n"), 0o600); err != nil {
					t.Fatalf("change Runtime source before upgrade retry: %v", err)
				}
			} else if sourceState != "partial-valid-source" {
				if err := os.Remove(sourcePath); err != nil {
					t.Fatalf("remove Runtime source before upgrade retry: %v", err)
				}
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("upgrade v24 source fixture: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			_, retainErr := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			unrecoverable := sourceState == "changed-invalid-legacy" || sourceState == "partial-terminal-missing"
			if unrecoverable {
				if !isSemanticCode(retainErr, "evidence_source_changed") {
					t.Fatalf("unrecoverable v24 retry error = %#v, want evidence_source_changed", retainErr)
				}
			} else if retainErr != nil {
				t.Fatalf("recover v24 temp before %s source validation: %v", sourceState, retainErr)
			}
			if _, err := os.Lstat(legacyTemp); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("v24 %s source retry left legacy temp: %v", sourceState, err)
			}
			if unrecoverable {
				var requestCount int
				if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&requestCount); err != nil {
					t.Fatalf("count cleaned unrecoverable v24 request: %v", err)
				}
				if requestCount != 0 {
					t.Fatalf("unrecoverable v24 request count = %d, want zero", requestCount)
				}
				if _, err := os.Lstat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unrecoverable v24 retry left deterministic temp: %v", err)
				}
				return
			}
			finalBytes, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath)))
			if err != nil || string(finalBytes) != payload {
				t.Fatalf("v24 %s source final size=%d err=%v", sourceState, len(finalBytes), err)
			}
		})
	}
}
