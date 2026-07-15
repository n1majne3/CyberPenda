package runner_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestProjectRuntimeConfigKeepsContinuationInterfaceTokenOutOfContextFile(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-security", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare task layout: %v", err)
	}

	const interfaceToken = "continuation-interface-token-must-stay-secret"
	context := projectinterface.RuntimeBlackboardContextV1{
		ProjectID: "project-security", TaskID: "task-security", ContinuationID: "continuation-security",
		RuntimeConfigVersionID: "config-security", RuntimeProfileID: "profile-security",
		RuntimePluginID: string(runtimeprofile.ProviderClaudeCode), Runner: "sandbox",
		APIURL: "http://host.docker.internal:8787/api", MCPURL: "http://host.docker.internal:8787/mcp",
		ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		BlackboardGraphRevision: 7, BlackboardRendererVersion: blackboard.CanonicalMainGraphRendererV1,
		BlackboardEstimatorVersion: blackboard.UTF8BytesDiv4EstimatorV1,
		BlackboardProjectionHash:   "projection-security", BlackboardProjectionBytes: 256,
		BlackboardEstimatedTokens: 64,
	}
	profile := runtimeprofile.Profile{
		ID: "profile-security", Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{Model: "claude-test"},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID: "project-security", TaskID: "task-security", DaemonAddr: "127.0.0.1:8787",
		AuthToken: interfaceToken, Sandbox: true, RuntimeContext: &context,
	}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(layout.Workdir, ".pentest", "context.json"))
	if err != nil {
		t.Fatalf("read projected Runtime context: %v", err)
	}
	if bytes.Contains(raw, []byte(interfaceToken)) {
		t.Fatal(".pentest/context.json leaked the Continuation Interface Grant token")
	}
	var projected map[string]any
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatalf("decode projected Runtime context: %v", err)
	}
	if _, leaked := projected["interface_token"]; leaked {
		t.Fatal(".pentest/context.json exposed an interface_token field")
	}
}
