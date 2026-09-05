package runner

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed fgs-input.schema.json
var fgsInputSchema []byte

const FGSLaunchInstruction = "Read AGENTS.md or CLAUDE.md and follow the FGS work protocol."

const fgsInstructions = `## FGS work protocol

You decide how to do the work. Record durable progress as Goal, Step, and Fact.
The update input schema is in .pentest/fgs-input.schema.json. Use low, normal, or high for optional Step priority. Fact data_refs are references only; they do not retain files.
Read accepted state with ` + "`pentestctl working-graph read`" + ` before planning and after resume. Local graph files are working state. Reconcile accepted state and Receipts without replacing local drafts.

- Goal: state the desired result and success criteria. Use goal.create, goal.describe, and goal.transition.
- Step: state work under a Goal. Use step.create, step.describe, and step.transition. Use inputs for Fact keys and after for earlier Step keys.
- Fact: append an observed result with fact.append. Supply a Step key, summary, and optional body. Facts are immutable. Correct an inaccurate Fact with a new Fact whose corrects field names the earlier Fact.
- Goal states: open, active, done, abandoned. A terminal Goal can reopen with a reason. To mark done, supply supporting facts and a summary that explains how they meet the success criteria.
- Step states: open, running, blocked, done, cancelled. A done Step means work finished, including a negative result. Supply outputs that name Facts. A retry is a new Step. Supply a reason when blocked or cancelled.
- Description changes require expected values for the changed fields. Transitions require from and to. Keep node keys stable and unique in the graph.

Publish an object with an operations array through ` + "`pentestctl working-graph emit --input update.json`" + `. Example:

` + "```json" + `
{"operations":[{"op":"goal.create","key":"goal:check","title":"Check access","success_criteria":"The access result is known"},{"op":"step.create","key":"step:check","goal":"goal:check","action":"Read the authorized health endpoint"}]}
` + "```" + `

Publish before work starts and when execution, a blocker, a result, or a decision changes. The Harness reads graph/outbox/<continuation>/ during work. Emit allocates the immutable update ID and sequence. Do not edit published files. Publication is not acceptance: inspect graph/receipts/<continuation>/ for applied or action_required, or use ` + "`pentestctl working-graph status`" + `.

For action_required, publish a new update with resolves: {continuation_id, intent_id} and corrected operations. To withdraw it, use resolves, an empty operations array, and withdrawal_reason. Later dependent updates wait for repair. Independent work can continue. After resume, repair unresolved earlier Continuation updates before dependent updates.

When using multiple agents, Decide publishes updates. Execute writes a Step-specific result file for Decide to inspect. One Runtime can perform both roles. A done Goal does not finish the Task or submit a platform result.

Use graph/ for drafts and Step result files. Blackboard state is the last accepted report, not Runtime liveness. Scope and platform actions retain their own rules. For a Project Task, read .pentest/scope.json and stay within its limits.
`

func writeFGSInstructions(workdir string, ctx RuntimeOwnerContext) error {
	if ctx.Owner.ID == "" {
		return nil
	}
	schemaPath:=filepath.Join(workdir,".pentest","fgs-input.schema.json")
	if err:=os.MkdirAll(filepath.Dir(schemaPath),0700);err!=nil {return err}
	if err:=writeOwnerOnlyFile(schemaPath,fgsInputSchema);err!=nil {return err}
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
