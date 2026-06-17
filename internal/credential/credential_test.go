package credential_test

import (
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/store"
)

func newTestService(t *testing.T) *credential.Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return credential.NewService(db)
}

func TestUpsertRejectsBlankRef(t *testing.T) {
	service := newTestService(t)

	_, err := service.Upsert("  ", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "API_KEY"}, false)
	if err != credential.ErrMissingCredentialRef {
		t.Fatalf("expected ErrMissingCredentialRef, got %v", err)
	}
}

func TestUpsertRejectsInvalidSourceKind(t *testing.T) {
	service := newTestService(t)

	_, err := service.Upsert("api-key", credential.ScopeGlobal, "", credential.Source{Kind: "bogus", Value: "x"}, false)
	if !errors.Is(err, credential.ErrInvalidSourceKind) {
		t.Fatalf("expected ErrInvalidSourceKind, got %v", err)
	}
}

func TestUpsertRequiresScopeIDForProjectBinding(t *testing.T) {
	service := newTestService(t)

	_, err := service.Upsert("api-key", credential.ScopeProject, "", credential.Source{Kind: credential.SourceEnv, Value: "API_KEY"}, false)
	if err == nil {
		t.Fatal("expected error for project binding without scope_id")
	}
}

func TestResolveUsesGlobalBindingByDefault(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}

	res, err := service.Resolve("codex-api-key", "project-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Found {
		t.Fatal("expected resolution found via global binding")
	}
	if res.Source == nil || res.Source.Kind != credential.SourceEnv || res.Source.Value != "CODEX_API_KEY" {
		t.Fatalf("expected env source, got %#v", res.Source)
	}
}

func TestResolveProjectOverrideWins(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "GLOBAL_KEY"}, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if _, err := service.Upsert("codex-api-key", credential.ScopeProject, "project-1", credential.Source{Kind: credential.SourceFile, Value: "/secrets/project1"}, false); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	res, err := service.Resolve("codex-api-key", "project-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Source == nil || res.Source.Kind != credential.SourceFile || res.Source.Value != "/secrets/project1" {
		t.Fatalf("expected project override to win, got %#v", res.Source)
	}

	// A different project still falls back to the global binding.
	resOther, err := service.Resolve("codex-api-key", "project-2")
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}
	if resOther.Source == nil || resOther.Source.Value != "GLOBAL_KEY" {
		t.Fatalf("expected other project to use global binding, got %#v", resOther.Source)
	}
}

func TestResolveDisabledProjectBindingBlocksGlobalFallback(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "GLOBAL_KEY"}, false); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if _, err := service.Upsert("codex-api-key", credential.ScopeProject, "project-1", credential.Source{}, true); err != nil {
		t.Fatalf("upsert project disabled: %v", err)
	}

	res, err := service.Resolve("codex-api-key", "project-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Found {
		t.Fatal("expected resolution not found because project disabled the binding")
	}
	if !res.Disabled {
		t.Fatal("expected resolution to report disabled=true")
	}
}

func TestResolveMissingReferenceReturnsNotFound(t *testing.T) {
	service := newTestService(t)

	res, err := service.Resolve("nothing", "project-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Found {
		t.Fatal("expected not found for missing reference")
	}
}

func TestUpsertIsIdempotentPerRef(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "OLD"}, false); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	if _, err := service.Upsert("api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "NEW"}, false); err != nil {
		t.Fatalf("upsert second: %v", err)
	}

	globals, err := service.ListGlobal()
	if err != nil {
		t.Fatalf("list global: %v", err)
	}
	if len(globals) != 1 {
		t.Fatalf("expected one global binding after upsert, got %d", len(globals))
	}
	if globals[0].Source.Value != "NEW" {
		t.Fatalf("expected replaced value NEW, got %q", globals[0].Source.Value)
	}
}

func TestListForProjectReturnsOnlyThatProject(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Upsert("key", credential.ScopeProject, "p1", credential.Source{Kind: credential.SourceEnv, Value: "P1"}, false); err != nil {
		t.Fatalf("upsert p1: %v", err)
	}
	if _, err := service.Upsert("key", credential.ScopeProject, "p2", credential.Source{Kind: credential.SourceEnv, Value: "P2"}, false); err != nil {
		t.Fatalf("upsert p2: %v", err)
	}

	p1, err := service.ListForProject("p1")
	if err != nil {
		t.Fatalf("list p1: %v", err)
	}
	if len(p1) != 1 || p1[0].ScopeID != "p1" {
		t.Fatalf("expected only p1 binding, got %#v", p1)
	}
}

func TestDeleteRemovesBinding(t *testing.T) {
	service := newTestService(t)
	binding, err := service.Upsert("key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "K"}, false)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := service.Delete(binding.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	globals, _ := service.ListGlobal()
	if len(globals) != 0 {
		t.Fatalf("expected no bindings after delete, got %d", len(globals))
	}
}
