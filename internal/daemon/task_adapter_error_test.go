package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/task"
)

func TestBuildTaskAdapterReturnsActionableErrorForClaudeGLMProfile(t *testing.T) {
	server := newAdapterErrorTestServer(t)

	profile := createAdapterErrorTestProfile(t, server)
	proj := createAdapterErrorTestProject(t, server)
	created := createAdapterErrorTestTask(t, server, proj.ID, profile.ID)

	_, _, err := server.buildTaskAdapter(created, "")
	if err == nil {
		t.Fatal("expected buildTaskAdapter to fail")
	}
	if !strings.Contains(err.Error(), "GLM_API_KEY") {
		t.Fatalf("expected actionable API key error, got %v", err)
	}
}

func TestResumeTaskSurfacesAdapterPrepareError(t *testing.T) {
	server := newSkillBundleAdapterErrorServer(t)
	profile := createAdapterErrorTestProfile(t, server)
	proj := createAdapterErrorTestProject(t, server)
	created := createAdapterErrorTestTask(t, server, proj.ID, profile.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/tasks/"+created.ID+"/resume/handoff", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(payload.Error, "invalid skill") {
		t.Fatalf("expected actionable skill bundle error in response, got %q", payload.Error)
	}
}

func TestCreateTaskSurfacesSkillBundleErrorViaPreflight(t *testing.T) {
	server := newSkillBundleAdapterErrorServer(t)
	profile := createAdapterErrorTestProfile(t, server)
	proj := createAdapterErrorTestProject(t, server)

	body := []byte(`{
		"goal":"enumerate example.com",
		"runtime_profile_id":` + quoteJSON(profile.ID) + `,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+proj.ID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Error     string `json:"error"`
		Preflight struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"checks"`
		} `json:"preflight"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error != "preflight failed" {
		t.Fatalf("expected preflight failed, got %q", payload.Error)
	}
	var skillsDetail string
	for _, check := range payload.Preflight.Checks {
		if check.Name == "skills" && check.Status == "fail" {
			skillsDetail = check.Detail
			break
		}
	}
	if !strings.Contains(skillsDetail, "invalid skill") {
		t.Fatalf("expected actionable skill bundle error in preflight, got %q", skillsDetail)
	}
}

func TestBuildTaskAdapterAppliesHostProxyOnlySandboxNetworkFromRunControls(t *testing.T) {
	server, err := NewServer(Config{
		Version:              "test",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
		SandboxImage:         "pentest-sandbox:latest",
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	profile, err := server.CreateLocalRuntimeProfile("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model:         "gpt-5",
		DefaultRunner: "sandbox",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	proj, err := server.projects.Create("test", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "test http://localhost:3000",
		RuntimeProfileID: profile.ID,
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, runtimeConfig, err := server.buildTaskAdapter(created, "")
	if err != nil {
		t.Fatalf("build adapter: %v", err)
	}
	launchCommand, ok := runtimeConfig["launch_command"].(map[string]any)
	if !ok {
		t.Fatalf("expected launch_command in runtime config, got %#v", runtimeConfig["launch_command"])
	}
	args, ok := launchCommand["args"].([]string)
	if !ok {
		t.Fatalf("expected launch command args, got %#v", launchCommand["args"])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network pentest-host-proxy-only") {
		t.Fatalf("expected host-proxy-only sandbox network in launch args, got %v", args)
	}
}

func newAdapterErrorTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(Config{
		Version:              "test",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
		SandboxImage:         "pentest-sandbox:latest",
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	if _, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name:      "GLM",
		BaseURL:   "https://open.bigmodel.cn/api/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"glm-5.2"}, DefaultModel: "glm-5.2"},
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "test-token")
	return server
}

func newSkillBundleAdapterErrorServer(t *testing.T) *Server {
	t.Helper()
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	server, err := NewServer(Config{
		Version:              "test",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
		SkillsRoot:           skillsRoot,
		SandboxImage:         "pentest-sandbox:latest",
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	if _, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name:      "GLM",
		BaseURL:   "https://open.bigmodel.cn/api/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"glm-5.2"}, DefaultModel: "glm-5.2"},
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv("GLM_API_KEY", "sk-test")
	published, err := server.skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files:    map[string]string{"SKILL.md": "# Recon"},
	})
	if err != nil {
		t.Fatalf("publish skill: %v", err)
	}
	if err := os.RemoveAll(published.BundlePath); err != nil {
		t.Fatalf("remove bundle: %v", err)
	}
	return server
}

func createAdapterErrorTestProfile(t *testing.T, server *Server) runtimeprofile.Profile {
	t.Helper()
	profile, err := server.CreateLocalRuntimeProfile("Juice Shop Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: "glm",
		ModelOverride:   "glm-5.2",
		CustomArgs:      []string{"-p", "--dangerously-skip-permissions", "--permission-mode", "bypassPermissions"},
		DefaultRunner:   "sandbox",
		SandboxImage:    "pentest-sandbox:latest",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return profile
}

func createAdapterErrorTestProject(t *testing.T, server *Server) project.Project {
	t.Helper()
	proj, err := server.projects.Create("test", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return proj
}

func createAdapterErrorTestTask(t *testing.T, server *Server, projectID, profileID string) task.Task {
	t.Helper()
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID:        projectID,
		Goal:             "enumerate example.com",
		RuntimeProfileID: profileID,
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return created
}

func quoteJSON(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
