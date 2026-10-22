package store

// baselineBlackboardStatements holds part of the baseline schema.
var baselineBlackboardStatements = []string{
	`CREATE TABLE blackboard_store_state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	canonical_store TEXT NOT NULL,
	cutover_state TEXT NOT NULL,
	migration_contract_version TEXT NOT NULL DEFAULT '',
	graph_schema_version INTEGER NOT NULL DEFAULT 0,
	cutover_id TEXT NOT NULL DEFAULT '',
	source_digest TEXT NOT NULL DEFAULT '',
	mapping_digest TEXT NOT NULL DEFAULT '',
	verified_backup_path TEXT NOT NULL DEFAULT '',
	verified_backup_sha256 TEXT NOT NULL DEFAULT '',
	cutover_application_version TEXT NOT NULL DEFAULT '',
	cutover_started_at TEXT NOT NULL DEFAULT '',
	cutover_committed_at TEXT NOT NULL DEFAULT '',
	post_cutover_write_committed INTEGER NOT NULL DEFAULT 0,
	latest_verification_at TEXT NOT NULL DEFAULT '',
	latest_verification_result_hash TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
)`,
	`CREATE TABLE blackboard_read_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 cursor_secret BLOB NOT NULL CHECK(length(cursor_secret)=32)
)`,
	`CREATE TABLE blackboard_continuation_grants (
 grant_id TEXT PRIMARY KEY,
 token_hash TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 continuation_id TEXT NOT NULL,
 runtime_config_version_id TEXT NOT NULL,
 runtime_profile_id TEXT NOT NULL,
 runtime_plugin_id TEXT NOT NULL,
 runner TEXT NOT NULL,
 actor_id TEXT NOT NULL,
 issued_at TEXT NOT NULL,
 finished_at TEXT NOT NULL DEFAULT '',
 revoked_at TEXT NOT NULL DEFAULT '',
 terminal_at TEXT NOT NULL DEFAULT '', access_mode TEXT NOT NULL DEFAULT 'full' CHECK (access_mode IN ('full', 'read_only')),
 CHECK (token_hash <> '')
)`,
	`CREATE TABLE blackboard_v2_project_state (
	project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL CHECK (revision >= 0)
)`,
	`CREATE TABLE blackboard_v2_records (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
	version INTEGER NOT NULL CHECK (version >= 1),
	record_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key)
)`,
	`CREATE TABLE blackboard_v2_record_history (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
	record_json TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key, version)
)`,
	`CREATE TABLE blackboard_v2_idempotency_receipts (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL, continuation_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_relationships (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, from_key, relation, to_key)
)`,
	`CREATE TABLE blackboard_v2_relationship_history (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, from_key, relation, to_key, version)
)`,
	`CREATE TABLE blackboard_v2_attempt_origins (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key)
)`,
	`CREATE TABLE blackboard_v2_evidence_requests (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	source_identity TEXT NOT NULL,
	source_sha256 TEXT NOT NULL,
	source_size_bytes INTEGER NOT NULL CHECK (source_size_bytes >= 0),
	managed_internal_path TEXT NOT NULL,
	payload_owned INTEGER NOT NULL DEFAULT 0 CHECK (payload_owned IN (0,1)),
	status TEXT NOT NULL CHECK (status IN ('reserved','published','completed')),
	result_json TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL, temp_internal_path TEXT NOT NULL DEFAULT '', publisher_token TEXT NOT NULL DEFAULT '', publisher_temp_identity TEXT NOT NULL DEFAULT '', previous_temp_internal_path TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, continuation_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_evidence_payloads (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	managed_internal_path TEXT NOT NULL,
	sha256 TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	state TEXT NOT NULL CHECK (state IN ('active','gc')),
	gc_continuation_id TEXT NOT NULL DEFAULT '',
	gc_idempotency_key TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, managed_internal_path),
	CHECK (state = 'active' OR (gc_continuation_id <> '' AND gc_idempotency_key <> ''))
)`,
	`CREATE TABLE blackboard_v2_key_redirects (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source_key TEXT NOT NULL,
	canonical_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, source_key),
	CHECK (source_key <> canonical_key)
)`,
	`CREATE TABLE blackboard_v2_continuation_pins (
	continuation_id TEXT PRIMARY KEY REFERENCES task_continuations(id) ON DELETE RESTRICT,
	snapshot_schema TEXT NOT NULL CHECK (snapshot_schema = 'runtime-blackboard/v2'),
	snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
	snapshot_bytes BLOB NOT NULL CHECK (length(snapshot_bytes) > 0),
	integrity_sha256 TEXT NOT NULL CHECK (length(integrity_sha256) = 64),
	created_at TEXT NOT NULL
)`,
	`CREATE TABLE blackboard_v2_continuation_state (
	continuation_id TEXT PRIMARY KEY REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	last_acknowledged_revision INTEGER NOT NULL CHECK (last_acknowledged_revision >= 0),
	working_snapshot_bytes BLOB NOT NULL CHECK (length(working_snapshot_bytes) > 0),
	updated_at TEXT NOT NULL
)`,
	`CREATE TABLE blackboard_v2_continuation_finishes (
	continuation_id TEXT PRIMARY KEY REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	result_json TEXT NOT NULL,
	finished_at TEXT NOT NULL,
	UNIQUE (project_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_sync_delivery_receipts (
	continuation_id TEXT NOT NULL REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	request_fingerprint TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('claimed', 'finalized')),
	from_revision INTEGER NOT NULL,
	revision INTEGER NOT NULL DEFAULT 0,
	attachment_json TEXT NOT NULL DEFAULT '',
	working_snapshot_bytes BLOB NOT NULL DEFAULT x'',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (continuation_id, request_fingerprint),
	CHECK (
		(status = 'claimed' AND revision = 0 AND attachment_json = '')
		OR (status = 'finalized' AND attachment_json <> '')
	)
)`,
	`CREATE TABLE blackboard_v2_session_state (
	session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL CHECK (revision >= 0)
)`,
	`CREATE TABLE blackboard_v2_session_records (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	type TEXT NOT NULL CHECK (type IN ('entity', 'objective', 'attempt', 'fact')),
	version INTEGER NOT NULL CHECK (version >= 1),
	record_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, key)
)`,
	`CREATE TABLE blackboard_v2_session_record_history (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	type TEXT NOT NULL CHECK (type IN ('entity', 'objective', 'attempt', 'fact')),
	record_json TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (session_id, key, version)
)`,
	`CREATE TABLE blackboard_v2_session_idempotency_receipts (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	idempotency_key TEXT NOT NULL,
	continuation_id TEXT NOT NULL DEFAULT '',
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_session_snapshots (
	session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL CHECK (revision >= 0),
	snapshot_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
	`CREATE TABLE blackboard_v2_session_continuation_pins (
	continuation_id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	launch_revision INTEGER NOT NULL CHECK (launch_revision >= 0),
	launch_snapshot_json TEXT NOT NULL,
	launch_snapshot_sha256 TEXT NOT NULL,
	working_revision INTEGER NOT NULL CHECK (working_revision >= 0),
	working_snapshot_json TEXT NOT NULL,
	working_snapshot_sha256 TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (session_id, continuation_id)
)`,
	`CREATE TABLE blackboard_v2_session_finish_receipts (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	continuation_id TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, continuation_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_session_relationships (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL CHECK (relation IN ('about', 'part_of', 'tests', 'produced', 'evidences', 'supports', 'contradicts', 'derived_from', 'depends_on', 'satisfies', 'supersedes')),
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, from_key, relation, to_key)
)`,
	`CREATE TABLE blackboard_v2_session_relationship_history (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL,
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (session_id, from_key, relation, to_key, version)
)`,
	`CREATE TABLE blackboard_v2_session_key_redirects (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	source_key TEXT NOT NULL,
	canonical_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, source_key),
	UNIQUE (session_id, canonical_key)
)`,
	`CREATE TABLE blackboard_v2_session_attempt_origins (
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	continuation_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, key)
)`,
	`CREATE TABLE blackboard_v2_finish_intents (
	continuation_id TEXT PRIMARY KEY REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	source_turn_id TEXT NOT NULL,
	source_work_watermark INTEGER NOT NULL CHECK (source_work_watermark >= 0),
	invalidated INTEGER NOT NULL DEFAULT 0 CHECK (invalidated IN (0, 1)),
	invalidated_at TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL,
	UNIQUE (project_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_session_finish_intents (
	continuation_id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	source_turn_id TEXT NOT NULL,
	source_work_watermark INTEGER NOT NULL CHECK (source_work_watermark >= 0),
	invalidated INTEGER NOT NULL DEFAULT 0 CHECK (invalidated IN (0, 1)),
	invalidated_at TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL,
	UNIQUE (session_id, idempotency_key)
)`,
	`CREATE TABLE blackboard_v2_operator_attempt_origins (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key),
	CHECK (actor_id <> '')
)`,
	`CREATE TABLE blackboard_v2_operator_evidence_origins (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	actor_id TEXT NOT NULL,
	source_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
	source_continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	source_path TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key, version),
	CHECK (actor_id <> ''),
	CHECK (source_path <> '')
)`,
	`CREATE TABLE blackboard_v2_operator_evidence_requests (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	source_continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, source_task_id, idempotency_key),
	CHECK (idempotency_key <> ''),
	CHECK (request_hash <> ''),
	CHECK (actor_id <> '')
)`,
}
