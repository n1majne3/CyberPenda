package report

import (
	"context"
	"fmt"
	"strings"

	"pentest/internal/blackboardv2"
)

// V2Reader is the semantic v2 report seam used by the renderer.
type V2Reader interface {
	PentestReport(context.Context, string) (blackboardv2.PentestReportProjection, error)
}

// V2Request selects the Project whose current v2 conclusions are rendered.
type V2Request struct {
	ProjectID string
}

// V2Generator renders the deterministic Blackboard v2 Pentest projection.
type V2Generator struct {
	reader V2Reader
}

// NewV2Generator returns a report generator backed by current v2 semantics.
func NewV2Generator(reader V2Reader) *V2Generator {
	return &V2Generator{reader: reader}
}

// Generate renders current confirmed conclusions separately from tentative
// Facts and unconfirmed Findings. It adds no clock, identifiers, or history.
func (g *V2Generator) Generate(ctx context.Context, request V2Request) (Report, error) {
	projection, err := g.reader.PentestReport(ctx, request.ProjectID)
	if err != nil {
		return Report{}, fmt.Errorf("project v2 Pentest report: %w", err)
	}
	return Report{Status: "generated", Format: "markdown", Markdown: renderV2Markdown(projection)}, nil
}

func renderV2Markdown(projection blackboardv2.PentestReportProjection) string {
	var output strings.Builder
	if isV2Multiline(projection.Project.Name) {
		output.WriteString("# Pentest Report\n\n")
		writeV2NamedValue(&output, "Project", projection.Project.Name)
	} else {
		output.WriteString("# ")
		output.WriteString(escapeV2Markdown(projection.Project.Name))
		output.WriteString(" Pentest Report\n\n")
	}
	if projection.Project.Description != "" {
		writeV2Paragraph(&output, projection.Project.Description)
		output.WriteString("\n")
	}
	writeV2Findings(&output, "Confirmed Findings", projection.ConfirmedFindings)
	writeV2Findings(&output, "Unconfirmed Findings", projection.UnconfirmedFindings)
	writeV2Facts(&output, "Confirmed Facts", projection.ConfirmedFacts)
	writeV2Facts(&output, "Tentative Facts", projection.TentativeFacts)
	return strings.TrimRight(output.String(), "\n") + "\n"
}

func writeV2Findings(output *strings.Builder, heading string, findings []blackboardv2.ReportFinding) {
	output.WriteString("## ")
	output.WriteString(heading)
	output.WriteString("\n\n")
	if len(findings) == 0 {
		output.WriteString("_No records._\n\n")
		return
	}
	for _, finding := range findings {
		if isV2Multiline(finding.Title) {
			output.WriteString("### Finding\n\n")
			writeV2NamedValue(output, "Title", finding.Title)
		} else {
			output.WriteString("### ")
			output.WriteString(escapeV2Markdown(finding.Title))
			output.WriteString("\n\n")
		}
		writeV2Label(output, "Status", finding.Status)
		if finding.CVSSPending {
			writeV2Label(output, "CVSS", "pending")
		} else {
			writeV2Label(output, "Severity", finding.Severity)
			writeV2Label(output, "CVSS", finding.CVSSVersion+" "+finding.CVSSVector)
		}
		writeV2OptionalLabel(output, "Target", finding.Target)
		writeV2Paragraph(output, finding.Description)
		writeV2OptionalLabel(output, "Proof", finding.Proof)
		writeV2OptionalLabel(output, "Impact", finding.Impact)
		writeV2OptionalLabel(output, "Recommendation", finding.Recommendation)
		writeV2FactList(output, "Supporting Facts", finding.SupportingFacts)
		writeV2FactList(output, "Contradictions", finding.Contradictions)
		if len(finding.Evidence) != 0 {
			output.WriteString("\n**Evidence**\n\n")
			for _, evidence := range finding.Evidence {
				if isV2Multiline(evidence.Summary) || isV2Multiline(evidence.ArtifactType) || isV2Multiline(evidence.Status) {
					output.WriteString("#### Evidence\n\n")
					writeV2NamedValue(output, "Summary", evidence.Summary)
					writeV2NamedValue(output, "Artifact Type", evidence.ArtifactType)
					writeV2NamedValue(output, "Status", evidence.Status)
					continue
				}
				output.WriteString("- ")
				output.WriteString(escapeV2Markdown(evidence.Summary))
				output.WriteString(" (")
				output.WriteString(escapeV2Markdown(evidence.ArtifactType))
				output.WriteString(", ")
				output.WriteString(escapeV2Markdown(evidence.Status))
				output.WriteString(")\n")
			}
		}
		output.WriteString("\n")
	}
}

func writeV2Facts(output *strings.Builder, heading string, facts []blackboardv2.ReportFact) {
	output.WriteString("## ")
	output.WriteString(heading)
	output.WriteString("\n\n")
	if len(facts) == 0 {
		output.WriteString("_No records._\n\n")
		return
	}
	for _, fact := range facts {
		if isV2Multiline(fact.Summary) || isV2Multiline(fact.Category) || isV2Multiline(fact.ScopeStatus) || isV2Multiline(fact.Body) {
			output.WriteString("### Fact\n\n")
			writeV2NamedValue(output, "Summary", fact.Summary)
			writeV2NamedValue(output, "Category", fact.Category)
			writeV2NamedValue(output, "Scope Status", fact.ScopeStatus)
			if fact.Body != "" {
				writeV2NamedValue(output, "Detail", fact.Body)
			}
			continue
		}
		output.WriteString("- **")
		output.WriteString(escapeV2Markdown(fact.Summary))
		output.WriteString("** (")
		output.WriteString(escapeV2Markdown(fact.Category))
		output.WriteString(", ")
		output.WriteString(escapeV2Markdown(fact.ScopeStatus))
		output.WriteString(")")
		if fact.Body != "" {
			output.WriteString(": ")
			output.WriteString(escapeV2Markdown(fact.Body))
		}
		output.WriteString("\n")
	}
	output.WriteString("\n")
}

func writeV2FactList(output *strings.Builder, heading string, facts []blackboardv2.ReportFact) {
	if len(facts) == 0 {
		return
	}
	output.WriteString("\n**")
	output.WriteString(heading)
	output.WriteString("**\n\n")
	for _, fact := range facts {
		if isV2Multiline(fact.Summary) || isV2Multiline(fact.Confidence) {
			output.WriteString("#### Fact\n\n")
			writeV2NamedValue(output, "Summary", fact.Summary)
			writeV2NamedValue(output, "Confidence", fact.Confidence)
			continue
		}
		output.WriteString("- ")
		output.WriteString(escapeV2Markdown(fact.Summary))
		output.WriteString(" (")
		output.WriteString(escapeV2Markdown(fact.Confidence))
		output.WriteString(")\n")
	}
}

func writeV2Label(output *strings.Builder, label, value string) {
	if isV2Multiline(value) {
		output.WriteString("\n")
		writeV2NamedValue(output, label, value)
		return
	}
	output.WriteString("- **")
	output.WriteString(label)
	output.WriteString(":** ")
	output.WriteString(escapeV2Markdown(value))
	output.WriteString("\n")
}

func writeV2OptionalLabel(output *strings.Builder, label, value string) {
	if value != "" {
		writeV2Label(output, label, value)
	}
}

func writeV2Paragraph(output *strings.Builder, value string) {
	if value != "" {
		output.WriteString("\n")
		if isV2Multiline(value) {
			writeV2LiteralBlock(output, value)
		} else {
			output.WriteString(escapeV2Markdown(value))
			output.WriteString("\n")
		}
	}
}

func writeV2NamedValue(output *strings.Builder, label, value string) {
	output.WriteString("**")
	output.WriteString(label)
	output.WriteString(":**")
	if isV2Multiline(value) {
		output.WriteString("\n\n")
		writeV2LiteralBlock(output, value)
		output.WriteString("\n")
		return
	}
	output.WriteString(" ")
	output.WriteString(escapeV2Markdown(value))
	output.WriteString("\n\n")
}

func writeV2LiteralBlock(output *strings.Builder, value string) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		output.WriteString("    ")
		output.WriteString(line)
		output.WriteString("\n")
	}
}

func isV2Multiline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func escapeV2Markdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]",
		"<", "\\<", ">", "\\>", "#", "\\#", "|", "\\|", "!", "\\!", "-", "\\-",
		"+", "\\+", ".", "\\.", "=", "\\=", "~", "\\~", "(", "\\(", ")", "\\)",
	)
	return replacer.Replace(value)
}
