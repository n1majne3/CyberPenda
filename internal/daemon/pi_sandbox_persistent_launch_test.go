package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// Persistent Pi sandbox sessions rewrite docker create argv at the bare "pi"
// token. The one-shot bootstrap wrapper (sh -c) must not be applied when a
// ProviderSessionFactory is installed, or rewrite fails closed before Docker
// starts.
func TestBuildTaskLaunchPlanPiSandboxKeepsBarePiWhenFactoryPresent(t *testing.T) {
	root := t.TempDir()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		Docker: newProductionFactoryDocker(),
	})
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pi-bare.db"),
		RuntimeRoot: filepath.Join(root, "runs"), SandboxImage: "sandbox:test",
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("pi bare", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		BinaryPath: "pi", Model: "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "probe example.test",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "high")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	_, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare continuation: %v", err)
	}
	createArgs, ok := runtime.DockerSandboxCreateArgs(bound.Adapter)
	if !ok {
		t.Fatal("expected docker sandbox adapter")
	}
	imageIdx := -1
	for i, arg := range createArgs {
		if arg == "sandbox:test" {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 || imageIdx+1 >= len(createArgs) {
		t.Fatalf("sandbox image not found in create args: %#v", createArgs)
	}
	runtimeArgv := createArgs[imageIdx+1:]
	if len(runtimeArgv) == 0 || filepath.Base(runtimeArgv[0]) != "pi" {
		t.Fatalf("expected bare pi image command for persistent rewrite, got %#v\nfull=%#v", runtimeArgv, createArgs)
	}
	if runtimeArgv[0] == "sh" {
		t.Fatalf("pi sandbox still used bootstrap wrapper: %#v", runtimeArgv)
	}

	// Prove the rewrite seam used by the factory succeeds on these args.
	rewritten, err := runtime.RewriteDockerCreateCommand(createArgs, "pi", []string{
		"/usr/local/bin/pentest-provider-bridge", "--provider", "pi", "--", "pi", "--mode", "rpc",
	})
	if err != nil {
		t.Fatalf("rewrite pi create args: %v\nargs=%#v", err, createArgs)
	}
	if !strings.Contains(strings.Join(rewritten, " "), "pentest-provider-bridge --provider pi") {
		t.Fatalf("rewritten args missing bridge: %#v", rewritten)
	}
}

func TestBuildTaskLaunchPlanPiSandboxWrapsWithoutFactory(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pi-wrap.db"),
		RuntimeRoot: filepath.Join(root, "runs"), SandboxImage: "sandbox:test",
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("pi wrap", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		BinaryPath: "pi", Model: "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "probe example.test",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "high")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	_, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare continuation: %v", err)
	}
	createArgs, ok := runtime.DockerSandboxCreateArgs(bound.Adapter)
	if !ok {
		t.Fatal("expected docker sandbox adapter")
	}
	imageIdx := -1
	for i, arg := range createArgs {
		if arg == "sandbox:test" {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 || imageIdx+1 >= len(createArgs) {
		t.Fatalf("sandbox image not found in create args: %#v", createArgs)
	}
	if createArgs[imageIdx+1] != "sh" {
		t.Fatalf("expected one-shot pi bootstrap wrapper without factory, got %#v", createArgs[imageIdx+1:])
	}
}
