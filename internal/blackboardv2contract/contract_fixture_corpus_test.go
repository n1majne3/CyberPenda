package blackboardv2contract_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestEmptyRuntimeSnapshotFixtureIsExactAndConformant(t *testing.T) {
	harness := mustHarness(t)

	got, err := harness.Fixture("runtime_snapshot.empty")
	if err != nil {
		t.Fatalf("load empty Runtime Snapshot fixture: %v", err)
	}
	want := []byte(`{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":0,"work":{},"knowledge":{},"relations":[]}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("empty Runtime Snapshot fixture = %s, want %s", got, want)
	}
	if err := harness.ValidateFixture("runtime_snapshot.empty"); err != nil {
		t.Fatalf("empty Runtime Snapshot fixture violates v2 contract: %v", err)
	}
}

func TestSemanticHistoryFixtureCarriesRelationshipIdentityAndVersion(t *testing.T) {
	harness := mustHarness(t)
	raw, err := harness.Fixture("read.history.relationship_page")
	if err != nil {
		t.Fatalf("load relationship history fixture: %v", err)
	}
	if err := harness.ValidateFixture("read.history.relationship_page"); err != nil {
		t.Fatalf("relationship history fixture violates v2 contract: %v", err)
	}
	var page struct {
		Items []struct {
			Kind     string `json:"kind"`
			Version  int    `json:"version"`
			From     string `json:"from"`
			Relation string `json:"relation"`
			To       string `json:"to"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode relationship history fixture: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "relationship" || page.Items[0].Version != 2 || page.Items[0].From != "fact:login" || page.Items[0].Relation != "supports" || page.Items[0].To != "finding:sqli" {
		t.Fatalf("relationship history item = %+v", page.Items)
	}
}

func TestFixtureCorpusCoversEveryFrozenV2WireShape(t *testing.T) {
	harness := mustHarness(t)

	want := []string{
		"attempt.checkpoint",
		"change.create",
		"change.merge",
		"change.relate",
		"change.result",
		"change.supersede",
		"change.transition",
		"change.unrelate",
		"change.update",
		"continuation.finish.request",
		"continuation.finish.result",
		"evidence.retain",
		"migration.plan",
		"migration.result",
		"read.current",
		"read.history.page",
		"read.history.relationship_page",
		"record.attempt",
		"record.entity",
		"record.evidence",
		"record.fact",
		"record.finding",
		"record.objective",
		"record.solution",
		"relationship.about",
		"relationship.contradicts",
		"relationship.depends_on",
		"relationship.derived_from",
		"relationship.evidences",
		"relationship.invalidates",
		"relationship.part_of",
		"relationship.produced",
		"relationship.satisfies",
		"relationship.supersedes",
		"relationship.supports",
		"relationship.tests",
		"response.error",
		"response.sync",
		"runtime_snapshot.ctf_complete",
		"runtime_snapshot.empty",
		"runtime_snapshot.pentest",
		"runtime_snapshot.pentest_complete",
		"runtime_snapshot.solution",
		"semantic_health.healthy_empty",
		"semantic_health.with_anomalies",
	}
	if got := harness.FixtureNames(); !slices.Equal(got, want) {
		t.Fatalf("fixture names = %v, want %v", got, want)
	}
	for _, name := range want {
		if err := harness.ValidateFixture(name); err != nil {
			t.Errorf("fixture %s violates v2 contract: %v", name, err)
		}
	}
}
