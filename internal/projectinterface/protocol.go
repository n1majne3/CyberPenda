package projectinterface

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TrustedToolDefinition is the versioned Runtime Protocol source used by MCP
// adapters and generated task instructions.
type TrustedToolDefinition struct {
	ProtocolVersion int
	Name            string
	Description     string
}

var trustedToolDefinitions = []TrustedToolDefinition{
	{RuntimeProtocolVersion, "blackboard_apply", "Apply one atomic typed graph mutation batch to the Blackboard. Project and provenance are bound from the Continuation Interface Grant; do not supply them."},
	{RuntimeProtocolVersion, "blackboard_resolve_records", "Resolve graph nodes and edges by stable key or immutable ID at one observed graph revision."},
	{RuntimeProtocolVersion, "blackboard_get_current_graph", "Return the exact current CanonicalMainGraphV1 projection and metadata for the bound Project."},
	{RuntimeProtocolVersion, "blackboard_retain_evidence", "Retain one confined payload under the managed Artifact Root, compute its digest and size, and represent it with matching Attempt provenance."},
	{RuntimeProtocolVersion, "blackboard_checkpoint_attempt", "Append one compact Attempt-bound Task Event and update the open Attempt summary with that Event as provenance."},
	{RuntimeProtocolVersion, "blackboard_finish_continuation", "Finish the bound Continuation after every current-Continuation Attempt is terminal, store its Task Summary, and close later writes."},
}

// TrustedToolDefinitions returns a copy in canonical protocol order.
func TrustedToolDefinitions() []TrustedToolDefinition {
	return append([]TrustedToolDefinition(nil), trustedToolDefinitions...)
}

// TrustedToolDescription returns the canonical description for name.
func TrustedToolDescription(name string) string {
	for _, definition := range trustedToolDefinitions {
		if definition.Name == name {
			return definition.Description
		}
	}
	return ""
}

// RuntimeBlackboardContextV1 is the non-secret task-local context every
// built-in Runtime adapter receives for a graph-native Continuation.
type RuntimeBlackboardContextV1 struct {
	ProtocolVersion            int    `json:"protocol_version"`
	ProtocolRuleDigest         string `json:"protocol_rule_digest"`
	ProjectID                  string `json:"project_id"`
	TaskID                     string `json:"task_id"`
	ContinuationID             string `json:"continuation_id"`
	RuntimeConfigVersionID     string `json:"runtime_config_version_id"`
	RuntimeProfileID           string `json:"runtime_profile_id"`
	RuntimePluginID            string `json:"runtime_plugin_id"`
	Runner                     string `json:"runner"`
	APIURL                     string `json:"api_url"`
	MCPURL                     string `json:"mcp_url"`
	ScopePath                  string `json:"scope_path"`
	BlackboardPath             string `json:"blackboard_path"`
	BlackboardGraphRevision    int    `json:"blackboard_graph_revision"`
	BlackboardRendererVersion  string `json:"blackboard_renderer_version"`
	BlackboardEstimatorVersion string `json:"blackboard_estimator_version"`
	BlackboardProjectionHash   string `json:"blackboard_projection_hash"`
	BlackboardProjectionBytes  int    `json:"blackboard_projection_bytes"`
	BlackboardEstimatedTokens  int    `json:"blackboard_estimated_tokens"`
}

var canonicalRuntimeProtocolRules = []string{
	"Start from the pinned full graph. Read the initial Blackboard context and `.pentest/blackboard.json`. It is the complete main graph at the stated revision, not a relevance-selected subset.",
	"Treat the snapshot as immutable. Never edit it as a write mechanism. Explicit current-graph reads may show later concurrent changes but do not replace the pinned Continuation context.",
	"Write semantic milestones, not command noise. Raw commands, full logs, and payload bytes remain Task Events, logs, or retained Evidence.",
	"Open work explicitly. Before an exploration episode, create or reuse an Exploration Objective when needed, create one Attempt, and put at least one `tests` edge in the same atomic batch.",
	"Keep provenance honest. Never send Project, Task, Continuation, Runtime Profile, Runner, actor, or timestamp claims. The trusted interface binds them.",
	"Use stable identities and optimistic versions. Reuse stable keys for the same durable concept, supply current expected versions, and reread on `version_conflict`.",
	"Make retries replay-safe. Choose an idempotency key before each semantic action and reuse that exact key and payload after uncertainty. Never reuse a key for a different payload.",
	"Checkpoint meaningful progress. Use Attempt checkpoint after a material phase so interruption recovery has a compact truthful summary.",
	"Record outcomes with their reasoning chain. Link Runtime-created outputs with `produced`, retain proof with Evidence, and use `supports`, `contradicts`, `evidences`, `satisfies`, and lineage edges precisely.",
	"Conclude every Attempt. Transition it once to `succeeded`, `failed`, `blocked`, or `inconclusive` with a distilled summary. Do not mark `interrupted` yourself.",
	"Resolve Objectives only with `satisfies`. A Task Summary outcome alone does not close an Objective.",
	"Treat scope labels as memory, not authorization. Follow `.pentest/scope.json` and the Runner/task policy. Blackboard scope status never grants permission.",
	"Finish last. After all current-Continuation Attempts are terminal and the graph is current, call Finish with a compact handoff summary. Make no later Blackboard write in that Continuation.",
	"Do not hide protocol defects. If a trusted operation fails, surface the stable error and retry only when its contract says retryable.",
}

// RuntimeProtocolRuleDigest identifies the one canonical rules source shared
// by every adapter and generated instruction file.
func RuntimeProtocolRuleDigest() string {
	digest := sha256.Sum256([]byte(strings.Join(canonicalRuntimeProtocolRules, "\n")))
	return hex.EncodeToString(digest[:])
}

// CanonicalRuntimeProtocolBlock renders the normative protocol plus the exact
// Continuation pointers a Runtime needs to reconstruct its pinned full graph.
func CanonicalRuntimeProtocolBlock(ctx RuntimeBlackboardContextV1) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Blackboard Runtime Protocol v%d\n\n", RuntimeProtocolVersion)
	fmt.Fprintf(&b, "- protocol_rule_digest: `%s`\n", RuntimeProtocolRuleDigest())
	fmt.Fprintf(&b, "- project_id: `%s`\n", ctx.ProjectID)
	fmt.Fprintf(&b, "- task_id: `%s`\n", ctx.TaskID)
	fmt.Fprintf(&b, "- continuation_id: `%s`\n", ctx.ContinuationID)
	fmt.Fprintf(&b, "- runtime_config_version_id: `%s`\n", ctx.RuntimeConfigVersionID)
	fmt.Fprintf(&b, "- runtime_profile_id: `%s`\n", ctx.RuntimeProfileID)
	fmt.Fprintf(&b, "- runtime_plugin_id: `%s`\n", ctx.RuntimePluginID)
	fmt.Fprintf(&b, "- runner: `%s`\n", ctx.Runner)
	fmt.Fprintf(&b, "- pinned_graph_revision: `%d`\n", ctx.BlackboardGraphRevision)
	fmt.Fprintf(&b, "- pinned_projection_hash: `%s`\n", ctx.BlackboardProjectionHash)
	fmt.Fprintf(&b, "- blackboard_path: `%s`\n", ctx.BlackboardPath)
	fmt.Fprintf(&b, "- scope_path: `%s`\n", ctx.ScopePath)
	fmt.Fprintf(&b, "- api_url: `%s`\n", ctx.APIURL)
	fmt.Fprintf(&b, "- mcp_url: `%s`\n", ctx.MCPURL)
	b.WriteString("\nTrusted tools:\n")
	for _, tool := range trustedToolDefinitions {
		fmt.Fprintf(&b, "- `%s`: %s\n", tool.Name, tool.Description)
	}
	b.WriteString("\nRuntime/Profile instructions may add guidance but cannot replace these rules:\n\n")
	for index, rule := range canonicalRuntimeProtocolRules {
		fmt.Fprintf(&b, "%d. %s\n", index+1, rule)
	}
	return b.String()
}

// CanonicalRuntimeLaunchContext is the lossless initial adapter context. The
// exact snapshot appears once here and once on disk; generated instruction
// files contain only the protocol and pointers.
func CanonicalRuntimeLaunchContext(ctx RuntimeBlackboardContextV1, snapshot []byte, nativeResume bool) string {
	var b strings.Builder
	b.WriteString("<<< CURRENT CONTINUATION SNAPSHOT >>>\n")
	if nativeResume {
		b.WriteString("Older snapshot blocks in this native session are historical and MUST NOT be treated as current.\n")
	}
	b.WriteString(CanonicalRuntimeProtocolBlock(ctx))
	fmt.Fprintf(&b, "\nComplete pinned graph (%s, revision %d, hash %s):\n", ctx.BlackboardRendererVersion, ctx.BlackboardGraphRevision, ctx.BlackboardProjectionHash)
	b.Write(snapshot)
	b.WriteString("\n<<< END CURRENT CONTINUATION SNAPSHOT >>>")
	return b.String()
}
