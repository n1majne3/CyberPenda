package project_test

import (
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/store"
)

func newTestService(t *testing.T) *project.Service {
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
	return project.NewService(db)
}

func TestCreateRejectsBlankName(t *testing.T) {
	service := newTestService(t)

	_, err := service.Create("   ", "", project.Scope{}, project.Defaults{})
	if err != project.ErrMissingName {
		t.Fatalf("expected ErrMissingName, got %v", err)
	}
}

func TestCreatePersistsScopeAndDefaults(t *testing.T) {
	service := newTestService(t)

	created, err := service.Create(
		"Acme External",
		"External perimeter test",
		project.Scope{
			Domains:       []string{"example.com"},
			Excluded:      []string{"admin.example.com"},
			TestingLimits: []string{"no destructive payloads"},
		},
		project.Defaults{Runner: project.RunnerSandbox, RuntimeProfile: "codex-default"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if created.ID == "" {
		t.Fatal("expected project id")
	}
	if created.Defaults.Runner != project.RunnerSandbox {
		t.Fatalf("expected default runner sandbox, got %q", created.Defaults.Runner)
	}
	if created.Defaults.RuntimeProfile != "codex-default" {
		t.Fatalf("expected default runtime profile, got %q", created.Defaults.RuntimeProfile)
	}

	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := fetched.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope domains preserved, got %#v", got)
	}
	if got := fetched.Scope.TestingLimits; len(got) != 1 || got[0] != "no destructive payloads" {
		t.Fatalf("expected testing limits preserved, got %#v", got)
	}
	if fetched.Defaults.RuntimeProfile != "codex-default" {
		t.Fatalf("expected defaults preserved, got %#v", fetched.Defaults)
	}
}

func TestCreateWithKindPersistsCTFAndRejectsUnknownKind(t *testing.T) {
	service := newTestService(t)

	created, err := service.CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get CTF Project: %v", err)
	}
	if fetched.Kind != project.KindCTFChallenge {
		t.Fatalf("kind = %q want %q", fetched.Kind, project.KindCTFChallenge)
	}

	if _, err := service.CreateWithKind("Unknown", "", "other", project.Scope{}, project.Defaults{}); err != project.ErrInvalidKind {
		t.Fatalf("expected ErrInvalidKind, got %v", err)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	service := newTestService(t)

	_, err := service.Get("does-not-exist")
	if err != project.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListOrdersByCreationTime(t *testing.T) {
	service := newTestService(t)

	if _, err := service.Create("First", "", project.Scope{Domains: []string{"first.example"}}, project.Defaults{}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := service.Create("Second", "", project.Scope{Domains: []string{"second.example"}}, project.Defaults{}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	projects, err := service.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].Name != "First" || projects[1].Name != "Second" {
		t.Fatalf("expected creation order First then Second, got %q then %q", projects[0].Name, projects[1].Name)
	}
}

func TestUpdateRejectsBlankName(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Original", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = service.Update(created.ID, "   ", "", project.Scope{}, false, project.Defaults{}, false)
	if err != project.ErrMissingName {
		t.Fatalf("expected ErrMissingName, got %v", err)
	}
}

func TestUpdatePreservesUntouchedScopeAndDefaults(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create(
		"Acme",
		"original description",
		project.Scope{Domains: []string{"example.com"}, Notes: "keep me"},
		project.Defaults{Runner: project.RunnerHost},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := service.Update(created.ID, "Acme Renamed", "new description", project.Scope{}, false, project.Defaults{}, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if updated.Name != "Acme Renamed" {
		t.Fatalf("expected renamed project, got %q", updated.Name)
	}
	if updated.Description != "new description" {
		t.Fatalf("expected updated description, got %q", updated.Description)
	}
	if got := updated.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope preserved when not touched, got %#v", got)
	}
	if updated.Scope.Notes != "keep me" {
		t.Fatalf("expected scope notes preserved, got %q", updated.Scope.Notes)
	}
	if updated.Defaults.Runner != project.RunnerHost {
		t.Fatalf("expected defaults preserved, got %q", updated.Defaults.Runner)
	}
}

func TestUpdateOverwritesScopeWhenTouched(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create(
		"Acme",
		"",
		project.Scope{Domains: []string{"example.com"}, Notes: "original"},
		project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newScope := project.Scope{Domains: []string{"acme.example"}, TestingLimits: []string{"business hours only"}}
	updated, err := service.Update(created.ID, "Acme", "", newScope, true, project.Defaults{}, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if got := updated.Scope.Domains; len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("expected scope domain overwritten, got %#v", got)
	}
	if updated.Scope.Notes != "" {
		t.Fatalf("expected scope notes cleared by overwrite, got %q", updated.Scope.Notes)
	}
	if got := updated.Scope.TestingLimits; len(got) != 1 || got[0] != "business hours only" {
		t.Fatalf("expected new testing limits, got %#v", got)
	}
}

func TestUpdateBumpsUpdatedAt(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := service.Update(created.ID, "Acme Renamed", "", project.Scope{}, false, project.Defaults{}, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("expected updated_at to advance, created=%s updated=%s", created.UpdatedAt, updated.UpdatedAt)
	}
}
