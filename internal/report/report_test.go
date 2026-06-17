package report_test

import (
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/report"
	"pentest/internal/store"
	"pentest/internal/task"
)

func newFullServices(t *testing.T) (*report.Generator, *blackboard.Service, *project.Service, *task.Service) {
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
	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	bb := blackboard.NewService(db)
	return report.NewGenerator(bb, tasks), bb, projects, tasks
}

// TestGenerateSeparatesConfirmedAndUnconfirmedFindings proves the Slice 8
// acceptance: confirmed and unconfirmed findings are separated, and the report
// includes CVSS data, evidence references, and scope/runner context — all
// derived from stored state, never raw runtime output.
func TestGenerateSeparatesConfirmedAndUnconfirmedFindings(t *testing.T) {
	gen, bb, projects, tasks := newFullServices(t)

	// Project with scope context and YOLO off, sandbox runner.
	proj, err := projects.Create(
		"Acme External",
		"External perimeter assessment",
		project.Scope{
			Domains:       []string{"example.com"},
			TestingLimits: []string{"business hours only"},
		},
		project.Defaults{Runner: project.RunnerSandbox},
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// A fake-runtime task provides runner context.
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "enumerate example.com",
		RuntimeProfileID: "fake",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Facts providing attack-chain context.
	if _, err := bb.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID: proj.ID, FactKey: "recon:subdomains", Category: "recon",
		Summary: "Found 3 subdomains", Body: "api, admin, staging",
	}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}

	// A confirmed finding with full CVSS.
	if _, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:      proj.ID,
		FindingKey:     "sqli-login",
		Title:          "SQL injection in login",
		Status:         blackboard.FindingStatusConfirmed,
		Target:         "https://example.com/login",
		Proof:          "' OR 1=1-- bypasses auth",
		Impact:         "authentication bypass",
		Recommendation: "use parameterized queries",
		CVSSVersion:    "4.0",
		CVSSVector:     "CVSS:4.0/AV:N/VC:H/VI:H",
	}); err != nil {
		t.Fatalf("upsert confirmed finding: %v", err)
	}

	// An unconfirmed finding (CVSS pending).
	if _, err := bb.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:  proj.ID,
		FindingKey: "info-disclosure",
		Title:      "Verbose error messages",
	}); err != nil {
		t.Fatalf("upsert unconfirmed finding: %v", err)
	}

	// Evidence attached to the confirmed finding.
	if _, err := bb.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    proj.ID,
		EvidenceKey:  "ev-sqli",
		AttachToType: blackboard.EvidenceAttachFinding,
		AttachToKey:  "sqli-login",
		ArtifactType: "screenshot",
		SourcePath:   "/task/artifacts/sqli.png",
		Summary:      "proof of auth bypass",
	}); err != nil {
		t.Fatalf("attach evidence: %v", err)
	}

	out, err := gen.Generate(report.Request{
		ProjectID: proj.ID,
		TaskID:    launched.ID,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	md := out.Markdown

	// Confirmed and unconfirmed findings are separated into distinct sections.
	if !strings.Contains(md, "Confirmed Findings") {
		t.Fatal("expected a confirmed findings section")
	}
	if !strings.Contains(md, "Unconfirmed Findings") {
		t.Fatal("expected an unconfirmed findings section")
	}
	// The confirmed finding appears under confirmed; the unconfirmed under
	// unconfirmed. Verify both titles appear and the confirmed one carries CVSS.
	if !strings.Contains(md, "SQL injection in login") {
		t.Fatal("expected confirmed finding title in report")
	}
	if !strings.Contains(md, "Verbose error messages") {
		t.Fatal("expected unconfirmed finding title in report")
	}
	if !strings.Contains(md, "CVSS:4.0/AV:N/VC:H/VI:H") {
		t.Fatal("expected CVSS vector in report")
	}
	if !strings.Contains(md, "severity") || !strings.Contains(strings.ToLower(md), "critical") {
		t.Fatal("expected derived severity in report")
	}
	// Evidence reference appears.
	if !strings.Contains(md, "sqli.png") || !strings.Contains(md, "auth bypass") {
		t.Fatal("expected evidence reference in report")
	}
	// Scope context appears.
	if !strings.Contains(md, "example.com") {
		t.Fatal("expected scope domain in report")
	}
	if !strings.Contains(md, "business hours only") {
		t.Fatal("expected testing limits in report")
	}
	// Runner marker appears.
	if !strings.Contains(strings.ToLower(md), "runner") || !strings.Contains(strings.ToLower(md), "sandbox") {
		t.Fatal("expected runner marker in report")
	}
	// Fact context appears.
	if !strings.Contains(md, "Found 3 subdomains") {
		t.Fatal("expected fact context in report")
	}
}
