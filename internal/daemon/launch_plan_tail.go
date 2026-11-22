package daemon

import (
	"fmt"
	"os"
	"strings"

	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func resolveProviderBinary(registry *runtimeplugin.Registry, profile runtimeprofile.Profile) (string, error) {
	if registry == nil {
		registry = runtimeplugin.MustBuiltinRegistry()
	}
	plugin, ok := registry.Get(string(profile.Provider))
	if !ok {
		return "", fmt.Errorf("unsupported provider %q", profile.Provider)
	}
	binary := strings.TrimSpace(profile.Fields.BinaryPath)
	if binary == "" {
		binary = strings.TrimSpace(plugin.Binary.Default)
	}
	if binary == "" {
		return "", fmt.Errorf("no binary path configured for provider %q", profile.Provider)
	}
	return binary, nil
}

// providerNativeSessionMetadata builds the post-launch native session metadata
// collector: the sandbox container id plus the provider-native session
// discovery for runtimes that keep one.
func providerNativeSessionMetadata(sandbox bool, provider runtimeprofile.Provider, layout runner.Layout, containerIDFile string) func() (runtime.NativeSessionMetadata, error) {
	if !sandbox && provider != runtimeprofile.ProviderCodex && provider != runtimeprofile.ProviderPi {
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
