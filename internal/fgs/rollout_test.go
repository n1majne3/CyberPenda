package fgs_test

import (
	"path/filepath"
	"pentest/internal/project"
	"pentest/internal/session"
	"pentest/internal/store"
	"testing"
)

func TestNewOwnersPersistFGSProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	projects := project.NewService(db)
	p, err := projects.Create("FGS", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if p.BlackboardProtocol != "fgs" {
		t.Fatalf("protocol %q", p.BlackboardProtocol)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	found, err := project.NewService(db).Get(p.ID)
	if err != nil || found.BlackboardProtocol != "fgs" {
		t.Fatalf("restart: %+v %v", found, err)
	}
	sessions := session.NewService(db, t.TempDir())
	created, err := sessions.Create(session.CreateRequest{Input: "FGS", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := sessions.Get(created.ID)
	if err != nil || loaded.BlackboardProtocol != "fgs" {
		t.Fatalf("Session protocol: %+v %v", loaded, err)
	}
}
