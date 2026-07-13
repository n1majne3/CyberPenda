package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboard"
	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

// piSeed carries everything a daemon-level project-interface test needs after
// seeding a graph_v1 database with a bound Continuation Interface Grant.
type piSeed struct {
	dbPath        string
	token         string
	projectID     string
	runtimePlugin string
}

// seedProjectInterfaceGrant opens a graph_v1 database and creates the Project.
// The grant is issued after daemon startup so startup orphan reconciliation
// cannot correctly terminalize what the test intends to be a new Continuation.
func seedProjectInterfaceGrant(t *testing.T) piSeed {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pi-daemon.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE blackboard_store_state SET canonical_store=?, cutover_state='graph' WHERE id=1`,
		store.CanonicalStoreGraphV1,
	); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	projects := project.NewService(db)
	proj, err := projects.Create("PI daemon project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return piSeed{dbPath: dbPath, projectID: proj.ID, runtimePlugin: "codex"}
}

func issueProjectInterfaceGrant(t *testing.T, seed *piSeed) {
	t.Helper()
	db, err := store.Open(seed.dbPath)
	if err != nil {
		t.Fatalf("open grant store: %v", err)
	}
	defer db.Close()
	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	created, err := tasks.Create(task.CreateRequest{ProjectID: seed.projectID, Goal: "Drive the project interface", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	const runtimeProfile = "rp-daemon"
	const runtimePlugin = "codex"
	const runner = task.RunnerSandbox
	configVersion, err := tasks.RecordRuntimeConfig(created.ID, runtimeProfile, map[string]any{"model": "daemon-model"})
	if err != nil {
		t.Fatalf("record runtime config: %v", err)
	}
	continuation, err := tasks.CreateContinuation(created.ID, runtimeProfile, runtimePlugin, runner)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	grants := projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	token, _, err := grants.Issue(context.Background(), projectinterface.IssueGrantRequest{
		ProjectID:              seed.projectID,
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
	seed.token = token
	seed.runtimePlugin = runtimePlugin
}

func objectiveMutationBody() []byte {
	body := projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "obj:daemon-round-trip",
			Operations: []blackboard.Operation{{
				OpID: "obj",
				Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeExplorationObjective, StableKey: "objective:daemon-surface"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"objective": "Locate the authenticated admin surface via the daemon path",
					"status":    "open",
				}},
			}},
		},
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

// TestProjectInterfaceDaemonHTTPRoundTrip drives the canonical path through the
// daemon HTTP routes: a Runtime Apply lands a record that is then visible
// through records:resolve and the grant-authed runtime-graph, with grant auth
// enforced and structured errors mapped.
func TestProjectInterfaceDaemonHTTPRoundTrip(t *testing.T) {
	seed := seedProjectInterfaceGrant(t)
	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: seed.dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()
	issueProjectInterfaceGrant(t, &seed)

	// POST mutations with the grant token lands the objective.
	mutationURL := "/api/projects/" + seed.projectID + "/blackboard/mutations"
	mutationReq := httptest.NewRequest(http.MethodPost, mutationURL, bytes.NewReader(objectiveMutationBody()))
	mutationReq.Header.Set("Authorization", "Bearer "+seed.token)
	mutationRec := httptest.NewRecorder()
	server.ServeHTTP(mutationRec, mutationReq)
	if mutationRec.Code != http.StatusOK {
		t.Fatalf("apply status = %d body %s", mutationRec.Code, mutationRec.Body.String())
	}
	if cc := mutationRec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("mutation Cache-Control = %q want no-store", cc)
	}
	var apply projectinterface.ApplyMutationResponse
	if err := json.Unmarshal(mutationRec.Body.Bytes(), &apply); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if apply.ProjectID != seed.projectID || apply.ObservedGraphRevision < 1 {
		t.Fatalf("apply response = %+v", apply)
	}

	// records:resolve returns the objective over the daemon route.
	resolveBody, _ := json.Marshal(projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType:  string(blackboard.NodeTypeExplorationObjective),
			StableKey: "objective:daemon-surface",
		}},
	})
	resolveReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+seed.projectID+"/blackboard/records:resolve", bytes.NewReader(resolveBody))
	resolveReq.Header.Set("Authorization", "Bearer "+seed.token)
	resolveRec := httptest.NewRecorder()
	server.ServeHTTP(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body %s", resolveRec.Code, resolveRec.Body.String())
	}

	// The grant-authed runtime-graph path returns the canonical projection (not
	// the operator read envelope), proving the dual-mode route delegates to the
	// project-interface module.
	graphReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+seed.projectID+"/blackboard/runtime-graph", nil)
	graphReq.Header.Set("Authorization", "Bearer "+seed.token)
	graphRec := httptest.NewRecorder()
	server.ServeHTTP(graphRec, graphReq)
	if graphRec.Code != http.StatusOK {
		t.Fatalf("runtime graph status = %d body %s", graphRec.Code, graphRec.Body.String())
	}
	if got := graphRec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("runtime graph Cache-Control = %q", got)
	}
	if etag := graphRec.Header().Get("ETag"); etag == "" {
		t.Fatal("runtime graph ETag missing")
	}
	if !strings.Contains(graphRec.Body.String(), "objective:daemon-surface") {
		t.Fatal("runtime graph body does not contain the created objective")
	}

	// A path Project mismatch is 403 before any graph access.
	mismatchReq := httptest.NewRequest(http.MethodPost, "/api/projects/another-project/blackboard/mutations", bytes.NewReader(objectiveMutationBody()))
	mismatchReq.Header.Set("Authorization", "Bearer "+seed.token)
	mismatchRec := httptest.NewRecorder()
	server.ServeHTTP(mismatchRec, mismatchReq)
	if mismatchRec.Code != http.StatusForbidden {
		t.Fatalf("path mismatch status = %d want 403", mismatchRec.Code)
	}

	// The plaintext token never appears in any response.
	if strings.Contains(mutationRec.Body.String(), seed.token) ||
		strings.Contains(graphRec.Body.String(), seed.token) {
		t.Fatal("plaintext grant token appeared in a daemon response")
	}
}

// TestProjectInterfaceDaemonAcceptsGrantAlongsideOperatorCredential proves the
// daemon-wide operator credential does not shadow a valid Continuation Grant,
// and both trusted caller modes reach the same HTTP project-interface route.
func TestProjectInterfaceDaemonAcceptsGrantAlongsideOperatorCredential(t *testing.T) {
	seed := seedProjectInterfaceGrant(t)
	server, err := daemon.NewServer(daemon.Config{
		Version: "v", DBPath: seed.dbPath, AuthToken: "daemon-operator-token", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()
	issueProjectInterfaceGrant(t, &seed)

	runtimeReq := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+seed.projectID+"/blackboard/mutations",
		bytes.NewReader(objectiveMutationBody()))
	runtimeReq.Header.Set("Authorization", "Bearer "+seed.token)
	runtimeRec := httptest.NewRecorder()
	server.ServeHTTP(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK {
		t.Fatalf("Runtime grant status = %d body %s", runtimeRec.Code, runtimeRec.Body.String())
	}

	var operatorRequest projectinterface.ApplyMutationRequest
	if err := json.Unmarshal(objectiveMutationBody(), &operatorRequest); err != nil {
		t.Fatalf("decode operator request: %v", err)
	}
	operatorRequest.Batch.IdempotencyKey = "obj:operator-http"
	operatorRequest.Batch.Operations[0].Node.StableKey = "objective:operator-http"
	operatorBody, _ := json.Marshal(operatorRequest)
	operatorReq := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+seed.projectID+"/blackboard/mutations",
		bytes.NewReader(operatorBody))
	operatorReq.Header.Set("Authorization", "Bearer daemon-operator-token")
	operatorReq.Header.Set(projectinterface.OperatorActorHeader, "operator:alice")
	operatorRec := httptest.NewRecorder()
	server.ServeHTTP(operatorRec, operatorReq)
	if operatorRec.Code != http.StatusOK {
		t.Fatalf("operator credential status = %d body %s", operatorRec.Code, operatorRec.Body.String())
	}
	if !strings.Contains(operatorRec.Body.String(), "objective:operator-http") {
		t.Fatalf("operator response missing semantic result: %s", operatorRec.Body.String())
	}
	operatorGraphReq := httptest.NewRequest(http.MethodGet,
		"/api/projects/"+seed.projectID+"/blackboard/runtime-graph", nil)
	operatorGraphReq.Header.Set("Authorization", "Bearer daemon-operator-token")
	operatorGraphReq.Header.Set(projectinterface.OperatorActorHeader, "alice")
	operatorGraphRec := httptest.NewRecorder()
	server.ServeHTTP(operatorGraphRec, operatorGraphReq)
	if operatorGraphRec.Code != http.StatusOK {
		t.Fatalf("operator current graph status = %d body %s", operatorGraphRec.Code, operatorGraphRec.Body.String())
	}
	var operatorGraph projectinterface.CurrentGraphResponse
	if err := json.Unmarshal(operatorGraphRec.Body.Bytes(), &operatorGraph); err != nil {
		t.Fatalf("decode operator current graph: %v", err)
	}
	if operatorGraph.RequestKind != "current_graph" || operatorGraph.ProjectID != seed.projectID {
		t.Fatalf("operator current graph used non-project-interface envelope: %#v", operatorGraph)
	}

	missingCredential := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+seed.projectID+"/blackboard/mutations",
		bytes.NewReader(objectiveMutationBody()))
	missingRec := httptest.NewRecorder()
	server.ServeHTTP(missingRec, missingCredential)
	if missingRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential status = %d want 401", missingRec.Code)
	}
	var missingEnvelope struct {
		Error projectinterface.Error `json:"error"`
	}
	if err := json.Unmarshal(missingRec.Body.Bytes(), &missingEnvelope); err != nil {
		t.Fatalf("decode structured missing-credential error: %v", err)
	}
	if missingEnvelope.Error.Code != projectinterface.ErrCodeGrantNotFound ||
		missingEnvelope.Error.ProtocolVersion != projectinterface.RuntimeProtocolVersion {
		t.Fatalf("missing-credential error = %#v", missingEnvelope.Error)
	}

	missingGraphReq := httptest.NewRequest(http.MethodGet,
		"/api/projects/"+seed.projectID+"/blackboard/runtime-graph", nil)
	missingGraphRec := httptest.NewRecorder()
	server.ServeHTTP(missingGraphRec, missingGraphReq)
	if missingGraphRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing graph credential status = %d want 401; body %s", missingGraphRec.Code, missingGraphRec.Body.String())
	}
	if err := json.Unmarshal(missingGraphRec.Body.Bytes(), &missingEnvelope); err != nil {
		t.Fatalf("decode structured missing graph credential error: %v", err)
	}
	if missingEnvelope.Error.Code != projectinterface.ErrCodeGrantNotFound {
		t.Fatalf("missing graph credential error = %#v", missingEnvelope.Error)
	}
}

// TestProjectInterfaceDaemonMCPApplyAndCurrentGraph drives the canonical path
// through the trusted MCP endpoint authenticated with a Continuation Interface
// Grant (runtime protocol §12.2): blackboard_apply lands a record and
// blackboard_get_current_graph returns it.
func TestProjectInterfaceDaemonMCPApplyAndCurrentGraph(t *testing.T) {
	seed := seedProjectInterfaceGrant(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	server, err := daemon.NewServer(daemon.Config{
		Version: "v", DBPath: seed.dbPath, AuthToken: "daemon-operator-token", DisableBuiltinSkills: true, ListenAddr: addr,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	issueProjectInterfaceGrant(t, &seed)
	httpServer := &http.Server{Handler: server}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		_ = server.Close()
	})

	endpoint := "http://" + addr + "/mcp?token=" + seed.token
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "pentest-i01", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list grant-authenticated MCP tools: %v", err)
	}
	allowed := map[string]bool{
		"blackboard_apply":             true,
		"blackboard_resolve_records":   true,
		"blackboard_get_current_graph": true,
		"blackboard_retain_evidence":   true,
	}
	for _, tool := range tools.Tools {
		if !allowed[tool.Name] {
			t.Fatalf("grant-authenticated MCP exposed compatibility tool %q", tool.Name)
		}
		if tool.Description != projectinterface.TrustedToolDescription(tool.Name) {
			t.Fatalf("MCP description drift for %q: %q", tool.Name, tool.Description)
		}
		delete(allowed, tool.Name)
	}
	if len(allowed) != 0 {
		t.Fatalf("grant-authenticated MCP missing canonical tools: %v", allowed)
	}

	operatorSession, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: "http://" + addr + "/mcp?token=daemon-operator-token",
	}, nil)
	if err != nil {
		t.Fatalf("operator MCP connect: %v", err)
	}
	defer func() { _ = operatorSession.Close() }()
	operatorTools, err := operatorSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list operator MCP tools: %v", err)
	}
	foundLegacy := false
	for _, tool := range operatorTools.Tools {
		if tool.Name == "upsert_project_fact" {
			foundLegacy = true
			break
		}
	}
	if !foundLegacy {
		t.Fatal("daemon operator token lost compatibility MCP tools")
	}

	applyRes, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "blackboard_apply",
		Arguments: map[string]any{
			"protocol_version": projectinterface.RuntimeProtocolVersion,
			"batch": map[string]any{
				"schema_version":  blackboard.GraphMutationSchemaVersion,
				"idempotency_key": "obj:mcp-round-trip",
				"operations": []map[string]any{{
					"op_id": "obj",
					"kind":  "create_node",
					"node":  map[string]any{"node_type": "exploration_objective", "stable_key": "objective:mcp-surface"},
					"create": map[string]any{"property_map": map[string]any{
						"objective": "MCP-driven admin surface discovery",
						"status":    "open",
					}},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("blackboard_apply: %v", err)
	}
	if applyRes.IsError {
		t.Fatalf("blackboard_apply returned isError: %s", mcpResultText(applyRes))
	}

	graphRes, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "blackboard_get_current_graph",
		Arguments: map[string]any{"protocol_version": projectinterface.RuntimeProtocolVersion},
	})
	if err != nil {
		t.Fatalf("blackboard_get_current_graph: %v", err)
	}
	if graphRes.IsError {
		t.Fatalf("blackboard_get_current_graph returned isError: %s", mcpResultText(graphRes))
	}
	if !strings.Contains(mcpResultText(graphRes), "objective:mcp-surface") {
		t.Fatalf("current graph missing created objective: %s", mcpResultText(graphRes))
	}

	// The plaintext token never appears in any MCP result.
	if strings.Contains(mcpResultText(applyRes), seed.token) || strings.Contains(mcpResultText(graphRes), seed.token) {
		t.Fatal("plaintext grant token appeared in an MCP result")
	}
}

// TestProjectInterfaceDaemonInactiveBeforeGraphCutover verifies the
// project-interface HTTP routes stay dark while the store epoch is legacy_v1:
// no production graph write path exists before the M05 cutover (slices §1).
func TestProjectInterfaceDaemonInactiveBeforeGraphCutover(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pi-legacy.db")
	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer server.Close()

	// The mutations route is not registered in legacy_v1, so it falls through to
	// the SPA handler (404 or HTML), never reaching graph code.
	req := httptest.NewRequest(http.MethodPost, "/api/projects/any/blackboard/mutations", bytes.NewReader(objectiveMutationBody()))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		body := rec.Body.String()
		var probe map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &probe) == nil {
			if kind, _ := probe["request_kind"].(string); kind == "apply" {
				t.Fatalf("project-interface Apply unexpectedly active in legacy_v1: %s", body)
			}
		}
	}
}

func mcpResultText(result *sdkmcp.CallToolResult) string {
	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}
