package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmokeRuntimeTasksLivePayloadsAcceptedByDaemon(t *testing.T) {
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		pythonBin, err = exec.LookPath("python")
		if err != nil {
			t.Skip("python not available")
		}
	}

	scriptPath := filepath.Join("..", "..", "scripts", "smoke-runtime-tasks-live.py")
	pyCode := `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("smoke", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
profiles = {}
for key, (name, provider, fields) in mod.build_smoke_profiles("https://open.bigmodel.cn/api/anthropic", "test-anthropic", "test-openai").items():
    profiles[key] = {"name": name, "provider": provider, "fields": fields}
out = {
    "project": mod.build_smoke_project_payload(),
    "profiles": profiles,
    "sandbox_task": mod.build_smoke_task_payload("smoke goal", "__PROFILE_ID__", "sandbox"),
    "host_task": mod.build_smoke_task_payload("smoke goal", "__PROFILE_ID__", "host"),
}
print(json.dumps(out))
`
	raw, err := exec.Command(pythonBin, "-c", pyCode, scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("extract smoke payloads: %v\n%s", err, string(raw))
	}

	var payloads struct {
		Project     json.RawMessage            `json:"project"`
		Profiles    map[string]json.RawMessage `json:"profiles"`
		SandboxTask json.RawMessage            `json:"sandbox_task"`
		HostTask    json.RawMessage            `json:"host_task"`
	}
	if err := json.Unmarshal(raw, &payloads); err != nil {
		t.Fatalf("unmarshal extracted payloads: %v", err)
	}

	server := newDaemon(t)

	projReq := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(payloads.Project))
	projReq.Header.Set("Content-Type", "application/json")
	projResp := httptest.NewRecorder()
	server.ServeHTTP(projResp, projReq)
	if projResp.Code != http.StatusCreated {
		t.Fatalf("POST /api/projects returned %d: %s", projResp.Code, projResp.Body.String())
	}
	var createdProject struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(projResp.Body.Bytes(), &createdProject); err != nil {
		t.Fatalf("decode project response: %v", err)
	}

	for key, body := range payloads.Profiles {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusCreated {
			t.Fatalf("POST /api/runtime-profiles (%s) returned %d: %s", key, resp.Code, resp.Body.String())
		}
		var createdProfile struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &createdProfile); err != nil {
			t.Fatalf("decode profile response (%s): %v", key, err)
		}
		patchReq := httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+createdProfile.ID, bytes.NewReader(body))
		patchReq.Header.Set("Content-Type", "application/json")
		patchResp := httptest.NewRecorder()
		server.ServeHTTP(patchResp, patchReq)
		if patchResp.Code != http.StatusOK {
			t.Fatalf("PATCH /api/runtime-profiles/%s (%s) returned %d: %s", createdProfile.ID, key, patchResp.Code, patchResp.Body.String())
		}
	}

	fakeProfileID := createRuntimeProfile(t, server, `{"name":"Fake Smoke","provider":"fake"}`)
	for name, rawTask := range map[string]json.RawMessage{
		"sandbox": payloads.SandboxTask,
		"host":    payloads.HostTask,
	} {
		body := strings.ReplaceAll(string(rawTask), "__PROFILE_ID__", fakeProfileID)
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusCreated {
			t.Fatalf("POST /api/projects/%s/tasks (%s) returned %d: %s", createdProject.ID, name, resp.Code, resp.Body.String())
		}
	}
}
