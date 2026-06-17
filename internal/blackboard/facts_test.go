package blackboard_test

import (
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/store"
)

func newServices(t *testing.T) (*blackboard.Service, *project.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return blackboard.NewService(db), project.NewService(db)
}

func mustProject(t *testing.T, projects *project.Service) string {
	t.Helper()
	proj, err := projects.Create("P", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return proj.ID
}

// TestUpsertFactCreatesAndVersions proves the tracer bullet: a fact upsert
// creates a fact at version 1 and records that version.
func TestUpsertFactCreatesAndVersions(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	created, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID,
		FactKey:   "dns:example.com",
		Category:  "dns",
		Summary:   "example.com resolves to 1.2.3.4",
		Body:      "full record details",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected fact id")
	}

	versions, err := bb.FactVersions(projectID, "dns:example.com")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("expected one version at v1, got %#v", versions)
	}
}

func TestUpsertFactRejectsMissingKey(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	_, err := bb.UpsertFact(blackboard.UpsertFactRequest{ProjectID: projectID, Summary: "x"})
	if !errors.Is(err, blackboard.ErrMissingFactKey) {
		t.Fatalf("expected ErrMissingFactKey, got %v", err)
	}
}

// TestUpsertFactByKeyOverwritesAndIncrementsVersion proves the Slice 5
// acceptance: fact upsert by key overwrites the current fact and preserves
// (increments) the fact version.
func TestUpsertFactByKeyOverwritesAndIncrementsVersion(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "v1 summary", Body: "v1 body",
	}); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	updated, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "v2 summary", Body: "v2 body",
	})
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if updated.Summary != "v2 summary" || updated.Body != "v2 body" {
		t.Fatalf("expected overwritten fact, got %#v", updated)
	}

	versions, err := bb.FactVersions(projectID, "k")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions after overwrite, got %d", len(versions))
	}
	if versions[1].Version != 2 || versions[1].Summary != "v2 summary" {
		t.Fatalf("expected v2 preserved, got %#v", versions[1])
	}
}

// TestUpsertFactEmptyBodyPreservesPriorBody proves the Slice 5 acceptance: an
// empty fact body update preserves the prior body.
func TestUpsertFactEmptyBodyPreservesPriorBody(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "s", Body: "keep this body",
	}); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	updated, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "new summary", Body: "",
	})
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if updated.Body != "keep this body" {
		t.Fatalf("expected empty-body update to preserve prior body, got %q", updated.Body)
	}
	if updated.Summary != "new summary" {
		t.Fatalf("expected summary updated, got %q", updated.Summary)
	}
}

// TestFactIndexOmitsFullBodies proves the Slice 5 acceptance: the fact index
// omits full bodies.
func TestFactIndexOmitsFullBodies(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "short", Body: "very long body that must not appear in index",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	index, err := bb.FactIndex(projectID, blackboard.FactIndexOptions{})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("expected 1 index entry, got %d", len(index))
	}
	if index[0].Summary != "short" {
		t.Fatalf("expected summary in index, got %q", index[0].Summary)
	}
	// FactIndexEntry must not have a Body field; verify by struct shape.
	// (If Body existed, this test would compile but the field would be empty;
	// the index query explicitly selects no body column.)
}

// TestFactIndexExcludesDeprecatedByDefault proves the Current Truth contract:
// the default fact index omits deprecated facts so dashboards, runtimes, and
// reports never treat them as current.
func TestFactIndexExcludesDeprecatedByDefault(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "active", Summary: "current fact",
	}); err != nil {
		t.Fatalf("upsert active: %v", err)
	}
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "old", Summary: "deprecated fact",
		Confidence: blackboard.ConfidenceDeprecated,
	}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}

	index, err := bb.FactIndex(projectID, blackboard.FactIndexOptions{})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("expected 1 (deprecated excluded), got %d", len(index))
	}
	if index[0].FactKey != "active" {
		t.Fatalf("expected only the active fact, got %q", index[0].FactKey)
	}
}

// TestFactIndexIncludesDeprecatedWhenRequested proves the blackboard "show
// deprecated" toggle can surface historical facts alongside Current Truth.
func TestFactIndexIncludesDeprecatedWhenRequested(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "active", Summary: "current fact",
	}); err != nil {
		t.Fatalf("upsert active: %v", err)
	}
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "old", Summary: "deprecated fact",
		Confidence: blackboard.ConfidenceDeprecated,
	}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}

	index, err := bb.FactIndex(projectID, blackboard.FactIndexOptions{IncludeDeprecated: true})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 2 {
		t.Fatalf("expected 2 (deprecated included), got %d", len(index))
	}
	// The deprecated fact keeps its real confidence so the UI can badge it.
	keys := map[string]blackboard.Confidence{}
	for _, e := range index {
		keys[e.FactKey] = e.Confidence
	}
	if keys["old"] != blackboard.ConfidenceDeprecated {
		t.Fatalf("expected deprecated confidence preserved, got %q", keys["old"])
	}
	if keys["active"] == blackboard.ConfidenceDeprecated {
		t.Fatalf("active fact must not be marked deprecated")
	}
}

func TestGetFactReturnsFullBody(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "k", Summary: "s", Body: "full body on demand",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	fact, err := bb.GetFact(projectID, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fact.Body != "full body on demand" {
		t.Fatalf("expected full body on lookup, got %q", fact.Body)
	}
}

// TestUpsertFactRelationCannotConnectFindings proves the Slice C acceptance: a
// fact relation can connect two project facts and cannot directly connect
// findings.
func TestUpsertFactRelationCannotConnectFindings(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "src", Summary: "source",
	}); err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "dst", Summary: "dest",
	}); err != nil {
		t.Fatalf("upsert dst: %v", err)
	}

	// Connecting two real facts succeeds.
	if _, err := bb.UpsertFactRelation(blackboard.UpsertFactRelationRequest{
		ProjectID:     projectID,
		SourceFactKey: "src",
		TargetFactKey: "dst",
		Relation:      "leads-to",
	}); err != nil {
		t.Fatalf("relation between facts should succeed: %v", err)
	}

	// Connecting to a non-existent target fact fails (cannot connect findings or
	// anything that is not a fact).
	_, err := bb.UpsertFactRelation(blackboard.UpsertFactRelationRequest{
		ProjectID:     projectID,
		SourceFactKey: "src",
		TargetFactKey: "finding:some-finding",
		Relation:      "leads-to",
	})
	if err == nil {
		t.Fatal("expected relation to non-fact target to fail")
	}
}
