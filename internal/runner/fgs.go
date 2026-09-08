package runner

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/blackboardv2"
)

//go:embed fgs-input.schema.json
var fgsInputSchema []byte

// ProjectFGSFiles restores work instructions and Scope in a validated layout.
// It does not project provider config or Continuation credentials.
func ProjectFGSFiles(layout Layout, ctx RuntimeOwnerContext) error {
	if ctx.Owner.IsTask() {
		if err := writeTaskScopeFile(filepath.Join(layout.Workdir, ".pentest"), ctx.ScopeSnapshot); err != nil {
			return err
		}
	}
	return writeFGSInstructions(layout.Workdir, ctx)
}

const FGSLaunchInstruction = `FGS startup: complete these steps before executing the Task Goal below.
1. Read AGENTS.md or CLAUDE.md for the FGS work protocol.
2. Run pentestctl working-graph read to inspect accepted state.
3. Publish a Goal and the first Step with pentestctl working-graph emit --input update.json. Reuse existing nodes after resume.
4. Run pentestctl working-graph status and confirm the update was applied.
Then execute the Step. Before each change of plan, publish the observed result, correct any earlier false Fact with corrects, and make the next Step describe the work you will actually do. During long tool loops, report a failed experiment or new observation before choosing the next experiment. Do not wait until final success to report these changes. Before your final reply, read accepted state, check that Step descriptions match the work done, use current supporting Facts for Goal completion, and confirm the final update receipt.
These reports are part of the Task, even for a small task. Do not substitute chat text, private notes, or a Skill's local files for accepted Blackboard updates. If reporting fails, report the exact blocker and follow the recovery rules in the instruction file.

Task Goal:`

const fgsInstructions = `## FGS work protocol

You decide how to do the work. Record durable progress as Goal, Step, and Fact.
The update input schema is in .pentest/fgs-input.schema.json. Use low, normal, or high for optional Step priority. Fact data_refs are references only; they do not retain files.
If a reporting command fails, use the supplied schema and error to correct the input. Do not reverse engineer the CLI or guess undocumented types. If reporting remains unavailable, keep local result files, report the blocker, and continue independent authorized work toward the user's goal. Do not write under the read-only .pentest directory. PENTEST_API_URL is the CyberPenda API, not a Challenge Platform API.
Read accepted state with ` + "`pentestctl working-graph read`" + ` before planning and after resume. Local graph files are working state. Reconcile accepted state and Receipts without replacing local drafts.

- Goal: state the desired result and success criteria. Use goal.create, goal.describe, and goal.transition.
- Step: state work under a Goal. Use step.create, step.describe, and step.transition. Use inputs for Fact keys and after for earlier Step keys.
- Fact: append an observed result with fact.append. Supply a Step key, summary, and optional body. Facts are immutable. Correct an inaccurate Fact with a new Fact whose corrects field names the earlier Fact.
- Goal states: open, active, done, abandoned. A terminal Goal can reopen with a reason. To mark done, supply supporting facts and a summary that explains how they meet the success criteria.
- Step states: open, running, blocked, done, cancelled. A done Step means work finished, including a negative result. Supply outputs that name Facts. A retry is a new Step. Supply a reason when blocked or cancelled.
- Description changes put the new value in the top-level field and the current value in expected: {"op":"step.describe","key":"step:check","action":"Read the new endpoint","expected":{"action":"Read the old endpoint"}}. Do not put the new value only in expected. Transitions require from and to. Keep node keys stable and unique in the graph.

### Results and changes of plan

Use a Step for an experiment or a decision with a clear result, not for every tool call. Use inputs to name the Facts that justify the work and after to name an earlier Step when order matters. Before choosing the next experiment, report what the previous experiment showed, including a negative result, a failed check, or a blocker. Several tool calls can serve one experiment. Do not leave a long sequence of different experiments inside one unchanged Step until final success.

When evidence changes the plan, publish the result and the next plan together. If the current Step still describes the same work but needs a correction, use step.describe with expected values. If you stop that approach, cancel the Step with a reason and keep its observed Facts. If an experiment finished with a negative result, mark its Step done with output Facts. Create a new Step for a different approach or a retry of completed work, with inputs that explain the change. Do not mark an old approach done as if a different approach had executed it.

Before publication, compare draft Facts and Steps with the latest tool results. Do not publish a draft that you already know is false. A Fact states what was observed, with the relevant conditions and a result file reference when available. Keep an untested plan in the Step action. If an interpretation is useful in a Fact body, label it as a hypothesis, state its evidence and limits, and state what remains untested. A missing expected result does not by itself prove its cause. For example, "the command returned no output" is an observation; "a policy blocked the command" needs separate evidence.

When a result disproves an accepted Fact, append a correction Fact with corrects set to that Fact key. Explain which claim is false, the new observation, and what remains valid. Repeat this for each false Fact; a later summary without corrects does not identify which earlier claim it replaces. Update the affected Step plan in the same batch. Do not cite a disproved Fact as current support for later work or Goal completion.

Before ending a Work Runtime Turn, read accepted state and compare it with the work done. Record missing results, correct false Facts, and make Step descriptions and states accurate. A done Goal must cite Facts that currently support its success criteria; do not include every historical Fact. Do not report a Goal done if its success criteria remain unproven. Check that the final update ID is applied, not merely published or absent from action_required. These checks report semantic progress; they do not finish the Task.

Publish an object with an operations array through ` + "`pentestctl working-graph emit --input update.json`" + `. Example:

` + "```json" + `
{"operations":[{"op":"goal.create","key":"goal:check","title":"Check access","success_criteria":"The access result is known"},{"op":"step.create","key":"step:check","goal":"goal:check","action":"Read the authorized health endpoint"}]}
` + "```" + `

Publish before work starts and when execution, a blocker, a result, or a decision changes. The Harness reads graph/outbox/<continuation>/ during work. Emit allocates the immutable update ID and sequence and waits up to 5 seconds for its Receipt. It returns applied on acceptance. A rejection or timeout returns a nonzero exit status. On timeout the update is still published: run status, keep its ID, and do not republish or withdraw it because the Receipt is missing. The optional --wait 0 returns published without waiting. Check pending Receipts at the next decision boundary; do not defer this until the final reply. Do not edit published files. Publication is not acceptance: inspect graph/receipts/<continuation>/ for applied or action_required, or use ` + "`pentestctl working-graph status`" + `.

Operations run in array order: create a Fact before a Step transition that names it in outputs. Updates are atomic: if any operation fails, NONE of that update's operations were applied. The receipt operation index identifies the error, not a partially applied prefix. Read accepted state again and resend the COMPLETE corrected batch, including its Facts and Step results. Use the accepted node state for from; creating a Goal leaves it open, not active.

For action_required, publish a new update with resolves: {continuation_id, intent_id} naming the ORIGINAL rejected update and the complete corrected operations. If that repair also fails, the original update remains the blocker: target it again, not the failed repair. To withdraw the original update, use resolves, an empty operations array, and withdrawal_reason. Withdrawal discards the whole batch; it does not preserve its earlier operations. After withdrawal, read accepted state before a fresh update. Never withdraw a missing receipt as if it were a rejected update.

Later dependent updates wait for repair. Independent work can continue. After resume, repair unresolved earlier Continuation updates before dependent updates. If a receipt is still missing after a bounded check, run status and report the blocker; do not spend repeated minutes sleeping or create speculative repair chains. Preserve result files so reporting can resume without repeating platform actions.

When using multiple agents, Decide publishes updates. Execute writes a Step-specific result file for Decide to inspect. One Runtime can perform both roles. A done Goal does not finish the Task or submit a platform result.

Use graph/ for drafts and Step result files. Blackboard state is the last accepted report, not Runtime liveness. Scope and platform actions retain their own rules. For a Project Task, read .pentest/scope.json and stay within its limits.
`

func writeFGSInstructions(workdir string, ctx RuntimeOwnerContext) error {
	if ctx.Owner.ID == "" {
		return nil
	}
	schemaPath := filepath.Join(workdir, ".pentest", "fgs-input.schema.json")
	if err := os.MkdirAll(filepath.Dir(schemaPath), 0700); err != nil {
		return err
	}
	if err := writeOwnerOnlyFile(schemaPath, fgsInputSchema); err != nil {
		return err
	}
	const start = "<!-- cyberpenda:fgs:start -->"
	const end = "<!-- cyberpenda:fgs:end -->"
	section := start + "\n" + fgsInstructions + "\n" + end
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(workdir, name)
		info, err := os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("FGS instructions require a regular file: %s", name)
		}
		raw, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		text := string(raw)
		// Replace only the exact checklist produced by ProjectBlackboardV2Files.
		// Operator additions and modified text remain intact.
		legacy := "# Blackboard workflow\n\n" + blackboardv2.CodexChecklist() + "\n"
		text = strings.TrimPrefix(text, legacy)
		a, b := strings.Index(text, start), strings.Index(text, end)
		if a >= 0 || b >= 0 {
			if a < 0 || b < a || strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
				return fmt.Errorf("invalid managed FGS section in %s", name)
			}
			text = text[:a] + section + text[b+len(end):]
		} else {
			text += "\n" + section + "\n"
		}
		if err = writeOwnerOnlyFile(path, []byte(text)); err != nil {
			return err
		}
	}
	return nil
}
