package blackboard_test

import (
	"errors"
	"testing"

	"pentest/internal/blackboard"
)

// TestUpsertFindingCreatesWithCVSSPending proves a finding can be recorded with
// CVSS pending (no vector yet).
func TestUpsertFindingCreatesWithCVSSPending(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	created, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:  projectID,
		FindingKey: "sqli-login",
		Title:      "SQL injection in login",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !created.CVSSPending {
		t.Fatal("expected CVSS pending when no vector supplied")
	}
	if created.Severity != "pending" {
		t.Fatalf("expected severity pending, got %q", created.Severity)
	}
	if created.Status != blackboard.FindingStatusUnconfirmed {
		t.Fatalf("expected default unconfirmed status, got %q", created.Status)
	}
}

func TestUpsertFindingRejectsMissingKey(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{ProjectID: projectID, Title: "x"})
	if !errors.Is(err, blackboard.ErrMissingFindingKey) {
		t.Fatalf("expected ErrMissingFindingKey, got %v", err)
	}
}

func TestUpsertFindingNewFindingRequiresTitle(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{ProjectID: projectID, FindingKey: "k"})
	if !errors.Is(err, blackboard.ErrMissingFindingTitle) {
		t.Fatalf("expected ErrMissingFindingTitle, got %v", err)
	}
}

// TestUpsertFindingByKeyOverwritesAndIncrementsVersion proves the Slice 5
// acceptance: finding upsert by key overwrites the current finding and
// preserves (increments) the finding version.
func TestUpsertFindingByKeyOverwritesAndIncrementsVersion(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "k", Title: "v1",
	}); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	updated, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "k", Title: "v2",
	})
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if updated.Title != "v2" {
		t.Fatalf("expected overwritten title, got %q", updated.Title)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Version)
	}

	versions, err := bb.FindingVersions(projectID, "k")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
}

// TestUpsertConfirmedFindingRequiresCompleteFields proves the Slice 7
// acceptance: a confirmed finding requires CVSS vector, target, proof, impact,
// and recommendation.
func TestUpsertConfirmedFindingRequiresCompleteFields(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	// Confirmed with only a vector -> incomplete.
	_, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "k", Title: "t",
		Status:     blackboard.FindingStatusConfirmed,
		CVSSVector: "CVSS:4.0/AV:N/AC:L",
	})
	if !errors.Is(err, blackboard.ErrConfirmedFindingIncomplete) {
		t.Fatalf("expected ErrConfirmedFindingIncomplete, got %v", err)
	}

	// Confirmed with all required fields -> succeeds.
	created, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:      projectID,
		FindingKey:     "k",
		Title:          "t",
		Status:         blackboard.FindingStatusConfirmed,
		Target:         "login endpoint",
		Proof:          "screenshots and request",
		Impact:         "auth bypass",
		Recommendation: "parameterize query",
		CVSSVector:     "CVSS:4.0/AV:N/AC:L/VC:H/VI:H",
	})
	if err != nil {
		t.Fatalf("expected complete confirmed finding to succeed, got: %v", err)
	}
	if created.CVSSPending {
		t.Fatal("expected CVSS not pending once vector supplied")
	}
}

// TestSeverityDerivedFromCVSSVector proves the Slice 7 acceptance: finding
// severity is derived from CVSS data.
func TestSeverityDerivedFromCVSSVector(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	// Two high-impact metrics -> critical.
	critical, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "k1", Title: "t",
		Status:     blackboard.FindingStatusConfirmed,
		Target:     "x", Proof: "p", Impact: "i", Recommendation: "r",
		CVSSVector: "CVSS:4.0/AV:N/VC:H/VI:H",
	})
	if err != nil {
		t.Fatalf("upsert critical: %v", err)
	}
	if critical.Severity != "critical" {
		t.Fatalf("expected severity critical for two highs, got %q", critical.Severity)
	}

	// One high-impact metric -> high.
	high, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "k2", Title: "t",
		Status:     blackboard.FindingStatusConfirmed,
		Target:     "x", Proof: "p", Impact: "i", Recommendation: "r",
		CVSSVector: "CVSS:4.0/AV:N/VC:H",
	})
	if err != nil {
		t.Fatalf("upsert high: %v", err)
	}
	if high.Severity != "high" {
		t.Fatalf("expected severity high for one high, got %q", high.Severity)
	}
}
