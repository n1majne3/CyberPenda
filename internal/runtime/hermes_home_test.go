package runtime

import (
	"testing"

	"pentest/internal/runtimeprofile"
)

func TestProjectedHermesHomeUsesHostCommandEnv(t *testing.T) {
	adapter := NewCommandAdapter(CommandAdapterConfig{
		Name:    string(runtimeprofile.ProviderHermes),
		Program: "hermes",
		Env:     map[string]string{"HERMES_HOME": "/tmp/hermes-home"},
	})
	if got := ProjectedHermesHome(adapter); got != "/tmp/hermes-home" {
		t.Fatalf("ProjectedHermesHome = %q, want /tmp/hermes-home", got)
	}
}

func TestProjectedHermesHomeMapsSandboxTaskBind(t *testing.T) {
	adapter := NewDockerSandboxAdapter(DockerSandboxConfig{
		Name:  string(runtimeprofile.ProviderHermes),
		Image: "sandbox:test",
		CreateArgs: []string{
			"create",
			"--mount", "type=bind,src=/host/runs/task-1,dst=/task",
			"-e", "HERMES_HOME=/task/runtime-home/hermes",
			"sandbox:test", "hermes", "--yolo", "acp",
		},
	})
	want := "/host/runs/task-1/runtime-home/hermes"
	if got := ProjectedHermesHome(adapter); got != want {
		t.Fatalf("ProjectedHermesHome = %q, want %q", got, want)
	}
}

func TestProjectedHermesHomePrefersExactSandboxHermesBind(t *testing.T) {
	adapter := NewDockerSandboxAdapter(DockerSandboxConfig{
		CreateArgs: []string{
			"create",
			"--mount", "type=bind,src=/host/runs/task-1,dst=/task",
			"--mount", "type=bind,src=/host/runs/task-1/runtime-home/hermes,dst=/task/runtime-home/hermes",
			"-e", "HERMES_HOME=/task/runtime-home/hermes",
			"sandbox:test", "hermes",
		},
	})
	want := "/host/runs/task-1/runtime-home/hermes"
	if got := ProjectedHermesHome(adapter); got != want {
		t.Fatalf("ProjectedHermesHome = %q, want exact Hermes bind %q", got, want)
	}
}
