package projectinterface_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboard"
	"pentest/internal/mcpserver"
	"pentest/internal/pentestctl"
	"pentest/internal/projectinterface"
)

// TestProjectInterfaceConformanceProducesSameResultAndErrorAcrossAllTransports
// is the I02 first-red test. The shared corpus drives the public module, HTTP,
// MCP, task CLI, and operator CLI interfaces. Transport-only metadata is
// ignored; the canonical response and observable graph result must agree.
func TestProjectInterfaceConformanceProducesSameResultAndErrorAcrossAllTransports(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	request := objectiveApplyRequest()
	requestArguments := map[string]any{
		"protocol_version": projectinterface.RuntimeProtocolVersion,
		"batch": map[string]any{
			"schema_version":  blackboard.GraphMutationSchemaVersion,
			"idempotency_key": request.Batch.IdempotencyKey,
			"operations": []map[string]any{{
				"op_id": "obj",
				"kind":  "create_node",
				"node": map[string]any{
					"node_type":  "exploration_objective",
					"stable_key": "objective:find-admin-surface",
				},
				"create": map[string]any{"property_map": map[string]any{
					"objective": "Locate the authenticated admin surface",
					"status":    "open",
				}},
			}},
		},
	}

	want, err := fixture.service.Apply(ctx, principal, request)
	if err != nil {
		t.Fatalf("project-interface apply: %v", err)
	}

	httpHandler := projectinterface.NewHTTPHandler(fixture.service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/blackboard/mutations", httpHandler.Apply)
	mux.HandleFunc("POST /api/projects/{id}/blackboard/records:resolve", httpHandler.ResolveRecords)
	mux.HandleFunc("GET /api/projects/{id}/blackboard/runtime-graph", httpHandler.CurrentGraph)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	httpRequestBody, _ := json.Marshal(requestArguments)
	httpRequest, err := http.NewRequest(http.MethodPost,
		httpServer.URL+"/api/projects/"+fixture.project.ID+"/blackboard/mutations",
		bytes.NewReader(httpRequestBody))
	if err != nil {
		t.Fatalf("build HTTP request: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("HTTP apply: %v", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		t.Fatalf("HTTP apply status = %d", httpResponse.StatusCode)
	}
	var gotHTTP projectinterface.ApplyMutationResponse
	if err := json.NewDecoder(httpResponse.Body).Decode(&gotHTTP); err != nil {
		t.Fatalf("decode HTTP apply: %v", err)
	}
	assertApplyResponseEqual(t, "HTTP", gotHTTP, want)

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	mcpServer := mcpserver.New(mcpserver.Deps{ProjectInterface: fixture.service, Principal: &principal})
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "i02-conformance", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	mcpResult, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "blackboard_apply",
		Arguments: requestArguments,
	})
	if err != nil {
		t.Fatalf("MCP apply: %v", err)
	}
	if mcpResult.IsError {
		t.Fatalf("MCP apply returned isError: %s", mcpText(mcpResult))
	}
	if mcpResult.StructuredContent == nil {
		t.Fatal("MCP success omitted structuredContent")
	}
	var gotMCP projectinterface.ApplyMutationResponse
	if err := json.Unmarshal([]byte(mcpText(mcpResult)), &gotMCP); err != nil {
		t.Fatalf("decode MCP apply: %v", err)
	}
	assertApplyResponseEqual(t, "MCP", gotMCP, want)
	mcpResolve, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "blackboard_resolve_records",
		Arguments: map[string]any{
			"protocol_version": projectinterface.RuntimeProtocolVersion,
			"nodes":            []any{},
			"edge_ids":         []any{"edge-missing"},
		},
	})
	if err != nil {
		t.Fatalf("MCP resolve edge: %v", err)
	}
	if mcpResolve.IsError {
		t.Fatalf("MCP resolve edge returned isError: %s", mcpText(mcpResolve))
	}
	var resolvedEdge projectinterface.ResolveRecordsResponse
	if err := json.Unmarshal([]byte(mcpText(mcpResolve)), &resolvedEdge); err != nil || len(resolvedEdge.MissingEdges) != 1 {
		t.Fatalf("MCP resolve edge result = %s err=%v", mcpText(mcpResolve), err)
	}

	inputPath := filepath.Join(t.TempDir(), "apply.json")
	if err := os.WriteFile(inputPath, httpRequestBody, 0o600); err != nil {
		t.Fatalf("write task CLI input: %v", err)
	}
	t.Setenv("PENTEST_API_URL", httpServer.URL)
	t.Setenv("PENTEST_INTERFACE_TOKEN", fixture.token)
	t.Setenv("PENTEST_PROJECT_ID", fixture.project.ID)
	var taskCLI bytes.Buffer
	if err := pentestctl.Run(&taskCLI, []string{"blackboard", "apply", "--input", inputPath}); err != nil {
		t.Fatalf("task CLI apply: %v", err)
	}
	var gotTaskCLI projectinterface.ApplyMutationResponse
	if err := json.Unmarshal(taskCLI.Bytes(), &gotTaskCLI); err != nil {
		t.Fatalf("decode task CLI apply: %v", err)
	}
	assertApplyResponseEqual(t, "task CLI", gotTaskCLI, want)

	resolveInput, _ := json.Marshal(projectinterface.ResolveRecordsRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Nodes: []projectinterface.NodeLookup{{
			NodeType: string(blackboard.NodeTypeExplorationObjective), StableKey: "objective:find-admin-surface",
		}},
	})
	resolvePath := filepath.Join(t.TempDir(), "resolve.json")
	if err := os.WriteFile(resolvePath, resolveInput, 0o600); err != nil {
		t.Fatalf("write resolve input: %v", err)
	}
	var resolveOut bytes.Buffer
	if err := pentestctl.Run(&resolveOut, []string{"blackboard", "records", "resolve", "--input", resolvePath}); err != nil {
		t.Fatalf("task CLI resolve: %v", err)
	}
	var resolved projectinterface.ResolveRecordsResponse
	if err := json.Unmarshal(resolveOut.Bytes(), &resolved); err != nil || len(resolved.Nodes) != 1 {
		t.Fatalf("task CLI resolve result = %s err=%v", resolveOut.String(), err)
	}
	var currentOut bytes.Buffer
	if err := pentestctl.Run(&currentOut, []string{"blackboard", "graph", "current"}); err != nil {
		t.Fatalf("task CLI current graph: %v", err)
	}
	if !bytes.Contains(currentOut.Bytes(), []byte("objective:find-admin-surface")) {
		t.Fatalf("task CLI current graph omitted objective: %s", currentOut.String())
	}

	operatorFixture := newServiceFixture(t)
	operatorInputPath := filepath.Join(t.TempDir(), "operator-apply.json")
	if err := os.WriteFile(operatorInputPath, httpRequestBody, 0o600); err != nil {
		t.Fatalf("write operator CLI input: %v", err)
	}
	var operatorCLI bytes.Buffer
	if err := pentestctl.Run(&operatorCLI, []string{
		"--db", operatorFixture.dbPath,
		"blackboard", "apply",
		"--project", operatorFixture.project.ID,
		"--actor-id", "local-operator",
		"--input", operatorInputPath,
	}); err != nil {
		t.Fatalf("operator CLI apply: %v", err)
	}
	var gotOperator projectinterface.ApplyMutationResponse
	if err := json.Unmarshal(operatorCLI.Bytes(), &gotOperator); err != nil {
		t.Fatalf("decode operator CLI apply: %v", err)
	}
	if gotOperator.RequestKind != want.RequestKind || gotOperator.ObservedGraphRevision != want.ObservedGraphRevision ||
		len(gotOperator.Result.Operations) != 1 ||
		gotOperator.Result.Operations[0].StableKey != want.Result.Operations[0].StableKey ||
		gotOperator.Result.Operations[0].NodeType != want.Result.Operations[0].NodeType ||
		gotOperator.Result.Operations[0].NodeVersion != want.Result.Operations[0].NodeVersion ||
		gotOperator.Result.Operations[0].Changed != want.Result.Operations[0].Changed {
		t.Fatalf("operator CLI semantic result = %+v", gotOperator)
	}
	var operatorCurrent bytes.Buffer
	if err := pentestctl.Run(&operatorCurrent, []string{
		"--db", operatorFixture.dbPath,
		"blackboard", "graph", "current",
		"--project", operatorFixture.project.ID,
		"--actor-id", "local-operator",
	}); err != nil {
		t.Fatalf("operator CLI current graph: %v", err)
	}
	if !bytes.Contains(operatorCurrent.Bytes(), []byte("objective:find-admin-surface")) ||
		!bytes.Contains(operatorCurrent.Bytes(), []byte("Locate the authenticated admin surface")) {
		t.Fatalf("operator CLI current graph omitted operator mutation: %s", operatorCurrent.String())
	}
}

func TestCheckpointAndFinishConformAcrossMCPAndTaskCLI(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "mcp-cli-finish")

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	mcpServer := mcpserver.New(mcpserver.Deps{ProjectInterface: fixture.service, Principal: &principal})
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "i04-conformance", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	checkpointRequest := projectinterface.CheckpointAttemptRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		IdempotencyKey:  "checkpoint:mcp-cli",
		Attempt:         blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:mcp-cli-finish"},
		ExpectedVersion: 1,
		Summary:         "MCP checkpoint replayed through task CLI.",
	}
	checkpointArgs := map[string]any{}
	checkpointBody, _ := json.Marshal(checkpointRequest)
	_ = json.Unmarshal(checkpointBody, &checkpointArgs)
	mcpCheckpoint, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_checkpoint_attempt", Arguments: checkpointArgs})
	if err != nil || mcpCheckpoint.IsError {
		t.Fatalf("MCP checkpoint: result=%#v err=%v", mcpCheckpoint, err)
	}
	var canonicalCheckpoint projectinterface.CheckpointAttemptResponse
	if err := json.Unmarshal([]byte(mcpText(mcpCheckpoint)), &canonicalCheckpoint); err != nil {
		t.Fatalf("decode MCP checkpoint: %v", err)
	}

	httpServer := httptest.NewServer(newHTTPMux(fixture))
	t.Cleanup(httpServer.Close)
	t.Setenv("PENTEST_API_URL", httpServer.URL)
	t.Setenv("PENTEST_INTERFACE_TOKEN", fixture.token)
	t.Setenv("PENTEST_PROJECT_ID", fixture.project.ID)
	t.Setenv("PENTEST_TASK_ID", fixture.task.ID)
	t.Setenv("PENTEST_CONTINUATION_ID", fixture.continuation.ID)
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := os.WriteFile(checkpointPath, checkpointBody, 0o600); err != nil {
		t.Fatalf("write checkpoint input: %v", err)
	}
	var cliCheckpointJSON bytes.Buffer
	if err := pentestctl.Run(&cliCheckpointJSON, []string{"blackboard", "attempt", "checkpoint", "--input", checkpointPath}); err != nil {
		t.Fatalf("task CLI checkpoint: %v", err)
	}
	var cliCheckpoint projectinterface.CheckpointAttemptResponse
	if err := json.Unmarshal(cliCheckpointJSON.Bytes(), &cliCheckpoint); err != nil {
		t.Fatalf("decode task CLI checkpoint: %v", err)
	}
	if cliCheckpoint.Result.Event.ID != canonicalCheckpoint.Result.Event.ID ||
		cliCheckpoint.Result.Mutation.MutationID != canonicalCheckpoint.Result.Mutation.MutationID {
		t.Fatalf("task CLI checkpoint drifted: MCP=%#v CLI=%#v", canonicalCheckpoint, cliCheckpoint)
	}

	if _, err := fixture.service.Apply(ctx, principal, projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "mcp-cli:terminal",
			Operations: []blackboard.Operation{{
				OpID: "terminal", Kind: blackboard.OpTransitionNode,
				Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:mcp-cli-finish"},
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: 2, Status: "failed", Summary: "MCP/CLI conformance Attempt failed."},
			}},
		},
	}); err != nil {
		t.Fatalf("conclude MCP/CLI Attempt: %v", err)
	}
	finishRequest := projectinterface.FinishContinuationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		IdempotencyKey:  "finish:mcp-cli",
		Summary:         "MCP Finish replayed through task CLI.",
	}
	finishArgs := map[string]any{}
	finishBody, _ := json.Marshal(finishRequest)
	_ = json.Unmarshal(finishBody, &finishArgs)
	mcpFinish, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_finish_continuation", Arguments: finishArgs})
	if err != nil || mcpFinish.IsError {
		t.Fatalf("MCP Finish: result=%#v err=%v", mcpFinish, err)
	}
	var canonicalFinish projectinterface.FinishContinuationResponse
	if err := json.Unmarshal([]byte(mcpText(mcpFinish)), &canonicalFinish); err != nil {
		t.Fatalf("decode MCP Finish: %v", err)
	}
	finishPath := filepath.Join(t.TempDir(), "finish.json")
	if err := os.WriteFile(finishPath, finishBody, 0o600); err != nil {
		t.Fatalf("write Finish input: %v", err)
	}
	var cliFinishJSON bytes.Buffer
	if err := pentestctl.Run(&cliFinishJSON, []string{"blackboard", "continuation", "finish", "--input", finishPath}); err != nil {
		t.Fatalf("task CLI Finish: %v", err)
	}
	var cliFinish projectinterface.FinishContinuationResponse
	if err := json.Unmarshal(cliFinishJSON.Bytes(), &cliFinish); err != nil {
		t.Fatalf("decode task CLI Finish: %v", err)
	}
	if cliFinish.Result.SummaryVersion.ID != canonicalFinish.Result.SummaryVersion.ID ||
		cliFinish.Result.GraphRevision != canonicalFinish.Result.GraphRevision {
		t.Fatalf("task CLI Finish drifted: MCP=%#v CLI=%#v", canonicalFinish, cliFinish)
	}

	closedCheckpoint := checkpointRequest
	closedCheckpoint.IdempotencyKey = "checkpoint:mcp-cli:after-finish"
	closedCheckpoint.ExpectedVersion = 2
	closedArgs := map[string]any{}
	closedBody, _ := json.Marshal(closedCheckpoint)
	_ = json.Unmarshal(closedBody, &closedArgs)
	mcpClosed, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_checkpoint_attempt", Arguments: closedArgs})
	if err != nil || !mcpClosed.IsError {
		t.Fatalf("MCP closed checkpoint: result=%#v err=%v", mcpClosed, err)
	}
	var mcpClosedEnvelope struct {
		Error projectinterface.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(mcpText(mcpClosed)), &mcpClosedEnvelope); err != nil || mcpClosedEnvelope.Error.Code != projectinterface.ErrCodeContinuationClosed {
		t.Fatalf("MCP closed error = %#v decode=%v", mcpClosedEnvelope.Error, err)
	}
	closedPath := filepath.Join(t.TempDir(), "closed-checkpoint.json")
	if err := os.WriteFile(closedPath, closedBody, 0o600); err != nil {
		t.Fatalf("write closed checkpoint input: %v", err)
	}
	cliClosedErr := pentestctl.Run(io.Discard, []string{"blackboard", "attempt", "checkpoint", "--input", closedPath})
	if got := projectinterface.AsError(cliClosedErr); got == nil || got.Code != projectinterface.ErrCodeContinuationClosed || pentestctl.ExitCode(cliClosedErr) != 5 {
		t.Fatalf("task CLI closed error = %#v exit=%d", cliClosedErr, pentestctl.ExitCode(cliClosedErr))
	}
}

func TestRetainEvidenceProducesSameResultAcrossModuleHTTPMCPAndTaskCLI(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prepareRetainEvidenceAttempt(t, fixture, principal, "parity")
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "parity.txt"), []byte("transport parity proof"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	request := projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:parity",
		StableKey: "evidence:parity", ArtifactType: "file", SourcePath: "parity.txt", Summary: "transport parity proof",
		ProducedByAttempt: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:parity"},
	}
	want, err := fixture.service.RetainEvidence(ctx, principal, request)
	if err != nil {
		t.Fatalf("module Retain Evidence: %v", err)
	}
	wantJSON, _ := json.Marshal(want)

	handler := projectinterface.NewHTTPHandler(fixture.service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/blackboard/evidence:retain", handler.RetainEvidence)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	body, _ := json.Marshal(request)
	httpRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects/"+fixture.project.ID+"/blackboard/evidence:retain", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("HTTP Retain Evidence: %v", err)
	}
	defer httpResponse.Body.Close()
	var gotHTTP json.RawMessage
	if err := json.NewDecoder(httpResponse.Body).Decode(&gotHTTP); err != nil || !bytes.Equal(gotHTTP, wantJSON) {
		t.Fatalf("HTTP retain result = %s want %s err=%v", gotHTTP, wantJSON, err)
	}

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	mcpServer := mcpserver.New(mcpserver.Deps{ProjectInterface: fixture.service, Principal: &principal})
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "i03-conformance", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	var args map[string]any
	_ = json.Unmarshal(body, &args)
	mcpResult, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_retain_evidence", Arguments: args})
	if err != nil {
		t.Fatalf("MCP Retain Evidence: %v", err)
	}
	if mcpResult.IsError || !bytes.Equal([]byte(mcpText(mcpResult)), wantJSON) {
		t.Fatalf("MCP retain result = %s want %s", mcpText(mcpResult), wantJSON)
	}

	inputPath := filepath.Join(t.TempDir(), "retain.json")
	if err := os.WriteFile(inputPath, body, 0o600); err != nil {
		t.Fatalf("write CLI input: %v", err)
	}
	t.Setenv("PENTEST_API_URL", server.URL)
	t.Setenv("PENTEST_INTERFACE_TOKEN", fixture.token)
	t.Setenv("PENTEST_PROJECT_ID", fixture.project.ID)
	var cli bytes.Buffer
	if err := pentestctl.Run(&cli, []string{"blackboard", "evidence", "retain", "--input", inputPath}); err != nil {
		t.Fatalf("task CLI Retain Evidence: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(cli.Bytes()), wantJSON) {
		t.Fatalf("task CLI retain result = %s want %s", cli.Bytes(), wantJSON)
	}
}

func TestOperatorCLIRetainEvidenceRequiresExplicitSourceRoot(t *testing.T) {
	fixture := newServiceFixture(t)
	sourceRoot := filepath.Join(fixture.artifactRoot, "operator-cli-source")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatalf("create operator CLI source root: %v", err)
	}
	source := filepath.Join(sourceRoot, "proof.txt")
	if err := os.WriteFile(source, []byte("operator CLI proof"), 0o600); err != nil {
		t.Fatalf("write operator CLI source: %v", err)
	}
	body, _ := json.Marshal(projectinterface.RetainEvidenceRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion, IdempotencyKey: "retain:operator-cli",
		StableKey: "evidence:operator-cli", ArtifactType: "file", SourcePath: source, Summary: "operator CLI proof",
	})
	inputPath := filepath.Join(t.TempDir(), "operator-retain.json")
	if err := os.WriteFile(inputPath, body, 0o600); err != nil {
		t.Fatalf("write operator CLI input: %v", err)
	}
	baseArgs := []string{"--db", fixture.dbPath, "blackboard", "evidence", "retain", "--project", fixture.project.ID, "--actor-id", "local-operator", "--input", inputPath}
	missingRootErr := pentestctl.Run(io.Discard, baseArgs)
	if interfaceErr := projectinterface.AsError(missingRootErr); interfaceErr == nil || interfaceErr.Code != projectinterface.ErrCodeInvalidRequest {
		t.Fatalf("operator CLI without source root error = %v", missingRootErr)
	}
	var output bytes.Buffer
	args := append(append([]string{}, baseArgs...), "--source-root", sourceRoot)
	if err := pentestctl.Run(&output, args); err != nil {
		t.Fatalf("operator CLI retain: %v", err)
	}
	var response projectinterface.RetainEvidenceResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Result.Node.StableKey != "evidence:operator-cli" {
		t.Fatalf("operator CLI response = %s err=%v", output.Bytes(), err)
	}
	var replay bytes.Buffer
	if err := pentestctl.Run(&replay, args); err != nil {
		t.Fatalf("operator CLI replay: %v", err)
	}
	if !bytes.Equal(output.Bytes(), replay.Bytes()) {
		t.Fatalf("operator CLI replay drifted:\nfirst %s\nreplay %s", output.Bytes(), replay.Bytes())
	}
	stored, err := fixture.graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: fixture.project.ID, NodeType: blackboard.NodeTypeEvidenceArtifact, Key: "evidence:operator-cli",
	})
	if err != nil || stored.Node.PropertyMap["sha256"] != response.Result.SHA256 {
		t.Fatalf("operator CLI graph state = %+v err=%v", stored.Node, err)
	}
}

// TestProjectInterfaceErrorsRemainStructuredAcrossMCPHTTPAndCLI proves all
// public adapters preserve the canonical code, path, retryability, and details
// while adding only transport-specific status/exit metadata.
func TestProjectInterfaceErrorsRemainStructuredAcrossMCPHTTPAndCLI(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	request := objectiveApplyRequest()
	request.ProtocolVersion = 99
	_, directErr := fixture.service.Apply(ctx, principal, request)
	want := projectinterface.AsError(directErr)
	if want == nil {
		t.Fatalf("direct error = %v", directErr)
	}

	requestArguments := map[string]any{
		"protocol_version": 99,
		"batch": map[string]any{
			"schema_version":  blackboard.GraphMutationSchemaVersion,
			"idempotency_key": "error:transport-parity",
			"operations": []map[string]any{{
				"op_id": "obj", "kind": "create_node",
				"node":   map[string]any{"node_type": "exploration_objective", "stable_key": "objective:error-parity"},
				"create": map[string]any{"property_map": map[string]any{"objective": "error parity", "status": "open"}},
			}},
		},
	}
	body, _ := json.Marshal(requestArguments)
	handler := projectinterface.NewHTTPHandler(fixture.service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/blackboard/mutations", handler.Apply)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	httpRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("HTTP apply: %v", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d want 400", httpResponse.StatusCode)
	}
	var httpEnvelope struct {
		Error projectinterface.Error `json:"error"`
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(&httpEnvelope); err != nil {
		t.Fatalf("decode HTTP error: %v", err)
	}
	assertInterfaceErrorEqual(t, "HTTP", &httpEnvelope.Error, want)

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	mcpServer := mcpserver.New(mcpserver.Deps{ProjectInterface: fixture.service, Principal: &principal})
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "i02-errors", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	mcpResult, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_apply", Arguments: requestArguments})
	if err != nil {
		t.Fatalf("MCP apply: %v", err)
	}
	if !mcpResult.IsError {
		t.Fatalf("MCP error returned isError=false: %s", mcpText(mcpResult))
	}
	var mcpEnvelope struct {
		Error projectinterface.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(mcpText(mcpResult)), &mcpEnvelope); err != nil {
		t.Fatalf("decode MCP error: %v", err)
	}
	assertInterfaceErrorEqual(t, "MCP", &mcpEnvelope.Error, want)
	if mcpResult.StructuredContent == nil {
		t.Fatal("MCP error omitted structuredContent")
	}

	inputPath := filepath.Join(t.TempDir(), "invalid-version.json")
	if err := os.WriteFile(inputPath, body, 0o600); err != nil {
		t.Fatalf("write CLI input: %v", err)
	}
	t.Setenv("PENTEST_API_URL", server.URL)
	t.Setenv("PENTEST_INTERFACE_TOKEN", fixture.token)
	t.Setenv("PENTEST_PROJECT_ID", fixture.project.ID)
	var cliOut bytes.Buffer
	cliErr := pentestctl.Run(&cliOut, []string{"blackboard", "apply", "--input", inputPath})
	gotCLI := projectinterface.AsError(cliErr)
	if gotCLI == nil {
		t.Fatalf("CLI error = %v", cliErr)
	}
	assertInterfaceErrorEqual(t, "CLI", gotCLI, want)
	if exit := pentestctl.ExitCode(cliErr); exit != 2 {
		t.Fatalf("CLI exit = %d want 2", exit)
	}

	forbiddenRequest := projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "error:actor-forbidden",
			Operations: []blackboard.Operation{{OpID: "merge", Kind: blackboard.OpMergeNodes}},
		},
	}
	_, directErr = fixture.service.Apply(ctx, principal, forbiddenRequest)
	want = projectinterface.AsError(directErr)
	if want == nil || len(want.Details) == 0 {
		t.Fatalf("direct actor error lacks structured details: %#v", directErr)
	}
	forbiddenArguments := map[string]any{
		"protocol_version": projectinterface.RuntimeProtocolVersion,
		"batch": map[string]any{
			"schema_version": blackboard.GraphMutationSchemaVersion, "idempotency_key": "error:actor-forbidden",
			"operations": []map[string]any{{
				"op_id": "merge", "kind": "merge_nodes",
				"node": map[string]any{},
				"merge": map[string]any{
					"source": map[string]any{"id": "source"}, "canonical": map[string]any{"id": "canonical"},
					"source_expected_version": 1, "canonical_expected_version": 1,
				},
			}},
		},
	}
	forbiddenBody, _ := json.Marshal(forbiddenArguments)
	forbiddenHTTP, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(forbiddenBody))
	forbiddenHTTP.Header.Set("Authorization", "Bearer "+fixture.token)
	forbiddenHTTPResponse, err := http.DefaultClient.Do(forbiddenHTTP)
	if err != nil {
		t.Fatalf("HTTP forbidden Apply: %v", err)
	}
	defer forbiddenHTTPResponse.Body.Close()
	if forbiddenHTTPResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP actor status = %d want 403", forbiddenHTTPResponse.StatusCode)
	}
	if err := json.NewDecoder(forbiddenHTTPResponse.Body).Decode(&httpEnvelope); err != nil {
		t.Fatalf("decode HTTP actor error: %v", err)
	}
	assertInterfaceErrorEqual(t, "HTTP actor", &httpEnvelope.Error, want)

	mcpResult, err = clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "blackboard_apply", Arguments: forbiddenArguments})
	if err != nil {
		t.Fatalf("MCP forbidden Apply: %v", err)
	}
	if !mcpResult.IsError {
		t.Fatal("MCP actor failure returned isError=false")
	}
	if err := json.Unmarshal([]byte(mcpText(mcpResult)), &mcpEnvelope); err != nil {
		t.Fatalf("decode MCP actor error: %v", err)
	}
	assertInterfaceErrorEqual(t, "MCP actor", &mcpEnvelope.Error, want)

	forbiddenPath := filepath.Join(t.TempDir(), "actor-forbidden.json")
	if err := os.WriteFile(forbiddenPath, forbiddenBody, 0o600); err != nil {
		t.Fatalf("write forbidden CLI input: %v", err)
	}
	cliErr = pentestctl.Run(&cliOut, []string{"blackboard", "apply", "--input", forbiddenPath})
	gotCLI = projectinterface.AsError(cliErr)
	if gotCLI == nil {
		t.Fatalf("CLI actor error = %v", cliErr)
	}
	assertInterfaceErrorEqual(t, "CLI actor", gotCLI, want)
	if exit := pentestctl.ExitCode(cliErr); exit != 3 {
		t.Fatalf("CLI actor exit = %d want 3", exit)
	}
}

func TestRevokedGrantCannotProbeNodeExistenceThroughApplyAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	ctx := context.Background()
	principal, err := fixture.service.Authenticate(ctx, fixture.token, fixture.project.ID)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := fixture.grants.Revoke(ctx, fixture.grant.ID); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	request := projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "revoked:unknown-node",
			Operations: []blackboard.Operation{{
				OpID: "probe", Kind: blackboard.OpPatchNode,
				Node:  blackboard.NodeRef{ID: "node-does-not-exist"},
				Patch: blackboard.PatchNodeInput{ExpectedVersion: 1, Properties: map[string]any{"status": "open"}},
			}},
		},
	}
	if _, err := fixture.service.Apply(ctx, principal, request); err == nil {
		t.Fatal("revoked direct Apply unexpectedly probed unknown node")
	} else {
		assertErrorCode(t, err, projectinterface.ErrCodeContinuationClosed)
	}

	handler := projectinterface.NewHTTPHandler(fixture.service)
	body, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+fixture.project.ID+"/blackboard/mutations", bytes.NewReader(body))
	httpRequest.SetPathValue("id", fixture.project.ID)
	httpRequest.Header.Set("Authorization", "Bearer "+fixture.token)
	recorder := httptest.NewRecorder()
	handler.Apply(recorder, httpRequest)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("revoked HTTP Apply status = %d want 403; body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error projectinterface.Error `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode revoked HTTP error: %v", err)
	}
	if envelope.Error.Code != projectinterface.ErrCodeContinuationClosed {
		t.Fatalf("revoked HTTP error = %#v", envelope.Error)
	}
}

func assertApplyResponseEqual(t *testing.T, transport string, got, want projectinterface.ApplyMutationResponse) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal %s response: %v", transport, err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected response: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("%s response differs from canonical project-interface result\n got: %s\nwant: %s", transport, gotJSON, wantJSON)
	}
}

func assertInterfaceErrorEqual(t *testing.T, transport string, got, want *projectinterface.Error) {
	t.Helper()
	if got.Code != want.Code || got.Path != want.Path || got.Retryable != want.Retryable ||
		!mapsEqual(got.Details, want.Details) {
		t.Fatalf("%s error = %#v want code=%q path=%q retryable=%v details=%#v", transport, got, want.Code, want.Path, want.Retryable, want.Details)
	}
}

func mapsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func mcpText(result *sdkmcp.CallToolResult) string {
	var out bytes.Buffer
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}
