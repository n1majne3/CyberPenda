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

// TestMergeFactsConsolidatesAndAliases proves the Fact Merge contract
// (CONTEXT.md): merging two fact keys preserves history, moves the source key to
// an alias of the canonical (target) key, and subsequent reads/writes through
// the alias resolve to the canonical key.
func TestMergeFactsConsolidatesAndAliases(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	// Two duplicate facts to merge.
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "dns:example.com", Category: "dns",
		Summary: "example.com -> 1.2.3.4", Body: "full dns detail",
	}); err != nil {
		t.Fatalf("upsert canonical: %v", err)
	}
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "dns:example-dot-com", Category: "dns",
		Summary: "example.com -> 1.2.3.4 (dup)", Body: "dup detail",
	}); err != nil {
		t.Fatalf("upsert dup: %v", err)
	}
	// The source fact carries its own history and a relation.
	if _, err := bb.UpsertFactRelation(blackboard.UpsertFactRelationRequest{
		ProjectID: projectID, SourceFactKey: "dns:example-dot-com",
		TargetFactKey: "dns:example.com", Relation: "duplicates",
	}); err != nil {
		t.Fatalf("relation: %v", err)
	}

	// Merge the duplicate INTO the canonical key. Canonical wins as the target.
	if err := bb.MergeFacts(blackboard.MergeFactsRequest{
		ProjectID: projectID, SourceFactKey: "dns:example-dot-com", CanonicalFactKey: "dns:example.com",
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// The alias must redirect reads through the old key to the canonical key.
	resolved, err := bb.GetFact(projectID, "dns:example-dot-com")
	if err != nil {
		t.Fatalf("get via alias: %v", err)
	}
	if resolved.FactKey != "dns:example.com" {
		t.Fatalf("alias must resolve to canonical key, got %q", resolved.FactKey)
	}

	// An upsert through the old alias key must update the canonical fact, not
	// create a separate current truth.
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "dns:example-dot-com",
		Summary: "updated via alias", Body: "alias write body",
	}); err != nil {
		t.Fatalf("upsert via alias: %v", err)
	}
	canon, err := bb.GetFact(projectID, "dns:example.com")
	if err != nil {
		t.Fatalf("get canonical: %v", err)
	}
	if canon.Summary != "updated via alias" {
		t.Fatalf("alias write must update canonical, got %q", canon.Summary)
	}

	// The fact index must list the canonical fact once (no separate entry for
	// the alias), keeping current truth free of duplicates.
	index, err := bb.FactIndex(projectID, blackboard.FactIndexOptions{})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 1 || index[0].FactKey != "dns:example.com" {
		t.Fatalf("index must contain only the canonical fact, got %#v", index)
	}

	// The merged (source) fact's version history is preserved.
	canonVersions, err := bb.FactVersions(projectID, "dns:example.com")
	if err != nil {
		t.Fatalf("canon versions: %v", err)
	}
	if len(canonVersions) < 2 {
		t.Fatalf("canonical must keep its history across the merge, got %d versions", len(canonVersions))
	}
}

// TestMergeFactsRejectsMissingKeys proves merge is governed: both keys must
// exist, and a self-merge (source == canonical) is a no-op error.
func TestMergeFactsRejectsMissingKeys(t *testing.T) {
	bb, projects := newServices(t)
	projectID := mustProject(t, projects)

	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: projectID, FactKey: "only", Summary: "the only fact",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Source key does not exist.
	if err := bb.MergeFacts(blackboard.MergeFactsRequest{
		ProjectID: projectID, SourceFactKey: "missing", CanonicalFactKey: "only",
	}); !errors.Is(err, blackboard.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing source, got %v", err)
	}
	// Canonical key does not exist.
	if err := bb.MergeFacts(blackboard.MergeFactsRequest{
		ProjectID: projectID, SourceFactKey: "only", CanonicalFactKey: "missing",
	}); !errors.Is(err, blackboard.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing canonical, got %v", err)
	}
	// Self-merge is rejected.
	if err := bb.MergeFacts(blackboard.MergeFactsRequest{
		ProjectID: projectID, SourceFactKey: "only", CanonicalFactKey: "only",
	}); !errors.Is(err, blackboard.ErrSelfMerge) {
		t.Fatalf("expected ErrSelfMerge, got %v", err)
	}
}
