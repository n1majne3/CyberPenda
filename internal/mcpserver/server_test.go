package mcpserver_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboard"
	"pentest/internal/mcpserver"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func connectMCP(t *testing.T, deps mcpserver.Deps) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcpserver.New(deps)
	t1, t2 := sdkmcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *sdkmcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned error: %#v", name, res)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("%s expected text content, got %T", name, res.Content[0])
	}
	return text.Text
}

// TestUpsertProjectFactWritesEquivalentState proves the trusted MCP server
// upserts facts through the same blackboard service as HTTP and CLI.
func TestUpsertProjectFactWritesEquivalentState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts})
	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id":   proj.ID,
		"fact_key":     "target:example.com",
		"category":     "target",
		"summary":      "example.com is in scope",
		"body":         "primary web target",
		"confidence":   "confirmed",
		"scope_status": "in_scope",
	})

	got, err := facts.GetFact(proj.ID, "target:example.com")
	if err != nil {
		t.Fatalf("get fact: %v", err)
	}
	if got.Summary != "example.com is in scope" || got.Body != "primary web target" {
		t.Fatalf("unexpected stored fact: %#v", got)
	}
}

func TestMCPFactReadSearchAndDeprecate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts})

	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com", "summary": "example.com resolves",
	})
	body := callTool(t, session, "get_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com",
	})
	if !strings.Contains(body, `"fact_key":"dns:example.com"`) {
		t.Fatalf("get_project_fact body missing key: %s", body)
	}
	index := callTool(t, session, "list_project_facts", map[string]any{"project_id": proj.ID})
	if !strings.Contains(index, "dns:example.com") {
		t.Fatalf("list_project_facts missing fact: %s", index)
	}
	search := callTool(t, session, "search_project_facts", map[string]any{
		"project_id": proj.ID, "query": "example",
	})
	if !strings.Contains(search, "dns:example.com") {
		t.Fatalf("search_project_facts missing match: %s", search)
	}
	callTool(t, session, "deprecate_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "dns:example.com",
	})
	index = callTool(t, session, "list_project_facts", map[string]any{"project_id": proj.ID})
	if strings.Contains(index, "dns:example.com") {
		t.Fatalf("deprecated fact should be excluded from default index: %s", index)
	}
}

func TestMCPFindingEvidenceAndReportFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	facts := blackboard.NewService(db)
	tasks := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Goal:      "enumerate example.com",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Facts: facts, Tasks: tasks})
	callTool(t, session, "upsert_project_fact", map[string]any{
		"project_id": proj.ID, "fact_key": "recon:subs", "summary": "Found 3 subdomains",
	})
	callTool(t, session, "record_vulnerability", map[string]any{
		"project_id": proj.ID, "finding_key": "sqli-login", "title": "SQL injection in login",
		"status": "confirmed", "target": "https://example.com/login", "proof": "' or 1=1--",
		"impact": "auth bypass", "recommendation": "parameterize queries",
		"cvss_version": "4.0", "cvss_vector": "CVSS:4.0/AV:N/VC:H/VI:H",
	})
	callTool(t, session, "attach_evidence", map[string]any{
		"project_id": proj.ID, "evidence_key": "login-proof", "attach_to_type": "finding",
		"attach_to_key": "sqli-login", "artifact_type": "http_exchange", "summary": "login replay",
	})

	listBody := callTool(t, session, "list_vulnerabilities", map[string]any{"project_id": proj.ID})
	if !strings.Contains(listBody, "sqli-login") {
		t.Fatalf("list_vulnerabilities missing finding: %s", listBody)
	}

	reportBody := callTool(t, session, "generate_report", map[string]any{
		"project_id": proj.ID, "task_id": launched.ID,
	})
	if !strings.Contains(reportBody, "SQL injection in login") || !strings.Contains(reportBody, "Confirmed Findings") {
		t.Fatalf("generate_report missing expected content: %s", reportBody)
	}
}

func TestMCPSubmitTaskSummaryWritesEquivalentState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	launched, err := tasks.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Goal:      "enumerate example.com",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	session := connectMCP(t, mcpserver.Deps{Projects: projects, Tasks: tasks})
	body := callTool(t, session, "submit_task_summary", map[string]any{
		"project_id":   proj.ID,
		"task_id":      launched.ID,
		"summary":      "Mapped APIs and solved 10 easy challenges.",
		"submitted_by": "claude_code",
	})
	if !strings.Contains(body, `"version":1`) {
		t.Fatalf("expected versioned summary response, got %s", body)
	}

	versions, err := tasks.SummaryVersions(launched.ID)
	if err != nil {
		t.Fatalf("list summary versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Summary != "Mapped APIs and solved 10 easy challenges." {
		t.Fatalf("unexpected stored summary: %#v", versions)
	}
}
