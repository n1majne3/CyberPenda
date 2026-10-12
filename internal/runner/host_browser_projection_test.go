package runner_test

import (
	"os"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestHostBrowserCannotBeEnabledByRuntimeProfile(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi} {
		t.Run(string(provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "browser-task", provider)
			if err != nil {
				t.Fatal(err)
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{MCPServers: []runtimeprofile.MCPServer{
				{Name: "host_browser", URL: "http://host.docker.internal:9999/mcp"},
				{Name: "reference", URL: "http://reference.test/mcp"},
			}}}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
			if err != nil {
				t.Fatal(err)
			}
			path := providerMCPConfigPath(layout, provider, projection)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "host_browser") || strings.Contains(string(data), "9999") {
				t.Fatalf("Profile implicitly enabled Host Browser: %s", data)
			}
			if !strings.Contains(string(data), "reference.test") {
				t.Fatalf("External MCP configuration lost: %s", data)
			}
		})
	}
}

func TestHostBrowserUsesManagedNativeMCPProjection(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi} {
		t.Run(string(provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "browser-task", provider)
			if err != nil {
				t.Fatal(err)
			}
			profile := runtimeprofile.Profile{Provider: provider}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{Sandbox: true, HostBrowserMCPURL: "http://host.docker.internal:8931/mcp"})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(providerMCPConfigPath(layout, provider, projection))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "host_browser") || !strings.Contains(string(data), "http://host.docker.internal:8931/mcp") {
				t.Fatalf("managed Host Browser endpoint missing: %s", data)
			}
		})
	}
}
