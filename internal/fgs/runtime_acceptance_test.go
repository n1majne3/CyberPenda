package fgs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

// This opt-in test makes real model calls. It checks protocol compliance with
// an isolated arithmetic task, without ctf-orchestrator or any other Skill.
// Set CYBERPENDA_FGS_PI_AGENT_DIR, CYBERPENDA_FGS_PI_PROVIDER,
// CYBERPENDA_FGS_PI_MODEL, and PENTEST_SANDBOX_IMAGE to use sandbox Pi.
func TestRealRuntimeFollowsProjectedFGSInstructions(t *testing.T) {
	if os.Getenv("CYBERPENDA_FGS_REAL_RUNTIME") != "1" {
		t.Skip("set CYBERPENDA_FGS_REAL_RUNTIME=1 for real Runtime acceptance")
	}
	piAgentDir := os.Getenv("CYBERPENDA_FGS_PI_AGENT_DIR")
	program := "codex"
	if piAgentDir != "" {
		program = "docker"
	}
	codex, err := exec.LookPath(program)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := fixture(t)
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "fgs-acceptance", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	c := owner.NewSessionContract("session-fgs-acceptance", layout.Workdir)
	if _, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}, runner.ProjectionRequest{Owner: c, BlackboardProtocol: "fgs"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "pentestctl")
	build := exec.Command("go", "build", "-o", bin, "./cmd/pentestctl")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fgs-test-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/status") {
			value, err := s.Status(r.Context(), c)
			if err != nil {
				http.Error(w, "read failed", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		value, err := s.Read(r.Context(), c)
		if err != nil {
			http.Error(w, "read failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	if piAgentDir != "" {
		listener, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		_ = api.Listener.Close()
		api.Listener = listener
	}
	api.Start()
	defer api.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	receiverDone := make(chan struct{})
	go func() {
		defer close(receiverDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(c.Workdir, "graph", "outbox", "real-1")); err == nil {
					_, _ = s.ReceivePage(ctx, c, "real-1", 64)
				}
			}
		}
	}()
	defer func() { cancel(); <-receiverDone; s.CloseScans() }()
	cmd := exec.CommandContext(ctx, codex, "exec", "--ephemeral", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", "shell_environment_policy.inherit=\"all\"", "Compute 2 + 2 with a local command and complete this small task. Follow the work protocol in AGENTS.md, including accepted durable progress. Do not invoke Skills or delegate. Do not inspect credentials or contact external targets.")
	if piAgentDir != "" {
		image := os.Getenv("PENTEST_SANDBOX_IMAGE")
		if image == "" {
			t.Fatal("PENTEST_SANDBOX_IMAGE is required")
		}
		provider, model := os.Getenv("CYBERPENDA_FGS_PI_PROVIDER"), os.Getenv("CYBERPENDA_FGS_PI_MODEL")
		if provider == "" || model == "" {
			t.Fatal("Pi provider and model are required")
		}
		agentDir := t.TempDir()
		for _, name := range []string{"models.json", "auth.json"} {
			raw, err := os.ReadFile(filepath.Join(piAgentDir, name))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			if name == "models.json" {
				var providers map[string]json.RawMessage
				if err := json.Unmarshal(document["providers"], &providers); err != nil {
					t.Fatal(err)
				}
				raw, err = json.Marshal(map[string]any{"providers": map[string]json.RawMessage{provider: providers[provider]}})
			} else {
				raw, err = json.Marshal(map[string]json.RawMessage{provider: document[provider]})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"run", "--rm", "--add-host=host.docker.internal:host-gateway", "--mount", "type=bind,src=" + layout.Workdir + ",dst=/task/workdir", "--mount", "type=bind,src=" + agentDir + ",dst=/task/agent", "--workdir", "/task/workdir"}
		for _, value := range []string{"PI_CODING_AGENT_DIR=/task/agent", "PENTEST_BLACKBOARD_PROTOCOL=fgs", "PENTEST_BLACKBOARD_MODE=working_graph", "PENTEST_SESSION_ID=" + c.ID, "PENTEST_CONTINUATION_ID=real-1", "PENTEST_WORKING_GRAPH_ROOT=/task/workdir", fmt.Sprintf("PENTEST_API_URL=http://host.docker.internal:%d/api", api.Listener.Addr().(*net.TCPAddr).Port), "PENTEST_INTERFACE_TOKEN=fgs-test-token"} {
			args = append(args, "-e", value)
		}
		args = append(args, image, "pi", "--no-session", "--provider", provider, "--model", model, "--mode", "json", "--print", runner.FGSLaunchInstruction+"\n\nCompute 2 + 2 with a local command, then compute 3 + 3 with a local command. Report both results and finish. Do not invoke Skills or delegate. Do not inspect credentials or contact external targets.")
		cmd = exec.CommandContext(ctx, codex, args...)
	}
	cmd.Dir = layout.Workdir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "PENTEST_") && !strings.HasPrefix(env, "PATH=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"), "PENTEST_BLACKBOARD_PROTOCOL=fgs", "PENTEST_BLACKBOARD_MODE=working_graph", "PENTEST_SESSION_ID="+c.ID, "PENTEST_CONTINUATION_ID=real-1", "PENTEST_WORKING_GRAPH_ROOT="+c.Workdir, "PENTEST_API_URL="+api.URL, "PENTEST_INTERFACE_TOKEN=fgs-test-token")
	var runtimeOutput bytes.Buffer
	cmd.Stdout = &runtimeOutput
	// Do not put raw Runtime output into the test log: native configuration or
	// tool output can contain credentials unrelated to this acceptance task.
	if err = cmd.Run(); err != nil {
		t.Fatalf("real Runtime did not complete: %v", err)
	}
	if _, err = s.Drain(t.Context(), c, "real-1"); err != nil {
		t.Fatal(err)
	}
	graph, err := s.Read(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	goal, step, fact := false, false, false
	for _, node := range graph.Nodes {
		goal = goal || (node.Type == "goal" && node.State == "done" && len(node.Facts) > 0)
		step = step || (node.Type == "step" && node.State == "done" && len(node.Outputs) > 0)
		value := strings.ToLower(node.Summary + " " + node.Body)
		fact = fact || (node.Type == "fact" && (strings.Contains(value, "4") || strings.Contains(value, "four")))
	}
	if !goal || !step || !fact {
		toolCalls, reportingCalls := 0, 0
		stops := map[string]int{}
		for _, line := range bytes.Split(runtimeOutput.Bytes(), []byte("\n")) {
			var event struct {
				Type     string `json:"type"`
				ToolName string `json:"toolName"`
				Args     struct {
					Command string `json:"command"`
				} `json:"args"`
				Message struct {
					StopReason string `json:"stopReason"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if event.Type == "tool_execution_start" {
				toolCalls++
				if strings.Contains(event.Args.Command, "working-graph") {
					reportingCalls++
				}
			}
			if event.Type == "message_end" && event.Message.StopReason != "" {
				stops[event.Message.StopReason]++
			}
		}
		t.Fatalf("Runtime did not report a completed FGS result: goal=%v step=%v fact=%v tools=%d reporting=%d stop_reasons=%v", goal, step, fact, toolCalls, reportingCalls, stops)
	}
}
