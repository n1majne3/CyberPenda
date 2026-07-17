package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/mcpserver"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

var wantV2TrustedTools = []string{
	"blackboard_change",
	"blackboard_read",
	"blackboard_history",
	"blackboard_retain_evidence",
	"blackboard_checkpoint_attempt",
	"blackboard_finish",
}

type v2MCPFixture struct {
	root         string
	db           *store.DB
	board        *blackboardv2.Service
	project      project.Project
	foreign      project.Project
	task         task.Task
	continuation task.TaskContinuation
	token        string
	peer         task.TaskContinuation
	peerToken    string
	foreignRun   task.TaskContinuation
	foreignToken string
	profile      runtimeprofile.Profile
	continuity   *blackboardv2.ContinuityService
	ownerGrant   projectinterface.Grant
	foreignGrant projectinterface.Grant
}

func newV2MCPFixture(t *testing.T) v2MCPFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	runtimeRoot := filepath.Join(root, "runs")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	created, err := projects.Create("MCP v2", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	foreign, err := projects.Create("Foreign MCP v2", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	profile, err := runtimeprofile.NewService(db).Create("MCP Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	tasks := task.NewService(db, projects)
	createTask := func(projectID, goal string) task.Task {
		createdTask, err := tasks.Create(task.CreateRequest{
			ProjectID: projectID, Goal: goal, RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		return createdTask
	}
	board := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: root, RuntimeRoot: runtimeRoot})
	continuity := blackboardv2.NewContinuityService(db, board, tasks, runtimeRoot)
	launch := func(createdTask task.Task) blackboardv2.ContinuationLaunch {
		result, err := continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
			ProjectID: createdTask.ProjectID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
			RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
			RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
		})
		if err != nil {
			t.Fatalf("create Continuation: %v", err)
		}
		return result
	}
	ownerTask := createTask(created.ID, "exercise trusted MCP")
	peerTask := createTask(created.ID, "observe shared changes")
	foreignTask := createTask(foreign.ID, "stay isolated")
	ownerLaunch := launch(ownerTask)
	peerLaunch := launch(peerTask)
	foreignLaunch := launch(foreignTask)
	grants := projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	ownerGrant, err := grants.Resolve(context.Background(), ownerLaunch.Token)
	if err != nil {
		t.Fatalf("resolve owner grant: %v", err)
	}
	foreignGrant, err := grants.Resolve(context.Background(), foreignLaunch.Token)
	if err != nil {
		t.Fatalf("resolve foreign grant: %v", err)
	}
	return v2MCPFixture{
		root: root, db: db, board: board, project: created, foreign: foreign,
		task: ownerTask, continuation: ownerLaunch.Continuation, token: ownerLaunch.Token,
		peer: peerLaunch.Continuation, peerToken: peerLaunch.Token,
		foreignRun: foreignLaunch.Continuation, foreignToken: foreignLaunch.Token,
		profile: profile, continuity: continuity, ownerGrant: ownerGrant, foreignGrant: foreignGrant,
	}
}

func (f v2MCPFixture) session(t *testing.T, grant *projectinterface.Grant, grantErr *blackboardv2.Error) *sdkmcp.ClientSession {
	t.Helper()
	return connectMCPV2(t, mcpserver.V2Deps{BlackboardV2: f.board, Grant: grant, GrantError: grantErr})
}

func keys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func mcpText(result *sdkmcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*sdkmcp.TextContent); ok {
		return text.Text
	}
	return ""
}

func callV2Tool(t *testing.T, session *sdkmcp.ClientSession, name string, args any) (*sdkmcp.CallToolResult, []byte) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result, []byte(mcpText(result))
}

func TestBlackboardV2MCPRegistersExactlySixTrustedTools(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		got[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("tool %s omitted InputSchema", tool.Name)
		}
	}
	if len(listed.Tools) != len(wantV2TrustedTools) {
		t.Fatalf("tools = %d (%v), want %d exact names %v", len(listed.Tools), keys(got), len(wantV2TrustedTools), wantV2TrustedTools)
	}
	for _, name := range wantV2TrustedTools {
		if !got[name] {
			t.Errorf("missing trusted tool %q; got %v", name, keys(got))
		}
	}
	for _, retired := range []string{
		"blackboard_apply", "blackboard_resolve_records", "blackboard_get_current_graph",
		"blackboard_finish_continuation", "upsert_project_fact", "list_project_facts",
	} {
		result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: retired, Arguments: map[string]any{}})
		if err == nil && (result == nil || !result.IsError) {
			// Unknown tools surface as transport errors from the SDK.
		}
		if err == nil || !strings.Contains(err.Error(), `unknown tool "`+retired+`"`) {
			if result != nil && result.IsError {
				continue
			}
			t.Fatalf("retired tool %q call err=%v result=%#v", retired, err, result)
		}
	}
}

func TestBlackboardV2MCPInputSchemasAreClosedContractObjects(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatalf("contract harness: %v", err)
	}
	tools, err := harness.TrustedTools()
	if err != nil {
		t.Fatalf("trusted tools: %v", err)
	}
	byName := make(map[string]blackboardv2contract.TrustedTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	forbiddenAuthority := []string{
		"project_id", "task_id", "continuation_id", "actor_id", "actor_type",
		"origin", "property_map", "fact_key", "get_current_graph", "protocol_version",
	}
	for _, tool := range listed.Tools {
		contract, ok := byName[tool.Name]
		if !ok {
			t.Fatalf("unexpected registered tool %q", tool.Name)
		}
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}
		if !bytes.Contains(schemaJSON, []byte(`"additionalProperties":false`)) &&
			!bytes.Contains(schemaJSON, []byte(`"additionalProperties": false`)) {
			// Root object must be closed; nested defs also carry additionalProperties:false.
			t.Errorf("%s schema is not closed: %s", tool.Name, schemaJSON)
		}
		if tool.Description != contract.Description {
			t.Errorf("%s description = %q, want contract %q", tool.Name, tool.Description, contract.Description)
		}
		for _, forbidden := range forbiddenAuthority {
			// Structural presence in nested enum values is fine; property names for
			// caller-supplied authority must not appear as schema properties.
			if bytes.Contains(schemaJSON, []byte(`"`+forbidden+`"`)) {
				// property maps and get_current_graph must not appear at all.
				if forbidden == "property_map" || forbidden == "get_current_graph" || forbidden == "fact_key" {
					t.Errorf("%s schema advertises forbidden %q", tool.Name, forbidden)
				}
			}
		}
		// Model-facing authority keys must not be required or optional properties.
		for _, authorityField := range []string{"project_id", "task_id", "continuation_id", "actor_id", "origin"} {
			if bytes.Contains(schemaJSON, []byte(`"`+authorityField+`":`)) {
				t.Errorf("%s schema exposes authority field %q: %s", tool.Name, authorityField, schemaJSON)
			}
		}
	}
}

func TestBlackboardV2MCPChangeReadHistoryParityAndExactReplay(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)
	ctx := context.Background()

	change := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-create-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:mcp", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "MCP host", ScopeStatus: "in_scope"},
		}},
	}
	want, err := fixture.board.ApplyForContinuation(ctx, fixture.project.ID, fixture.continuation.ID, change)
	if err != nil {
		t.Fatalf("service change: %v", err)
	}
	// Reset by replaying is no-op; use a second key for MCP path comparison after
	// clearing via a fresh batch. Apply MCP against a different idempotency for
	// service parity on a second entity, and assert MCP matches service for the
	// same batch on exact replay of the already-applied key.
	mcpFirst, firstRaw := callV2Tool(t, session, "blackboard_change", change)
	if mcpFirst.IsError {
		t.Fatalf("MCP change isError: %s", firstRaw)
	}
	var got blackboardv2.ChangeResult
	if err := json.Unmarshal(firstRaw, &got); err != nil {
		t.Fatalf("decode MCP change: %v", err)
	}
	if got.Schema != want.Schema || got.Revision != want.Revision || got.WorkingSnapshot.Path != want.WorkingSnapshot.Path {
		t.Fatalf("MCP change = %#v, service = %#v", got, want)
	}
	mcpReplay, replayRaw := callV2Tool(t, session, "blackboard_change", change)
	if mcpReplay.IsError || !bytes.Equal(firstRaw, replayRaw) {
		t.Fatalf("exact MCP change replay drifted: isError=%v\nfirst=%s\nreplay=%s", mcpReplay.IsError, firstRaw, replayRaw)
	}
	altered := change
	altered.Changes = append([]blackboardv2.Change(nil), change.Changes...)
	altered.Changes[0].Record = blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Different", ScopeStatus: "in_scope"}
	mcpAltered, alteredRaw := callV2Tool(t, session, "blackboard_change", altered)
	if !mcpAltered.IsError || !bytes.Contains(alteredRaw, []byte(`"idempotency_conflict"`)) {
		t.Fatalf("altered replay = isError=%v body=%s", mcpAltered.IsError, alteredRaw)
	}

	// Malicious unknown fields must not be accepted on the closed change envelope.
	malicious := map[string]any{
		"schema": "semantic-change-batch/v2", "idempotency_key": "mcp-malicious",
		"changes": []any{}, "project_id": fixture.project.ID, "actor_id": "model",
	}
	maliciousResult, maliciousRaw := callV2Tool(t, session, "blackboard_change", malicious)
	if !maliciousResult.IsError {
		t.Fatalf("malicious authority fields accepted: %s", maliciousRaw)
	}

	serviceDetail, err := fixture.board.ReadCurrent(ctx, fixture.project.ID, "entity:mcp")
	if err != nil {
		t.Fatalf("service read: %v", err)
	}
	mcpRead, readRaw := callV2Tool(t, session, "blackboard_read", map[string]any{"key": "entity:mcp"})
	if mcpRead.IsError {
		t.Fatalf("MCP read isError: %s", readRaw)
	}
	var detail blackboardv2.CurrentDetail
	if err := json.Unmarshal(readRaw, &detail); err != nil {
		t.Fatalf("decode MCP read: %v", err)
	}
	if detail.Schema != serviceDetail.Schema || detail.Key != serviceDetail.Key || detail.Version != serviceDetail.Version || detail.Record.Name != "MCP host" {
		t.Fatalf("MCP detail = %#v, service = %#v", detail, serviceDetail)
	}

	for version, name := range []string{"MCP host v2", "MCP host v3"} {
		updated := blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-update-" + name,
			Changes: []blackboardv2.Change{{
				Op: "update", Key: "entity:mcp", Version: version + 1, Type: "entity",
				Record: blackboardv2.EntityPatch{Name: &name},
			}},
		}
		if result, raw := callV2Tool(t, session, "blackboard_change", updated); result.IsError {
			t.Fatalf("update version %d: %s", version+1, raw)
		}
	}
	mcpHistory, historyRaw := callV2Tool(t, session, "blackboard_history", map[string]any{"key": "entity:mcp", "limit": 1})
	if mcpHistory.IsError {
		t.Fatalf("MCP history isError: %s", historyRaw)
	}
	var page blackboardv2.SemanticHistory
	if err := json.Unmarshal(historyRaw, &page); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if page.Schema != "semantic-history/v2" || len(page.Items) != 1 || page.Items[0].Version != 1 || page.NextCursor == "" {
		t.Fatalf("history page = %#v", page)
	}
	mcpPageTwo, pageTwoRaw := callV2Tool(t, session, "blackboard_history", map[string]any{
		"key": "entity:mcp", "cursor": page.NextCursor, "limit": 1,
	})
	if mcpPageTwo.IsError {
		t.Fatalf("MCP history page two isError: %s", pageTwoRaw)
	}
	var pageTwo blackboardv2.SemanticHistory
	if err := json.Unmarshal(pageTwoRaw, &pageTwo); err != nil {
		t.Fatalf("decode history page two: %v", err)
	}
	if len(pageTwo.Items) == 0 || pageTwo.Items[0].Version < 2 {
		t.Fatalf("history page two = %#v", pageTwo)
	}
}

func TestBlackboardV2MCPEvidenceCheckpointFinishAndDTOIsolation(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)

	prepare := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-open-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:mcp-target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "MCP target", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:mcp", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain MCP proof"}},
			{Op: "create", Key: "attempt:mcp", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Collecting MCP proof"}},
			{Op: "relate", From: "attempt:mcp", Relation: "tests", To: "objective:mcp"},
		},
	}
	if result, raw := callV2Tool(t, session, "blackboard_change", prepare); result.IsError {
		t.Fatalf("prepare open work: %s", raw)
	}
	workdir := filepath.Join(fixture.root, "runs", fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create Runtime workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("exact MCP evidence\n"), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}
	retain := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "mcp-retain-proof", Key: "evidence:mcp", Attempt: "attempt:mcp",
		SourcePath: "proof.txt", ArtifactType: "text", Summary: "MCP retained proof", MediaType: "text/plain",
		Links: []blackboardv2.EvidenceLink{{"about", "entity:mcp-target"}},
	}
	retainResult, retainRaw := callV2Tool(t, session, "blackboard_retain_evidence", retain)
	if retainResult.IsError {
		t.Fatalf("retain Evidence: %s", retainRaw)
	}
	var retained blackboardv2.ChangeResult
	if err := json.Unmarshal(retainRaw, &retained); err != nil || retained.Schema != "semantic-change-result/v2" {
		t.Fatalf("retain result=%s err=%v", retainRaw, err)
	}
	// Response DTO must not leak Project/Task/Continuation/internal authority.
	for _, leak := range []string{fixture.project.ID, fixture.task.ID, fixture.continuation.ID, fixture.token, `"project_id"`, `"task_id"`, `"continuation_id"`} {
		if bytes.Contains(retainRaw, []byte(leak)) {
			t.Fatalf("retain response leaked %q: %s", leak, retainRaw)
		}
	}

	checkpoint := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "mcp-checkpoint", Key: "attempt:mcp", Version: 1, Summary: "Proof retained; concluding",
	}
	if result, raw := callV2Tool(t, session, "blackboard_checkpoint_attempt", checkpoint); result.IsError {
		t.Fatalf("checkpoint: %s", raw)
	}
	terminal := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-terminal-attempt",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:mcp", Version: 2, Status: "succeeded", Summary: "MCP proof retained"}},
	}
	if result, raw := callV2Tool(t, session, "blackboard_change", terminal); result.IsError {
		t.Fatalf("terminalize Attempt: %s", raw)
	}
	finish := blackboardv2.FinishContinuationRequest{IdempotencyKey: "mcp-finish"}
	finishResult, finishRaw := callV2Tool(t, session, "blackboard_finish", finish)
	if finishResult.IsError {
		t.Fatalf("finish: %s", finishRaw)
	}
	replayResult, replayRaw := callV2Tool(t, session, "blackboard_finish", finish)
	if replayResult.IsError || !bytes.Equal(finishRaw, replayRaw) {
		t.Fatalf("finish replay drifted: isError=%v\nfirst=%s\nreplay=%s", replayResult.IsError, finishRaw, replayRaw)
	}
	var finished blackboardv2.FinishContinuationResult
	if err := json.Unmarshal(finishRaw, &finished); err != nil || finished.Schema != "continuation-finish/v2" || finished.Status != "finished" {
		t.Fatalf("finish result=%s err=%v", finishRaw, err)
	}
	if bytes.Contains(finishRaw, []byte(`"sync"`)) {
		t.Fatalf("finish response attached live sync: %s", finishRaw)
	}
	closedCheckpoint := checkpoint
	closedCheckpoint.IdempotencyKey = "mcp-checkpoint-after-finish"
	closedCheckpoint.Version = 2
	closedResult, closedRaw := callV2Tool(t, session, "blackboard_checkpoint_attempt", closedCheckpoint)
	if !closedResult.IsError || !bytes.Contains(closedRaw, []byte(`"closed_continuation"`)) {
		t.Fatalf("post-finish checkpoint = %s", closedRaw)
	}
}

func TestBlackboardV2MCPAuthorityIsolatesProjectsAndRequiresGrant(t *testing.T) {
	fixture := newV2MCPFixture(t)

	// No grant: authority denied.
	unauth := fixture.session(t, nil, nil)
	result, raw := callV2Tool(t, unauth, "blackboard_read", map[string]any{"key": "entity:none"})
	if !result.IsError || !bytes.Contains(raw, []byte(`"authority_denied"`)) {
		t.Fatalf("missing grant error = %s", raw)
	}

	// Invalid grant presentation.
	invalid := fixture.session(t, nil, &blackboardv2.Error{
		Code: "authority_denied", Message: "Continuation Interface capability is invalid", Path: "authorization",
	})
	result, raw = callV2Tool(t, invalid, "blackboard_read", map[string]any{"key": "entity:none"})
	if !result.IsError || !bytes.Contains(raw, []byte(`"authority_denied"`)) {
		t.Fatalf("invalid grant error = %s", raw)
	}

	// Owner creates project-local knowledge.
	owner := fixture.session(t, &fixture.ownerGrant, nil)
	create := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-owner-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:owner-only", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Owner host", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, owner, "blackboard_change", create); result.IsError {
		t.Fatalf("owner change: %s", raw)
	}

	// Foreign Continuation cannot read another Project's keys through its own grant.
	foreign := fixture.session(t, &fixture.foreignGrant, nil)
	foreignRead, foreignRaw := callV2Tool(t, foreign, "blackboard_read", map[string]any{"key": "entity:owner-only"})
	if !foreignRead.IsError {
		t.Fatalf("foreign grant read leaked owner data: %s", foreignRaw)
	}
	// Foreign project change with owner key is isolated (creates in foreign Project only).
	foreignCreate := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-foreign-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:owner-only", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Foreign host", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, foreign, "blackboard_change", foreignCreate); result.IsError {
		t.Fatalf("foreign change: %s", raw)
	}
	ownerRead, ownerRaw := callV2Tool(t, owner, "blackboard_read", map[string]any{"key": "entity:owner-only"})
	if ownerRead.IsError {
		t.Fatalf("owner reread: %s", ownerRaw)
	}
	var ownerDetail blackboardv2.CurrentDetail
	if err := json.Unmarshal(ownerRaw, &ownerDetail); err != nil {
		t.Fatalf("decode owner detail: %v", err)
	}
	if ownerDetail.Record.Name != "Owner host" {
		t.Fatalf("cross-project isolation broken: %#v", ownerDetail)
	}
}

func TestBlackboardV2MCPPeerReadAttachesSynchronization(t *testing.T) {
	fixture := newV2MCPFixture(t)
	owner := fixture.session(t, &fixture.ownerGrant, nil)
	create := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-sync-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:sync", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Sync host", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, owner, "blackboard_change", create); result.IsError {
		t.Fatalf("owner change: %s", raw)
	}
	peerGrant, err := projectinterface.NewGrantStore(fixture.db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}).
		Resolve(context.Background(), fixture.peerToken)
	if err != nil {
		t.Fatalf("resolve peer grant: %v", err)
	}
	peer := fixture.session(t, &peerGrant, nil)
	read, raw := callV2Tool(t, peer, "blackboard_read", map[string]any{"key": "entity:sync"})
	if read.IsError {
		t.Fatalf("peer read: %s", raw)
	}
	var synchronized struct {
		Schema string `json:"schema"`
		Sync   *struct {
			Reason       string `json:"reason"`
			FromRevision int    `json:"from_revision"`
			Revision     int    `json:"revision"`
		} `json:"sync"`
	}
	if err := json.Unmarshal(raw, &synchronized); err != nil {
		t.Fatalf("decode synchronized read: %v", err)
	}
	if synchronized.Schema != "blackboard-record/v2" || synchronized.Sync == nil ||
		synchronized.Sync.Reason != "another_task_changed_shared_project_knowledge" {
		t.Fatalf("synchronization attachment = %#v body=%s", synchronized, raw)
	}
}

// Issue #117 P1 — MCP response-loss retry redelivers the exact sync attachment
// for the same idempotency key; later operations stay ordinary.
func TestBlackboardV2MCPResponseLossRetryRedeliversExactSyncAttachment(t *testing.T) {
	fixture := newV2MCPFixture(t)
	owner := fixture.session(t, &fixture.ownerGrant, nil)
	peerGrant, err := projectinterface.NewGrantStore(fixture.db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}).
		Resolve(context.Background(), fixture.peerToken)
	if err != nil {
		t.Fatalf("resolve peer grant: %v", err)
	}
	peer := fixture.session(t, &peerGrant, nil)
	open := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-loss-open",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:mcp-loss", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Loss"}},
			{Op: "create", Key: "attempt:mcp-loss", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Watching"}},
			{Op: "relate", From: "attempt:mcp-loss", Relation: "tests", To: "objective:mcp-loss"},
		},
	}
	if result, raw := callV2Tool(t, peer, "blackboard_change", open); result.IsError {
		t.Fatalf("peer open: %s", raw)
	}
	external := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-loss-external",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:mcp-loss", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "MCP loss", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, owner, "blackboard_change", external); result.IsError {
		t.Fatalf("owner external: %s", raw)
	}
	checkpoint := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "mcp-loss-checkpoint", Key: "attempt:mcp-loss", Version: 1, Summary: "Pending sync checkpoint",
	}
	firstResult, firstRaw := callV2Tool(t, peer, "blackboard_checkpoint_attempt", checkpoint)
	if firstResult.IsError {
		t.Fatalf("first checkpoint: %s", firstRaw)
	}
	var delivered struct {
		Schema string                                 `json:"schema"`
		Sync   *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(firstRaw, &delivered); err != nil || delivered.Schema != "semantic-change-result/v2" || delivered.Sync == nil {
		t.Fatalf("first checkpoint body=%s err=%v", firstRaw, err)
	}
	// Post-action current Snapshot (checkpoint write + sync) is the delivery contract.
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	if delivered.Sync.Reason != "another_task_changed_shared_project_knowledge" || delivered.Sync.Revision != want.Snapshot.Revision {
		t.Fatalf("sync attachment = %#v want revision %d", delivered.Sync, want.Snapshot.Revision)
	}
	gotSnapshot, err := json.Marshal(delivered.Sync.Snapshot)
	if err != nil || !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("sync Snapshot drifted\ngot=%s\nwant=%s err=%v", gotSnapshot, want.Bytes, err)
	}
	retryResult, retryRaw := callV2Tool(t, peer, "blackboard_checkpoint_attempt", checkpoint)
	if retryResult.IsError || !bytes.Equal(firstRaw, retryRaw) {
		t.Fatalf("response-loss retry drifted: isError=%v\nfirst=%s\nretry=%s", retryResult.IsError, firstRaw, retryRaw)
	}
	later := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "mcp-loss-later", Key: "attempt:mcp-loss", Version: 2, Summary: "Later ordinary",
	}
	laterResult, laterRaw := callV2Tool(t, peer, "blackboard_checkpoint_attempt", later)
	if laterResult.IsError {
		t.Fatalf("later checkpoint: %s", laterRaw)
	}
	if bytes.Contains(laterRaw, []byte(`"sync"`)) {
		t.Fatalf("later checkpoint reattached sync: %s", laterRaw)
	}
}

// Issue #117 P1 — initial live MCP Finish carries exact sync when pending.
func TestBlackboardV2MCPFinishCarriesSyncWhenPendingAndExactReplayStable(t *testing.T) {
	fixture := newV2MCPFixture(t)
	owner := fixture.session(t, &fixture.ownerGrant, nil)
	peerGrant, err := projectinterface.NewGrantStore(fixture.db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}).
		Resolve(context.Background(), fixture.peerToken)
	if err != nil {
		t.Fatalf("resolve peer grant: %v", err)
	}
	peer := fixture.session(t, &peerGrant, nil)
	open := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-finish-sync-open",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:mcp-finish-sync", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Finish sync"}},
			{Op: "create", Key: "attempt:mcp-finish-sync", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Work"}},
			{Op: "relate", From: "attempt:mcp-finish-sync", Relation: "tests", To: "objective:mcp-finish-sync"},
		},
	}
	if result, raw := callV2Tool(t, peer, "blackboard_change", open); result.IsError {
		t.Fatalf("peer open: %s", raw)
	}
	terminal := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-finish-sync-terminal",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:mcp-finish-sync", Version: 1, Status: "failed", Summary: "Done without reusable outcome"}},
	}
	if result, raw := callV2Tool(t, peer, "blackboard_change", terminal); result.IsError {
		t.Fatalf("terminalize: %s", raw)
	}
	// External advance after the Runtime's own terminal write so Finish itself
	// carries the pending synchronization attachment.
	external := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-finish-sync-external",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:mcp-finish-sync", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Finish sync", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, owner, "blackboard_change", external); result.IsError {
		t.Fatalf("owner external: %s", raw)
	}
	want, err := fixture.board.ProjectRuntimeSnapshot(context.Background(), fixture.project.ID)
	if err != nil {
		t.Fatalf("project Snapshot: %v", err)
	}
	finish := blackboardv2.FinishContinuationRequest{IdempotencyKey: "mcp-finish-with-sync"}
	finishResult, finishRaw := callV2Tool(t, peer, "blackboard_finish", finish)
	if finishResult.IsError {
		t.Fatalf("finish: %s", finishRaw)
	}
	var envelope struct {
		Schema   string                                 `json:"schema"`
		Status   string                                 `json:"status"`
		Revision int                                    `json:"revision"`
		Sync     *blackboardv2.SynchronizationAttachment `json:"sync"`
	}
	if err := json.Unmarshal(finishRaw, &envelope); err != nil {
		t.Fatalf("decode finish: %v body=%s", err, finishRaw)
	}
	if envelope.Schema != "continuation-finish/v2" || envelope.Status != "finished" || envelope.Sync == nil {
		t.Fatalf("finish while pending must carry sync: %s", finishRaw)
	}
	if envelope.Sync.Reason != "another_task_changed_shared_project_knowledge" || envelope.Sync.Revision != want.Snapshot.Revision {
		t.Fatalf("finish sync = %#v", envelope.Sync)
	}
	gotSnapshot, err := json.Marshal(envelope.Sync.Snapshot)
	if err != nil || !bytes.Equal(gotSnapshot, want.Bytes) {
		t.Fatalf("finish sync Snapshot drifted\ngot=%s\nwant=%s err=%v", gotSnapshot, want.Bytes, err)
	}
	for _, leak := range []string{fixture.task.ID, fixture.peer.ID, `"task_id"`, `"project_id"`, `"continuation_id"`} {
		if leak != "" && bytes.Contains(finishRaw, []byte(leak)) {
			t.Fatalf("finish sync leaked %q: %s", leak, finishRaw)
		}
	}
	replayResult, replayRaw := callV2Tool(t, peer, "blackboard_finish", finish)
	if replayResult.IsError || !bytes.Equal(finishRaw, replayRaw) {
		t.Fatalf("exact Finish replay drifted: isError=%v\nfirst=%s\nreplay=%s", replayResult.IsError, finishRaw, replayRaw)
	}
}

func assertV2InvalidSchemaEnvelope(t *testing.T, raw []byte) {
	t.Helper()
	var envelope struct {
		Error *struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Path      string         `json:"path"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
		Sync json.RawMessage `json:"sync"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode invalid_schema envelope: %v body=%s", err, raw)
	}
	if envelope.Error == nil || envelope.Error.Code != "invalid_schema" || envelope.Error.Message == "" || envelope.Error.Retryable {
		t.Fatalf("invalid_schema envelope = %s", raw)
	}
	if envelope.Error.Path != "arguments" {
		t.Fatalf("invalid_schema path = %q, want arguments; body=%s", envelope.Error.Path, raw)
	}
	if len(envelope.Sync) != 0 {
		t.Fatalf("invalid_schema attached sync: %s", raw)
	}
	// Reject the SDK's generic validation text so agents always see the compact v2 envelope.
	if strings.Contains(string(raw), `validating "arguments"`) {
		t.Fatalf("generic SDK validation text leaked: %s", raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode envelope object: %v", err)
	}
	if len(fields) != 1 || fields["error"] == nil {
		t.Fatalf("envelope keys = %v, want only error; body=%s", keysRaw(fields), raw)
	}
	var errorFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["error"], &errorFields); err != nil {
		t.Fatalf("decode error object: %v", err)
	}
	for _, required := range []string{"code", "message", "retryable"} {
		if errorFields[required] == nil {
			t.Fatalf("error missing %s: %s", required, raw)
		}
	}
	for key := range errorFields {
		switch key {
		case "code", "message", "path", "retryable", "details":
		default:
			t.Fatalf("error has unexpected field %q: %s", key, raw)
		}
	}
}

func keysRaw(values map[string]json.RawMessage) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func TestBlackboardV2MCPInvalidSchemaReturnsStableErrorEnvelope(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)

	cases := []struct {
		name string
		tool string
		args any
	}{
		{
			name: "unknown project_id on change",
			tool: "blackboard_change",
			args: map[string]any{
				"schema": "semantic-change-batch/v2", "idempotency_key": "schema-project-id",
				"changes": []any{}, "project_id": fixture.project.ID,
			},
		},
		{
			name: "missing required change fields",
			tool: "blackboard_change",
			args: map[string]any{"idempotency_key": "schema-missing"},
		},
		{
			name: "unknown task_id on finish",
			tool: "blackboard_finish",
			args: map[string]any{"idempotency_key": "schema-finish", "task_id": fixture.task.ID},
		},
		{
			name: "unknown authority on read",
			tool: "blackboard_read",
			args: map[string]any{"key": "entity:x", "continuation_id": fixture.continuation.ID},
		},
		{
			name: "wrong type on history limit",
			tool: "blackboard_history",
			args: map[string]any{"key": "entity:x", "limit": "twenty"},
		},
		{
			name: "unknown field on retain",
			tool: "blackboard_retain_evidence",
			args: map[string]any{
				"idempotency_key": "schema-retain", "key": "evidence:x", "attempt": "attempt:x",
				"source_path": "a.txt", "artifact_type": "text", "summary": "x", "actor_id": "model",
			},
		},
		{
			name: "unknown field on checkpoint",
			tool: "blackboard_checkpoint_attempt",
			args: map[string]any{
				"idempotency_key": "schema-checkpoint", "key": "attempt:x", "version": 1, "summary": "x",
				"origin": "runtime",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, raw := callV2Tool(t, session, tc.tool, tc.args)
			if !result.IsError {
				t.Fatalf("expected schema error, got success: %s", raw)
			}
			assertV2InvalidSchemaEnvelope(t, raw)
		})
	}
}

func TestBlackboardV2MCPToolInputSchemasContainOnlyTransitiveRootDefs(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatalf("contract harness: %v", err)
	}
	tools, err := harness.TrustedTools()
	if err != nil {
		t.Fatalf("trusted tools: %v", err)
	}
	byName := make(map[string]blackboardv2contract.TrustedTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	if len(listed.Tools) != 6 {
		t.Fatalf("tools = %d, want 6", len(listed.Tools))
	}
	forbiddenSubstrings := []string{
		"project_id", "task_id", "continuation_id", "actor_id", "actor_type",
		"property_map", "get_current_graph", "get-current-graph",
		"list_project_facts", "migrationPlan", "migrationProject", "migrationDecision",
		"migrationBlocker", "migrationMapping", "migrationResult", "migrationSource",
		"migrationValidation", "runtimeSnapshot", "snapshotKnowledge", "snapshotWork",
		"unauthenticatedErrorEnvelope", "errorEnvelope", "errorBody",
		"FinishContinuation", "TaskSummary", "continuation_interface",
	}
	// Definition names that must never appear unless they are part of the selected
	// root DTO's transitive closure (checked below against the harness).
	unrelatedDefNames := []string{
		"migrationPlan", "migrationProjectPlan", "migrationDecision", "migrationBlocker",
		"migrationMapping", "migrationResult", "migrationSource", "migrationValidation",
		"migrationProjectResult", "runtimeSnapshot", "snapshotKnowledge", "snapshotWork",
		"snapshotEntity", "snapshotObjective", "snapshotAttempt", "snapshotFact",
		"snapshotFinding", "snapshotSolution", "snapshotEvidence",
		"errorEnvelope", "errorBody", "unauthenticatedErrorEnvelope", "syncAttachment",
		"syncResponse", "currentDetail", "semanticHistory", "historyItem",
		"changeResult", "finishResult", "workingSnapshot",
	}
	for _, tool := range listed.Tools {
		contract, ok := byName[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		wantSchema, err := harness.ToolInputSchema(contract.InputSchema)
		if err != nil {
			t.Fatalf("harness schema for %s: %v", tool.Name, err)
		}
		wantJSON, err := json.Marshal(wantSchema)
		if err != nil {
			t.Fatalf("marshal want schema %s: %v", tool.Name, err)
		}
		gotJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal listed schema %s: %v", tool.Name, err)
		}
		var wantObj, gotObj map[string]any
		if err := json.Unmarshal(wantJSON, &wantObj); err != nil {
			t.Fatalf("decode want schema %s: %v", tool.Name, err)
		}
		if err := json.Unmarshal(gotJSON, &gotObj); err != nil {
			t.Fatalf("decode listed schema %s: %v", tool.Name, err)
		}
		wantDefs, _ := wantObj["$defs"].(map[string]any)
		gotDefs, _ := gotObj["$defs"].(map[string]any)
		if wantDefs == nil {
			wantDefs = map[string]any{}
		}
		if gotDefs == nil {
			gotDefs = map[string]any{}
		}
		if len(gotDefs) != len(wantDefs) {
			t.Errorf("%s $defs count = %d, want transitive %d (%v vs %v)",
				tool.Name, len(gotDefs), len(wantDefs), keysAny(gotDefs), keysAny(wantDefs))
		}
		for name := range gotDefs {
			if _, ok := wantDefs[name]; !ok {
				t.Errorf("%s advertises unrelated $defs entry %q", tool.Name, name)
			}
		}
		for _, name := range unrelatedDefNames {
			if _, present := gotDefs[name]; present {
				// Only fail when the name is not part of the transitive root set.
				if _, allowed := wantDefs[name]; !allowed {
					t.Errorf("%s $defs includes unrelated %q", tool.Name, name)
				}
			}
		}
		// tools/list must not advertise authority or retired surfaces as free text
		// outside the selected root DTO tree.
		for _, forbidden := range forbiddenSubstrings {
			if bytes.Contains(gotJSON, []byte(forbidden)) {
				t.Errorf("%s tools/list schema contains forbidden %q", tool.Name, forbidden)
			}
		}
		// Refs must remain resolvable: every $ref target is present in $defs.
		for _, match := range collectSchemaDefRefs(gotJSON) {
			if _, ok := gotDefs[match]; !ok {
				t.Errorf("%s has unresolved $ref %q", tool.Name, match)
			}
		}
	}
}

func keysAny(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func collectSchemaDefRefs(schemaJSON []byte) []string {
	const prefix = `#/$defs/`
	seen := map[string]bool{}
	var out []string
	raw := string(schemaJSON)
	for {
		index := strings.Index(raw, prefix)
		if index < 0 {
			break
		}
		raw = raw[index+len(prefix):]
		end := 0
		for end < len(raw) {
			c := raw[end]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				end++
				continue
			}
			break
		}
		name := raw[:end]
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		raw = raw[end:]
	}
	return out
}

func TestBlackboardV2MCPExactReplayAfterFinishAndSupersession(t *testing.T) {
	fixture := newV2MCPFixture(t)
	session := fixture.session(t, &fixture.ownerGrant, nil)

	prepare := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-replay-open-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:replay-target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Replay target", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:replay", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replay objective"}},
			{Op: "create", Key: "attempt:replay", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Replay attempt"}},
			{Op: "relate", From: "attempt:replay", Relation: "tests", To: "objective:replay"},
		},
	}
	if result, raw := callV2Tool(t, session, "blackboard_change", prepare); result.IsError {
		t.Fatalf("prepare open work: %s", raw)
	}
	change := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-replay-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:replay-exact", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Exact replay host", ScopeStatus: "in_scope"},
		}},
	}
	changeResult, changeRaw := callV2Tool(t, session, "blackboard_change", change)
	if changeResult.IsError {
		t.Fatalf("first change: %s", changeRaw)
	}
	workdir := filepath.Join(fixture.root, "runs", fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "replay-proof.txt"), []byte("replay proof\n"), 0o600); err != nil {
		t.Fatalf("write evidence source: %v", err)
	}
	retain := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "mcp-replay-retain", Key: "evidence:replay", Attempt: "attempt:replay",
		SourcePath: "replay-proof.txt", ArtifactType: "text", Summary: "Replay retained proof", MediaType: "text/plain",
		Links: []blackboardv2.EvidenceLink{{"about", "entity:replay-target"}},
	}
	retainResult, retainRaw := callV2Tool(t, session, "blackboard_retain_evidence", retain)
	if retainResult.IsError {
		t.Fatalf("retain: %s", retainRaw)
	}
	checkpoint := blackboardv2.CheckpointAttemptRequest{
		IdempotencyKey: "mcp-replay-checkpoint", Key: "attempt:replay", Version: 1, Summary: "Checkpoint before finish",
	}
	checkpointResult, checkpointRaw := callV2Tool(t, session, "blackboard_checkpoint_attempt", checkpoint)
	if checkpointResult.IsError {
		t.Fatalf("checkpoint: %s", checkpointRaw)
	}
	terminal := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-replay-terminal",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:replay", Version: 2, Status: "succeeded", Summary: "Replay proof retained"}},
	}
	if result, raw := callV2Tool(t, session, "blackboard_change", terminal); result.IsError {
		t.Fatalf("terminalize attempt: %s", raw)
	}
	finish := blackboardv2.FinishContinuationRequest{IdempotencyKey: "mcp-replay-finish"}
	finishResult, finishRaw := callV2Tool(t, session, "blackboard_finish", finish)
	if finishResult.IsError {
		t.Fatalf("finish: %s", finishRaw)
	}

	// Exact non-mutating replays after Finish: match first response bytes, no live sync.
	for _, tc := range []struct {
		name string
		tool string
		args any
		raw  []byte
	}{
		{"change", "blackboard_change", change, changeRaw},
		{"retain", "blackboard_retain_evidence", retain, retainRaw},
		{"checkpoint", "blackboard_checkpoint_attempt", checkpoint, checkpointRaw},
		{"finish", "blackboard_finish", finish, finishRaw},
	} {
		result, raw := callV2Tool(t, session, tc.tool, tc.args)
		if result.IsError || !bytes.Equal(raw, tc.raw) {
			t.Fatalf("post-finish exact %s replay drifted: isError=%v\nfirst=%s\nreplay=%s", tc.name, result.IsError, tc.raw, raw)
		}
		if bytes.Contains(raw, []byte(`"sync"`)) {
			t.Fatalf("post-finish exact %s replay attached live sync: %s", tc.name, raw)
		}
	}

	// Changed retry and new writes remain rejected after Finish.
	altered := change
	altered.Changes = append([]blackboardv2.Change(nil), change.Changes...)
	altered.Changes[0].Record = blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Altered", ScopeStatus: "in_scope"}
	if result, raw := callV2Tool(t, session, "blackboard_change", altered); !result.IsError || !bytes.Contains(raw, []byte(`"idempotency_conflict"`)) {
		t.Fatalf("post-finish changed change retry = %s", raw)
	}
	alteredRetain := retain
	alteredRetain.Summary = "different summary"
	if result, raw := callV2Tool(t, session, "blackboard_retain_evidence", alteredRetain); !result.IsError || !bytes.Contains(raw, []byte(`"idempotency_conflict"`)) {
		t.Fatalf("post-finish changed retain retry = %s", raw)
	}
	alteredCheckpoint := checkpoint
	alteredCheckpoint.Summary = "different checkpoint"
	if result, raw := callV2Tool(t, session, "blackboard_checkpoint_attempt", alteredCheckpoint); !result.IsError || !bytes.Contains(raw, []byte(`"idempotency_conflict"`)) {
		t.Fatalf("post-finish changed checkpoint retry = %s", raw)
	}
	newWrite := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-replay-new-after-finish",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:after-finish", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "After finish", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, session, "blackboard_change", newWrite); !result.IsError || !bytes.Contains(raw, []byte(`"closed_continuation"`)) {
		t.Fatalf("post-finish new write = %s", raw)
	}
	// Closed Continuation loses live read/current knowledge authority.
	if result, raw := callV2Tool(t, session, "blackboard_read", map[string]any{"key": "entity:replay-exact"}); !result.IsError || !bytes.Contains(raw, []byte(`"closed_continuation"`)) {
		t.Fatalf("post-finish read authority = %s", raw)
	}
	if result, raw := callV2Tool(t, session, "blackboard_history", map[string]any{"key": "entity:replay-exact"}); !result.IsError || !bytes.Contains(raw, []byte(`"closed_continuation"`)) {
		t.Fatalf("post-finish history authority = %s", raw)
	}

	// Unauthorized cross-project principal cannot obtain the stored owner replay.
	foreign := fixture.session(t, &fixture.foreignGrant, nil)
	foreignChange, foreignChangeRaw := callV2Tool(t, foreign, "blackboard_change", change)
	if foreignChange.IsError {
		// Foreign may reject for its own open-work state; it must never return owner bytes.
		if bytes.Equal(foreignChangeRaw, changeRaw) {
			t.Fatalf("foreign error body matched owner change replay")
		}
	} else if bytes.Equal(foreignChangeRaw, changeRaw) {
		t.Fatalf("foreign principal received owner exact change replay")
	}
	foreignFinish, foreignFinishRaw := callV2Tool(t, foreign, "blackboard_finish", finish)
	if !foreignFinish.IsError {
		if bytes.Equal(foreignFinishRaw, finishRaw) {
			t.Fatalf("foreign principal received owner exact finish replay")
		}
	} else if bytes.Equal(foreignFinishRaw, finishRaw) {
		t.Fatalf("foreign finish error matched owner finish replay")
	}

	// Supersession: a newer Continuation on the same Task closes offline authority
	// for the previous run, but stored non-mutating replay still works without sync.
	superTask, err := task.NewService(fixture.db, project.NewService(fixture.db)).Create(task.CreateRequest{
		ProjectID: fixture.project.ID, Goal: "supersession replay", RuntimeProfileID: fixture.profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create supersession Task: %v", err)
	}
	first, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: superTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("create first supersession Continuation: %v", err)
	}
	firstGrant, err := projectinterface.NewGrantStore(fixture.db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}).
		Resolve(context.Background(), first.Token)
	if err != nil {
		t.Fatalf("resolve first supersession grant: %v", err)
	}
	firstSession := fixture.session(t, &firstGrant, nil)
	superChange := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-super-entity",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:super-exact", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Superseded host", ScopeStatus: "in_scope"},
		}},
	}
	superFirst, superFirstRaw := callV2Tool(t, firstSession, "blackboard_change", superChange)
	if superFirst.IsError {
		t.Fatalf("supersession first change: %s", superFirstRaw)
	}
	if _, err := task.NewService(fixture.db, project.NewService(fixture.db)).UpdateContinuationStatus(first.Continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("close first supersession Continuation: %v", err)
	}
	if _, err := fixture.continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: fixture.project.ID, TaskID: superTask.ID, RuntimeProfileID: fixture.profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	}); err != nil {
		t.Fatalf("create superseding Continuation: %v", err)
	}
	superReplay, superReplayRaw := callV2Tool(t, firstSession, "blackboard_change", superChange)
	if superReplay.IsError || !bytes.Equal(superReplayRaw, superFirstRaw) {
		t.Fatalf("superseded exact change replay drifted: isError=%v\nfirst=%s\nreplay=%s", superReplay.IsError, superFirstRaw, superReplayRaw)
	}
	if bytes.Contains(superReplayRaw, []byte(`"sync"`)) {
		t.Fatalf("superseded exact change replay attached live sync: %s", superReplayRaw)
	}
	superNew := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mcp-super-new",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:super-new", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "New after super", ScopeStatus: "in_scope"},
		}},
	}
	if result, raw := callV2Tool(t, firstSession, "blackboard_change", superNew); !result.IsError || !bytes.Contains(raw, []byte(`"closed_continuation"`)) {
		t.Fatalf("superseded new write = %s", raw)
	}
	if result, raw := callV2Tool(t, firstSession, "blackboard_read", map[string]any{"key": "entity:super-exact"}); !result.IsError || !bytes.Contains(raw, []byte(`"closed_continuation"`)) {
		t.Fatalf("superseded read authority = %s", raw)
	}
}
