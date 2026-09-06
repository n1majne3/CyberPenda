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
	"pentest/internal/projectinterface"
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
	server, err := NewServer(Config{Version: "smoke", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), AuthToken: "smoke-operator", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	p, err := server.projects.Create("Sandbox FGS smoke", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if p.BlackboardProtocol != "fgs" {
		t.Fatal("new Project must use FGS")
	}
	found, err := server.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "Check FGS delivery", Runner: task.RunnerSandbox, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := server.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, cont, err := server.tasks.CreateContinuationLaunchTx(t.Context(), tx, task.ContinuationLaunchRequest{
		TaskID: found.ID, ProjectID: p.ID, RuntimeProfileID: "smoke-profile", RuntimeProvider: "codex", Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"blackboard_protocol": "fgs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	token, _, err := server.projectInterfaceGrants.Issue(t.Context(), projectinterface.IssueGrantRequest{
		ProjectID: p.ID, TaskID: found.ID, ContinuationID: cont.ID,
		RuntimeConfigVersionID: cont.RuntimeConfigVersionID, RuntimeProfileID: cont.RuntimeProfileID,
		RuntimePluginID: cont.RuntimeProvider, Runner: string(cont.Runner), Access: projectinterface.GrantAccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = api.Serve(listener) }()
	defer api.Close()
	apiURL := fmt.Sprintf("http://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	workdir := filepath.Join(root, "runs", found.ID, "workdir")
	if err = os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	run := func(input string, command ...string) []byte {
		t.Helper()
		args := []string{"run", "--rm", "-i", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--add-host=host.docker.internal:host-gateway", "--mount", "type=bind,src=" + workdir + ",dst=/workspace", "--workdir", "/workspace"}
		env := []string{"PENTEST_BLACKBOARD_PROTOCOL=fgs", "PENTEST_BLACKBOARD_MODE=working_graph", "PENTEST_WORKING_GRAPH_ROOT=/workspace", "PENTEST_TASK_ID=" + found.ID, "PENTEST_PROJECT_ID=" + p.ID, "PENTEST_CONTINUATION_ID=" + cont.ID, "PENTEST_API_URL=" + apiURL, "PENTEST_INTERFACE_TOKEN=" + token}
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
	// Emit publishes to the mounted Outbox. Only the background receiver can
	// accept the update; this test never calls Apply or Drain.
	published := run(`{"operations":[
		{"op":"goal.create","key":"goal:smoke","title":"Check container delivery","success_criteria":"Read an accepted Fact from the container"},
		{"op":"step.create","key":"step:smoke","goal":"goal:smoke","action":"Publish from the container"},
		{"op":"fact.append","key":"fact:smoke","step":"step:smoke","summary":"Container Outbox reached the daemon","body":"Sandbox FGS smoke"},
		{"op":"step.transition","key":"step:smoke","from":"open","to":"done","outputs":["fact:smoke"]},
		{"op":"goal.transition","key":"goal:smoke","from":"open","to":"done","facts":["fact:smoke"],"summary":"Container delivery verified"}
	]}`, "pentestctl", "working-graph", "emit", "--input", "-")
	var emitted struct{ ID, State string }
	if err = json.Unmarshal(published, &emitted); err != nil || emitted.State != "published" || emitted.ID == "" {
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
