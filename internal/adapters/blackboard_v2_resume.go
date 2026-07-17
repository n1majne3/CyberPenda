package adapters

import (
	"strings"

	"pentest/internal/blackboardv2"
)

type BlackboardV2ResumeRequest struct {
	TaskGoal            string
	Steering            []string
	InterruptedAttempts []blackboardv2.InterruptedAttemptCheckpoint
}

// BuildBlackboardV2ResumePrompt carries only Task-owned goal and unconsumed
// Harness Steering. Current semantic state remains in the fresh Snapshot pin.
func BuildBlackboardV2ResumePrompt(request BlackboardV2ResumeRequest) string {
	var prompt strings.Builder
	prompt.WriteString("Task Goal:\n")
	prompt.WriteString(strings.TrimSpace(request.TaskGoal))
	if len(request.InterruptedAttempts) != 0 {
		prompt.WriteString("\n\nInterrupted Attempt Checkpoints:\n")
		for _, checkpoint := range request.InterruptedAttempts {
			prompt.WriteString("- ")
			prompt.WriteString(checkpoint.Key)
			prompt.WriteString(": ")
			prompt.WriteString(checkpoint.Summary)
			prompt.WriteByte('\n')
		}
	}
	if len(request.Steering) != 0 {
		prompt.WriteString("\n\nHarness Steering:\n")
		for _, directive := range request.Steering {
			prompt.WriteString("- ")
			prompt.WriteString(strings.TrimSpace(directive))
			prompt.WriteByte('\n')
		}
	}
	return strings.TrimSpace(prompt.String())
}
