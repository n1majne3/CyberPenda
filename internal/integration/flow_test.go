// Package integration_test holds cross-cutting integration tests that exercise
// the full daemon data flow with a temporary database. These prove the slices
// compose: project -> profile -> credential -> fake-runtime task -> facts ->
// findings -> evidence -> Markdown report.
package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/credential"
	"pentest/internal/project"
	"pentest/internal/report"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

// TestFullDaemonFlowProjectToReport proves the cross-cutting integration path:
// every slice composes into one coherent data flow, and writes through the
// shared service layer (the same layer MCP and CLI use) produce stored state a
// report can be generated from.
func TestFullDaemonFlowProjectToReport(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	// Assemble all services the daemon would wire.
	projects := project.NewService(db)
	profiles := runtimeprofile.NewService(db)
	creds := credential.NewService(db)
	tasks := task.NewService(db, projects)
	bb := blackboard.NewService(db)
	harness := runtime.NewHarness(tasks)
	generator := report.NewGenerator(bb, tasks)

	// 1. Create a project with structured scope.
	proj, err := projects.Create(
		"Acme External",
		"External perimeter assessment",
		project.Scope{
			Domains:       []string{"example.com", "api.example.com"},
			Excluded:      []string{"admin.example.com"},
			TestingLimits: []string{"no destructive payloads", "business hours"},
		},
		project.Defaults{Runner: project.RunnerSandbox},
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// 2. Create a runtime profile with a credential reference.
	profile, err := profiles.Create(
		"Codex Default",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{
			BinaryPath:     "/usr/local/bin/codex",
			Model:          "gpt-5",
			CredentialRefs: []string{"codex-api-key"},
		},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	// 3. Bind the credential reference globally (env source).
	if _, err := creds.Upsert("codex-api-key", credential.ScopeGlobal, "",
		credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("bind credential: %v", err)
	}

	// 4. Launch a fake-runtime task that captures goal, runner, and scope.
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "Enumerate example.com and assess exposure",
		RuntimeProfileID: profile.ID,
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID:  launched.ID,
		Goal:    launched.Goal,
		Adapter: runtime.NewFakeAdapter(),
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}

	// The task emitted normalized events; the runtime profile switch should be a
	// new config version, not a new task.
	if _, err := tasks.RecordRuntimeConfig(launched.ID, profile.ID, map[string]any{"model": "gpt-5"}); err != nil {
		t.Fatalf("record config: %v", err)
	}
	versions, err := tasks.RuntimeConfigVersions(launched.ID)
	if err != nil {
		t.Fatalf("config versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("expected 1 config version at v1, got %#v", versions)
	}
	stillSameTask, _ := tasks.Get(launched.ID)
	if stillSameTask.ID != launched.ID {
		t.Fatal("profile switch must not create a new task")
	}

	// 5. Write facts through the service layer (the same layer MCP uses).
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: proj.ID, FactKey: "recon:subdomains", Category: "recon",
		Summary: "Enumerated 2 in-scope subdomains", Body: "example.com, api.example.com",
	}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: proj.ID, FactKey: "dns:example.com", Category: "dns",
		Summary: "Resolves to 1.2.3.4",
	}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}

	// 6. Write a confirmed finding with CVSS (the same layer CLI uses).
	finding, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:      proj.ID,
		FindingKey:     "sqli-login",
		Title:          "SQL injection in login form",
		Status:         blackboard.FindingStatusConfirmed,
		Target:         "https://example.com/login",
		Proof:          "' OR 1=1-- returns admin session",
		Impact:         "Authentication bypass, full account takeover",
		Recommendation: "Use parameterized queries; add input validation",
		CVSSVersion:    "4.0",
		CVSSVector:     "CVSS:4.0/AV:N/AC:L/VC:H/VI:H",
	})
	if err != nil {
		t.Fatalf("upsert finding: %v", err)
	}
	if finding.Severity != "critical" {
		t.Fatalf("expected severity critical, got %q", finding.Severity)
	}

	// 7. Attach evidence to the finding with an explicit attach action.
	if _, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    proj.ID,
		EvidenceKey:  "ev-sqli-poc",
		AttachToType: blackboard.EvidenceAttachFinding,
		AttachToKey:  "sqli-login",
		ArtifactType: "screenshot",
		SourcePath:   "/task/artifacts/sqli-poc.png",
		SHA256:       "deadbeef",
		Summary:      "PoC showing auth bypass",
	}); err != nil {
		t.Fatalf("attach evidence: %v", err)
	}

	// 8. Generate the Markdown report from stored state.
	out, err := generator.Generate(report.Request{ProjectID: proj.ID, TaskID: launched.ID})
	if err != nil {
		t.Fatalf("generate report: %v", err)
	}
	md := out.Markdown

	// The report must be derived from stored state across all slices.
	for _, want := range []string{
		"Confirmed Findings",
		"SQL injection in login form",
		"CVSS:4.0/AV:N/AC:L/VC:H/VI:H",
		"critical",
		"Enumerated 2 in-scope subdomains",
		"PoC showing auth bypass",
		"sandbox", // runner marker
		"business hours",
		"example.com",
	} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(want)) {
			t.Fatalf("report missing %q\n---\n%s", want, md)
		}
	}

	// Counts are consistent across the flow.
	if facts, _ := bb.CountFacts(proj.ID); facts != 2 {
		t.Fatalf("expected 2 facts, got %d", facts)
	}
	if findings, _ := bb.CountFindings(proj.ID); findings != 1 {
		t.Fatalf("expected 1 finding, got %d", findings)
	}
	if evidence, _ := bb.CountEvidence(proj.ID); evidence != 1 {
		t.Fatalf("expected 1 evidence, got %d", evidence)
	}
}
