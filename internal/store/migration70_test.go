package store

import (
	"path/filepath"
	"testing"
)

func TestMigration70AddsGlobalSkillOptOutStorage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "migration70.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO skills (id, name, description, source_provenance_json, created_at, updated_at)
		 VALUES ('recon-helper', 'Recon Helper', '', '{}', 'now', 'now')`,
	); err != nil {
		t.Fatalf("insert skill: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO skill_global_opt_outs (skill_id, created_at) VALUES ('recon-helper', 'now')`,
	); err != nil {
		t.Fatalf("insert global Skill Opt-Out: %v", err)
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM skill_global_opt_outs WHERE skill_id = 'recon-helper'`,
	).Scan(&count); err != nil {
		t.Fatalf("read global Skill Opt-Out: %v", err)
	}
	if count != 1 {
		t.Fatalf("global Skill Opt-Out count = %d, want 1", count)
	}
}
