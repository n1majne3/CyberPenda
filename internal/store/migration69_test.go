package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMigration69PentestDatabaseCopyRehearsal(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("CYBERPENDA_MIGRATION69_REHEARSAL"))
	if path == "" {
		t.Skip("set CYBERPENDA_MIGRATION69_REHEARSAL to a disposable pentest.db copy")
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var automatic, manual, referenced int
	if err := legacy.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE kind='launch_resolve'`).Scan(&automatic); err != nil {
		t.Fatal(err)
	}
	if err := legacy.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE kind='manual'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if err := legacy.QueryRow(`SELECT COUNT(DISTINCT c.runtime_profile_id) FROM (SELECT runtime_profile_id FROM task_runtime_config_versions UNION ALL SELECT runtime_profile_id FROM session_runtime_config_versions) c JOIN runtime_profiles p ON p.id=c.runtime_profile_id WHERE p.kind='launch_resolve'`).Scan(&referenced); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	if automatic != 90 || manual != 4 || referenced != 76 {
		t.Fatalf("legacy fixture counts automatic=%d manual=%d referenced=%d", automatic, manual, referenced)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	var profiles int
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM runtime_profiles`).Scan(&profiles); err != nil || profiles != 4 {
		t.Fatalf("profiles after migration=%d err=%v", profiles, err)
	}
	if hasKind, err := storeDBHasColumn(migrated.DB, "runtime_profiles", "kind"); err != nil || hasKind {
		t.Fatalf("runtime_profiles.kind exists=%v err=%v", hasKind, err)
	}
	for _, table := range []string{"task_runtime_config_versions", "session_runtime_config_versions"} {
		rows, err := migrated.Query(`SELECT config_json FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var snapshot map[string]any
			if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot["snapshot_version"] != float64(1) {
				_ = rows.Close()
				t.Fatalf("invalid migrated Snapshot in %s: %v %s", table, err, raw)
			}
		}
		_ = rows.Close()
	}
	var preservedAutomaticSettings int
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM task_runtime_config_versions WHERE json_array_length(json_extract(config_json,'$.settings.custom_args')) > 0 OR length(json_extract(config_json,'$.settings.sandbox_image')) > 0`).Scan(&preservedAutomaticSettings); err != nil {
		t.Fatal(err)
	}
	if preservedAutomaticSettings == 0 {
		t.Fatal("Custom Args and Sandbox Image were not preserved in Task Snapshots")
	}
}

func storeDBHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func TestMigration69BackfillsSnapshotsAndDeletesOnlyAutomaticProfiles(t *testing.T) {
	db := newMigration69Fixture(t)
	defer db.Close()

	mustExec69(t, db, `INSERT INTO runtime_profiles VALUES
		('manual-1','Advanced','codex','manual','{"model_provider_id":"provider-1","model_override":"profile-model","custom_args":["--profile"],"sandbox_image":"profile-image"}','now','now'),
		('auto-1','Automatic','codex','launch_resolve','{"model_provider_id":"provider-1","model_override":"profile-model"}','now','now')`)
	mustExec69(t, db, `INSERT INTO skill_profile_opt_outs VALUES ('auto-1','disabled-skill','now')`)
	mustExec69(t, db, `INSERT INTO skills VALUES ('recon'),('disabled-skill')`)
	mustExec69(t, db, `INSERT INTO projects VALUES ('project-1','{"runtime_profile":"auto-1","runner":"sandbox"}')`)
	mustExec69(t, db, `INSERT INTO tasks VALUES ('task-1','project-1','sandbox','auto-1','now')`)
	mustExec69(t, db, `INSERT INTO task_runtime_config_versions VALUES
		('config-1','task-1',1,'auto-1','{"provider":"codex","model_provider_id":"provider-2","model_override":"actual-model","reasoning_effort":"high","custom_args":["--actual"],"sandbox_image":"actual-image","enabled_skill_ids":["recon"]}','now')`)
	mustExec69(t, db, `INSERT INTO task_continuations VALUES ('continuation-1','task-1',1,'auto-1','codex','sandbox','config-1','now')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration69Up(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migration69Up() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var profiles int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_profiles`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 {
		t.Fatalf("runtime profile count = %d, want 1", profiles)
	}
	var keptID string
	if err := db.QueryRow(`SELECT id FROM runtime_profiles`).Scan(&keptID); err != nil || keptID != "manual-1" {
		t.Fatalf("kept profile = %q, err = %v", keptID, err)
	}

	var configJSON, provenance string
	if err := db.QueryRow(`SELECT config_json,runtime_profile_id FROM task_runtime_config_versions WHERE id='config-1'`).Scan(&configJSON, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance != "" {
		t.Fatalf("automatic config provenance = %q", provenance)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(configJSON), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot["snapshot_version"] != float64(1) || snapshot["runtime_plugin_id"] != "codex" {
		t.Fatalf("snapshot identity = %#v", snapshot)
	}
	turn := snapshot["runtime_turn_selection"].(map[string]any)
	if turn["model_provider_id"] != "provider-2" || turn["model"] != "actual-model" {
		t.Fatalf("historical actual selection lost: %#v", turn)
	}
	settings := snapshot["settings"].(map[string]any)
	if settings["sandbox_image"] != "actual-image" || settings["custom_args"].([]any)[0] != "--actual" {
		t.Fatalf("historical settings lost: %#v", settings)
	}

	var defaultsJSON, taskProfile, continuationProfile string
	if err := db.QueryRow(`SELECT defaults_json FROM projects WHERE id='project-1'`).Scan(&defaultsJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(defaultsJSON, "runtime_profile") {
		t.Fatalf("project defaults still contain Runtime Profile: %s", defaultsJSON)
	}
	if err := db.QueryRow(`SELECT runtime_profile_id FROM tasks WHERE id='task-1'`).Scan(&taskProfile); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT runtime_profile_id FROM task_continuations WHERE id='continuation-1'`).Scan(&continuationProfile); err != nil {
		t.Fatal(err)
	}
	if taskProfile != "" || continuationProfile != "" {
		t.Fatalf("automatic owner provenance task=%q continuation=%q", taskProfile, continuationProfile)
	}
	if columnExists69(t, db, "runtime_profiles", "kind") {
		t.Fatal("runtime_profiles.kind still exists")
	}
}

func TestMigration69CapturesHistoricalProfileSkillOptOutResult(t *testing.T) {
	db := newMigration69Fixture(t)
	defer db.Close()
	mustExec69(t, db, `INSERT INTO runtime_profiles VALUES ('auto-1','Automatic','codex','launch_resolve','{"model_provider_id":"provider-1","model_override":"model-1"}','now','now')`)
	mustExec69(t, db, `INSERT INTO skills VALUES ('alpha'),('beta')`)
	mustExec69(t, db, `INSERT INTO skill_profile_opt_outs VALUES ('auto-1','beta','now')`)
	mustExec69(t, db, `INSERT INTO projects VALUES ('project-1','{}')`)
	mustExec69(t, db, `INSERT INTO tasks VALUES ('task-1','project-1','sandbox','auto-1','now')`)
	mustExec69(t, db, `INSERT INTO task_runtime_config_versions VALUES ('config-1','task-1',1,'auto-1','{"provider":"codex","model_provider_id":"provider-1","model":"model-1"}','now')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration69Up(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT config_json FROM task_runtime_config_versions WHERE id='config-1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Enabled []string `json:"enabled_skill_ids"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot.Enabled) != 1 || snapshot.Enabled[0] != "alpha" {
		t.Fatalf("captured Skills = %#v, err=%v", snapshot.Enabled, err)
	}
}

func TestMigration69RollsBackWhenCapturedSkillIsMissing(t *testing.T) {
	db := newMigration69Fixture(t)
	defer db.Close()
	mustExec69(t, db, `INSERT INTO runtime_profiles VALUES ('auto-1','Automatic','codex','launch_resolve','{"model_provider_id":"provider-1","model_override":"model-1"}','now','now')`)
	mustExec69(t, db, `INSERT INTO projects VALUES ('project-1','{}')`)
	mustExec69(t, db, `INSERT INTO tasks VALUES ('task-1','project-1','sandbox','auto-1','now')`)
	mustExec69(t, db, `INSERT INTO task_runtime_config_versions VALUES ('config-1','task-1',1,'auto-1','{"provider":"codex","model_provider_id":"provider-1","model":"model-1","enabled_skill_ids":["deleted-skill"]}','now')`)
	mustExec69(t, db, `INSERT INTO task_continuations VALUES ('continuation-1','task-1',1,'auto-1','codex','sandbox','config-1','now')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = migration69Up(tx)
	if err == nil || !strings.Contains(err.Error(), "captured Skill deleted-skill") {
		t.Fatalf("migration69Up() error = %v, want missing captured Skill diagnostic", err)
	}
	_ = tx.Rollback()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE id='auto-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("automatic Profile after rollback count=%d err=%v", count, err)
	}
}

func TestMigration69RollsBackWhenSessionCannotBeBackfilled(t *testing.T) {
	db := newMigration69Fixture(t)
	defer db.Close()
	mustExec69(t, db, `INSERT INTO sessions VALUES ('session-gap','host','','now')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = migration69Up(tx)
	if err == nil || !strings.Contains(err.Error(), "Session session-gap") {
		t.Fatalf("migration69Up() error = %v, want Session Snapshot diagnostic", err)
	}
	_ = tx.Rollback()
}

func TestMigration69RollsBackWhenLegacyProfileContainsInlineSecret(t *testing.T) {
	db := newMigration69Fixture(t)
	defer db.Close()
	mustExec69(t, db, `INSERT INTO runtime_profiles VALUES ('auto-secret','Automatic','codex','launch_resolve','{"api_keys":{"OPENAI_API_KEY":"secret"}}','now','now')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = migration69Up(tx)
	if err == nil || !strings.Contains(err.Error(), "inline secret") {
		t.Fatalf("migration69Up() error = %v, want inline secret diagnostic", err)
	}
	_ = tx.Rollback()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE id='auto-secret'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("profile after rollback count=%d err=%v", count, err)
	}
	if !columnExists69(t, db, "runtime_profiles", "kind") {
		t.Fatal("kind column changed despite rollback")
	}
}

func newMigration69Fixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE runtime_profiles (id TEXT PRIMARY KEY,name TEXT,provider TEXT,kind TEXT,fields_json TEXT,created_at TEXT,updated_at TEXT)`,
		`CREATE TABLE skill_profile_opt_outs (profile_id TEXT,skill_id TEXT,created_at TEXT)`,
		`CREATE TABLE skills (id TEXT PRIMARY KEY)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY,defaults_json TEXT)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY,project_id TEXT,runner TEXT,runtime_profile_id TEXT,created_at TEXT)`,
		`CREATE TABLE task_runtime_config_versions (id TEXT PRIMARY KEY,task_id TEXT,version INTEGER,runtime_profile_id TEXT,config_json TEXT,created_at TEXT)`,
		`CREATE TABLE task_continuations (id TEXT PRIMARY KEY,task_id TEXT,number INTEGER,runtime_profile_id TEXT,runtime_provider TEXT,runner TEXT,runtime_config_version_id TEXT,started_at TEXT)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY,runner TEXT,runtime_profile_id TEXT,created_at TEXT)`,
		`CREATE TABLE session_runtime_config_versions (id TEXT PRIMARY KEY,session_id TEXT,version INTEGER,runtime_profile_id TEXT,config_json TEXT,created_at TEXT)`,
		`CREATE TABLE session_continuations (id TEXT PRIMARY KEY,session_id TEXT,number INTEGER,runtime_profile_id TEXT,runtime_provider TEXT,runner TEXT,runtime_config_version_id TEXT,started_at TEXT)`,
	} {
		mustExec69(t, db, statement)
	}
	return db
}

func mustExec69(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("exec fixture SQL: %v\n%s", err, statement)
	}
}

func columnExists69(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}
