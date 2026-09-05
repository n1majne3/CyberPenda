package daemon

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"pentest/internal/fgs"
	"pentest/internal/session"
	"pentest/internal/projectinterface"
	"testing"
	"time"
)

func TestFGSReadGrantCannotCrossSessionBoundary(t *testing.T) {
 root:=t.TempDir()
 server,err:=NewServer(Config{Version:"test",DBPath:filepath.Join(root,"test.db"),RuntimeRoot:filepath.Join(root,"runs"),SessionRoot:filepath.Join(root,"sessions"),AuthToken:"operator-secret",DisableBuiltinSkills:true})
 if err!=nil {t.Fatal(err)};defer server.Close()
 a,err:=server.sessions.Create(session.CreateRequest{Input:"First",BlackboardMode:session.BlackboardModeWorkingGraph});if err!=nil {t.Fatal(err)}
 b,err:=server.sessions.Create(session.CreateRequest{Input:"Second",BlackboardMode:session.BlackboardModeWorkingGraph});if err!=nil {t.Fatal(err)}
 cont,err:=server.sessions.CreateContinuation(a.ID,"profile","claude_code",session.RunnerHost,map[string]any{});if err!=nil {t.Fatal(err)}
 token,_,err:=server.projectInterfaceGrants.IssueSession(t.Context(),projectinterface.IssueSessionGrantRequest{SessionID:a.ID,ContinuationID:cont.ID,RuntimeConfigVersionID:cont.RuntimeConfigID,RuntimeProfileID:cont.RuntimeProfileID,RuntimePluginID:cont.RuntimeProvider,Runner:string(cont.Runner),Access:projectinterface.GrantAccessReadOnly});if err!=nil {t.Fatal(err)}
 own:=sessionBlackboardRequest(t,server,http.MethodGet,"/api/v2/sessions/"+a.ID+"/fgs",token,"","")
 if own.status!=http.StatusOK {t.Fatalf("own read: %d %s",own.status,own.body)}
 other:=sessionBlackboardRequest(t,server,http.MethodGet,"/api/v2/sessions/"+b.ID+"/fgs",token,"","")
 if other.status!=http.StatusForbidden {t.Fatalf("cross read: %d %s",other.status,other.body)}
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
