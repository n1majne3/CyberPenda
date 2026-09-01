package runner_test

import (
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

// newGlobalEnvTestService opens an isolated credential service for global-env
// projection tests.
func newGlobalEnvTestService(t *testing.T) *credential.Service {
	t.Helper()
	storeDB, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = storeDB.Close() })
	return credential.NewService(storeDB)
}

func TestLaunchProcessEnvProjectsOwnerNeutralCLIGrantOnlyInProcessEnv(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-cli-env", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	env := runner.LaunchProcessEnv(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}, false, runner.RuntimeOwnerContext{
		Owner:          owner.NewTaskContract("task-cli-env", "project-1", layout.Workdir),
		APIURL:         "http://127.0.0.1:8787/api",
		InterfaceToken: "continuation-secret",
		BlackboardMode: "working_graph",
	})
	if env["PENTEST_API_URL"] != "http://127.0.0.1:8787/api" ||
		env["PENTEST_INTERFACE_TOKEN"] != "continuation-secret" ||
		env["PENTEST_BLACKBOARD_MODE"] != "working_graph" {
		t.Fatalf("CLI authority env = %#v", env)
	}
}

func TestModelProviderKeyDoesNotSkipProfileCredentialRefs(t *testing.T) {
	creds := newGlobalEnvTestService(t)
	t.Setenv("HOSTED_MODEL_API_KEY", "model-secret")
	if _, err := creds.Upsert("BENCHMARK_TOKEN", credential.ScopeProject, "project-1", credential.Source{
		Kind: credential.SourceLiteral, Value: "benchmark-secret", DestinationEnv: "BENCHMARK_TOKEN",
	}, false); err != nil {
		t.Fatalf("upsert BENCHMARK_TOKEN: %v", err)
	}
	if projected, err := creds.ResolveMaterializedEnv("project-1", []string{"BENCHMARK_TOKEN"}); err != nil || projected["BENCHMARK_TOKEN"] != "benchmark-secret" {
		t.Fatalf("resolve BENCHMARK_TOKEN fixture = %#v, %v", projected, err)
	}
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderPi, Fields: runtimeprofile.Fields{
		CredentialRefs: []string{"BENCHMARK_TOKEN"},
	}}
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-hosted-model-and-token", profile.Provider)
	if err != nil {
		t.Fatal(err)
	}
	env, err := runner.LaunchProcessEnvWithCredentials(layout, profile, false,
		runner.RuntimeOwnerContext{Owner: owner.NewTaskContract("task-hosted-model-and-token", "project-1", layout.Workdir)},
		runner.ProjectionRequest{
			Owner:         owner.NewTaskContract("task-hosted-model-and-token", "project-1", layout.Workdir),
			Credentials:   creds,
			ModelSnapshot: &modelprovider.Snapshot{APIKeyEnv: "HOSTED_MODEL_API_KEY"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if env["HOSTED_MODEL_API_KEY"] != "model-secret" || env["BENCHMARK_TOKEN"] != "benchmark-secret" {
		t.Fatalf("launch env must contain model key and profile credential ref; got %#v", env)
	}
}

// TestGlobalEnvInjectedWithoutCredentialRefs verifies the core Global
// Environment Variable behavior: a global binding projects into the launch
// process env even when the Runtime Profile has no credential_refs at all.
func TestGlobalEnvInjectedWithoutCredentialRefs(t *testing.T) {
	creds := newGlobalEnvTestService(t)
	if _, err := creds.Upsert("NSSCTF_AGENT_TOKEN", credential.ScopeGlobal, "", credential.Source{
		Kind:           credential.SourceLiteral,
		Value:          "nss_agent_global",
		DestinationEnv: "NSSCTF_AGENT_TOKEN",
	}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	// A profile with no credential_refs, no model provider snapshot, no inline
	// keys. Before the fix this produced an empty env map.
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{Model: "gpt-test"},
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-global", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	env, err := runner.LaunchProcessEnvWithCredentials(layout, profile, false,
		runner.RuntimeOwnerContext{Owner: owner.NewTaskContract("task-global", "project-1", layout.Workdir)},
		runner.ProjectionRequest{Credentials: creds},
	)
	if err != nil {
		t.Fatalf("launch process env: %v", err)
	}
	if env["NSSCTF_AGENT_TOKEN"] != "nss_agent_global" {
		t.Fatalf("global env var must be injected without credential_refs; got env=%#v", env)
	}
}

// TestGlobalEnvInjectedIntoClaudePath confirms the Claude Code provider branch
// (buildClaudeEnv) also receives global env vars.
func TestGlobalEnvInjectedIntoClaudePath(t *testing.T) {
	creds := newGlobalEnvTestService(t)
	if _, err := creds.Upsert("NSSCTF_AGENT_TOKEN", credential.ScopeGlobal, "", credential.Source{
		Kind:           credential.SourceLiteral,
		Value:          "nss_agent_claude",
		DestinationEnv: "NSSCTF_AGENT_TOKEN",
	}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{Model: "claude-test", Endpoint: "https://example.test/api/anthropic"},
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-claude-global", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	env, err := runner.LaunchProcessEnvWithCredentials(layout, profile, false,
		runner.RuntimeOwnerContext{Owner: owner.NewTaskContract("task-claude-global", "project-1", layout.Workdir)},
		runner.ProjectionRequest{Credentials: creds},
	)
	if err != nil {
		t.Fatalf("launch process env: %v", err)
	}
	if env["NSSCTF_AGENT_TOKEN"] != "nss_agent_claude" {
		t.Fatalf("global env var must reach Claude path; got env=%#v", env)
	}
}

// TestProfileCredentialRefOverridesGlobalEnv confirms precedence: when the same
// env var name is produced by both a global binding and a profile credential_ref
// resolution, the profile-scoped value wins.
func TestProfileCredentialRefOverridesGlobalEnv(t *testing.T) {
	creds := newGlobalEnvTestService(t)
	// Global default for the var.
	if _, err := creds.Upsert("SHARED_VAR", credential.ScopeGlobal, "", credential.Source{
		Kind:           credential.SourceLiteral,
		Value:          "global-value",
		DestinationEnv: "SHARED_VAR",
	}, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	// A second binding referenced explicitly by the profile, projecting under
	// the SAME destination env name but a different value.
	if _, err := creds.Upsert("profile-shared", credential.ScopeGlobal, "", credential.Source{
		Kind:           credential.SourceLiteral,
		Value:          "profile-value",
		DestinationEnv: "SHARED_VAR",
	}, false); err != nil {
		t.Fatalf("upsert profile ref: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model:          "gpt-test",
			CredentialRefs: []string{"profile-shared"},
		},
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-prio", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	env, err := runner.LaunchProcessEnvWithCredentials(layout, profile, false,
		runner.RuntimeOwnerContext{Owner: owner.NewTaskContract("task-prio", "project-1", layout.Workdir)},
		runner.ProjectionRequest{Credentials: creds},
	)
	if err != nil {
		t.Fatalf("launch process env: %v", err)
	}
	if env["SHARED_VAR"] != "profile-value" {
		t.Fatalf("profile credential_ref must override global env; got SHARED_VAR=%q", env["SHARED_VAR"])
	}
}
