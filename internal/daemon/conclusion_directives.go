package daemon

import (
	"fmt"

	"pentest/internal/owner"
)

// conclusionDirectiveProfile carries only the owner-specific wording that
// separates a Task conclusion directive from a Session one. Every semantic
// rule in the builders below is owner-neutral (ADR 0020): both owners enforce
// identical shape, anti-fabrication, and regeneration rules, so the two
// prompts cannot drift apart again.
type conclusionDirectiveProfile struct {
	// StopCommand opens the conclude directive, e.g. "Stop security testing".
	StopCommand string
	// RepairCommand opens the repair directive, e.g. "Stop exploratory work".
	RepairCommand string
	// BoardName names the owner's Blackboard, e.g. "Session Blackboard".
	BoardName string
	// FinishClause forbids closing the owner, e.g. "finish the Task".
	FinishClause string
	// WorkNoun names the completed source work in the conclude example,
	// e.g. "Session work".
	WorkNoun string
	// ChangedLead opens the regeneration directive, e.g.
	// "The Project Blackboard changed".
	ChangedLead string
}

var taskConclusionDirectiveProfile = conclusionDirectiveProfile{
	StopCommand:   "Stop security testing",
	RepairCommand: "Stop security testing",
	BoardName:     "Blackboard",
	FinishClause:  "finish the Task",
	WorkNoun:      "work",
	ChangedLead:   "The Project Blackboard changed",
}

var sessionConclusionDirectiveProfile = conclusionDirectiveProfile{
	StopCommand:   "Stop the Session's exploratory work",
	RepairCommand: "Stop exploratory work",
	BoardName:     "Session Blackboard",
	FinishClause:  "finish the Session",
	WorkNoun:      "Session work",
	ChangedLead:   "The Session Blackboard changed",
}

// concludeDirective builds the Conclude Runtime Turn directive for one owner.
// The Turn must return one closed JSON Attempt result bound to baseRevision
// without further testing.
func concludeDirective(profile conclusionDirectiveProfile, baseRevision int) string {
	return fmt.Sprintf(`%s and perform only the Harness conclusion below.
Return exactly one JSON object (no markdown fences, no prose) with this shape and base_revision %d:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt/example","create":true,"summary":"One sentence outcome of the completed %s.","outcome":"inconclusive"},"tested_targets":[{"key":"objective/example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Conclude only the current source Work Turn. Do not restate an older terminal Attempt.
Use only existing %s Keys and versions already present in the conversation from the completed source Work Turn. Copy them exactly; never change punctuation or switch between ':' and '/'. If an exact existing key and version are not already known, do not guess or look them up. Create a new descriptive slash-style Attempt or Objective key and use an inconclusive, failed, or blocked outcome without produced targets. A new key must not be a punctuation alias of a current or historical key.
Replace example keys and summaries with this Turn's real semantic targets.
Rules: outcome must be one of succeeded, failed, blocked, or inconclusive. Use inconclusive/failed/blocked when the Turn did not create durable produced graph targets. succeeded requires at least one produced_targets entry that references an already-existing %s key with expected_version; do not invent produced_targets on an empty board.
Describe one Attempt and at least one tested target. Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, %s, or write the %s directly.`,
		profile.StopCommand, baseRevision, baseRevision, profile.WorkNoun, profile.BoardName, profile.BoardName, profile.FinishClause, profile.BoardName)
}

// repairDirective builds the repair directive for one rejected closed result.
// The bounded validation reason from conclusionValidationRepairLine leads the
// directive; the owner-neutral fallback and example keep the repaired result
// from inventing produced targets.
func repairDirective(profile conclusionDirectiveProfile, baseRevision int, detail owner.ConclusionValidationDetail) string {
	directive := fmt.Sprintf(`Your previous %s conclusion result was invalid.
%s and correct only that semantic result.
Return exactly one JSON object (no markdown fences, no prose) with schema runtime-attempt-result/v1 and base_revision %d.
If the board has no existing produced targets, use outcome "inconclusive" (or failed/blocked) with produced_targets [].
Example:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt/example","create":true,"summary":"One sentence outcome of the completed work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective/example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Conclude only the current source Work Turn. Use only existing %s Keys and versions already present in the conversation. Copy them exactly; never change punctuation or switch between ':' and '/'. If an exact existing key and version are not already known, do not guess or look them up. Create a new descriptive slash-style Attempt or Objective key and use an inconclusive, failed, or blocked outcome without produced targets. Do not restate an older terminal Attempt.
Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, %s, or write the %s directly.`,
		profile.BoardName, profile.RepairCommand, baseRevision, baseRevision, profile.BoardName, profile.FinishClause, profile.BoardName)
	return conclusionValidationRepairLine(detail) + "\n" + directive
}

// regenerateDirective rebuilds the semantic result against a synchronized
// baseRevision after an external Blackboard change invalidated the prior one.
func regenerateDirective(profile conclusionDirectiveProfile, baseRevision int) string {
	return fmt.Sprintf(`%s after your previous semantic result was produced.
Regenerate the semantic result against base_revision %d. Use only exact %s Keys and versions already present in the conversation. If a required current version is not already known, create new descriptive slash-style Attempt and Objective keys and use an inconclusive, failed, or blocked outcome with no produced targets. Do not guess or look up current state.
Return exactly one JSON object with schema runtime-attempt-result/v1.
Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, %s, or write the %s directly.`,
		profile.ChangedLead, baseRevision, profile.BoardName, profile.FinishClause, profile.BoardName)
}
