package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

const codexV2SymlinkOperatorSecret = "operator-secret-must-not-be-projected"

func TestCodexV2FreshResumeRejectsWorkdirSymlinkBeforeProjection(t *testing.T) {
	fixture := newCodexV2ResumeSecurityFixture(t)
	attackerRoot := t.TempDir()
	writeTestFile(t, filepath.Join(attackerRoot, "keep.txt"), []byte("attacker tree must remain unchanged"))

	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.RemoveAll(workdir); err != nil {
		t.Fatalf("remove task workdir: %v", err)
	}
	if err := os.Symlink(attackerRoot, workdir); err != nil {
		t.Fatalf("replace task workdir with symlink: %v", err)
	}

	assertCodexV2ResumeRejectedWithoutSideEffects(t, fixture, "/resume", attackerRoot)
}

func TestCodexV2NativeResumeRejectsProviderHomeSymlinkBeforeDiscoveryOrProjection(t *testing.T) {
	fixture := newCodexV2ResumeSecurityFixture(t)
	attackerRoot := t.TempDir()
	sessionPath := filepath.Join(attackerRoot, "sessions", "2026", "07", "17", "rollout-attacker.jsonl")
	writeTestFile(t, sessionPath, []byte(`{"type":"session_meta","payload":{"session_id":"sess-attacker"}}`+"\n"))

	providerHome := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "runtime-home", "codex")
	if err := os.RemoveAll(providerHome); err != nil {
		t.Fatalf("remove Codex provider home: %v", err)
	}
	if err := os.Symlink(attackerRoot, providerHome); err != nil {
		t.Fatalf("replace Codex provider home with symlink: %v", err)
	}

	assertCodexV2ResumeRejectedWithoutSideEffects(t, fixture, "/resume", attackerRoot)
}

func TestCodexV2ValidFreshResumeStillSucceeds(t *testing.T) {
	fixture := newCodexV2ResumeSecurityFixture(t)
	response := fixture.resume("/resume")
	if response.Code != http.StatusAccepted {
		t.Fatalf("valid Codex v2 handoff status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	waitForSecurityFixtureTaskStatus(t, fixture.server, fixture.task.ID, task.StatusCompleted)
	latest, err := fixture.server.tasks.LatestContinuation(fixture.task.ID)
	if err != nil {
		t.Fatalf("read valid resumed Continuation: %v", err)
	}
	if latest == nil || latest.Number != 2 {
		t.Fatalf("valid Codex v2 handoff latest Continuation = %#v, want number 2", latest)
	}
}

type codexV2ResumeSecurityFixture struct {
	server      *Server
	runtimeRoot string
	projectID   string
	profileID   string
	task        task.Task
}

func newCodexV2ResumeSecurityFixture(t *testing.T) codexV2ResumeSecurityFixture {
	t.Helper()
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runs")
	shim := filepath.Join(root, "codex-shim")
	writeTestFile(t, shim, []byte("#!/bin/sh\nprintf 'codex-v2-test\\n'\n"))
	if err := os.Chmod(shim, 0o700); err != nil {
		t.Fatalf("make Codex shim executable: %v", err)
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "security.db"), RuntimeRoot: runtimeRoot,
		AuthToken: codexV2SymlinkOperatorSecret, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start Codex v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Symlink security", "", project.Scope{Domains: []string{"security.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex security shim", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: shim, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Codex profile: %v", err)
	}

	body := fmt.Sprintf(`{"goal":"inspect security.example","runtime_profile_id":%q,"runner":"host","run_controls":{"host_activated":true}}`, profile.ID)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+codexV2SymlinkOperatorSecret)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create initial Codex v2 Task status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode initial Task: %v", err)
	}
	waitForSecurityFixtureTaskStatus(t, server, created.ID, task.StatusCompleted)
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatalf("read initial Task: %v", err)
	}
	return codexV2ResumeSecurityFixture{
		server: server, runtimeRoot: runtimeRoot, projectID: createdProject.ID, profileID: profile.ID, task: found,
	}
}

func (fixture codexV2ResumeSecurityFixture) resume(suffix string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+fixture.projectID+"/tasks/"+fixture.task.ID+suffix, nil)
	request.Header.Set("Authorization", "Bearer "+codexV2SymlinkOperatorSecret)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	return response
}

func assertCodexV2ResumeRejectedWithoutSideEffects(t *testing.T, fixture codexV2ResumeSecurityFixture, suffix, attackerRoot string) {
	t.Helper()
	beforeTree := snapshotTestTree(t, attackerRoot)
	beforeContinuation, err := fixture.server.tasks.LatestContinuation(fixture.task.ID)
	if err != nil || beforeContinuation == nil {
		t.Fatalf("read initial Continuation: %#v, %v", beforeContinuation, err)
	}

	first := fixture.resume(suffix)
	second := fixture.resume(suffix)
	if first.Code != http.StatusInternalServerError || second.Code != first.Code {
		t.Fatalf("invalid Codex v2 resumes status = %d then %d, want stable 500; bodies=%q / %q", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("invalid Codex v2 resume failure changed between attempts: %q then %q", first.Body.String(), second.Body.String())
	}

	afterTree := snapshotTestTree(t, attackerRoot)
	if !reflect.DeepEqual(afterTree, beforeTree) {
		t.Fatalf("invalid Codex v2 resume modified external symlink target\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
	afterContinuation, err := fixture.server.tasks.LatestContinuation(fixture.task.ID)
	if err != nil {
		t.Fatalf("read Continuation after rejected resume: %v", err)
	}
	if afterContinuation == nil || afterContinuation.ID != beforeContinuation.ID || afterContinuation.Number != beforeContinuation.Number {
		t.Fatalf("rejected resume created a Continuation: before=%#v after=%#v", beforeContinuation, afterContinuation)
	}
	found, err := fixture.server.tasks.Get(fixture.task.ID)
	if err != nil {
		t.Fatalf("read Task after rejected resume: %v", err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("rejected resume changed Task status to %q", found.Status)
	}

	visible := strings.Join(afterTree, "\n")
	for _, forbidden := range []string{
		codexV2SymlinkOperatorSecret, fixture.projectID, fixture.task.ID, fixture.profileID,
		"config.toml", "context.json", "auth.json", "PENTEST_MCP_URL", "/mcp?token=", "runtime_profile_id",
	} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("external symlink target received forbidden launch artifact %q: %s", forbidden, visible)
		}
	}
}

func snapshotTestTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot = append(snapshot, "dir:"+filepath.ToSlash(relative))
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot = append(snapshot, "file:"+filepath.ToSlash(relative)+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot test tree: %v", err)
	}
	sort.Strings(snapshot)
	return snapshot
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("prepare test file parent: %v", err)
	}
	if err := os.WriteFile(path, bytes.Clone(data), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func waitForSecurityFixtureTaskStatus(t *testing.T, server *Server, taskID string, want task.Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found, err := server.tasks.Get(taskID)
		if err == nil && found.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	found, err := server.tasks.Get(taskID)
	t.Fatalf("Task status = %#v, %v; want %q", found, err, want)
}
