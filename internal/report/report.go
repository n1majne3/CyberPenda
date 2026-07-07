// Package report owns report assembly from stored project state. Reports are
// generated deliverables, not the source of truth: they are derived from facts,
// findings, evidence, tasks, and scope — never from raw runtime output.
package report

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/task"
)

// Counts is the inventory summary used by the lightweight stub generator.
type Counts struct {
	Facts    int `json:"facts"`
	Findings int `json:"findings"`
	Evidence int `json:"evidence"`
}

// Stub is the placeholder report used before full templating. Retained for
// backward compatibility with the existing report trigger response shape.
type Stub struct {
	Status   string `json:"status"`
	Format   string `json:"format"`
	Counts   Counts `json:"counts"`
	Markdown string `json:"markdown"`
}

// GenerateStub renders a minimal inventory stub. Real reports use Generate.
func GenerateStub(found project.Project, counts Counts) Stub {
	var markdown strings.Builder
	markdown.WriteString("# ")
	markdown.WriteString(found.Name)
	markdown.WriteString("\n\nStatus: generated stub\n\n## Inventory\n\n")
	markdown.WriteString("- Facts: ")
	markdown.WriteString(fmt.Sprint(counts.Facts))
	markdown.WriteString("\n- Findings: ")
	markdown.WriteString(fmt.Sprint(counts.Findings))
	markdown.WriteString("\n- Evidence: ")
	markdown.WriteString(fmt.Sprint(counts.Evidence))
	markdown.WriteString("\n")
	return Stub{
		Status:   "generated_stub",
		Format:   "markdown",
		Counts:   counts,
		Markdown: markdown.String(),
	}
}

// Reader is the subset of the blackboard service the report generator needs.
// Defined as an interface so the generator depends on reads, not the full
// service, and stays testable.
type Reader interface {
	ListFindings(projectID string) ([]blackboard.Finding, error)
	FactIndex(projectID string, opts blackboard.FactIndexOptions) ([]blackboard.FactIndexEntry, error)
	ListEvidence(projectID string) ([]blackboard.EvidenceArtifact, error)
}

// TaskReader is the subset of the task service the report generator needs.
type TaskReader interface {
	Get(taskID string) (task.Task, error)
}

// Request describes what to render into a report.
type Request struct {
	ProjectID string
	TaskID    string
}

// Report is the assembled deliverable.
type Report struct {
	Status   string `json:"status"`
	Format   string `json:"format"`
	Markdown string `json:"markdown"`
}

// Generator assembles Markdown reports from stored project state.
type Generator struct {
	reader Reader
	tasks  TaskReader
}

// NewGenerator returns a Generator that reads findings/facts/evidence through
// reader and task context through tasks.
func NewGenerator(reader Reader, tasks TaskReader) *Generator {
	return &Generator{reader: reader, tasks: tasks}
}

// Generate assembles a full Markdown report derived from stored state only.
// It never reads raw runtime output.
func (g *Generator) Generate(req Request) (Report, error) {
	findings, err := g.reader.ListFindings(req.ProjectID)
	if err != nil {
		return Report{}, fmt.Errorf("list findings: %w", err)
	}
	facts, err := g.reader.FactIndex(req.ProjectID, blackboard.FactIndexOptions{})
	if err != nil {
		return Report{}, fmt.Errorf("list facts: %w", err)
	}
	evidence, err := g.reader.ListEvidence(req.ProjectID)
	if err != nil {
		return Report{}, fmt.Errorf("list evidence: %w", err)
	}

	var md strings.Builder
	md.WriteString("# Pentest Report\n\n")
	md.WriteString("_Generated: ")
	md.WriteString(time.Now().UTC().Format(time.RFC3339))
	md.WriteString("_\n\n")

	g.writeContext(&md, req)
	g.writeFindings(&md, findings)
	g.writeFacts(&md, facts)
	g.writeEvidence(&md, evidence)

	return Report{Status: "generated", Format: "markdown", Markdown: md.String()}, nil
}

func (g *Generator) writeContext(md *strings.Builder, req Request) {
	md.WriteString("## Engagement Context\n\n")
	md.WriteString("Runner and scope context recorded with this report.\n\n")

	if req.TaskID == "" {
		md.WriteString("\n")
		return
	}
	t, err := g.tasks.Get(req.TaskID)
	if err != nil {
		md.WriteString("\n")
		return
	}

	md.WriteString("- **Runner:** `")
	md.WriteString(string(t.Runner))
	md.WriteString("`")
	md.WriteString("\n")
	writeScopeLines(md, t.ScopeSnapshot)
	md.WriteString("\n")
}

func writeScopeLines(md *strings.Builder, scope task.ScopeSnapshot) {
	if len(scope.Domains) > 0 {
		md.WriteString("- **In-scope domains:** ")
		md.WriteString(strings.Join(scope.Domains, ", "))
		md.WriteString("\n")
	}
	if len(scope.IPs) > 0 {
		md.WriteString("- **In-scope IPs:** ")
		md.WriteString(strings.Join(scope.IPs, ", "))
		md.WriteString("\n")
	}
	if len(scope.CIDRs) > 0 {
		md.WriteString("- **In-scope CIDRs:** ")
		md.WriteString(strings.Join(scope.CIDRs, ", "))
		md.WriteString("\n")
	}
	if len(scope.URLs) > 0 {
		md.WriteString("- **In-scope URLs:** ")
		md.WriteString(strings.Join(scope.URLs, ", "))
		md.WriteString("\n")
	}
	if len(scope.Excluded) > 0 {
		md.WriteString("- **Excluded:** ")
		md.WriteString(strings.Join(scope.Excluded, ", "))
		md.WriteString("\n")
	}
	if len(scope.TestingLimits) > 0 {
		md.WriteString("- **Testing limits:** ")
		md.WriteString(strings.Join(scope.TestingLimits, "; "))
		md.WriteString("\n")
	}
	if scope.Notes != "" {
		md.WriteString("- **Scope notes:** ")
		md.WriteString(scope.Notes)
		md.WriteString("\n")
	}
}

func (g *Generator) writeFindings(md *strings.Builder, findings []blackboard.Finding) {
	// Split confirmed vs unconfirmed; sort each by severity (desc) then key.
	var confirmed, unconfirmed []blackboard.Finding
	for _, f := range findings {
		if f.Status == blackboard.FindingStatusConfirmed {
			confirmed = append(confirmed, f)
		} else {
			unconfirmed = append(unconfirmed, f)
		}
	}
	sort.SliceStable(confirmed, func(i, j int) bool {
		return severityRank(confirmed[i].Severity) > severityRank(confirmed[j].Severity)
	})
	sort.SliceStable(unconfirmed, func(i, j int) bool {
		return severityRank(unconfirmed[i].Severity) > severityRank(unconfirmed[j].Severity)
	})

	md.WriteString("## Confirmed Findings\n\n")
	if len(confirmed) == 0 {
		md.WriteString("_No confirmed findings._\n\n")
	}
	for _, f := range confirmed {
		writeFinding(md, f)
	}

	md.WriteString("## Unconfirmed Findings\n\n")
	if len(unconfirmed) == 0 {
		md.WriteString("_No unconfirmed findings._\n\n")
	}
	for _, f := range unconfirmed {
		writeFinding(md, f)
	}
}

func writeFinding(md *strings.Builder, f blackboard.Finding) {
	md.WriteString("### ")
	md.WriteString(f.Title)
	md.WriteString("\n\n")
	md.WriteString("- **Key:** `")
	md.WriteString(f.FindingKey)
	md.WriteString("`\n")
	md.WriteString("- **Status:** ")
	md.WriteString(string(f.Status))
	md.WriteString("\n")
	md.WriteString("- **Severity:** ")
	md.WriteString(f.Severity)
	md.WriteString("\n")
	if f.Target != "" {
		md.WriteString("- **Target:** ")
		md.WriteString(f.Target)
		md.WriteString("\n")
	}
	if f.CVSSVector != "" {
		md.WriteString("- **CVSS ")
		md.WriteString(f.CVSSVersion)
		md.WriteString(":** `")
		md.WriteString(f.CVSSVector)
		md.WriteString("` (severity: ")
		md.WriteString(f.Severity)
		md.WriteString(")\n")
	} else {
		md.WriteString("- **CVSS:** pending\n")
	}
	if f.Description != "" {
		md.WriteString("\n")
		md.WriteString(f.Description)
		md.WriteString("\n")
	}
	if f.Proof != "" {
		md.WriteString("\n**Proof:** ")
		md.WriteString(f.Proof)
		md.WriteString("\n")
	}
	if f.Impact != "" {
		md.WriteString("\n**Impact:** ")
		md.WriteString(f.Impact)
		md.WriteString("\n")
	}
	if f.Recommendation != "" {
		md.WriteString("\n**Recommendation:** ")
		md.WriteString(f.Recommendation)
		md.WriteString("\n")
	}
	md.WriteString("\n")
}

func severityRank(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "pending":
		return 1
	default:
		return 0
	}
}

func (g *Generator) writeFacts(md *strings.Builder, facts []blackboard.FactIndexEntry) {
	md.WriteString("## Context: Blackboard Facts\n\n")
	if len(facts) == 0 {
		md.WriteString("_No recorded facts._\n\n")
		return
	}
	for _, f := range facts {
		md.WriteString("- **")
		md.WriteString(f.Summary)
		md.WriteString("** (`")
		md.WriteString(f.FactKey)
		md.WriteString("`, ")
		md.WriteString(string(f.Confidence))
		if f.ScopeStatus != "" {
			md.WriteString(", ")
			md.WriteString(string(f.ScopeStatus))
		}
		md.WriteString(")\n")
	}
	md.WriteString("\n")
}

func (g *Generator) writeEvidence(md *strings.Builder, evidence []blackboard.EvidenceArtifact) {
	md.WriteString("## Evidence References\n\n")
	if len(evidence) == 0 {
		md.WriteString("_No evidence artifacts attached._\n\n")
		return
	}
	for _, e := range evidence {
		md.WriteString("- **")
		if e.Summary != "" {
			md.WriteString(e.Summary)
		} else {
			md.WriteString(e.EvidenceKey)
		}
		md.WriteString("** — ")
		md.WriteString(e.ArtifactType)
		md.WriteString(" (`")
		md.WriteString(filepath.Base(e.SourcePath))
		md.WriteString("`) attached to ")
		md.WriteString(string(e.AttachToType))
		md.WriteString(" `")
		md.WriteString(e.AttachToKey)
		md.WriteString("`\n")
	}
	md.WriteString("\n")
}
