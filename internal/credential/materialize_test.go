package credential_test

import (
	"errors"
	"os"
	"path/filepath"
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

func TestMaterializeFileSourceReadsFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := credential.Materialize(credential.Source{Kind: credential.SourceFile, Value: path})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("expected file contents, got %q", got)
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

func TestResolveMaterializedEnvUsesDestinationEnvForFileSource(t *testing.T) {
	service := newTestService(t)
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	source := credential.Source{Kind: credential.SourceFile, Value: path, DestinationEnv: "API_KEY"}
	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	out, err := service.ResolveMaterializedEnv("project-1", []string{"codex-api-key"})
	if err != nil {
		t.Fatalf("resolve materialized: %v", err)
	}
	if got := out["API_KEY"]; got != "file-secret" {
		t.Fatalf("expected API_KEY=file-secret, got %q (full map: %v)", got, out)
	}
	if _, leaked := out[path]; leaked {
		t.Fatalf("file path leaked as env key: %v", out)
	}
}

func TestResolveMaterializedEnvUsesDestinationEnvForCommandSource(t *testing.T) {
	t.Setenv("PENTEST_ALLOW_COMMAND_CREDENTIALS", "1")
	service := newTestService(t)
	source := credential.Source{Kind: credential.SourceCommand, Value: "printf cmd-secret", DestinationEnv: "API_KEY"}
	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	out, err := service.ResolveMaterializedEnv("project-1", []string{"codex-api-key"})
	if err != nil {
		t.Fatalf("resolve materialized: %v", err)
	}
	if got := out["API_KEY"]; got != "cmd-secret" {
		t.Fatalf("expected API_KEY=cmd-secret, got %q", got)
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

func TestResolveMaterializedEnvErrorsWhenFileSourceHasNoDestination(t *testing.T) {
	service := newTestService(t)
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	source := credential.Source{Kind: credential.SourceFile, Value: path}
	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	_, err := service.ResolveMaterializedEnv("project-1", []string{"codex-api-key"})
	if err == nil {
		t.Fatal("expected error when file source has no destination_env")
	}
}

// TestMaterializeRejectsCommandSourceByDefault pins issue #159 at the execution
// chokepoint: even a command binding that already exists (or was injected
// directly into the store) must not run unless the operator opted in.
func TestMaterializeRejectsCommandSourceByDefault(t *testing.T) {
	t.Setenv("PENTEST_ALLOW_COMMAND_CREDENTIALS", "")

	_, err := credential.Materialize(credential.Source{Kind: credential.SourceCommand, Value: "printf secret", DestinationEnv: "API_KEY"})
	if !errors.Is(err, credential.ErrCommandSourceDisabled) {
		t.Fatalf("expected ErrCommandSourceDisabled, got %v", err)
	}
}

func TestMaterializeAllowsCommandSourceWhenOptedIn(t *testing.T) {
	t.Setenv("PENTEST_ALLOW_COMMAND_CREDENTIALS", "1")

	got, err := credential.Materialize(credential.Source{Kind: credential.SourceCommand, Value: "printf cmd-secret", DestinationEnv: "API_KEY"})
	if err != nil {
		t.Fatalf("materialize opted-in command: %v", err)
	}
	if got != "cmd-secret" {
		t.Fatalf("expected cmd-secret, got %q", got)
	}
}
