package daemon

import (
	"os"
	"path/filepath"

	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// The launch-plan tail below is the owner-neutral part of every Runtime
// launch: the sandbox Pi bootstrap rule, the Pi session tailer, the native
// session metadata collector, and the container stop confirmation. Task and
// Session launch plans share these steps verbatim (ADR 0020); owners keep only
// their plan middles (Project binding, Scope, captured replay).

// sandboxPiRuntimeCommand applies the one-shot Pi bootstrap wrapper unless this
// launch will open a persistent provider session: the provider-session bridge
// rewrites the image command itself and needs a bare "pi" token in the docker
// create argv.
func (server *Server) sandboxPiRuntimeCommand(providerCommand []string, profile runtimeprofile.Profile, run task.Runner) ([]string, error) {
	usePersistentSession := server.providerSessionFactory != nil &&
		supportsPersistentProviderSession(run, profile.Provider)
	if usePersistentSession {
		return providerCommand, nil
	}
	return runner.WrapSandboxPiCommand(providerCommand, profile.Fields.Env)
}

// withPiSessionTail wraps a sandboxed Pi adapter with the session-file tailer.
// Pi writes its real-time progress to a session jsonl file instead of stdout,
// so a sandboxed Pi timeline is empty until exit; the tailer re-emits appended
// lines as runtime_output events the transcript parser already understands.
func withPiSessionTail(adapter runtime.Adapter, sandbox bool, provider runtimeprofile.Provider, layout runner.Layout) runtime.Adapter {
	if sandbox && provider == runtimeprofile.ProviderPi {
		return runtime.NewPiSessionTailAdapter(adapter, filepath.Join(layout.ProviderHome, "agent", "sessions"))
	}
	return adapter
}

// providerNativeSessionMetadata builds the post-launch native session metadata
// collector: the sandbox container id plus the provider-native session
// discovery for runtimes that keep one.
func providerNativeSessionMetadata(sandbox bool, provider runtimeprofile.Provider, layout runner.Layout, containerIDFile string) func() (runtime.NativeSessionMetadata, error) {
	if !sandbox && provider != runtimeprofile.ProviderCodex && provider != runtimeprofile.ProviderPi && provider != runtimeprofile.ProviderHermes {
		return nil
	}
	return func() (runtime.NativeSessionMetadata, error) {
		collected := runtime.NativeSessionMetadata{}
		if containerIDFile != "" {
			containerID, err := runtime.ReadContainerIDFile(containerIDFile)
			if err != nil && !os.IsNotExist(err) {
				return runtime.NativeSessionMetadata{}, err
			}
			collected.ContainerID = containerID
		}
		switch provider {
		case runtimeprofile.ProviderCodex:
			native, err := runtime.DiscoverCodexSession(layout.ProviderHome)
			if err != nil {
				return runtime.NativeSessionMetadata{}, err
			}
			collected.NativeSessionID, collected.NativeSessionPath = native.NativeSessionID, native.NativeSessionPath
		case runtimeprofile.ProviderPi:
			native, err := runtime.DiscoverPiSession(layout.ProviderHome)
			if err != nil {
				return runtime.NativeSessionMetadata{}, err
			}
			collected.NativeSessionID, collected.NativeSessionPath = native.NativeSessionID, native.NativeSessionPath
		}
		return collected, nil
	}
}

// dockerStopConfirmation confirms a sandbox container stopped before the
// Runtime is declared terminal. It matches the launch-selected CLI
// (podman/docker), not only the daemon flag.
func dockerStopConfirmation(ownerContainerCLI, daemonContainerCLI, containerIDFile string) runtime.StopConfirmation {
	if containerIDFile == "" {
		return nil
	}
	return runtime.DockerContainerStopConfirmation(
		task.ResolveContainerCLI(ownerContainerCLI, daemonContainerCLI), containerIDFile,
	)
}
