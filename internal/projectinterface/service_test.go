package projectinterface_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

// sequenceTokenSource is a deterministic TokenSource for tests. The plaintext
// token never matches its own hash, so tests can assert the plaintext is absent
// from persisted output (runtime protocol §4.1, slices §4.1).
type sequenceTokenSource struct {
	mu     sync.Mutex
	values []string
	next   int
}

func newSequenceTokenSource(values ...string) *sequenceTokenSource {
	return &sequenceTokenSource{values: values}
}

func (s *sequenceTokenSource) NewToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.values) {
		panic("sequenceTokenSource exhausted")
	}
	token := s.values[s.next]
	s.next++
	return token, nil
}

// serviceFixture wires a file-backed graph_v1 store with a Project, Task,
// Runtime Configuration Version, and Continuation, then issues a Continuation
// Interface Grant and returns everything an I01 test needs.
type serviceFixture struct {
	service        *projectinterface.Service
	grants         *projectinterface.GrantStore
	graph          *blackboard.GraphService
	db             *store.DB
	dbPath         string
	projects       *project.Service
	tasks          *task.Service
	project        project.Project
	task           task.Task
	continuation   task.TaskContinuation
	configVersion  task.RuntimeConfigVersion
	token          string
	grant          projectinterface.Grant
	runtimeProfile string
	runtimePlugin  string
	runner         string
}

func newServiceFixture(t *testing.T) serviceFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "projectinterface.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Activate the graph epoch so graph services are exercised. Production
	// wiring gates this behind the M05 cutover; tests flip it directly.
	if _, err := db.Exec(
		`UPDATE blackboard_store_state SET canonical_store=?, cutover_state='graph' WHERE id=1`,
		store.CanonicalStoreGraphV1,
	); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}

	projects := project.NewService(db)
	proj, err := projects.Create("I01 project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "Find the admin surface", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	const runtimeProfile = "rp-i01"
	const runtimePlugin = "codex"
	const runner = task.RunnerSandbox
	configVersion, err := tasks.RecordRuntimeConfig(created.ID, runtimeProfile, map[string]any{"model": "test-model"})
	if err != nil {
		t.Fatalf("record runtime config: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, runtimeProfile, runtimePlugin, runner)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	clock := blackboard.NewSequenceClock(fixedClockValues()...)
	ids := blackboard.NewSequenceIDSource(fixedIDValues()...)
	tokens := newSequenceTokenSource("grant-token-one", "grant-token-two", "grant-token-three", "grant-token-four")
	grants := projectinterface.NewGrantStore(db, clock, ids, tokens)
	tasks.SetContinuationTerminalMarker(grants)
	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	service := projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Grants: grants})

	token, grant, err := grants.Issue(context.Background(), projectinterface.IssueGrantRequest{
		ProjectID:              proj.ID,
		TaskID:                 created.ID,
		ContinuationID:         continuation.ID,
		RuntimeConfigVersionID: configVersion.ID,
		RuntimeProfileID:       runtimeProfile,
		RuntimePluginID:        runtimePlugin,
		Runner:                 string(runner),
	})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}
	return serviceFixture{
		service: service, grants: grants, graph: graph, db: db, dbPath: dbPath,
		projects: projects, tasks: tasks, project: proj, task: created,
		continuation: continuation, configVersion: configVersion,
		token: token, grant: grant, runtimeProfile: runtimeProfile,
		runtimePlugin: runtimePlugin, runner: string(runner),
	}
}

func fixedClockValues() []string {
	return []string{
		"2024-03-04T05:06:07.000000000Z",
		"2024-03-04T05:06:08.000000000Z",
		"2024-03-04T05:06:09.000000000Z",
		"2024-03-04T05:06:10.000000000Z",
		"2024-03-04T05:06:11.000000000Z",
		"2024-03-04T05:06:12.000000000Z",
		"2024-03-04T05:06:13.000000000Z",
		"2024-03-04T05:06:14.000000000Z",
	}
}

func fixedIDValues() []string {
	return []string{
		"grant_1", "grant_2", "grant_3", "grant_4",
		"grant_5", "grant_6", "grant_7", "grant_8",
	}
}

// objectiveApplyRequest builds a valid Runtime Apply that creates an
// ExplorationObjective. Objectives need no produced edge, so they are the
// simplest honest Runtime mutation for the I01 seam.
func objectiveApplyRequest() projectinterface.ApplyMutationRequest {
	return projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "obj:create-admin-surface",
			Operations: []blackboard.Operation{{
				OpID: "obj",
				Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:find-admin-surface"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"objective": "Locate the authenticated admin surface",
					"status":    "open",
				}},
			}},
		},
	}
}

// TestProjectInterfaceRejectsActorIneligibleOperationsBeforeGraphAccess proves
// the project-interface authorization table is enforced before graph-domain
// validation can reclassify a forbidden request.
func TestProjectInterfaceRejectsActorIneligibleOperationsBeforeGraphAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if err := fixture.graph.ProjectTaskGoal(fixture.task.ID); err != nil {
		t.Fatalf("project Task Goal: %v", err)
	}
	goal, err := fixture.graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeGoal, Key: "task:" + fixture.task.ID + ":goal",
	})
	if err != nil {
		t.Fatalf("read Task Goal: %v", err)
	}
	tests := []struct {
		name string
		op   blackboard.Operation
	}{
		{
			name: "merge",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpMergeNodes},
		},
		{
			name: "archive",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpSetDisposition, Disposition: blackboard.SetDispositionInput{Disposition: blackboard.DispositionArchived}},
		},
		{
			name: "Goal",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeGoal}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"goal": "spoof"}}},
		},
		{
			name: "Goal by immutable ID",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpPatchNode, Node: blackboard.NodeRef{ID: goal.Node.ID}, Patch: blackboard.PatchNodeInput{ExpectedVersion: 1, Properties: map[string]any{"text": "spoof"}}},
		},
		{
			name: "available Evidence",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "available"}}},
		},
		{
			name: "default-available Evidence",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"managed_path": "artifacts/probe.bin"}}},
		},
		{
			name: "Evidence metadata patch preserving available status",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpPatchNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact}, Patch: blackboard.PatchNodeInput{ExpectedVersion: 1, Properties: map[string]any{"managed_path": "artifacts/replaced.bin"}}},
		},
		{
			name: "active Project Directive",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectDirective}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "active"}}},
		},
		{
			name: "interrupted Attempt",
			op:   blackboard.Operation{OpID: "forbidden", Kind: blackboard.OpTransitionNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt}, Transition: blackboard.TransitionNodeInput{Status: "interrupted"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := projectinterface.ApplyMutationRequest{
				ProtocolVersion: projectinterface.RuntimeProtocolVersion,
				Batch: projectinterface.RequestBatch{
					SchemaVersion:  blackboard.GraphMutationSchemaVersion,
					IdempotencyKey: "forbidden:" + tt.name,
					Operations:     []blackboard.Operation{tt.op},
				},
			}
			_, err := fixture.service.Apply(context.Background(), principal, request)
			if err == nil {
				t.Fatal("actor-ineligible operation unexpectedly reached graph Apply")
			}
			assertErrorCode(t, err, projectinterface.ErrCodeActorForbidden)
			var got *projectinterface.Error
			if !errors.As(err, &got) || got.OperationIndex == nil || *got.OperationIndex != 0 || got.OpID != "forbidden" {
				t.Fatalf("authorization error lacks operation context: %#v", err)
			}
		})
	}
}

// TestProjectInterfaceSourceEventsStayBoundToTaskAndContinuation proves source
// Events from the bound Task/Continuation are accepted while cross-Task and
// cross-Continuation provenance is rejected at the public Apply seam.
func TestProjectInterfaceSourceEventsStayBoundToTaskAndContinuation(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	matching, err := fixture.tasks.AppendContinuationEvent(
		fixture.task.ID, fixture.continuation.ID,
		task.EventKindRuntimeOutput, task.EventPayload{"phase": "checkpoint"},
	)
	if err != nil {
		t.Fatalf("append matching Event: %v", err)
	}
	accepted := objectiveApplyRequest()
	accepted.Batch.IdempotencyKey = "source-event:matching"
	accepted.Batch.Operations[0].Node.StableKey = "objective:matching-event"
	accepted.SourceEventIDsByOp = map[string][]string{"obj": {matching.ID}}
	if _, err := fixture.service.Apply(ctx, principal, accepted); err != nil {
		t.Fatalf("Apply matching Event: %v", err)
	}

	otherContinuation, err := fixture.tasks.CreateContinuation(
		fixture.task.ID, fixture.runtimeProfile, fixture.runtimePlugin, task.Runner(fixture.runner),
	)
	if err != nil {
		t.Fatalf("create other Continuation: %v", err)
	}
	crossContinuation, err := fixture.tasks.AppendContinuationEvent(
		fixture.task.ID, otherContinuation.ID,
		task.EventKindRuntimeOutput, task.EventPayload{"phase": "other-continuation"},
	)
	if err != nil {
		t.Fatalf("append cross-Continuation Event: %v", err)
	}
	assertRejectedEvent := func(name string, eventID, wantCode string) {
		t.Helper()
		request := objectiveApplyRequest()
		request.Batch.IdempotencyKey = "source-event:" + name
		request.Batch.Operations[0].Node.StableKey = "objective:" + name
		request.SourceEventIDsByOp = map[string][]string{"obj": {eventID}}
		_, err := fixture.service.Apply(ctx, principal, request)
		if err == nil {
			t.Fatalf("%s source Event unexpectedly accepted", name)
		}
		assertErrorCode(t, err, wantCode)
	}
	assertRejectedEvent("cross-continuation", crossContinuation.ID, projectinterface.ErrCodeSourceEventMismatch)

	otherTask, err := fixture.tasks.Create(task.CreateRequest{
		ProjectID: fixture.project.ID, Goal: "Other Task", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create other Task: %v", err)
	}
	crossTask, err := fixture.tasks.AppendEvent(otherTask.ID, task.EventKindRuntimeOutput, task.EventPayload{"phase": "other-task"})
	if err != nil {
		t.Fatalf("append cross-Task Event: %v", err)
	}
	assertRejectedEvent("cross-task", crossTask.ID, projectinterface.ErrCodeSourceEventMismatch)
	assertRejectedEvent("missing", "event-does-not-exist", projectinterface.ErrCodeSourceEventNotFound)
}

func TestResolveRecordsReturnsAliasMergeAndMissingMetadataAtOneRevision(t *testing.T) {
	fixture := newServiceFixture(t)
	operator, err := projectinterface.OperatorPrincipal(fixture.project.ID, "local-operator")
	if err != nil {
		t.Fatalf("operator principal: %v", err)
	}
	created, err := fixture.service.Apply(context.Background(), operator, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "resolve:create-duplicates",
			Operations: []blackboard.Operation{
				{OpID: "source", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:old"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Old objective", "status": "open"}}},
				{OpID: "canonical", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:canonical"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Canonical objective", "status": "open"}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create duplicate objectives: %v", err)
	}
	sourceID := created.Result.Operations[0].NodeID
	if _, err := fixture.service.Apply(context.Background(), operator, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "resolve:merge",
			Operations: []blackboard.Operation{{
				OpID: "merge", Kind: blackboard.OpMergeNodes,
				Merge: blackboard.MergeNodesInput{
					Source: blackboard.NodeRef{ID: sourceID}, Canonical: blackboard.NodeRef{ID: created.Result.Operations[1].NodeID},
					SourceExpectedVersion: 1, CanonicalExpectedVersion: 1,
				},
			}},
		},
	}); err != nil {
		t.Fatalf("merge duplicate objectives: %v", err)
	}

	runtimePrincipal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate Runtime: %v", err)
	}
	resolved, err := fixture.service.ResolveRecords(context.Background(), runtimePrincipal, projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{
			{NodeType: string(blackboard.NodeTypeExplorationObjective), StableKey: "objective:old"},
			{ID: sourceID},
			{NodeType: string(blackboard.NodeTypeExplorationObjective), StableKey: "objective:missing"},
		},
		EdgeIDs: []string{"edge-missing"},
	})
	if err != nil {
		t.Fatalf("resolve alias/merge/missing: %v", err)
	}
	if len(resolved.Nodes) != 2 || resolved.Nodes[0].ResolvedFromAlias != "objective:old" || resolved.Nodes[1].ResolvedFromMergedID != sourceID {
		t.Fatalf("resolution metadata = %#v", resolved.Nodes)
	}
	if len(resolved.Missing) != 1 || len(resolved.MissingEdges) != 1 || resolved.ObservedGraphRevision != 2 {
		t.Fatalf("missing/revision result = %#v", resolved)
	}
}

func TestApplyLostResponseReplayIsByteIdenticalAndChangedPayloadConflicts(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	first, err := fixture.service.Apply(context.Background(), principal, objectiveApplyRequest())
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Simulate a transport losing the first response: the caller retries the
	// exact same key and payload without observing first.
	replay, err := fixture.service.Apply(context.Background(), principal, objectiveApplyRequest())
	if err != nil {
		t.Fatalf("lost-response replay: %v", err)
	}
	if !bytes.Equal(first.Result.ResultBytes, replay.Result.ResultBytes) ||
		first.Result.MutationID != replay.Result.MutationID ||
		first.Result.RecordedAt != replay.Result.RecordedAt ||
		first.Result.GraphRevision != replay.Result.GraphRevision {
		t.Fatalf("replay drifted: first=%+v replay=%+v", first.Result, replay.Result)
	}
	changed := objectiveApplyRequest()
	changed.Batch.Operations[0].Create.PropertyMap["objective"] = "Changed payload under the same key"
	if _, err := fixture.service.Apply(context.Background(), principal, changed); err == nil {
		t.Fatal("changed payload under the same idempotency key unexpectedly applied")
	} else {
		assertErrorCode(t, err, blackboard.ErrCodeIdempotencyConflict)
	}
}

// TestTaskBoundApplyCannotSpoofProjectAndIsReadableThroughCurrentGraph is the
// I01 first-red test. A Runtime records a graph mutation without supplying
// Project or provenance fields; the same record is visible through Resolve
// Records and Current Graph; and a path/grant project mismatch or smuggled
// provenance field is rejected before graph access.
func TestTaskBoundApplyCannotSpoofProjectAndIsReadableThroughCurrentGraph(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()

	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	apply, err := fixture.service.Apply(ctx, principal, objectiveApplyRequest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if apply.ProjectID != fixture.project.ID {
		t.Fatalf("apply project id = %q want %q (project must come from the grant, not the request)", apply.ProjectID, fixture.project.ID)
	}
	if apply.RequestKind != "apply" {
		t.Fatalf("request kind = %q want apply", apply.RequestKind)
	}
	if apply.ObservedGraphRevision != 1 {
		t.Fatalf("observed graph revision = %d want 1", apply.ObservedGraphRevision)
	}
	if apply.Result.Operations[0].StableKey != "objective:find-admin-surface" {
		t.Fatalf("operation stable key = %q", apply.Result.Operations[0].StableKey)
	}

	// The provenance bound on the graph node is the grant's Runtime actor, not
	// anything the request could have supplied. Observe it through the Current
	// Graph projection (canonical main graph carries created_provenance.actor_id).
	currentAfterApply, err := fixture.service.CurrentGraph(ctx, principal, projectinterface.CurrentGraphRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
	})
	if err != nil {
		t.Fatalf("current graph for actor check: %v", err)
	}
	if graphJSON, _ := json.Marshal(currentAfterApply.Result.Graph); !strings.Contains(string(graphJSON), fixture.grant.ActorID) {
		t.Fatalf("current graph does not carry the grant-derived actor id %q", fixture.grant.ActorID)
	}

	// A path-declared project that disagrees with the grant is rejected before
	// the graph service is touched.
	if _, err := fixture.service.Authenticate(ctx, fixture.token, "another-project"); err == nil {
		t.Fatal("authenticate with mismatched path project unexpectedly succeeded")
	} else {
		assertErrorCode(t, err, projectinterface.ErrCodeProjectMismatch)
	}

	// A Runtime request must not smuggle trusted provenance through a property
	// map; doing so is rejected as provenance_spoofed.
	spoofed := objectiveApplyRequest()
	spoofed.Batch.IdempotencyKey = "obj:spoof-attempt"
	spoofed.Batch.Operations[0].Node.StableKey = "objective:spoofed"
	spoofed.Batch.Operations[0].Create.PropertyMap["project_id"] = fixture.project.ID
	if _, err := fixture.service.Apply(ctx, principal, spoofed); err == nil {
		t.Fatal("spoofed project_id property unexpectedly applied")
	} else {
		assertErrorCode(t, err, projectinterface.ErrCodeProvenanceSpoofed)
	}
	// The spoofed objective never landed: resolving it reports missing.
	if !resolveMissing(t, fixture, principal, "objective:spoofed") {
		t.Fatal("spoofed objective should not resolve")
	}

	// The same record is visible through Resolve Records.
	resolved, err := fixture.service.ResolveRecords(ctx, principal, projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType:  string(blackboard.NodeTypeExplorationObjective),
			StableKey: "objective:find-admin-surface",
		}},
	})
	if err != nil {
		t.Fatalf("resolve records: %v", err)
	}
	if len(resolved.Nodes) != 1 || len(resolved.Missing) != 0 {
		t.Fatalf("resolve result = %+v", resolved)
	}
	if resolved.ObservedGraphRevision != 1 {
		t.Fatalf("resolve observed revision = %d want 1", resolved.ObservedGraphRevision)
	}

	// The same record is visible through Current Graph, with measured projection
	// metadata and the canonical renderer version.
	current, err := fixture.service.CurrentGraph(ctx, principal, projectinterface.CurrentGraphRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
	})
	if err != nil {
		t.Fatalf("current graph: %v", err)
	}
	if current.Result.RendererVersion != blackboard.CanonicalMainGraphRendererV1 {
		t.Fatalf("renderer version = %q", current.Result.RendererVersion)
	}
	if current.Result.ProjectionHash == "" || current.Result.ProjectionBytes == 0 {
		t.Fatalf("current graph projection unmeasured: %+v", current.Result)
	}
	graphJSON, err := json.Marshal(current.Result.Graph)
	if err != nil {
		t.Fatalf("marshal current graph: %v", err)
	}
	if !strings.Contains(string(graphJSON), "objective:find-admin-surface") {
		t.Fatal("current graph does not contain the created objective")
	}

	// The mutation is bound to the grant's Project only: a second Project has an
	// empty graph and authenticating the grant token against its path fails
	// before any graph access.
	other, err := fixture.projects.Create("Other project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	if _, err := fixture.service.Authenticate(ctx, fixture.token, other.ID); err == nil {
		t.Fatal("authenticating grant token against a foreign project path unexpectedly succeeded")
	}
	if graphNodeResolves(t, fixture.graph, other.ID, "objective:find-admin-surface") {
		t.Fatal("created objective leaked into another project")
	}
}

// TestPlaintextGrantTokenNeverPersisted proves the bearer token plaintext never
// reaches the database, graph records, or Events (runtime protocol §4.1, I01
// exit gate).
func TestPlaintextGrantTokenNeverPersisted(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, principal, objectiveApplyRequest()); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The stored grant row hashes the token; the plaintext column does not
	// exist. Scan every persisted byte (ledger, provenance, events, grant table)
	// for the plaintext as a strong negative assertion.
	data, err := os.ReadFile(fixture.dbPath)
	if err != nil {
		t.Fatalf("read database file: %v", err)
	}
	if strings.Contains(string(data), fixture.token) {
		t.Fatal("plaintext grant token appears in the database file")
	}
	var storedHash string
	if err := fixture.db.QueryRow(
		`SELECT token_hash FROM blackboard_continuation_grants WHERE grant_id = ?`, fixture.grant.ID,
	).Scan(&storedHash); err != nil {
		t.Fatalf("read grant token hash: %v", err)
	}
	if storedHash == fixture.token {
		t.Fatal("stored token hash equals the plaintext token")
	}
}

// TestClosedGrantsRejectNewWritesWhileReadsAndReplayRemain proves the grant
// lifecycle exit gate. Finished and terminal grants reject new writes while
// reads and exact replay remain available; revocation rejects every use
// (runtime protocol §4.2 distinguishes revocation from finish/terminal).
func TestClosedGrantsRejectNewWritesWhileReadsAndReplayRemain(t *testing.T) {
	cases := []struct {
		name    string
		close   func(t *testing.T, fixture serviceFixture, ctx context.Context) error
		rejects bool // revocation rejects every use, including reads and replay
	}{
		{"finished", func(t *testing.T, fixture serviceFixture, ctx context.Context) error {
			_, err := fixture.grants.Finish(ctx, fixture.grant.ID)
			return err
		}, false},
		{"revoked", func(t *testing.T, fixture serviceFixture, ctx context.Context) error {
			_, err := fixture.grants.Revoke(ctx, fixture.grant.ID)
			return err
		}, true},
		{"terminal", func(t *testing.T, fixture serviceFixture, ctx context.Context) error {
			_, err := fixture.tasks.UpdateContinuationStatus(fixture.continuation.ID, task.StatusCompleted)
			return err
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			ctx := context.Background()
			principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if _, err := fixture.service.Apply(ctx, principal, objectiveApplyRequest()); err != nil {
				t.Fatalf("seed apply: %v", err)
			}
			if err := tc.close(t, fixture, ctx); err != nil {
				t.Fatalf("close grant (%s): %v", tc.name, err)
			}

			// A new write is rejected as continuation_closed by every close kind.
			rejected := objectiveApplyRequest()
			rejected.Batch.IdempotencyKey = "obj:after-close"
			rejected.Batch.Operations[0].Node.StableKey = "objective:after-close"
			if _, err := fixture.service.Apply(ctx, principal, rejected); err == nil {
				t.Fatalf("new write after %s unexpectedly succeeded", tc.name)
			} else {
				assertErrorCode(t, err, projectinterface.ErrCodeContinuationClosed)
			}

			if tc.rejects {
				// Revocation rejects every use: replay and reads also fail.
				if _, err := fixture.service.Apply(ctx, principal, objectiveApplyRequest()); err == nil {
					t.Fatalf("exact replay after %s unexpectedly succeeded", tc.name)
				} else {
					assertErrorCode(t, err, projectinterface.ErrCodeContinuationClosed)
				}
				if _, err := fixture.service.ResolveRecords(ctx, principal, projectinterface.ResolveRecordsRequest{
					ProtocolVersion: projectinterface.RuntimeProtocolVersion,
					Nodes: []projectinterface.NodeLookup{{
						NodeType:  string(blackboard.NodeTypeExplorationObjective),
						StableKey: "objective:find-admin-surface",
					}},
				}); err == nil {
					t.Fatalf("resolve records after %s unexpectedly succeeded", tc.name)
				}
				return
			}

			// Finish and terminal: exact replay still returns the stored result.
			replay, err := fixture.service.Apply(ctx, principal, objectiveApplyRequest())
			if err != nil {
				t.Fatalf("exact replay after %s: %v", tc.name, err)
			}
			if replay.ObservedGraphRevision < 1 {
				t.Fatalf("replay revision = %d want >= 1", replay.ObservedGraphRevision)
			}

			// Reads remain available.
			if _, err := fixture.service.ResolveRecords(ctx, principal, projectinterface.ResolveRecordsRequest{
				ProtocolVersion: projectinterface.RuntimeProtocolVersion,
				Nodes: []projectinterface.NodeLookup{{
					NodeType:  string(blackboard.NodeTypeExplorationObjective),
					StableKey: "objective:find-admin-surface",
				}},
			}); err != nil {
				t.Fatalf("resolve records after %s: %v", tc.name, err)
			}
			if _, err := fixture.service.CurrentGraph(ctx, principal, projectinterface.CurrentGraphRequest{
				ProtocolVersion: projectinterface.RuntimeProtocolVersion,
			}); err != nil {
				t.Fatalf("current graph after %s: %v", tc.name, err)
			}
		})
	}
}

// TestUnknownTokenRejectsBeforeGraphAccess proves an invalid or missing bearer
// token is rejected without touching the graph (runtime protocol §4.1).
func TestUnknownTokenRejectsBeforeGraphAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.Authenticate(ctx, "not-a-real-token", fixture.project.ID); err == nil {
		t.Fatal("unknown token unexpectedly authenticated")
	} else {
		assertErrorCode(t, err, projectinterface.ErrCodeGrantNotFound)
	}
	if _, err := fixture.service.Authenticate(ctx, "", fixture.project.ID); err == nil {
		t.Fatal("empty token unexpectedly authenticated")
	}
}

// graphNodeResolves reports whether a node resolves through the graph service
// (the BlackboardGraphService seam) for the given Project and stable key. It
// observes graph state through the public interface rather than inspecting
// ledger tables directly (spec §17).
func graphNodeResolves(t *testing.T, graph *blackboard.GraphService, projectID, stableKey string) bool {
	t.Helper()
	_, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectID, NodeType: blackboard.NodeTypeExplorationObjective, Key: stableKey,
	})
	return err == nil
}

// resolveMissing reports whether the project-interface Resolve Records
// capability reports the given node missing for the bound principal.
func resolveMissing(t *testing.T, fixture serviceFixture, principal projectinterface.Principal, stableKey string) bool {
	t.Helper()
	resolved, err := fixture.service.ResolveRecords(context.Background(), principal, projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType:  string(blackboard.NodeTypeExplorationObjective),
			StableKey: stableKey,
		}},
	})
	if err != nil {
		t.Fatalf("resolve %q: %v", stableKey, err)
	}
	return len(resolved.Missing) > 0
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %q, got nil", code)
	}
	var iface *projectinterface.Error
	if !errors.As(err, &iface) {
		t.Fatalf("expected projectinterface.Error with code %q, got %T: %v", code, err, err)
	}
	if iface.Code != code {
		t.Fatalf("error code = %q want %q (%s)", iface.Code, code, iface.Message)
	}
}
