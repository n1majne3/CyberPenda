package runner_test

import (
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestFormatContainerHostPathNormalizesWindowsDrivePaths(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`C:\Users\op\CyberPenda\runs\task-1`, "C:/Users/op/CyberPenda/runs/task-1"},
		{`c:/data/runs`, "c:/data/runs"},
		{`\\wsl$\Ubuntu\home\op\runs`, "//wsl$/Ubuntu/home/op/runs"},
		{"/Users/op/runs", "/Users/op/runs"},
		{"./relative", "./relative"},
	}
	for _, tc := range cases {
		got := runner.FormatContainerHostPath(tc.in)
		if got != tc.want {
			t.Fatalf("FormatContainerHostPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildSandboxCommandUsesBindMountNotShortVolumeFlag(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-bind", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderCodex,
		Image:          "pentest-kali:local",
		RuntimeCommand: []string{"codex", "run"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, " -v ") || strings.HasPrefix(joined, "-v ") {
		// Also catch leading -v as first arg after create.
		for i, arg := range command.Args {
			if arg == "-v" {
				t.Fatalf("expected --mount bind form, found -v at index %d in %v", i, command.Args)
			}
		}
	}
	absRoot, err := filepath.Abs(layout.TaskRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=" + runner.FormatContainerHostPath(absRoot) + ",dst=/task"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected bind mount %q in %v", want, command.Args)
	}
}

func TestWindowsHostPathHasSingleDriveColon(t *testing.T) {
	formatted := runner.FormatContainerHostPath(`D:\pentest\runs\task-a`)
	if strings.Contains(formatted, `\`) {
		t.Fatalf("formatted path still has backslashes: %q", formatted)
	}
	if strings.Count(formatted, ":") != 1 {
		t.Fatalf("expected single drive colon in %q", formatted)
	}
	if formatted != "D:/pentest/runs/task-a" {
		t.Fatalf("got %q", formatted)
	}
}
