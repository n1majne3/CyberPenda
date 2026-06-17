package blackboard_test

import (
	"errors"
	"strings"
	"testing"

	"pentest/internal/blackboard"
)

func TestAttachEvidenceToFactCreatesManagedArtifact(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "s",
	}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}

	artifact, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    projectID,
		EvidenceKey:  "ev-1",
		AttachToType: blackboard.EvidenceAttachFact,
		AttachToKey:  "k",
		ArtifactType: "screenshot",
		SourcePath:   "/task/artifacts/shot.png",
		SHA256:       "abc123",
		Summary:      "proof screenshot",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if artifact.ID == "" {
		t.Fatal("expected artifact id")
	}
	// The managed path references a managed artifact root, not the raw runtime
	// workdir source path.
	if artifact.ManagedPath == "" {
		t.Fatal("expected managed path")
	}
	if artifact.ManagedPath == artifact.SourcePath {
		t.Fatal("managed path must differ from raw workdir source path")
	}
}

func TestAttachEvidenceToFindingCreatesManagedArtifact(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID: projectID, FindingKey: "f1", Title: "title",
	}); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	artifact, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    projectID,
		EvidenceKey:  "ev-2",
		AttachToType: blackboard.EvidenceAttachFinding,
		AttachToKey:  "f1",
		ArtifactType: "har",
		SourcePath:   "/task/artifacts/req.har",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !strings.Contains(artifact.ManagedPath, "ev-2") {
		t.Fatalf("expected managed path to encode evidence key, got %q", artifact.ManagedPath)
	}
}

func TestAttachEvidenceRejectsMissingKey(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID: projectID,
		AttachToType: blackboard.EvidenceAttachFact,
	})
	if !errors.Is(err, blackboard.ErrMissingEvidenceKey) {
		t.Fatalf("expected ErrMissingEvidenceKey, got %v", err)
	}
}

// TestAttachEvidenceRejectsUnknownTarget proves evidence cannot attach to a
// non-existent fact/finding: runtime workdir files do not become evidence
// against phantom targets.
func TestAttachEvidenceRejectsUnknownTarget(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    projectID,
		EvidenceKey:  "ev-3",
		AttachToType: blackboard.EvidenceAttachFact,
		AttachToKey:  "no-such-fact",
		ArtifactType: "screenshot",
	})
	if !errors.Is(err, blackboard.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown target, got %v", err)
	}
}

func TestAttachEvidenceRejectsUnsupportedTargetType(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    projectID,
		EvidenceKey:  "ev-4",
		AttachToType: "bogus-type",
		AttachToKey:  "x",
		ArtifactType: "screenshot",
	})
	if !errors.Is(err, blackboard.ErrUnsupportedEvidenceTarget) {
		t.Fatalf("expected ErrUnsupportedEvidenceTarget, got %v", err)
	}
}
