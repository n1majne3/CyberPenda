package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/fgs"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// This opt-in smoke uses a real container and the production daemon receiver.
// Setup creates an owner and a Continuation without starting a model Runtime.
func TestSandboxFGSOutboxLive(t *testing.T) {
	if os.Getenv("PENTEST_SANDBOX_FGS_SMOKE") != "1" {
		t.Skip("run make smoke-sandbox-fgs for container acceptance")
	}
	image := os.Getenv("PENTEST_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("PENTEST_SANDBOX_IMAGE is required")
	}
	cli := os.Getenv("PENTEST_CONTAINER_CLI")
	if cli == "" {
		cli = "docker"
	}
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRuns, err := filepath.Rel(cwd, filepath.Join(root, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	config := Config{ListenAddr: listener.Addr().String(), SandboxImage: image, Version: "smoke", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: relativeRuns, SessionRoot: filepath.Join(root, "sessions"), AuthToken: "smoke-operator", DisableBuiltinSkills: true}
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	p, err := server.projects.Create("Sandbox FGS smoke", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if p.BlackboardProtocol != "fgs" {
		t.Fatal("new Project must use FGS")
	}
	// Reproduce upgrading an existing Project before starting a new Task.
	if _, err = server.db.Exec(`UPDATE projects SET blackboard_protocol='legacy' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = server.db.Exec(`DELETE FROM schema_migrations WHERE version=77`); err != nil {
		t.Fatal(err)
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	server = reopened
	profile, err := server.profiles.Create("Smoke Runtime", runtimeprofile.ProviderPi, runtimeprofile.Fields{Model: "smoke-model"})
	if err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "Check FGS delivery", RuntimeProfileID: profile.ID, RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox), Runner: task.RunnerSandbox, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}})
	if err != nil {
		t.Fatal(err)
	}
	if found.BlackboardProtocol != "fgs" {
		t.Fatal("new Task in upgraded Project must use FGS")
	}
	plan, err := server.buildTaskLaunchPlan(found, found.Goal, "", "", "high")
	if err != nil {
		t.Fatal(err)
	}
	cont, bound, err := server.prepareBlackboardV2ContinuationLaunch(found, plan, found.Goal)
	if err != nil {
		t.Fatal(err)
	}
	createArgs, ok := runtime.DockerSandboxCreateArgs(bound.Adapter)
	if !ok {
		t.Fatal("missing sandbox adapter")
	}
	processEnv := map[string]string{}
	for i := 0; i+1 < len(createArgs); i++ {
		if createArgs[i] == "-e" {
			key, value, ok := strings.Cut(createArgs[i+1], "=")
			if ok {
				processEnv[key] = value
			}
		}
	}
	token := processEnv["PENTEST_INTERFACE_TOKEN"]
	if token == "" || processEnv["PENTEST_CONTINUATION_ID"] != cont.ID {
		t.Fatal("bound Runtime is missing its Continuation capability or identity")
	}
	api := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = api.Serve(listener) }()
	defer api.Close()
	workdir := filepath.Join(root, "runs", found.ID, "workdir")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run := func(input string, command ...string) []byte {
		t.Helper()
		args := []string{"run", "--rm", "-i", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--add-host=host.docker.internal:host-gateway", "--mount", "type=bind,src=" + workdir + ",dst=/task/workdir", "--workdir", "/task/workdir"}
		var env []string
		for key, value := range processEnv {
			if strings.HasPrefix(key, "PENTEST_") {
				env = append(env, key+"="+value)
			}
		}
		for _, value := range env {
			args = append(args, "-e", strings.SplitN(value, "=", 2)[0])
		}
		args = append(args, image)
		args = append(args, command...)
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("container command %v failed: %v\n%s", command, err, strings.ReplaceAll(string(output), token, "[redacted]"))
		}
		return output
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		instructions := run("", "cat", name)
		if !strings.Contains(string(instructions), "## FGS work protocol") || strings.Contains(string(instructions), "Exploration flows through an open Attempt") {
			t.Fatalf("container did not receive FGS instructions in %s", name)
		}
	}
	if schema := run("", "cat", ".pentest/fgs-input.schema.json"); !json.Valid(schema) {
		t.Fatal("container did not receive valid FGS schema")
	}
	// Emit publishes to the mounted Outbox. Only the background receiver can
	// accept the update; this test never calls Apply or Drain.
	published := run(`{"operations":[
		{"op":"goal.create","key":"goal:smoke","title":"Check container delivery","success_criteria":"Read an accepted Fact from the container"},
		{"op":"step.create","key":"step:smoke","goal":"goal:smoke","action":"Publish from the container"},
		{"op":"fact.append","key":"fact:smoke","step":"step:smoke","summary":"Container Outbox reached the daemon","body":"Sandbox FGS smoke"},
		{"op":"step.transition","key":"step:smoke","from":"open","to":"done","outputs":["fact:smoke"]},
		{"op":"goal.transition","key":"goal:smoke","from":"open","to":"done","facts":["fact:smoke"],"summary":"Container delivery verified"}
	]}`, "pentestctl", "working-graph", "emit", "--input", "-")
	// Emit waits up to 5s for the receipt by default, so the response is
	// "published" only when acceptance outlasts that window; a fast daemon
	// receiver returns the settled receipt with state "applied".
	var emitted struct{ ID, State string }
	if err = json.Unmarshal(published, &emitted); err != nil || (emitted.State != "published" && emitted.State != "applied") || emitted.ID == "" {
		t.Fatalf("invalid publish response: %s (%v)", published, err)
	}
	for {
		var status fgs.Status
		output := run("", "pentestctl", "working-graph", "status")
		if err = json.Unmarshal(output, &status); err != nil {
			t.Fatalf("invalid status: %s (%v)", output, err)
		}
		if status.ActionRequired != 0 {
			t.Fatalf("FGS update rejected: %s", output)
		}
		if len(status.Receipts) == 1 && status.Receipts[0].ID == emitted.ID && status.Receipts[0].ContinuationID == cont.ID && status.Receipts[0].State == "applied" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("FGS receipt was not applied: %s", output)
		case <-time.After(250 * time.Millisecond):
		}
	}
	var graph fgs.Graph
	output := run("", "pentestctl", "working-graph", "read")
	if err = json.Unmarshal(output, &graph); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]fgs.Node{}
	for _, node := range graph.Nodes {
		if node.OwnerID != found.ID || node.ContinuationID != cont.ID {
			t.Fatalf("wrong accepted provenance: %+v", node)
		}
		nodes[node.Key] = node
	}
	if graph.Revision != 1 || len(nodes) != 3 || nodes["goal:smoke"].State != "done" || nodes["step:smoke"].State != "done" || nodes["fact:smoke"].Body != "Sandbox FGS smoke" {
		t.Fatalf("wrong accepted graph: %s", output)
	}
	t.Log("Sandbox Outbox publication, background acceptance, and scoped FGS reads passed")
}
