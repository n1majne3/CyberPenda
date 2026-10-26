package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"

	"pentest/internal/blackboardv2"
	"pentest/internal/report"
)

func TestV2MarkdownRendersMultilineSemanticTextAsInertLiteralBlocks(t *testing.T) {
	hostile := "first line\n# injected heading\n- injected list\n1. injected ordered list\n~~~\n```backticks\n---\n[link](javascript:alert(1))\n<div>html</div>"
	projection := blackboardv2.PentestReportProjection{
		Schema:  "pentest-report/v2",
		Project: blackboardv2.ReportProject{Name: "Project\n# project heading", Description: hostile},
		ConfirmedFindings: []blackboardv2.ReportFinding{{
			Title: hostile, Status: "confirmed", Severity: "high", CVSSVersion: "3.1",
			CVSSVector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:L/A:N", Target: hostile,
			Description: hostile, Proof: hostile, Impact: hostile, Recommendation: hostile,
			SupportingFacts: []blackboardv2.ReportFact{{Category: hostile, Summary: hostile, Body: hostile, Confidence: "confirmed", ScopeStatus: hostile}},
			Contradictions:  []blackboardv2.ReportFact{},
			Evidence:        []blackboardv2.ReportEvidence{{Status: "available", ArtifactType: hostile, Summary: hostile}},
		}},
		UnconfirmedFindings: []blackboardv2.ReportFinding{},
		ConfirmedFacts:      []blackboardv2.ReportFact{{Category: hostile, Summary: hostile, Body: hostile, Confidence: "confirmed", ScopeStatus: hostile}},
		TentativeFacts:      []blackboardv2.ReportFact{},
	}
	first := report.RenderV2Markdown(projection)
	second := report.RenderV2Markdown(projection)
	if first != second {
		t.Fatal("hostile multiline rendering is not deterministic")
	}

	for _, unsafe := range []string{
		"\n# injected heading", "\n- injected list", "\n1. injected ordered list",
		"\n~~~", "\n```backticks", "\n---", "\n[link]", "\n<div>",
	} {
		if strings.Contains(first, unsafe) {
			t.Fatalf("multiline semantic text escaped its literal block via %q:\n%s", unsafe, first)
		}
	}
	for _, literal := range []string{
		"    # injected heading", "    - injected list", "    1. injected ordered list",
		"    ~~~", "    ```backticks", "    ---", "    [link](javascript:alert(1))", "    <div>html</div>",
	} {
		if !strings.Contains(first, literal) {
			t.Fatalf("multiline semantic text did not preserve literal %q:\n%s", literal, first)
		}
	}
	if strings.Count(first, "# Pentest Report") != 1 || strings.Contains(first, "# project heading Pentest Report") {
		t.Fatalf("multiline Project name altered report structure:\n%s", first)
	}
	var rendered bytes.Buffer
	markdown := goldmark.New(goldmark.WithRendererOptions(html.WithUnsafe()))
	if err := markdown.Convert([]byte(first), &rendered); err != nil {
		t.Fatalf("parse rendered Markdown: %v", err)
	}
	for _, active := range []string{
		"<a href=", "<div>html</div>", "<h1>injected heading", "<li>injected list",
		"<li>injected ordered list", "<hr", `class="language-backticks"`,
	} {
		if strings.Contains(rendered.String(), active) {
			t.Fatalf("multiline semantic text became active Markdown %q:\n%s", active, rendered.String())
		}
	}
	if !strings.HasSuffix(first, "\n") || strings.HasSuffix(first, "\n\n") {
		t.Fatalf("Markdown must end with exactly one LF: %q", first)
	}
}
