// Package report owns report assembly from stored project state. Reports are
// generated deliverables, not the source of truth.
package report

import (
	"fmt"
	"strings"

	"pentest/internal/project"
)

type Counts struct {
	Facts    int `json:"facts"`
	Findings int `json:"findings"`
	Evidence int `json:"evidence"`
}

type Stub struct {
	Status   string `json:"status"`
	Format   string `json:"format"`
	Counts   Counts `json:"counts"`
	Markdown string `json:"markdown"`
}

func GenerateStub(found project.Project, counts Counts) Stub {
	var markdown strings.Builder
	markdown.WriteString("# ")
	markdown.WriteString(found.Name)
	markdown.WriteString("\n\n")
	markdown.WriteString("Status: generated stub\n\n")
	markdown.WriteString("## Inventory\n\n")
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
