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
	artifactRoot   string
	runtimeRoot    string
}

func newServiceFixture(t *testing.T) serviceFixture {
	t.Helper()
	artifactRoot := t.TempDir()
	dbPath := filepath.Join(artifactRoot, "projectinterface.db")
	runtimeRoot := filepath.Join(artifactRoot, "runs")
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
	service := projectinterface.NewService(projectinterface.Deps{
		DB: db, Graph: graph, Grants: grants,
		ArtifactRoot: artifactRoot, RuntimeRoot: runtimeRoot,
	})

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
		artifactRoot: artifactRoot, runtimeRoot: runtimeRoot,
	}
}

type failEvidenceOnce struct {
	point projectinterface.EvidenceFailurePoint
	fired bool
}

func (f *failEvidenceOnce) FailAfter(point projectinterface.EvidenceFailurePoint) error {
	if f.fired || point != f.point {
		return nil
	}
	f.fired = true
	return errors.New("injected evidence retention failure after " + string(point))
}

func prepareRetainEvidenceAttempt(t *testing.T, fixture serviceFixture, principal projectinterface.Principal, stableKey string) {
	t.Helper()
	_, err := fixture.service.Apply(context.Background(), principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "retain:prepare:" + stableKey,
			Operations: []blackboard.Operation{
				{OpID: "objective", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:" + stableKey}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"objective": "Capture durable proof"}}},
				{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:" + stableKey}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{}}},
				{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{OpID: "objective"}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare retaining Attempt: %v", err)
	}
}

// TestRetainEvidenceConvergesAcrossFilePublishGraphCommitAndLostResponseFailures
// is the I03 first-red test at the transport-neutral Retain Evidence seam. A
// retry must converge from every durable boundary without duplicating the
// managed payload, EvidenceArtifact, or produced edge.
func TestRetainEvidenceConvergesAcrossFilePublishGraphCommitAndLostResponseFailures(t *testing.T) {
	for _, failurePoint := range []projectinterface.EvidenceFailurePoint{
		projectinterface.EvidenceFailureAfterFilePublish,
		projectinterface.EvidenceFailureAfterGraphCommit,
		projectinterface.EvidenceFailureAfterResultStore,
	} {
		t.Run(string(failurePoint), func(t *testing.T) {
			fixture := newServiceFixture(t)
			principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			key := strings.ReplaceAll(string(failurePoint), "_", "-")
			prepareRetainEvidenceAttempt(t, fixture, principal, key)

			workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir", "captures")
			if err := os.MkdirAll(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			source := filepath.Join(workdir, "admin-login.txt")
			payload := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nadmin=true\n")
			if err := os.WriteFile(source, payload, 0o600); err != nil {
				t.Fatalf("write source: %v", err)
			}

			injector := &failEvidenceOnce{point: failurePoint}
			fixture.service = projectinterface.NewService(projectinterface.Deps{
				DB: fixture.db, Graph: fixture.graph, Grants: fixture.grants,
				ArtifactRoot: fixture.artifactRoot, RuntimeRoot: fixture.runtimeRoot,
				EvidenceFailures: injector,
			})
			request := projectinterface.RetainEvidenceRequest{
				ProtocolVersion:   projectinterface.RuntimeProtocolVersion,
				IdempotencyKey:    "retain:" + key,
				StableKey:         "evidence:" + key,
				ArtifactType:      "http_exchange",
				MediaType:         "application/http",
				SourcePath:        "captures/admin-login.txt",
				Summary:           "Authenticated response proving the admin endpoint",
				ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:" + key},
			}
			if _, err := fixture.service.RetainEvidence(context.Background(), principal, request); err == nil {
				t.Fatal("injected failure unexpectedly returned success")
			}
			first, err := fixture.service.RetainEvidence(context.Background(), principal, request)
			if err != nil {
				t.Fatalf("retry Retain Evidence: %v", err)
			}
			replay, err := fixture.service.RetainEvidence(context.Background(), principal, request)
			if err != nil {
				t.Fatalf("replay Retain Evidence: %v", err)
			}
			firstJSON, _ := json.Marshal(first)
			replayJSON, _ := json.Marshal(replay)
			if !bytes.Equal(firstJSON, replayJSON) {
				t.Fatalf("compound replay drifted:\nfirst %s\nreplay %s", firstJSON, replayJSON)
			}
			if first.Result.Node.StableKey != request.StableKey || first.Result.Node.Version != 1 {
				t.Fatalf("retained Evidence node = %+v", first.Result.Node)
			}
			provenance, err := fixture.graph.ReadNodeRuntimeProvenance(context.Background(), fixture.project.ID, first.Result.Node.ID)
			if err != nil || provenance.TaskID != fixture.task.ID || provenance.ContinuationID != fixture.continuation.ID {
				t.Fatalf("retained Evidence provenance = %+v, err = %v", provenance, err)
			}
			if got := first.Result.Node.PropertyMap["sha256"]; got != first.Result.SHA256 {
				t.Fatalf("graph sha256 = %v, result sha256 = %s", got, first.Result.SHA256)
			}
			if got := int64(first.Result.Node.PropertyMap["size_bytes"].(float64)); got != int64(len(payload)) {
				t.Fatalf("graph size = %d want %d", got, len(payload))
			}
			managed := filepath.Join(fixture.artifactRoot, filepath.FromSlash(first.Result.ManagedPath))
			if got, err := os.ReadFile(managed); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("managed payload = %q, err = %v", got, err)
			}
			matches, err := filepath.Glob(filepath.Join(fixture.runtimeRoot, fixture.task.ID, "artifacts", "retained", "*", "*"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("managed payload copies = %v, err = %v", matches, err)
			}
			if len(first.Result.Edges) != 1 || first.Result.Edges[0].EdgeType != blackboard.EdgeTypeProduced {
				t.Fatalf("retained Evidence edges = %+v", first.Result.Edges)
			}
		})
	}
}

type replaceEvidenceSource struct {
	path string
}

type mutateEvidenceSource struct{ path string }

func (m mutateEvidenceSource) FailAfter(point projectinterface.EvidenceFailurePoint) error {
	if point != projectinterface.EvidenceFailureBeforeFilePublish {
		return nil
	}
	return os.WriteFile(m.path, []byte("mutated in place"), 0o600)
}

func (r replaceEvidenceSource) FailAfter(point projectinterface.EvidenceFailurePoint) error {
	if point != projectinterface.EvidenceFailureBeforeFilePublish {
		return nil
	}
	if err := os.Rename(r.path, r.path+".original"); err != nil {
		return err
	}
	return os.WriteFile(r.path, []byte("replacement bytes"), 0o600)
}

func TestRetainEvidenceConfinesRuntimeSourcesAndRejectsReplacementRaces(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "confinement")
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	outside := filepath.Join(fixture.artifactRoot, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	symlink := filepath.Join(workdir, "escape.txt")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	otherTaskSource := filepath.Join(fixture.runtimeRoot, "another-task", "workdir", "capture.txt")
	if err := os.MkdirAll(filepath.Dir(otherTaskSource), 0o700); err != nil {
		t.Fatalf("create other Task root: %v", err)
	}
	if err := os.WriteFile(otherTaskSource, []byte("other Task"), 0o600); err != nil {
		t.Fatalf("write other Task source: %v", err)
	}

	for name, sourcePath := range map[string]string{
		"traversal":         "../../outside.txt",
		"symlink escape":    "escape.txt",
		"another Task root": otherTaskSource,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.service.RetainEvidence(context.Background(), principal, projectinterface.RetainEvidenceRequest{
				ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "forbidden:" + name,
				StableKey: "evidence:forbidden-" + strings.ReplaceAll(name, " ", "-"), ArtifactType: "file",
				SourcePath: sourcePath, Summary: "must not be retained",
				ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:confinement"},
			})
			assertErrorCode(t, err, projectinterface.ErrCodeEvidenceSourceForbidden)
		})
	}

	source := filepath.Join(workdir, "replace.txt")
	if err := os.WriteFile(source, []byte("original bytes"), 0o600); err != nil {
		t.Fatalf("write race source: %v", err)
	}
	fixture.service = projectinterface.NewService(projectinterface.Deps{
		DB: fixture.db, Graph: fixture.graph, Grants: fixture.grants,
		ArtifactRoot: fixture.artifactRoot, RuntimeRoot: fixture.runtimeRoot,
		EvidenceFailures: replaceEvidenceSource{path: source},
	})
	_, err = fixture.service.RetainEvidence(context.Background(), principal, projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "race:replace",
		StableKey: "evidence:race-replace", ArtifactType: "file", SourcePath: "replace.txt", Summary: "original bytes only",
		ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:confinement"},
	})
	assertErrorCode(t, err, projectinterface.ErrCodeEvidenceSourceChanged)
	if _, err := fixture.graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeEvidenceArtifact, Key: "evidence:race-replace"}); err == nil {
		t.Fatal("replacement race created false graph support")
	}

	inPlace := filepath.Join(workdir, "mutate.txt")
	if err := os.WriteFile(inPlace, []byte("original in-place bytes"), 0o600); err != nil {
		t.Fatalf("write in-place source: %v", err)
	}
	fixture.service = projectinterface.NewService(projectinterface.Deps{
		DB: fixture.db, Graph: fixture.graph, Grants: fixture.grants,
		ArtifactRoot: fixture.artifactRoot, RuntimeRoot: fixture.runtimeRoot,
		EvidenceFailures: mutateEvidenceSource{path: inPlace},
	})
	_, err = fixture.service.RetainEvidence(context.Background(), principal, projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "race:mutate",
		StableKey: "evidence:race-mutate", ArtifactType: "file", SourcePath: "mutate.txt", Summary: "original in-place bytes only",
		ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:confinement"},
	})
	assertErrorCode(t, err, projectinterface.ErrCodeEvidenceSourceChanged)
}

func TestRetainEvidenceExactReplayRemainsAvailableAfterFinish(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "finished-replay")
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "finished.txt"), []byte("finished replay"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	request := projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:finished-replay",
		StableKey: "evidence:finished-replay", ArtifactType: "file", SourcePath: "finished.txt", Summary: "finished replay",
		ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:finished-replay"},
	}
	want, err := fixture.service.RetainEvidence(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("initial retain: %v", err)
	}
	if _, err := fixture.grants.Finish(context.Background(), fixture.grant.ID); err != nil {
		t.Fatalf("finish grant: %v", err)
	}
	got, err := fixture.service.RetainEvidence(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("replay after Finish: %v", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("finished replay = %s want %s", gotJSON, wantJSON)
	}
}

func TestRetainEvidenceReplacesContentWithExpectedVersionAndPreservesPriorPayload(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "replace-content")
	attempt, err := fixture.graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeAttempt, Key: "attempt:replace-content"})
	if err != nil {
		t.Fatalf("read producing Attempt: %v", err)
	}
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	source := filepath.Join(workdir, "replace-content.txt")
	request := projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:replace-content:v1",
		StableKey: "evidence:replace-content", ArtifactType: "file", SourcePath: "replace-content.txt", Summary: "version one",
		ProducedByAttempt: blackboard.NodeRef{ID: attempt.Node.ID},
	}
	if err := os.WriteFile(source, []byte("version one"), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	first, err := fixture.service.RetainEvidence(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("retain v1: %v", err)
	}
	if err := os.WriteFile(source, []byte("version two"), 0o600); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	expectedVersion := 1
	request.IdempotencyKey = "retain:replace-content:v2"
	request.ExpectedVersion = &expectedVersion
	request.Summary = "version two"
	second, err := fixture.service.RetainEvidence(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("retain v2: %v", err)
	}
	if second.Result.Node.Version != 2 || second.Result.SHA256 == first.Result.SHA256 {
		t.Fatalf("replacement results: first=%+v second=%+v", first.Result, second.Result)
	}
	for path, want := range map[string]string{first.Result.ManagedPath: "version one", second.Result.ManagedPath: "version two"} {
		got, err := os.ReadFile(filepath.Join(fixture.artifactRoot, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Fatalf("retained %s = %q, err=%v", path, got, err)
		}
	}
	if _, err := fixture.service.Apply(context.Background(), principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "evidence:replace-content:missing",
			Operations: []blackboard.Operation{{
				OpID: "missing", Kind: blackboard.OpTransitionNode,
				Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:replace-content"},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: 2, Status: "missing"},
			}},
		},
	}); err != nil {
		t.Fatalf("mark Evidence missing: %v", err)
	}
	if err := os.WriteFile(source, []byte("version three"), 0o600); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	expectedVersion = 3
	request.IdempotencyKey = "retain:replace-content:v3"
	request.ExpectedVersion = &expectedVersion
	request.Summary = "version three"
	third, err := fixture.service.RetainEvidence(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("retain missing Evidence as available: %v", err)
	}
	if third.Result.Node.Version != 5 || third.Result.Node.PropertyMap["status"] != "available" {
		t.Fatalf("missing Evidence was not restored by Retain: %+v", third.Result.Node)
	}
}

func TestOperatorRetainEvidenceIsConfinedToConfiguredSourceRoots(t *testing.T) {
	fixture := newServiceFixture(t)
	operator, err := projectinterface.OperatorPrincipal(fixture.project.ID, "local-operator")
	if err != nil {
		t.Fatalf("operator principal: %v", err)
	}
	sourceRoot := filepath.Join(fixture.artifactRoot, "operator-sources")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatalf("create operator source root: %v", err)
	}
	source := filepath.Join(sourceRoot, "operator.txt")
	if err := os.WriteFile(source, []byte("operator proof"), 0o600); err != nil {
		t.Fatalf("write operator source: %v", err)
	}
	fixture.service = projectinterface.NewService(projectinterface.Deps{
		DB: fixture.db, Graph: fixture.graph, Grants: fixture.grants,
		ArtifactRoot: fixture.artifactRoot, RuntimeRoot: fixture.runtimeRoot, OperatorRoots: []string{sourceRoot},
	})
	result, err := fixture.service.RetainEvidence(context.Background(), operator, projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:operator",
		StableKey: "evidence:operator", ArtifactType: "file", SourcePath: source, Summary: "operator proof",
	})
	if err != nil {
		t.Fatalf("operator retain: %v", err)
	}
	if len(result.Result.Edges) != 0 || result.Result.Node.PropertyMap["sha256"] == "" {
		t.Fatalf("operator retained Evidence = %+v", result.Result)
	}
	_, err = fixture.service.RetainEvidence(context.Background(), operator, projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:operator-outside",
		StableKey: "evidence:operator-outside", ArtifactType: "file", SourcePath: fixture.dbPath, Summary: "database must stay forbidden",
	})
	assertErrorCode(t, err, projectinterface.ErrCodeEvidenceSourceForbidden)
}

func TestRetainEvidenceRejectsSymlinkedTaskArtifactRoot(t *testing.T) {
	fixture := newServiceFixture(t)
	principal, err := fixture.service.Authenticate(context.Background(), fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "artifact-symlink")
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("proof"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	escape := filepath.Join(fixture.artifactRoot, "managed-escape")
	if err := os.MkdirAll(escape, 0o700); err != nil {
		t.Fatalf("create escape directory: %v", err)
	}
	if err := os.Symlink(escape, filepath.Join(fixture.runtimeRoot, fixture.task.ID, "artifacts")); err != nil {
		t.Fatalf("symlink Task Artifact Root: %v", err)
	}
	_, err = fixture.service.RetainEvidence(context.Background(), principal, projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:artifact-symlink",
		StableKey: "evidence:artifact-symlink", ArtifactType: "file", SourcePath: "proof.txt", Summary: "proof",
		ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:artifact-symlink"},
	})
	assertErrorCode(t, err, projectinterface.ErrCodeInternal)
	entries, readErr := os.ReadDir(escape)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("symlink escape received managed files: %v, err=%v", entries, readErr)
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
