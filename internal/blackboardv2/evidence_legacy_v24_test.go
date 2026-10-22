package blackboardv2_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestV24UpgradeAdoptsSyncedLegacyTempForTerminalAttemptReplay(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "V24 Evidence Temp Upgrade")
	payload := strings.Repeat("legacy-v24-synced-proof-", 4096)
	fixture.writeSource(t, "legacy.txt", payload)
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-v24-upgrade", Key: "evidence:v24-upgrade", Attempt: "attempt:evidence",
		SourcePath: "legacy.txt", ArtifactType: "text", Summary: "Legacy v24 crash payload",
	}
	failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
	})
	if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("v24 fixture publication crash unexpectedly succeeded")
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-v24-upgrade-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "Attempt ended after v24 temp sync",
		}},
	}); err != nil {
		t.Fatalf("terminalize v24 producing Attempt: %v", err)
	}
	var tempPath, finalPath string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath, &finalPath); err != nil {
		t.Fatalf("read v24 fixture publication paths: %v", err)
	}
	absoluteTemp := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))
	legacyTemp := filepath.Join(filepath.Dir(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath))), ".retain-00112233445566778899aabb")
	if err := os.Rename(absoluteTemp, legacyTemp); err != nil {
		t.Fatalf("convert deterministic temp to v24 random temp: %v", err)
	}
	currentVersionTemp := filepath.Join(filepath.Dir(absoluteTemp), filepath.Base(finalPath)+".stage-active-current-version")
	if err := os.WriteFile(currentVersionTemp, []byte("active current-version publisher bytes"), 0o600); err != nil {
		t.Fatalf("write current-version temp sweep sentinel: %v", err)
	}
	directory, err := os.Open(filepath.Dir(legacyTemp))
	if err != nil {
		t.Fatalf("open v24 temp directory: %v", err)
	}
	// Directory fsync is unsupported on Windows (golang/go#47366), the same
	// constraint the durability sync helpers work around.
	if runtime.GOOS != "windows" {
		if err := directory.Sync(); err != nil {
			_ = directory.Close()
			t.Fatalf("sync v24 temp rename: %v", err)
		}
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close v24 temp directory: %v", err)
	}
	if _, err := fixture.db.Exec(`DROP TABLE blackboard_v2_evidence_payloads`); err != nil {
		t.Fatalf("remove post-v24 payload claims: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey); err != nil {
		t.Fatalf("restore v24 request shape: %v", err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version>=25`); err != nil {
		t.Fatalf("restore v24 migration ledger: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close v24 crash fixture: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.workdir, "legacy.txt")); err != nil {
		t.Fatalf("remove v24 source before terminal replay: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("upgrade v24 crash fixture: %v", err)
	}
	adopting := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
	})
	if _, err := adopting.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("adopted v24 temp fsync crash unexpectedly succeeded")
	}
	if _, err := os.Lstat(legacyTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v24 adoption left legacy temp after fsync: %v", err)
	}
	if info, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))); err != nil || info.Size() != int64(len(payload)) {
		t.Fatalf("adopted deterministic temp size=%v err=%v", func() int64 {
			if info == nil {
				return -1
			}
			return info.Size()
		}(), err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close upgraded store after adopted temp fsync: %v", err)
	}
	reopened, err = store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("restart after adopted v24 temp fsync: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
	result, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("terminal replay from upgraded v24 temp: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{request.Key, float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upgraded v24 replay records = %#v, want %#v", got, want)
	}
	for _, path := range []string{legacyTemp, filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("upgraded v24 replay left temp %q: %v", path, err)
		}
	}
	currentBytes, err := os.ReadFile(currentVersionTemp)
	if err != nil || string(currentBytes) != "active current-version publisher bytes" {
		t.Fatalf("v24 sweep changed current-version temp = %q err=%v", currentBytes, err)
	}
	if err := os.Remove(currentVersionTemp); err != nil {
		t.Fatalf("remove current-version temp sweep sentinel: %v", err)
	}
	finalBytes, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath)))
	if err != nil || string(finalBytes) != payload {
		t.Fatalf("upgraded v24 final size=%d err=%v", len(finalBytes), err)
	}
	history, err := restarted.ReadHistory(context.Background(), fixture.projectID, request.Key, blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read upgraded v24 Evidence history: %v", err)
	}
	foundProduced := false
	for _, item := range history.Items {
		foundProduced = foundProduced || (item.Kind == "relationship" && item.From == request.Attempt && item.Relation == "produced" && item.To == request.Key)
	}
	if !foundProduced {
		t.Fatalf("upgraded v24 Evidence lacks produced history: %#v", history.Items)
	}
}

func TestV24SharedLegacyProofSurvivesBothTerminalReplayOrders(t *testing.T) {
	for _, candidateCount := range []int{1, 2} {
		for _, order := range [][]int{{0, 1}, {1, 0}} {
			name := fmt.Sprintf("%d-candidate-%d-then-%d", candidateCount, order[0], order[1])
			t.Run(name, func(t *testing.T) {
				fixture := newEvidenceV2Fixture(t, "V24 Shared Legacy "+name)
				payload := strings.Repeat("shared-v24-terminal-proof-", 4096)
				requests := []blackboardv2.RetainEvidenceRequest{
					{IdempotencyKey: "retain-v24-shared-a", Key: "evidence:v24-shared-a", Attempt: "attempt:evidence", SourcePath: "shared-a.txt", ArtifactType: "text", Summary: "First shared v24 proof"},
					{IdempotencyKey: "retain-v24-shared-b", Key: "evidence:v24-shared-b", Attempt: "attempt:evidence", SourcePath: "shared-b.txt", ArtifactType: "text", Summary: "Second shared v24 proof"},
				}
				legacyPaths := make([]string, len(requests))
				finalPaths := make([]string, len(requests))
				tempPaths := make([]string, len(requests))
				for index, request := range requests {
					fixture.writeSource(t, request.SourcePath, payload)
					failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
						RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
						Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
					})
					if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
						t.Fatalf("shared v24 reservation %d unexpectedly published", index)
					}
					if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPaths[index], &finalPaths[index]); err != nil {
						t.Fatalf("read shared v24 reservation %d: %v", index, err)
					}
				}
				for index := range requests {
					legacyPaths[index] = filepath.Join(filepath.Dir(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPaths[index]))), fmt.Sprintf(".retain-%024x", index+101))
					if err := os.Rename(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPaths[index])), legacyPaths[index]); err != nil {
						t.Fatalf("convert shared reservation %d to v24 temp: %v", index, err)
					}
				}
				if candidateCount == 1 {
					if err := os.Remove(legacyPaths[1]); err != nil {
						t.Fatalf("remove duplicate shared v24 candidate: %v", err)
					}
				}
				if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
					Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-shared-v24-attempt-" + name,
					Changes: []blackboardv2.Change{{
						Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "Attempt ended with two shared v24 proof reservations",
					}},
				}); err != nil {
					t.Fatalf("terminalize shared v24 Attempt: %v", err)
				}
				if _, err := fixture.db.Exec(`DROP TABLE blackboard_v2_evidence_payloads`); err != nil {
					t.Fatalf("remove post-v24 shared payload claims: %v", err)
				}
				if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path='',previous_temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=?`, fixture.projectID); err != nil {
					t.Fatalf("restore shared v24 request shape: %v", err)
				}
				if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version>=25`); err != nil {
					t.Fatalf("restore shared v24 migration ledger: %v", err)
				}
				if err := fixture.db.Close(); err != nil {
					t.Fatalf("close shared v24 fixture: %v", err)
				}
				for _, request := range requests {
					if err := os.Remove(filepath.Join(fixture.workdir, request.SourcePath)); err != nil {
						t.Fatalf("remove shared v24 source %q: %v", request.SourcePath, err)
					}
				}
				reopened, err := store.Open(fixture.dbPath)
				if err != nil {
					t.Fatalf("upgrade shared v24 fixture: %v", err)
				}
				service := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
				var unresolved int
				if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND source_sha256=(SELECT source_sha256 FROM blackboard_v2_evidence_requests WHERE project_id=? AND idempotency_key=? LIMIT 1) AND status<>'completed'`, fixture.projectID, fixture.projectID, requests[order[0]].IdempotencyKey).Scan(&unresolved); err != nil {
					t.Fatalf("count unresolved shared v24 reservations: %v", err)
				}
				if unresolved != 2 {
					t.Fatalf("unresolved shared v24 reservations = %d, want two", unresolved)
				}
				if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[order[0]]); err != nil {
					t.Fatalf("first shared v24 replay %d: %v", order[0], err)
				}
				remaining := 0
				for _, legacyPath := range legacyPaths {
					if _, err := os.Lstat(legacyPath); err == nil {
						remaining++
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("inspect shared v24 temp: %v", err)
					}
				}
				if remaining != 1 {
					t.Fatalf("legacy candidates after first replay = %d, want one for unresolved request", remaining)
				}
				if err := reopened.Close(); err != nil {
					t.Fatalf("close between shared v24 replay order: %v", err)
				}
				reopened, err = store.Open(fixture.dbPath)
				if err != nil {
					t.Fatalf("restart between shared v24 replays: %v", err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				service = blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
				if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[order[1]]); err != nil {
					t.Fatalf("second shared v24 replay %d: %v", order[1], err)
				}
				for index, finalPath := range finalPaths {
					got, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath)))
					if err != nil || string(got) != payload {
						t.Fatalf("shared v24 final %d size=%d err=%v", index, len(got), err)
					}
				}
				for _, legacyPath := range legacyPaths {
					if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("shared v24 replay left legacy temp %q: %v", legacyPath, err)
					}
				}
			})
		}
	}
}
