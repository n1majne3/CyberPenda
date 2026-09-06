package fgs_test

import (
	"strings"
	"testing"

	"pentest/internal/fgs"
)

func TestReportShowsAcceptedCriteriaAndResultsWithoutLegacyRecords(t *testing.T) {
	s, c := fixture(t)
	submit(t, s, c, 1, fgs.Operation{Op: "goal.create", Key: "goal:a", Title: "Check access", SuccessCriteria: "Access result known"}, fgs.Operation{Op: "step.create", Key: "step:a", Goal: "goal:a", Action: "Check endpoint"}, fgs.Operation{Op: "fact.append", Key: "fact:a", Step: "step:a", Summary: "Access denied", Body: "HTTP 403"}, fgs.Operation{Op: "step.transition", Key: "step:a", From: "open", To: "done", Outputs: []string{"fact:a"}})
	report, err := s.Report(t.Context(), c, "Access review")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Access review", "Access result known", "Check endpoint", "Access denied", "HTTP 403", "open"} {
		if !strings.Contains(report.Markdown, text) {
			t.Fatalf("missing %q in report", text)
		}
	}
	if report.Revision != 1 || strings.Contains(report.Markdown, "Finding") {
		t.Fatalf("report invented legacy semantics: %+v", report)
	}
}
