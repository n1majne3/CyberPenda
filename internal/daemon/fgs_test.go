package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"pentest/internal/fgs"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/session"
	"pentest/internal/task"
	"testing"
	"time"
)

func TestFGSTaskReceiverResolvesRelativeRuntimeRoot(t *testing.T) {
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filepath.Join(root, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: relative, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	p, err := server.projects.Create("FGS", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "Check", Runner: task.RunnerHost, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := server.tasks.CreateContinuation(found.ID, "profile", "codex", task.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, "runs", found.ID, "workdir")
	if err = os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	contract := found.OwnerContract(workdir)
	if _, err = fgs.Emit(t.Context(), contract, cont.ID, []fgs.Operation{{Op: "goal.create", Key: "goal:check", Title: "Check", SuccessCriteria: "Checked"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		graph, err := server.fgs.Read(t.Context(), contract)
		if err != nil {
			t.Fatal(err)
		}
		if len(graph.Nodes) == 1 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Task Outbox was not received from relative runtime root")
}

func TestFGSReadGrantCannotCrossSessionBoundary(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), AuthToken: "operator-secret", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	a, err := server.sessions.Create(session.CreateRequest{Input: "First", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	b, err := server.sessions.Create(session.CreateRequest{Input: "Second", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := server.sessions.CreateContinuation(a.ID, "profile", "claude_code", session.RunnerHost, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := server.projectInterfaceGrants.IssueSession(t.Context(), projectinterface.IssueSessionGrantRequest{SessionID: a.ID, ContinuationID: cont.ID, RuntimeConfigVersionID: cont.RuntimeConfigID, RuntimeProfileID: cont.RuntimeProfileID, RuntimePluginID: cont.RuntimeProvider, Runner: string(cont.Runner), Access: projectinterface.GrantAccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	own := sessionBlackboardRequest(t, server, http.MethodGet, "/api/v2/sessions/"+a.ID+"/fgs", token, "", "")
	if own.status != http.StatusOK {
		t.Fatalf("own read: %d %s", own.status, own.body)
	}
	other := sessionBlackboardRequest(t, server, http.MethodGet, "/api/v2/sessions/"+b.ID+"/fgs", token, "", "")
	if other.status != http.StatusForbidden {
		t.Fatalf("cross read: %d %s", other.status, other.body)
	}
}

func TestFGSSessionRejectsLegacyGraphWrites(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "test.db"), SessionRoot: filepath.Join(root, "sessions"), RuntimeRoot: filepath.Join(root, "runs"), AuthToken: "operator-secret", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	found, err := server.sessions.Create(session.CreateRequest{Input: "FGS", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	response := sessionBlackboardRequest(t, server, http.MethodPost, "/api/v2/sessions/"+found.ID+"/blackboard/changes", "operator-secret", "legacy-write", `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"fact:legacy","type":"fact","record":{"category":"note","summary":"Legacy","confidence":"tentative"}}]}`)
	if response.status != http.StatusConflict {
		t.Fatalf("legacy write: %d %s", response.status, response.body)
	}
}

func TestFGSOutboxAcceptedDuringRuntimeAndReadableByOperator(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), AuthToken: "operator-secret", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	found, err := server.sessions.Create(session.CreateRequest{Input: "Check", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, "profile-1", "claude_code", session.RunnerSandbox, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fgs.Emit(t.Context(), found.OwnerContract(), continuation.ID, []fgs.Operation{{Op: "goal.create", Key: "goal:check", Title: "Check", SuccessCriteria: "Checked"}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v2/sessions/" + found.ID + "/fgs"
	denied := sessionBlackboardRequest(t, server, http.MethodGet, base, "wrong", "", "")
	if denied.status != http.StatusForbidden && denied.status != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", denied.status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		read := sessionBlackboardRequest(t, server, http.MethodGet, base, "operator-secret", "", "")
		if read.status != http.StatusOK {
			t.Fatalf("read: %d %s", read.status, read.body)
		}
		var graph fgs.Graph
		if err = json.Unmarshal(read.body, &graph); err != nil {
			t.Fatal(err)
		}
		if len(graph.Nodes) == 1 && graph.Nodes[0].Key == "goal:check" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("FGS was not accepted while the continuation was open")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
