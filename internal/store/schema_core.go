package store

// baselineCoreStatements holds part of the baseline schema.
var baselineCoreStatements = []string{
	`CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	checksum TEXT NOT NULL,
	applied_at TEXT NOT NULL
)`,
	`CREATE TABLE projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	scope_json TEXT NOT NULL,
	defaults_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
, kind TEXT NOT NULL DEFAULT 'pentest', blackboard_protocol TEXT NOT NULL DEFAULT 'legacy')`,
	`CREATE TABLE runtime_profiles (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider TEXT NOT NULL,
	fields_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
	`CREATE TABLE model_providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	base_url TEXT NOT NULL,
	protocols_json TEXT NOT NULL DEFAULT '[]',
	catalog_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
, endpoints_json TEXT NOT NULL DEFAULT '[]')`,
	`CREATE TABLE skills (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	source_provenance_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
	`CREATE TABLE skill_profile_opt_outs (
	profile_id TEXT NOT NULL,
	skill_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (profile_id, skill_id)
)`,
	`CREATE TABLE credential_bindings (
	id TEXT PRIMARY KEY,
	credential_ref TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL DEFAULT '',
	source_json TEXT NOT NULL,
	disabled INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (credential_ref, scope, scope_id)
)`,
	`CREATE TABLE tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	goal TEXT NOT NULL,
	status TEXT NOT NULL,
	runner TEXT NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	run_controls_json TEXT NOT NULL,
	scope_snapshot_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
, deleted_at TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'pentest' CHECK (task_type IN ('pentest','ctf_challenge')))`,
	`CREATE TABLE task_runtime_config_versions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	config_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (task_id, version)
)`,
	`CREATE TABLE task_continuations (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	number INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	runtime_provider TEXT NOT NULL,
	runner TEXT NOT NULL,
	status TEXT NOT NULL,
	container_id TEXT NOT NULL DEFAULT '',
	native_session_id TEXT NOT NULL DEFAULT '',
	native_session_path TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	ended_at TEXT NOT NULL DEFAULT '', runtime_config_version_id TEXT, blackboard_reconciliation_status TEXT NOT NULL DEFAULT '', blackboard_reconciliation_mutation_id TEXT NOT NULL DEFAULT '', blackboard_reconciled_at TEXT NOT NULL DEFAULT '',
	UNIQUE (task_id, number)
)`,
	`CREATE TABLE task_events (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL, continuation_id TEXT, attempt_node_id TEXT,
	UNIQUE (task_id, seq)
)`,
}
