package daemon

import (
	"path/filepath"
	"slices"
	"testing"

	"pentest/internal/owner"
)

func TestPiSandboxResumeUsesMountedNativeSessionPath(t *testing.T) {
	root := t.TempDir()
	relative := filepath.Join("runtime-home", "pi", "agent", "sessions", "retained.jsonl")
	for _, tc := range []struct{ name, retained, want string }{
		{"discovered host path", filepath.Join(root, relative), "/task/runtime-home/pi/agent/sessions/retained.jsonl"},
		{"bridge path", "/task/runtime-home/pi/agent/sessions/retained.jsonl", "/task/runtime-home/pi/agent/sessions/retained.jsonl"},
		{"other mount", "/sessions/retained.jsonl", "/sessions/retained.jsonl"},
		{"sibling root", filepath.Join(root+"-other", relative), filepath.Join(root+"-other", relative)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assembly := providerSessionAssembly{request: ProviderSessionLaunchRequest{
				Continuation: owner.Continuation{NativeSessionID: "retained", NativeSessionPath: tc.retained},
				Facts:        ProviderSessionLaunchFacts{ProviderBinary: "pi", HostTaskRoot: root},
			}}
			args, err := (piAssembler{}).SandboxCommand(assembly)
			if err != nil {
				t.Fatal(err)
			}
			index := slices.Index(args, "--session")
			if index < 0 || index+1 >= len(args) || args[index+1] != tc.want {
				t.Fatalf("resume argv = %q, want --session %q", args, tc.want)
			}
			if assembly.request.Continuation.NativeSessionPath != tc.retained {
				t.Fatal("projection changed the durable native session path")
			}
		})
	}
}
