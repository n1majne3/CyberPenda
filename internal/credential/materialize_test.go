package credential_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
)

func TestMaterializeEnvSourceReadsHostEnvironment(t *testing.T) {
	t.Setenv("PENTEST_TEST_API_KEY", "secret-from-host")

	got, err := credential.Materialize(credential.Source{Kind: credential.SourceEnv, Value: "PENTEST_TEST_API_KEY"})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got != "secret-from-host" {
		t.Fatalf("expected host env value, got %q", got)
	}
}

// TestMaterializeRejectsFileSource pins the source-kind simplification: only
// env and literal sources exist. A stored file source is refused outright.
func TestMaterializeRejectsFileSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := credential.Materialize(credential.Source{Kind: "file", Value: path})
	if !errors.Is(err, credential.ErrInvalidSourceKind) {
		t.Fatalf("expected ErrInvalidSourceKind for file source, got %v", err)
	}
}

// TestMaterializeRejectsCommandSource pins the source-kind simplification: a
// command source (potential host RCE) is no longer a supported kind at all.
func TestMaterializeRejectsCommandSource(t *testing.T) {
	t.Setenv("PENTEST_ALLOW_COMMAND_CREDENTIALS", "1")

	_, err := credential.Materialize(credential.Source{Kind: "command", Value: "printf cmd-secret", DestinationEnv: "API_KEY"})
	if !errors.Is(err, credential.ErrInvalidSourceKind) {
		t.Fatalf("expected ErrInvalidSourceKind for command source, got %v", err)
	}
}

func TestMaterializeLiteralSourceReadsStoredSecret(t *testing.T) {
	got, err := credential.Materialize(credential.Source{Kind: credential.SourceLiteral, Value: "sk-local-secret\n"})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got != "sk-local-secret" {
		t.Fatalf("expected literal secret, got %q", got)
	}
}

func TestMaterializeMissingEnvReturnsError(t *testing.T) {
	_, err := credential.Materialize(credential.Source{Kind: credential.SourceEnv, Value: "PENTEST_MISSING_KEY_XYZ"})
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestResolveMaterializedEnvUsesDestinationEnvForLiteralSource(t *testing.T) {
	service := newTestService(t)
	source := credential.Source{Kind: credential.SourceLiteral, Value: "lit-secret", DestinationEnv: "API_KEY"}
	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	out, err := service.ResolveMaterializedEnv("project-1", []string{"codex-api-key"})
	if err != nil {
		t.Fatalf("resolve materialized: %v", err)
	}
	if got := out["API_KEY"]; got != "lit-secret" {
		t.Fatalf("expected API_KEY=lit-secret, got %q", got)
	}
}

func TestResolveMaterializedEnvFallsBackToSourceValueForEnvKind(t *testing.T) {
	service := newTestService(t)
	t.Setenv("CODEX_API_KEY", "env-secret")
	source := credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}
	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	out, err := service.ResolveMaterializedEnv("project-1", []string{"codex-api-key"})
	if err != nil {
		t.Fatalf("resolve materialized: %v", err)
	}
	if got := out["CODEX_API_KEY"]; got != "env-secret" {
		t.Fatalf("expected CODEX_API_KEY=env-secret, got %q", got)
	}
}

// TestResolveGlobalEnvMaterializesActiveLiteralBindings verifies that every
// non-disabled global binding projects under its destination_env as a runtime
// environment variable. This is the core of the "global environment variable"
// concept: one binding injects into every Runtime without a per-profile
// credential_ref.
func TestResolveGlobalEnvMaterializesActiveLiteralBindings(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("NSSCTF_AGENT_TOKEN", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceLiteral, Value: "nss_agent_abc", DestinationEnv: "NSSCTF_AGENT_TOKEN"}, false); err != nil {
		t.Fatalf("upsert nssctf: %v", err)
	}
	if _, err := service.Upsert("EXTRA_VAR", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceLiteral, Value: "extra-value", DestinationEnv: "EXTRA_VAR"}, false); err != nil {
		t.Fatalf("upsert extra: %v", err)
	}

	got, err := service.ResolveGlobalEnv()
	if err != nil {
		t.Fatalf("resolve global env: %v", err)
	}
	if got["NSSCTF_AGENT_TOKEN"] != "nss_agent_abc" {
		t.Fatalf("expected NSSCTF_AGENT_TOKEN=nss_agent_abc, got %q", got["NSSCTF_AGENT_TOKEN"])
	}
	if got["EXTRA_VAR"] != "extra-value" {
		t.Fatalf("expected EXTRA_VAR=extra-value, got %q", got["EXTRA_VAR"])
	}
}

// TestResolveGlobalEnvSkipsDisabledBindings confirms a disabled global binding
// is not injected into any Runtime.
func TestResolveGlobalEnvSkipsDisabledBindings(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("ACTIVE_VAR", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceLiteral, Value: "on", DestinationEnv: "ACTIVE_VAR"}, false); err != nil {
		t.Fatalf("upsert active: %v", err)
	}
	if _, err := service.Upsert("OFF_VAR", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceLiteral, Value: "off", DestinationEnv: "OFF_VAR"}, true); err != nil {
		t.Fatalf("upsert disabled: %v", err)
	}

	got, err := service.ResolveGlobalEnv()
	if err != nil {
		t.Fatalf("resolve global env: %v", err)
	}
	if _, present := got["OFF_VAR"]; present {
		t.Fatalf("disabled binding OFF_VAR must not be injected, got %q", got["OFF_VAR"])
	}
	if got["ACTIVE_VAR"] != "on" {
		t.Fatalf("expected ACTIVE_VAR=on, got %q", got["ACTIVE_VAR"])
	}
}

// TestResolveGlobalEnvReportsUnmaterializableBinding ensures a global binding
// whose source cannot be materialized (an env source pointing at an unset
// variable) surfaces a clear error naming the credential reference, so
// preflight can block launch.
func TestResolveGlobalEnvReportsUnmaterializableBinding(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("BROKEN_VAR", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "PENTEST_UNSET_VAR_XYZ", DestinationEnv: "BROKEN_VAR"}, false); err != nil {
		t.Fatalf("upsert broken: %v", err)
	}

	_, err := service.ResolveGlobalEnv()
	if err == nil {
		t.Fatal("expected error for unmaterializable global binding, got nil")
	}
	if !strings.Contains(err.Error(), "BROKEN_VAR") {
		t.Fatalf("error must name the credential reference, got %q", err.Error())
	}
}
