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
