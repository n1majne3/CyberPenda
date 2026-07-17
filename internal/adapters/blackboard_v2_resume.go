package adapters

import "strings"

// BuildBlackboardV2ResumePrompt carries only Task-owned goal and unconsumed
// Harness Steering. Current semantic state remains in the fresh Snapshot pin.
func BuildBlackboardV2ResumePrompt(goal string, steering []string) string {
	var prompt strings.Builder
	prompt.WriteString("Task Goal:\n")
	prompt.WriteString(strings.TrimSpace(goal))
	if len(steering) != 0 {
		prompt.WriteString("\n\nHarness Steering:\n")
		for _, directive := range steering {
			prompt.WriteString("- ")
			prompt.WriteString(strings.TrimSpace(directive))
			prompt.WriteByte('\n')
		}
	}
	return strings.TrimSpace(prompt.String())
}
