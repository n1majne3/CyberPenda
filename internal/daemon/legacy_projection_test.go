package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

// seedLegacyDaemonServer stands up a daemon backed by the graph store (epoch
// graph_v1) with a pentest Project carrying a fact, finding, evidence, and an
// alias fact, plus a CTF Project for the report kind-mismatch case.
func seedLegacyDaemonServer(t *testing.T) (httpServer *httptest.Server, pentestID, aliasKey, ctfID string) {
	t.Helper()
	dbPath := t.TempDir() + "/legacy-daemon.db"
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projects := project.NewService(db)
	pentest, err := projects.Create("Acme", "external", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create pentest: %v", err)
	}
	ctf, err := projects.CreateWithKind("Flag CTF", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create ctf: %v", err)
	}
	tasks := task.NewService(db, projects)
	sandbox, err := tasks.Create(task.CreateRequest{ProjectID: pentest.ID, Goal: "enumerate", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	ctx := blackboard.SystemExecutionContext(pentest.ID, pentest.Kind, "u05-daemon")
	ctx.TaskID = sandbox.ID
	ctx.Runner = string(task.RunnerSandbox)
	graph := blackboard.NewGraphService(db, blackboard.NewSequenceClock("2024-01-02T03:04:05Z"), blackboard.RandomIDSource{})
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "u05:daemon", Context: ctx,
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:admin"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "Admin exposed", "body": "admin body", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "alias", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:old-admin"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "service", "summary": "old", "body": "old", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:sqli"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"title": "SQLi", "description": "auth bypass", "status": "confirmed", "target": "https://example.com/login", "proof": "dump", "impact": "high", "recommendation": "parameterize", "cvss_version": "3.1", "cvss_vector": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}}},
			{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:resp"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"artifact_type": "http_exchange", "managed_path": "artifacts/evidence/resp.txt", "sha256": "111aa9a2bbc2dd417859723c4073f7f99e3a3a4bb21a86e9b6c9d6e0f0a1b2c3", "summary": "captured", "status": "available"}}},
			{OpID: "merge", Kind: blackboard.OpMergeNodes, Merge: blackboard.MergeNodesInput{Source: blackboard.NodeRef{OpID: "alias"}, Canonical: blackboard.NodeRef{OpID: "fact"}, SourceExpectedVersion: 1, CanonicalExpectedVersion: 1}},
			{OpID: "ev", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{OpID: "evidence"}, To: blackboard.NodeRef{OpID: "finding"}, Summary: "proves"}},
		},
	}); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("enable graph epoch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	server, err := daemon.NewServer(daemon.Config{Version: "v", DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer = httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return httpServer, pentest.ID, "fact:old-admin", ctf.ID
}

func getLegacyBody(t *testing.T, httpServer *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(httpServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	return resp.StatusCode, parsed
}

func postLegacyBody(t *testing.T, httpServer, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(httpServer+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestLegacyRoutesDelegateToGraphReadService(t *testing.T) {
	httpServer, projectID, aliasKey, ctfID := seedLegacyDaemonServer(t)

	t.Run("fact_index_returns_graph_projection", func(t *testing.T) {
		status, body := getLegacyBody(t, httpServer, "/api/projects/"+projectID+"/facts/index")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		facts, _ := body["facts"].([]any)
		if len(facts) != 1 {
			t.Fatalf("facts = %v", body)
		}
		first, _ := facts[0].(map[string]any)
		if first["fact_key"] != "fact:admin" {
			t.Fatalf("fact_key = %v", first["fact_key"])
		}
	})

	t.Run("fact_detail_resolves_alias", func(t *testing.T) {
		status, body := getLegacyBody(t, httpServer, "/api/projects/"+projectID+"/facts/"+aliasKey)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if body["fact_key"] != "fact:admin" {
			t.Fatalf("alias resolved to %v", body["fact_key"])
		}
		if body["resolved_from_alias"] != aliasKey {
			t.Fatalf("resolved_from_alias = %v", body["resolved_from_alias"])
		}
		if body["version"] == nil {
			t.Fatalf("additive version field missing")
		}
	})

	t.Run("findings_include_additive_pagination_marker", func(t *testing.T) {
		status, body := getLegacyBody(t, httpServer, "/api/projects/"+projectID+"/findings")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		findings, _ := body["findings"].([]any)
		if len(findings) != 1 {
			t.Fatalf("findings = %v", body)
		}
		if _, ok := body["compatibility_truncated"]; !ok {
			t.Fatalf("compatibility_truncated missing: %v", body)
		}
	})

	t.Run("evidence_has_attachments", func(t *testing.T) {
		status, body := getLegacyBody(t, httpServer, "/api/projects/"+projectID+"/evidence")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		evidence, _ := body["evidence"].([]any)
		if len(evidence) != 1 {
			t.Fatalf("evidence = %v", body)
		}
		first, _ := evidence[0].(map[string]any)
		attachments, _ := first["attachments"].([]any)
		if len(attachments) != 1 {
			t.Fatalf("attachments = %v", first["attachments"])
		}
	})

	t.Run("report_delegates_to_pentest_markdown_v1", func(t *testing.T) {
		status, raw := postLegacyBody(t, httpServer.URL, "/api/projects/"+projectID+"/report", `{}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		if parsed["status"] != "generated" || parsed["format"] != "markdown" {
			t.Fatalf("report envelope = %v", parsed)
		}
		markdown, _ := parsed["markdown"].(string)
		if !strings.Contains(markdown, "SQLi") {
			t.Fatalf("report markdown missing finding: %s", markdown)
		}
	})

	t.Run("ctf_report_rejects_with_project_kind_mismatch", func(t *testing.T) {
		status, raw := postLegacyBody(t, httpServer.URL, "/api/projects/"+ctfID+"/report", `{}`)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("ctf report status = %d, want 422: %s", status, raw)
		}
	})
}

func callMCPTool(t *testing.T, httpServer *httptest.Server, tool string, args map[string]any) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/call %s: %v", tool, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call %s status = %d: %s", tool, resp.StatusCode, raw)
	}
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode tools/call %s response: %v: %s", tool, err, raw)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("tools/call %s returned no content: %s", tool, raw)
	}
	return parsed.Result.Content[0].Text
}

func TestMCPCompatibilityToolsDelegateToGraphReadService(t *testing.T) {
	httpServer, projectID, aliasKey, _ := seedLegacyDaemonServer(t)

	t.Run("list_project_facts", func(t *testing.T) {
		text := callMCPTool(t, httpServer, "list_project_facts", map[string]any{"project_id": projectID})
		if !strings.Contains(text, "fact:admin") {
			t.Fatalf("list_project_facts missing seeded fact: %s", text)
		}
	})

	t.Run("get_project_fact_resolves_alias", func(t *testing.T) {
		text := callMCPTool(t, httpServer, "get_project_fact", map[string]any{"project_id": projectID, "fact_key": aliasKey})
		if !strings.Contains(text, "fact:admin") || !strings.Contains(text, "resolved_from_alias") {
			t.Fatalf("get_project_fact did not resolve alias: %s", text)
		}
	})

	t.Run("search_project_facts", func(t *testing.T) {
		text := callMCPTool(t, httpServer, "search_project_facts", map[string]any{"project_id": projectID, "query": "Admin"})
		if !strings.Contains(text, "fact:admin") {
			t.Fatalf("search_project_facts missing match: %s", text)
		}
	})

	t.Run("list_vulnerabilities", func(t *testing.T) {
		text := callMCPTool(t, httpServer, "list_vulnerabilities", map[string]any{"project_id": projectID})
		if !strings.Contains(text, "finding:sqli") {
			t.Fatalf("list_vulnerabilities missing finding: %s", text)
		}
	})
}
