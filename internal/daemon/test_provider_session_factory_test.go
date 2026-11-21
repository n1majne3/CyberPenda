package daemon_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/adapters"
	"pentest/internal/daemon"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

type testScriptProviderSessionFactory struct {
	capabilities *runtimeplugin.Capabilities
}

func (factory testScriptProviderSessionFactory) Open(_ context.Context, req daemon.ProviderSessionLaunchRequest) (daemon.ProviderSessionBinding, error) {
	capabilities := runtimeplugin.Capabilities{
		PersistentSession:    true,
		SendTurn:             true,
		InterruptThenReplace: true,
		ResumeSession:        true,
	}
	if factory.capabilities != nil {
		capabilities = *factory.capabilities
	}
	session := fakeprovider.New(fakeprovider.Config{
		Capabilities: capabilities,
	})
	if req.Runner == task.RunnerSandbox {
		image, requiredNetwork := sandboxImageAndNetworkFromCreateArgs(req.Facts.SandboxCreateArgs)
		return daemon.ProviderSessionBinding{
			Session: session,
			Adapter: runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
				Name:            string(req.Provider),
				ContainerCLI:    req.Facts.SandboxContainerCLI,
				Image:           image,
				CreateArgs:      req.Facts.SandboxCreateArgs,
				CreateEnv:       req.Facts.SandboxCreateEnv,
				RequiredNetwork: requiredNetwork,
			}),
		}, nil
	}
	argv, err := adapters.BuildLaunchOrResumeArgs(adapters.LaunchArgsRequest{
		Provider: req.Provider,
		Profile: runtimeprofile.Profile{
			Provider: req.Provider,
			Fields: runtimeprofile.Fields{
				BinaryPath: req.Facts.ProviderBinary,
				Model:      req.Facts.Model,
				CustomArgs: req.Facts.CustomArgs,
			},
		},
		Goal:       req.LaunchGoal,
		ConfigPath: req.Facts.SettingsPath,
	}, req.Continuation.NativeSessionID)
	if err != nil {
		return daemon.ProviderSessionBinding{}, err
	}
	return daemon.ProviderSessionBinding{
		Session: session,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    string(req.Provider),
			Program: argv[0],
			Args:    argv[1:],
			Workdir: req.Facts.Workdir,
			Env:     req.Facts.Env,
		}),
	}, nil
}

func sandboxImageAndNetworkFromCreateArgs(args []string) (string, *runtime.DockerNetworkRequirement) {
	var image string
	var requiredNetwork *runtime.DockerNetworkRequirement
	i := 0
	if len(args) > 0 && args[0] == "create" {
		i = 1
	}
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			image = arg
			break
		}
		if arg == "--network" && i+1 < len(args) {
			networkName := strings.TrimSpace(args[i+1])
			if networkName == "pentest-host-proxy-only" {
				requiredNetwork = &runtime.DockerNetworkRequirement{
					Name:     networkName,
					Driver:   "bridge",
					Internal: false,
				}
			}
			i += 2
			continue
		}
		if strings.HasPrefix(arg, "--network=") {
			networkName := strings.TrimSpace(strings.TrimPrefix(arg, "--network="))
			if networkName == "pentest-host-proxy-only" {
				requiredNetwork = &runtime.DockerNetworkRequirement{
					Name:     networkName,
					Driver:   "bridge",
					Internal: false,
				}
			}
			i++
			continue
		}
		switch arg {
		case "-v", "-e", "-w", "--workdir", "--name", "--cidfile", "--user", "--add-host", "--cap-drop", "--security-opt", "--pids-limit", "--memory", "--cpus", "--tmpfs", "--mount", "--label", "--entrypoint":
			i += 2
		default:
			i++
		}
	}
	return image, requiredNetwork
}

func TestLaunchPersistentProviderRequiresProviderSessionFactory(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          t.TempDir(),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createLocalRuntimeProfile(t, server, "Codex Host", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test",
	})
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(`{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 without ProviderSessionFactory, got %d with body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "provider session factory is required") {
		t.Fatalf("expected missing factory error, got %s", resp.Body.String())
	}
}
