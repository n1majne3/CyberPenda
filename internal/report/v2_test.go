package report_test

import (
	"context"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/report"
)

type staticV2ReportReader struct {
	projection blackboardv2.PentestReportProjection
}

func (reader staticV2ReportReader) PentestReport(context.Context, string) (blackboardv2.PentestReportProjection, error) {
	return reader.projection, nil
}

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
	generator := report.NewV2Generator(staticV2ReportReader{projection: projection})

	first, err := generator.Generate(context.Background(), report.V2Request{ProjectID: "ignored"})
	if err != nil {
		t.Fatalf("render hostile multiline report: %v", err)
	}
	second, err := generator.Generate(context.Background(), report.V2Request{ProjectID: "ignored"})
	if err != nil {
		t.Fatalf("render hostile multiline report again: %v", err)
	}
	if first.Markdown != second.Markdown {
		t.Fatal("hostile multiline rendering is not deterministic")
	}

	for _, unsafe := range []string{
		"\n# injected heading", "\n- injected list", "\n1. injected ordered list",
		"\n~~~", "\n```backticks", "\n---", "\n[link]", "\n<div>",
	} {
		if strings.Contains(first.Markdown, unsafe) {
			t.Fatalf("multiline semantic text escaped its literal block via %q:\n%s", unsafe, first.Markdown)
		}
	}
	for _, literal := range []string{
		"    # injected heading", "    - injected list", "    1. injected ordered list",
		"    ~~~", "    ```backticks", "    ---", "    [link](javascript:alert(1))", "    <div>html</div>",
	} {
		if !strings.Contains(first.Markdown, literal) {
			t.Fatalf("multiline semantic text did not preserve literal %q:\n%s", literal, first.Markdown)
		}
	}
	if strings.Count(first.Markdown, "# Pentest Report") != 1 || strings.Contains(first.Markdown, "# project heading Pentest Report") {
		t.Fatalf("multiline Project name altered report structure:\n%s", first.Markdown)
	}
	if !strings.HasSuffix(first.Markdown, "\n") || strings.HasSuffix(first.Markdown, "\n\n") {
		t.Fatalf("Markdown must end with exactly one LF: %q", first.Markdown)
	}
}
