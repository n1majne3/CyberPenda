package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/challengeworkflow"
	"pentest/internal/project"
	"pentest/internal/task"
)

type challengePlatformFixture struct{}

func (challengePlatformFixture) Claim(_ context.Context, request challengeworkflow.PlatformClaimRequest) (challengeworkflow.PlatformClaimResponse, error) {
	return challengeworkflow.PlatformClaimResponse{ExternalAttemptID: "attempt-42", ChallengeID: request.ChallengeID, Summary: "claimed", Rating: 2100}, nil
}
func (challengePlatformFixture) Submit(_ context.Context, request challengeworkflow.PlatformSubmitRequest) (challengeworkflow.PlatformSubmitResponse, error) {
	return challengeworkflow.PlatformSubmitResponse{Accepted: request.Candidate == "FLAG{ok}", Summary: "checked", Rating: 2113}, nil
}
func (challengePlatformFixture) Abandon(context.Context, challengeworkflow.PlatformAbandonRequest) (challengeworkflow.PlatformAbandonResponse, error) {
	return challengeworkflow.PlatformAbandonResponse{Summary: "abandoned"}, nil
}
func (challengePlatformFixture) Finalize(context.Context, challengeworkflow.PlatformFinalizeRequest) (challengeworkflow.PlatformFinalizeResponse, error) {
	return challengeworkflow.PlatformFinalizeResponse{Summary: "finalized"}, nil
}

func TestChallengeWorkflowHTTPAndFinishReadiness(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{DBPath: filepath.Join(root, "db.sqlite"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true, ChallengePlatforms: map[string]challengeworkflow.PlatformAdapter{"arena": challengePlatformFixture{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	proj, err := server.projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypeCTFChallenge, Goal: "solve", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.CreateContinuation(created.ID, "profile", "codex", task.RunnerSandbox); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeRoot, created.ID, "workdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	accessURL, err := url.Parse(server.GeneratedOperatorAccessURL())
	if err != nil || accessURL.Query().Get("token") == "" {
		t.Fatalf("generated operator access URL = %q, error=%v", server.GeneratedOperatorAccessURL(), err)
	}
	operatorToken := accessURL.Query().Get("token")
	disabled, err := server.tasks.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypeCTFChallenge, Goal: "keep Challenge output operator-only",
		Runner: task.RunnerHost,
		RunControls: task.RunControls{
			HostActivated: true, BlackboardConclusionMode: task.BlackboardConclusionModeDisabled,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.CreateContinuation(disabled.ID, "profile", "codex", task.RunnerHost); err != nil {
		t.Fatal(err)
	}
	disabledClaim := serveChallenge(
		t, server, http.MethodPost,
		"/api/projects/"+proj.ID+"/tasks/"+disabled.ID+"/challenges/claim",
		operatorToken, `{"platform":"arena","operation_id":"disabled-claim","challenge_id":"3121"}`,
	)
	if disabledClaim.Code != http.StatusConflict || !strings.Contains(disabledClaim.Body.String(), "Disabled") {
		t.Fatalf("Disabled Challenge claim = %d %s, want conflict", disabledClaim.Code, disabledClaim.Body.String())
	}
	disabledSnapshot := serveChallenge(
		t, server, http.MethodGet, "/api/v2/projects/"+proj.ID+"/blackboard/snapshot", operatorToken, "",
	)
	if disabledSnapshot.Code != http.StatusOK || strings.Contains(disabledSnapshot.Body.String(), "attempt-42") {
		t.Fatalf("Disabled Challenge action changed public Blackboard: %d %s", disabledSnapshot.Code, disabledSnapshot.Body.String())
	}

	claimPath := "/api/projects/" + proj.ID + "/tasks/" + created.ID + "/challenges/claim"
	denied := serveChallenge(t, server, http.MethodPost, claimPath, "", `{"platform":"arena","operation_id":"tokenless-claim","challenge_id":"3121"}`)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless Challenge claim = %d %s", denied.Code, denied.Body.String())
	}

	claim := serveChallenge(t, server, http.MethodPost, claimPath, operatorToken, `{"platform":"arena","operation_id":"claim-1","challenge_id":"3121"}`)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", claim.Code, claim.Body.String())
	}
	readiness := serveChallenge(t, server, http.MethodGet, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/finish-readiness", operatorToken, "")
	var blocked struct {
		Ready bool `json:"ready_to_finish"`
	}
	if err := json.NewDecoder(readiness.Body).Decode(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.Ready {
		t.Fatal("open Challenge Attempt must block Finish")
	}
	finishBlocked := serveChallenge(t, server, http.MethodPost, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/finish", operatorToken, `{}`)
	if finishBlocked.Code != http.StatusConflict {
		t.Fatalf("Finish with blockers = %d %s", finishBlocked.Code, finishBlocked.Body.String())
	}
	var conflict struct {
		FinishReadiness struct {
			Blockers []struct {
				Code string `json:"code"`
			} `json:"blockers"`
		} `json:"finish_readiness"`
	}
	if err := json.NewDecoder(finishBlocked.Body).Decode(&conflict); err != nil {
		t.Fatal(err)
	}
	if len(conflict.FinishReadiness.Blockers) == 0 {
		t.Fatal("Finish conflict did not return Finish Readiness blockers")
	}

	submit := serveChallenge(t, server, http.MethodPost, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/challenges/submit", operatorToken, `{"platform":"arena","operation_id":"submit-1","external_attempt_id":"attempt-42","candidate":"FLAG{ok}"}`)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", submit.Code, submit.Body.String())
	}
	finalize := serveChallenge(t, server, http.MethodPost, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/challenges/finalize", operatorToken, `{"platform":"arena","operation_id":"finalize-1","external_attempt_id":"attempt-42"}`)
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize = %d %s", finalize.Code, finalize.Body.String())
	}
	readiness = serveChallenge(t, server, http.MethodGet, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/finish-readiness", operatorToken, "")
	var ready struct {
		Ready    bool  `json:"ready_to_finish"`
		Blockers []any `json:"blockers"`
	}
	if err := json.NewDecoder(readiness.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready {
		t.Fatalf("expected ready, blockers %#v", ready.Blockers)
	}
}

func serveChallenge(t *testing.T, server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
