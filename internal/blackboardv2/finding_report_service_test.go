package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/report"
	"pentest/internal/store"
	"pentest/internal/task"
)

const criticalCVSS40 = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"

func TestFindingConfirmationAndPentestReportEndToEnd(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	alpha, err := projects.Create("Alpha External", "External assessment", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Alpha Project: %v", err)
	}
	beta, err := projects.Create("Beta External", "Separate assessment", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Beta Project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finding-report-seed",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:confirmed-support", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Authentication bypass was reproduced", Body: "Independent reproduction confirmed the bypass.", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:tentative-context", Type: "fact", Record: blackboardv2.FactRecord{Category: "reconnaissance", Summary: "A second endpoint may share the flaw", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "finding:login-sqli", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "SQL injection in login", Target: "https://alpha.example/login", Description: "The login query accepts attacker-controlled SQL.", Proof: "A boolean payload bypassed authentication.", Impact: "An attacker can access arbitrary accounts.", Recommendation: "Use parameterized queries.", CVSSVersion: "4.0", CVSSVector: criticalCVSS40}},
			{Op: "create", Key: "finding:verbose-errors", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Verbose error disclosure", Target: "https://alpha.example/api", Description: "The API may disclose stack details."}},
			{Op: "transition", Key: "finding:login-sqli", Version: 1, Status: "confirmed"},
			{Op: "relate", From: "fact:confirmed-support", Relation: "supports", To: "finding:login-sqli", Reason: "Independent reproduction supports the Finding"},
			{Op: "relate", From: "fact:tentative-context", Relation: "contradicts", To: "finding:login-sqli", Reason: "The second endpoint did not reproduce"},
		},
	})
	if err != nil {
		t.Fatalf("create and confirm Finding with final-batch support: %v", err)
	}

	detail, err := service.ReadCurrent(ctx, alpha.ID, "finding:login-sqli")
	if err != nil {
		t.Fatalf("read confirmed Finding: %v", err)
	}
	if detail.Record.Status != "confirmed" || detail.Record.Severity != "critical" || detail.Record.CVSSPending {
		t.Fatalf("derived Finding state = %#v", detail.Record)
	}
	assertContractJSON(t, mustHarness(t), "currentDetail", detail)

	snapshot, err := service.RuntimeSnapshot(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("read Finding snapshot: %v", err)
	}
	assertContractJSON(t, mustHarness(t), "runtimeSnapshot", snapshot)
	if got := snapshot.Knowledge.Findings["finding:login-sqli"]; got.Status != "confirmed" || got.Severity != "critical" || got.CVSSPending {
		t.Fatalf("snapshot Finding = %#v", got)
	}

	projection, err := service.PentestReport(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("project Pentest report: %v", err)
	}
	if len(projection.ConfirmedFindings) != 1 || len(projection.UnconfirmedFindings) != 1 || len(projection.ConfirmedFacts) != 1 || len(projection.TentativeFacts) != 1 {
		t.Fatalf("report sections = %#v", projection)
	}
	if len(projection.ConfirmedFindings[0].SupportingFacts) != 1 || len(projection.ConfirmedFindings[0].Contradictions) != 1 {
		t.Fatalf("report Finding support = %#v", projection.ConfirmedFindings[0])
	}
	raw := string(mustJSON(t, projection))
	for _, forbidden := range []string{alpha.ID, "finding:login-sqli", "fact:confirmed-support", "trusted_origin", "origin", "sha256", "source_hash", "managed_path", "task_id", "continuation_id", "history", "created_at", "updated_at", "revision"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(forbidden)) {
			t.Fatalf("report leaked forbidden storage/execution content %q: %s", forbidden, raw)
		}
	}
	_, err = service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "unrelated-report-revision",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:report-unrelated", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "unrelated.example", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("advance unrelated graph state: %v", err)
	}
	afterUnrelatedChange, err := service.PentestReport(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("project report after unrelated graph change: %v", err)
	}
	if afterRaw := string(mustJSON(t, afterUnrelatedChange)); afterRaw != raw {
		t.Fatalf("unrelated graph revision changed report content\nbefore=%s\nafter=%s", raw, afterRaw)
	}

	generator := report.NewV2Generator(service)
	first, err := generator.Generate(ctx, report.V2Request{ProjectID: alpha.ID})
	if err != nil {
		t.Fatalf("render Pentest report: %v", err)
	}
	second, err := generator.Generate(ctx, report.V2Request{ProjectID: alpha.ID})
	if err != nil {
		t.Fatalf("render Pentest report again: %v", err)
	}
	if first.Markdown != second.Markdown || !strings.Contains(first.Markdown, "Confirmed Findings") || !strings.Contains(first.Markdown, "Tentative Facts") || !strings.Contains(first.Markdown, "Unconfirmed Findings") {
		t.Fatalf("report Markdown is not deterministic or clearly sectioned:\n%s", first.Markdown)
	}
	if !strings.HasSuffix(first.Markdown, "\n") || strings.HasSuffix(first.Markdown, "\n\n") {
		t.Fatalf("report Markdown must end with exactly one LF:\n%q", first.Markdown)
	}
	if strings.Contains(first.Markdown, "_Generated:") || strings.Contains(first.Markdown, alpha.ID) || strings.Contains(first.Markdown, "finding:login-sqli") {
		t.Fatalf("report Markdown leaked time or identifiers:\n%s", first.Markdown)
	}

	_, err = service.Apply(ctx, beta.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "isolated-finding",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "finding:login-sqli", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Separate SQL injection", Target: "https://beta.example/login", Proof: "Reproduced", Impact: "Account access", Recommendation: "Parameterize", CVSSVersion: "4.0", CVSSVector: criticalCVSS40}},
			{Op: "transition", Key: "finding:login-sqli", Version: 1, Status: "confirmed"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("cross-Project support error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, beta.ID, "finding:login-sqli"); !isSemanticCode(err, "not_found") {
		t.Fatalf("rejected cross-Project confirmation was not atomic: %#v", err)
	}
}

func TestFindingClosedFieldsAndBrokenSupportRollBackAtomically(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Atomic Findings", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	service := blackboardv2.NewService(db)

	for name, raw := range map[string]string{
		"caller severity":  `{"schema":"semantic-change-batch/v2","idempotency_key":"caller-severity","changes":[{"op":"create","key":"finding:bad","type":"finding","record":{"status":"unconfirmed","title":"Bad","severity":"critical"}}]}`,
		"caller pending":   `{"schema":"semantic-change-batch/v2","idempotency_key":"caller-pending","changes":[{"op":"create","key":"finding:bad","type":"finding","record":{"status":"unconfirmed","title":"Bad","cvss_pending":false}}]}`,
		"update status":    `{"schema":"semantic-change-batch/v2","idempotency_key":"update-status","changes":[{"op":"update","key":"finding:bad","version":1,"type":"finding","record":{"status":"confirmed"}}]}`,
		"transition field": `{"schema":"semantic-change-batch/v2","idempotency_key":"transition-field","changes":[{"op":"transition","key":"finding:bad","version":1,"status":"confirmed","severity":"critical"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var batch blackboardv2.ChangeBatch
			if err := json.Unmarshal([]byte(raw), &batch); err == nil {
				t.Fatalf("closed Finding DTO accepted %s", raw)
			}
		})
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "programmatic-derived-field",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "finding:closed-programmatic", Type: "finding",
			Record: blackboardv2.Record{Status: "unconfirmed", Title: "Smuggled", Severity: "critical"},
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("programmatic derived Finding field error = %#v", err)
	}
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "programmatic-derived-field",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "finding:closed-programmatic", Type: "finding",
			Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Closed programmatic Finding"},
		}},
	}); err != nil {
		t.Fatalf("valid request after pre-idempotency DTO rejection: %v", err)
	}
	const criticalCVSS31 = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "score-v31-finding",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "finding:closed-programmatic", Version: 1, Type: "finding",
			Record: blackboardv2.FindingPatch{CVSSVersion: strPtr("3.1"), CVSSVector: strPtr(criticalCVSS31)},
		}},
	}); err != nil {
		t.Fatalf("score CVSS v3.1 Finding: %v", err)
	}
	v31Detail, err := service.ReadCurrent(ctx, createdProject.ID, "finding:closed-programmatic")
	if err != nil || v31Detail.Record.Severity != "critical" || v31Detail.Record.CVSSPending {
		t.Fatalf("CVSS v3.1 derived state = %#v, %v", v31Detail.Record, err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "incomplete-unconfirmed-cvss",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "finding:incomplete-cvss", Type: "finding",
			Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Incomplete CVSS", CVSSVersion: "4.0", CVSSVector: "CVSS:4.0/AV:N"},
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("incomplete optional CVSS error = %#v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "atomic-finding-seed",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:support", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "The issue was reproduced", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "finding:atomic", Type: "finding", Record: blackboardv2.FindingRecord{Status: "confirmed", Title: "Atomic issue", Target: "https://example.test", Proof: "Reproduced", Impact: "High impact", Recommendation: "Fix it", CVSSVersion: "4.0", CVSSVector: criticalCVSS40}},
			{Op: "relate", From: "fact:support", Relation: "supports", To: "finding:atomic"},
		},
	})
	if err != nil {
		t.Fatalf("seed confirmed Finding: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "advance-finding-version",
		Changes: []blackboardv2.Change{{Op: "update", Key: "finding:atomic", Version: 1, Type: "finding", Record: blackboardv2.FindingPatch{Description: strPtr("Current description")}}},
	})
	if err != nil {
		t.Fatalf("advance Finding version: %v", err)
	}
	before, err := service.PentestReport(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("report before broken support: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "break-finding-support",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:must-roll-back", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "rollback.example", ScopeStatus: "in_scope"}},
			{Op: "transition", Key: "fact:support", Version: 1, Status: "tentative"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("broken support error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:must-roll-back"); !isSemanticCode(err, "not_found") {
		t.Fatalf("broken-support batch retained marker: %#v", err)
	}
	support, err := service.ReadCurrent(ctx, createdProject.ID, "fact:support")
	if err != nil || support.Record.Confidence != "confirmed" || support.Version != 1 {
		t.Fatalf("broken-support batch changed support Fact: %#v, %v", support, err)
	}
	after, err := service.PentestReport(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("report after broken support: %v", err)
	}
	if string(mustJSON(t, before)) != string(mustJSON(t, after)) {
		t.Fatalf("broken-support batch changed report\nbefore=%s\nafter=%s", mustJSON(t, before), mustJSON(t, after))
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-finding-update",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:stale-marker", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "stale.example", ScopeStatus: "in_scope"}},
			{Op: "update", Key: "finding:atomic", Version: 1, Type: "finding", Record: blackboardv2.FindingPatch{Title: strPtr("Stale title")}},
		},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "version_conflict" {
		t.Fatalf("invalid stale version error = %#v", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:stale-marker"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale batch retained marker: %#v", err)
	}
}

func TestFindingFalsePositiveAndSupersessionPreserveCurrentMeaning(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Finding History", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finding-history-seed",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "finding:false-alarm", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "False alarm"}},
			{Op: "create", Key: "finding:old", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Old issue"}},
			{Op: "create", Key: "finding:replacement", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Replacement issue"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Findings: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "false-positive-without-meaning",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "finding:false-alarm", Version: 1, Status: "false_positive", ResolutionSummary: "The response came from a test fixture"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("unsupported false-positive error = %#v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "false-positive-with-meaning",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:false-alarm-reason", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "The apparent issue was generated by a test fixture", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:false-alarm-reason", Relation: "contradicts", To: "finding:false-alarm", Reason: "The fixture explains the apparent issue"},
			{Op: "transition", Key: "finding:false-alarm", Version: 1, Status: "false_positive", ResolutionSummary: "The response came from a test fixture"},
		},
	})
	if err != nil {
		t.Fatalf("false-positive with reusable meaning: %v", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "finding:false-alarm"); !isSemanticCode(err, "not_found") {
		t.Fatalf("false-positive remained current: %#v", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "fact:false-alarm-reason"); err != nil {
		t.Fatalf("false-positive reason did not remain current: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-finding",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "finding:replacement", ReplacementVersion: 1, Replaced: "finding:old", ReplacedVersion: 1}},
	})
	if err != nil {
		t.Fatalf("supersede Finding: %v", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "finding:old"); !isSemanticCode(err, "not_found") {
		t.Fatalf("superseded Finding remained current: %#v", err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "finding:old", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Finding history: %v", err)
	}
	if len(history.Items) < 2 || history.Items[1].Record == nil || history.Items[1].Record.Status != "superseded" {
		t.Fatalf("superseded Finding history = %#v", history.Items)
	}
	reportProjection, err := service.PentestReport(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("report after Finding retirement: %v", err)
	}
	if len(reportProjection.UnconfirmedFindings) != 1 || reportProjection.UnconfirmedFindings[0].Title != "Replacement issue" {
		t.Fatalf("current report Findings = %#v", reportProjection.UnconfirmedFindings)
	}
}

func TestFindingCanBeConfirmedByCurrentEvidenceAndNotByMissingEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Evidence Finding", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Confirm issue", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "evidence-finding-seed",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:confirm", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Confirm the issue"}},
			{Op: "create", Key: "attempt:confirm", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture proof"}},
			{Op: "create", Key: "finding:evidence", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Evidence-backed issue", Target: "https://example.test", Proof: "Captured exchange", Impact: "Sensitive access", Recommendation: "Restrict access", CVSSVersion: "4.0", CVSSVector: criticalCVSS40}},
			{Op: "relate", From: "attempt:confirm", Relation: "tests", To: "finding:evidence"},
		},
	})
	if err != nil {
		t.Fatalf("seed Evidence Finding: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	_, err = service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-finding-proof", Key: "evidence:finding", Attempt: "attempt:confirm", SourcePath: "proof.txt", ArtifactType: "http_exchange", Summary: "Captured proof", Links: []blackboardv2.EvidenceLink{{"evidences", "finding:evidence"}},
	})
	if err != nil {
		t.Fatalf("retain Finding Evidence: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-with-evidence",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "finding:evidence", Version: 1, Status: "confirmed"}},
	})
	if err != nil {
		t.Fatalf("confirm Finding with Evidence: %v", err)
	}
	projection, err := service.PentestReport(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("report Evidence-backed Finding: %v", err)
	}
	if len(projection.ConfirmedFindings) != 1 || len(projection.ConfirmedFindings[0].Evidence) != 1 || projection.ConfirmedFindings[0].Evidence[0].Summary != "Captured proof" {
		t.Fatalf("report Evidence projection = %#v", projection.ConfirmedFindings)
	}
	reportJSON := string(mustJSON(t, projection))
	for _, forbidden := range []string{"proof.txt", "managed_path", "source_path", "sha256", "size", createdTask.ID, continuation.ID} {
		if strings.Contains(reportJSON, forbidden) {
			t.Fatalf("Evidence report leaked %q: %s", forbidden, reportJSON)
		}
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "break-evidence-finding-support",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:finding", Version: 1, Status: "missing", Summary: "The retained proof is no longer available"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("missing sole Finding Evidence error = %#v", err)
	}
	evidence, err := service.ReadCurrent(ctx, createdProject.ID, "evidence:finding")
	if err != nil || evidence.Record.Status != "available" {
		t.Fatalf("broken Evidence support was not rolled back: %#v, %v", evidence, err)
	}
}
