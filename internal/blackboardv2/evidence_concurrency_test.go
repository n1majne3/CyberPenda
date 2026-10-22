package blackboardv2_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestConcurrentExactRetainRetryCannotReplaceActivePublisherTemp(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Exact Evidence Publisher")
	payload := strings.Repeat("inode-bound-publication-proof-", 8192)
	fixture.writeSource(t, "overlap.txt", payload)
	barrier := &evidenceMidCopyBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier,
	})
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-overlapping-exact", Key: "evidence:overlapping-exact", Attempt: "attempt:evidence",
		SourcePath: "overlap.txt", ArtifactType: "text", Summary: "Concurrent exact retries share one publisher",
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
		firstResult <- err
	}()
	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("first publisher did not reach controlled mid-copy boundary")
	}
	secondResult := make(chan error, 1)
	go func() {
		_, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
		secondResult <- err
	}()
	var secondErr error
	select {
	case secondErr = <-secondResult:
	case <-time.After(5 * time.Second):
		close(barrier.release)
		t.Fatal("overlapping exact retry did not return while publisher was active")
	}
	var retryable *blackboardv2.Error
	if !errors.As(secondErr, &retryable) || retryable.Code != "evidence_publication_in_progress" || !retryable.Retryable {
		close(barrier.release)
		t.Fatalf("overlapping exact retry error = %#v, want retryable publisher claim", secondErr)
	}
	var tempPath, publisherToken, publisherIdentity string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path,publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath, &publisherToken, &publisherIdentity); err != nil {
		close(barrier.release)
		t.Fatalf("read active publisher temp path: %v", err)
	}
	if publisherToken == "" || publisherIdentity == "" {
		close(barrier.release)
		t.Fatalf("active publisher claim = token %q identity %q, want durable owner", publisherToken, publisherIdentity)
	}
	partial, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath)))
	if err != nil || partial.Size() == 0 || partial.Size() >= int64(len(payload)) {
		close(barrier.release)
		t.Fatalf("active publisher temp size=%v err=%v", func() int64 {
			if partial == nil {
				return -1
			}
			return partial.Size()
		}(), err)
	}
	close(barrier.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("active publisher failed after overlapping retry: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&publisherToken, &publisherIdentity); err != nil {
		t.Fatalf("read completed publisher claim: %v", err)
	}
	if publisherToken != "" || publisherIdentity != "" {
		t.Fatalf("completed publisher claim = token %q identity %q, want cleared", publisherToken, publisherIdentity)
	}
	if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
		t.Fatalf("exact replay after active publisher completed: %v", err)
	}
	files := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(files) != 1 {
		t.Fatalf("overlapping exact retry payloads = %v, want one final", files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil || string(got) != payload {
		t.Fatalf("overlapping exact retry final size=%d err=%v", len(got), err)
	}
}

func TestLongIdempotencyKeyUsesFixedLengthStagingForRetryAndCleanup(t *testing.T) {
	for _, mode := range []string{"retry", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Long Evidence Idempotency "+mode)
			payload := strings.Repeat("long-idempotency-proof-", 1024)
			fixture.writeSource(t, "long-key.txt", payload)
			longKey := strings.Repeat("long-valid-idempotency-key-", 512) + mode
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: longKey, Key: "evidence:long-key-" + mode, Attempt: "attempt:evidence",
				SourcePath: "long-key.txt", ArtifactType: "text", Summary: "Long idempotency key stays outside filesystem names",
			}
			point := blackboardv2.EvidenceFailureAfterTempDirectorySync
			if mode == "cleanup" {
				point = blackboardv2.EvidenceFailureBeforeTempCopy
			}
			failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
				Failures: &failEvidenceV2Once{point: point},
			})
			if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("long-key staging crash unexpectedly succeeded")
			}
			var tempPath string
			if err := fixture.db.QueryRow(`SELECT temp_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, longKey).Scan(&tempPath); err != nil {
				t.Fatalf("read long-key staging reservation: %v", err)
			}
			components := strings.Split(filepath.ToSlash(tempPath), "/")
			for _, component := range components {
				if len(component) > 64 {
					t.Fatalf("long-key staging component length = %d in %q", len(component), tempPath)
				}
			}
			if strings.Contains(tempPath, longKey[:128]) {
				t.Fatalf("long idempotency key leaked into staging path %q", tempPath)
			}
			if mode == "cleanup" {
				if err := os.WriteFile(filepath.Join(fixture.workdir, "long-key.txt"), []byte("changed source after durable reservation\n"), 0o600); err != nil {
					t.Fatalf("change long-key source: %v", err)
				}
				service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
				if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "evidence_source_changed") {
					t.Fatalf("long-key cleanup error = %#v, want evidence_source_changed", err)
				}
				var requestCount int
				if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, longKey).Scan(&requestCount); err != nil {
					t.Fatalf("count cleaned long-key reservation: %v", err)
				}
				if requestCount != 0 {
					t.Fatalf("cleaned long-key reservation count = %d, want zero", requestCount)
				}
				if _, err := os.Lstat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("long-key cleanup left staging temp: %v", err)
				}
				return
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close long-key retry store: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("restart long-key retry store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
				t.Fatalf("retry long-key Evidence reservation: %v", err)
			}
		})
	}
}

func TestConcurrentEvidenceReservationAtomicallyRejectsConflictingSemantics(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Evidence Reservation")
	fixture.writeSource(t, "race-a.txt", "reservation A\n")
	fixture.writeSource(t, "race-b.txt", "reservation B\n")
	barrier := newEvidenceReservationBarrier(2)
	serviceA := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier})
	serviceB := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier})
	requests := []blackboardv2.RetainEvidenceRequest{
		{IdempotencyKey: "reservation-race", Key: "evidence:race-a", Attempt: "attempt:evidence", SourcePath: "race-a.txt", ArtifactType: "text", Summary: "Reservation A"},
		{IdempotencyKey: "reservation-race", Key: "evidence:race-b", Attempt: "attempt:evidence", SourcePath: "race-b.txt", ArtifactType: "text", Summary: "Reservation B"},
	}
	services := []*blackboardv2.Service{serviceA, serviceB}
	type outcome struct {
		index  int
		result blackboardv2.ChangeResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for index := range requests {
		go func(index int) {
			result, err := services[index].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[index])
			outcomes <- outcome{index: index, result: result, err: err}
		}(index)
	}
	first, second := <-outcomes, <-outcomes
	results := []outcome{first, second}
	winner := -1
	for _, result := range results {
		if result.err == nil {
			if winner != -1 {
				t.Fatalf("both conflicting reservations succeeded: %#v", results)
			}
			winner = result.index
			continue
		}
		if !isSemanticCode(result.err, "idempotency_conflict") {
			t.Fatalf("reservation loser error = %#v, want idempotency_conflict", result.err)
		}
	}
	if winner == -1 {
		t.Fatalf("neither conflicting reservation succeeded: %#v", results)
	}
	if _, err := services[winner].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[winner]); err != nil {
		t.Fatalf("winner exact replay was poisoned: %v", err)
	}
	loser := 1 - winner
	if _, err := services[loser].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[loser]); !isSemanticCode(err, "idempotency_conflict") {
		t.Fatalf("loser replay error = %#v, want idempotency_conflict", err)
	}
}

func TestRetainEvidenceConfinesSourcesAndRejectsSymlinkAndReplacementRaces(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Source Confinement")
	outside := filepath.Join(filepath.Dir(fixture.runtimeRoot), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	fixture.writeSource(t, "inside.txt", "inside\n")
	if err := os.Symlink(outside, filepath.Join(fixture.workdir, "escape.txt")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(fixture.workdir, "inside-link.txt")); err != nil {
		t.Fatalf("create in-root symlink: %v", err)
	}
	realDirectory := filepath.Join(fixture.workdir, "real-directory")
	if err := os.MkdirAll(realDirectory, 0o700); err != nil {
		t.Fatalf("create real source directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "nested.txt"), []byte("nested\n"), 0o600); err != nil {
		t.Fatalf("write nested source: %v", err)
	}
	if err := os.Symlink("real-directory", filepath.Join(fixture.workdir, "linked-directory")); err != nil {
		t.Fatalf("create relative directory symlink: %v", err)
	}
	otherTaskSource := filepath.Join(fixture.runtimeRoot, "another-task", "workdir", "proof.txt")
	if err := os.MkdirAll(filepath.Dir(otherTaskSource), 0o700); err != nil {
		t.Fatalf("create other Task root: %v", err)
	}
	if err := os.WriteFile(otherTaskSource, []byte("other task\n"), 0o600); err != nil {
		t.Fatalf("write other Task source: %v", err)
	}
	for name, sourcePath := range map[string]string{
		"traversal":         "../../outside.txt",
		"escaping-symlink":  "escape.txt",
		"in-root-symlink":   "inside-link.txt",
		"directory-symlink": filepath.Join("linked-directory", "nested.txt"),
		"other-task":        otherTaskSource,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "forbidden-" + name, Key: "evidence:forbidden-" + name, Attempt: "attempt:evidence",
				SourcePath: sourcePath, ArtifactType: "text", Summary: "Forbidden source must not be retained",
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "evidence_source_forbidden" {
				t.Fatalf("forbidden source error = %#v, want evidence_source_forbidden", err)
			}
		})
	}
	assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)

	replacePath := filepath.Join(fixture.workdir, "replace.txt")
	if err := os.WriteFile(replacePath, []byte("original bytes\n"), 0o600); err != nil {
		t.Fatalf("write replacement-race source: %v", err)
	}
	fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: replaceEvidenceV2Source{path: replacePath},
	})
	_, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "replacement-race", Key: "evidence:replacement-race", Attempt: "attempt:evidence",
		SourcePath: "replace.txt", ArtifactType: "text", Summary: "Only original bytes may be retained",
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "evidence_source_changed" {
		t.Fatalf("replacement race error = %#v, want evidence_source_changed", err)
	}
	if _, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:replacement-race"); !isSemanticCode(err, "not_found") {
		t.Fatalf("replacement race Evidence read error = %#v, want not_found", err)
	}
	assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)
}
