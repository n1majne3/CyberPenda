package fgs_test

import (
	"context"
	"encoding/json"
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
func TestRealRuntimeFollowsProjectedFGSInstructions(t *testing.T) {
	if os.Getenv("CYBERPENDA_FGS_REAL_RUNTIME") != "1" {
		t.Skip("set CYBERPENDA_FGS_REAL_RUNTIME=1 for real Codex acceptance")
	}
	codex, err := exec.LookPath("codex")
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
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	cmd.Dir = layout.Workdir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "PENTEST_") && !strings.HasPrefix(env, "PATH=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"), "PENTEST_BLACKBOARD_PROTOCOL=fgs", "PENTEST_BLACKBOARD_MODE=working_graph", "PENTEST_SESSION_ID="+c.ID, "PENTEST_CONTINUATION_ID=real-1", "PENTEST_WORKING_GRAPH_ROOT="+c.Workdir, "PENTEST_API_URL="+api.URL, "PENTEST_INTERFACE_TOKEN=fgs-test-token")
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
		t.Fatalf("Runtime did not report a completed FGS result: goal=%v step=%v fact=%v", goal, step, fact)
	}
}
