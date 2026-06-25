package credential_test

import (
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
