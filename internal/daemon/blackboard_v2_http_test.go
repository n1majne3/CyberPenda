package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestBlackboardV2HTTPRoutesServeAllSixCLICommandsWithTrustedContinuation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "v2-http.db")
	runtimeRoot := filepath.Join(root, "runs")
	const operatorToken = "operator-daemon-secret"
	server, err := NewServer(Config{
		Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot,
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.blackboardV2 == nil || server.blackboardV2Continuity == nil || server.projectInterfaceGrants == nil {
		t.Fatal("blackboard v2 HTTP surface is not wired")
	}

	createdProject, err := server.projects.Create("V2 HTTP", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "exercise /api/v2", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch Continuation: %v", err)
	}
	if strings.TrimSpace(launch.Token) == "" {
		t.Fatal("Continuation launch omitted opaque capability token")
	}

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v2/projects/" + createdProject.ID

	// Operator seed entity (no Continuation required).
	seedBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"entity:http","type":"entity","record":{"status":"active","kind":"host","name":"HTTP host","scope_status":"in_scope"}}]}`
	seeded := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", operatorToken, "operator", "http-seed", seedBody)
	if !bytes.Contains(seeded, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("operator seed result = %s", seeded)
	}

	// Operator read + history.
	read := mustV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http", operatorToken, "operator", "", "")
	if !bytes.Contains(read, []byte(`"schema":"blackboard-record/v2"`)) || !bytes.Contains(read, []byte(`"key":"entity:http"`)) {
		t.Fatalf("operator read = %s", read)
	}
	history := mustV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http/history?limit=1", operatorToken, "operator", "", "")
	if !bytes.Contains(history, []byte(`"schema":"semantic-history/v2"`)) {
		t.Fatalf("operator history = %s", history)
	}

	// Trusted Continuation owns open work it later checkpoints/finishes.
	workBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:http","type":"objective","record":{"status":"open","objective":"HTTP proof"}},{"op":"create","key":"attempt:http","type":"attempt","record":{"status":"open","summary":"Collect HTTP proof"}},{"op":"relate","from":"attempt:http","relation":"tests","to":"objective:http"}]}`
	worked := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", launch.Token, "", "http-work", workBody)
	if !bytes.Contains(worked, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("continuation work result = %s", worked)
	}

	// Trusted Continuation evidence retain, checkpoint, finish.
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("http proof\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	retainBody := `{"key":"evidence:http","attempt":"attempt:http","source_path":"proof.txt","artifact_type":"text","summary":"HTTP retained proof","media_type":"text/plain","links":[["about","entity:http"]]}`
	retained := mustV2HTTP(t, http.MethodPost, base+"/blackboard/evidence:retain", launch.Token, "", "http-evidence", retainBody)
	if !bytes.Contains(retained, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("evidence retain = %s", retained)
	}
	checkpointBody := `{"version":1,"summary":"Proof retained"}`
	checkpointed := mustV2HTTP(t, http.MethodPost, base+"/blackboard/attempts/attempt:http:checkpoint", launch.Token, "", "http-checkpoint", checkpointBody)
	if !bytes.Contains(checkpointed, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("checkpoint = %s", checkpointed)
	}
	terminalBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"transition","key":"attempt:http","version":2,"status":"succeeded","summary":"HTTP proof retained successfully"}]}`
	if body := mustV2HTTP(t, http.MethodPost, base+"/blackboard/changes", launch.Token, "", "http-terminal", terminalBody); !bytes.Contains(body, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("terminal change = %s", body)
	}
	finished := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "http-finish", `{}`)
	if !bytes.Contains(finished, []byte(`"schema":"continuation-finish/v2"`)) || bytes.Contains(finished, []byte(`"sync"`)) {
		t.Fatalf("finish = %s", finished)
	}
	// Exact Finish replay keeps the same compact result and never attaches sync.
	replayed := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "http-finish", `{}`)
	if !bytes.Equal(finished, replayed) {
		t.Fatalf("finish replay drifted\nfirst=%s\nreplay=%s", finished, replayed)
	}
	// Closed Continuation loses trusted read/history.
	closedRead := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http", launch.Token, "", "", "")
	if closedRead.status != http.StatusGone || !bytes.Contains(closedRead.body, []byte(`"code":"closed_continuation"`)) {
		t.Fatalf("closed read = %d %s", closedRead.status, closedRead.body)
	}
	closedHistory := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/entity:http/history", launch.Token, "", "", "")
	if closedHistory.status != http.StatusGone || !bytes.Contains(closedHistory.body, []byte(`"code":"closed_continuation"`)) {
		t.Fatalf("closed history = %d %s", closedHistory.status, closedHistory.body)
	}

	// Foreign Project path with owner capability is denied before service access.
	foreign, err := server.projects.Create("Foreign HTTP", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	foreignBase := httpServer.URL + "/api/v2/projects/" + foreign.ID
	foreignDenied := doV2HTTP(t, http.MethodGet, foreignBase+"/blackboard/records/entity:http", launch.Token, "", "", "")
	if foreignDenied.status != http.StatusForbidden && foreignDenied.status != http.StatusUnauthorized {
		t.Fatalf("foreign Project capability status = %d %s", foreignDenied.status, foreignDenied.body)
	}
	if !bytes.Contains(foreignDenied.body, []byte(`"code":"authority_denied"`)) {
		t.Fatalf("foreign Project capability body = %s", foreignDenied.body)
	}

	// Snapshot route is registered and operator-reachable.
	snapshot := mustV2HTTP(t, http.MethodGet, base+"/blackboard/snapshot", operatorToken, "operator", "", "")
	if !bytes.Contains(snapshot, []byte(`"schema":"runtime-blackboard/v2"`)) {
		t.Fatalf("snapshot = %s", snapshot)
	}
}

func TestBlackboardV2HTTPRejectsQueryCredentialAndMissingIdempotency(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "v2-auth.db"), RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: "operator-secret", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Auth", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	url := httpServer.URL + "/api/v2/projects/" + createdProject.ID + "/blackboard/changes?token=operator-secret"
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"schema":"semantic-change-batch/v2","changes":[]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer operator-secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "query-cred")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("query credential request: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK || !bytes.Contains(body, []byte(`"code":"invalid_schema"`)) {
		t.Fatalf("query credential accepted: %d %s", response.StatusCode, body)
	}

	missingKey := doV2HTTP(t, http.MethodPost, httpServer.URL+"/api/v2/projects/"+createdProject.ID+"/blackboard/changes", "operator-secret", "operator", "", `{"schema":"semantic-change-batch/v2","changes":[]}`)
	if missingKey.status != http.StatusUnprocessableEntity || !bytes.Contains(missingKey.body, []byte(`"code":"semantic_validation"`)) {
		t.Fatalf("missing idempotency = %d %s", missingKey.status, missingKey.body)
	}
}

type v2HTTPResult struct {
	status int
	body   []byte
}

func mustV2HTTP(t *testing.T, method, url, token, actor, idempotencyKey, body string) []byte {
	t.Helper()
	result := doV2HTTP(t, method, url, token, actor, idempotencyKey, body)
	if result.status < 200 || result.status >= 300 {
		t.Fatalf("%s %s => %d %s", method, url, result.status, result.body)
	}
	return result.body
}

func doV2HTTP(t *testing.T, method, url, token, actor, idempotencyKey, body string) v2HTTPResult {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if actor != "" {
		request.Header.Set("CyberPenda-Actor-ID", actor)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return v2HTTPResult{status: response.StatusCode, body: raw}
}

// Ensure typed JSON still round-trips through the daemon success path for Finish.
func TestBlackboardV2HTTPFinishResultIsClosedTypedDTO(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "finish-dto.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Finish DTO", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "finish dto", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: createdTask.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v2/projects/" + createdProject.ID
	finished := mustV2HTTP(t, http.MethodPost, base+"/continuation:finish", launch.Token, "", "finish-dto", `{}`)
	var result blackboardv2.FinishContinuationResult
	if err := json.Unmarshal(finished, &result); err != nil {
		t.Fatalf("decode Finish DTO: %v %s", err, finished)
	}
	if result.Schema != "continuation-finish/v2" || result.Status != "finished" || result.WorkingSnapshot.Path != ".pentest/blackboard.json" {
		t.Fatalf("Finish DTO = %#v", result)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(finished, &fields); err != nil {
		t.Fatalf("raw fields: %v", err)
	}
	for field := range fields {
		switch field {
		case "schema", "status", "revision", "working_snapshot":
		default:
			t.Fatalf("Finish success DTO leaked field %q: %s", field, finished)
		}
	}
}
