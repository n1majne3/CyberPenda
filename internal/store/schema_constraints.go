package store

// baselineConstraintStatements holds part of the baseline schema.
var baselineConstraintStatements = []string{
	`CREATE INDEX idx_blackboard_continuation_grants_continuation
	ON blackboard_continuation_grants (continuation_id)`,
	`CREATE INDEX idx_blackboard_v2_attempt_origins_continuation
	ON blackboard_v2_attempt_origins (continuation_id)`,
	`CREATE INDEX idx_blackboard_v2_evidence_requests_managed_path
	ON blackboard_v2_evidence_requests (project_id, managed_internal_path)`,
	`CREATE INDEX idx_blackboard_v2_key_redirects_canonical
	ON blackboard_v2_key_redirects (project_id, canonical_key)`,
	`CREATE TRIGGER blackboard_v2_continuation_pins_no_update BEFORE UPDATE ON blackboard_v2_continuation_pins BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Launch Pin is immutable'); END`,
	`CREATE TRIGGER blackboard_v2_continuation_pins_no_delete BEFORE DELETE ON blackboard_v2_continuation_pins BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Launch Pin is immutable'); END`,
	`CREATE TRIGGER blackboard_v2_continuation_state_monotonic BEFORE UPDATE ON blackboard_v2_continuation_state WHEN NEW.continuation_id <> OLD.continuation_id OR NEW.last_acknowledged_revision < OLD.last_acknowledged_revision BEGIN SELECT RAISE(ABORT, 'Blackboard v2 acknowledged revision cannot move backwards'); END`,
	`CREATE TRIGGER blackboard_v2_continuation_finishes_no_update BEFORE UPDATE ON blackboard_v2_continuation_finishes BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish receipt is immutable'); END`,
	`CREATE TRIGGER blackboard_v2_continuation_finishes_no_delete BEFORE DELETE ON blackboard_v2_continuation_finishes BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish receipt is immutable'); END`,
	`CREATE UNIQUE INDEX ux_blackboard_v2_sync_delivery_open_claim
	ON blackboard_v2_sync_delivery_receipts (continuation_id)
	WHERE status = 'claimed'`,
	`CREATE INDEX assisted_conclusion_receipts_task_created
	ON assisted_conclusion_receipts(task_id, created_at DESC)`,
	`CREATE INDEX idx_sessions_lifecycle_activity
	ON sessions(lifecycle, last_activity_at DESC, created_at DESC)`,
	`CREATE INDEX idx_session_events_session_seq
	ON session_events(session_id, seq ASC)`,
	`CREATE INDEX idx_session_runtime_configs_session_version
	ON session_runtime_config_versions(session_id, version ASC)`,
	`CREATE INDEX idx_session_continuations_session_number
	ON session_continuations(session_id, number DESC)`,
	`CREATE INDEX idx_session_continuations_active
	ON session_continuations(session_id, status)`,
	`CREATE INDEX idx_blackboard_v2_session_history_key
	ON blackboard_v2_session_record_history(session_id, key, version ASC)`,
	`CREATE INDEX idx_blackboard_v2_session_relationship_history_key
	ON blackboard_v2_session_relationship_history(session_id, from_key, relation, to_key, version ASC)`,
	`CREATE TRIGGER trg_blackboard_v2_session_delete_continuations
		 AFTER DELETE ON sessions
		 BEGIN
		  DELETE FROM session_continuations WHERE session_id=OLD.id;
		 END`,
	`CREATE TRIGGER trg_blackboard_v2_session_continuation_pin_launch_immutable
		 BEFORE UPDATE OF launch_revision, launch_snapshot_json, launch_snapshot_sha256
		 ON blackboard_v2_session_continuation_pins
		 BEGIN
		  SELECT RAISE(ABORT, 'Session launch pin is immutable');
		 END`,
	`CREATE INDEX idx_session_assisted_conclusion_session_created
    ON session_assisted_conclusion_receipts(session_id, created_at DESC)`,
	`CREATE INDEX idx_session_assisted_conclusion_dispatch
    ON session_assisted_conclusion_receipts(dispatch_request_id)`,
	`CREATE INDEX idx_session_continuation_interface_grants_continuation
	ON session_continuation_interface_grants (continuation_id)`,
	`CREATE INDEX idx_tasks_project_activity
	ON tasks(project_id, deleted_at, updated_at DESC, created_at DESC)`,
	`CREATE INDEX idx_tasks_updated_at ON tasks(updated_at)`,
	`CREATE INDEX idx_pending_blackboard_conclusions_task_created
	ON pending_blackboard_conclusions(task_id, created_at DESC)`,
	`CREATE UNIQUE INDEX idx_conclusion_dispatches_one_active
	ON conclusion_dispatches(obligation_id)
	WHERE delivery_state IN ('dispatch_requested','awaiting_result','validated')`,
	`CREATE INDEX idx_conclusion_dispatches_obligation
	ON conclusion_dispatches(obligation_id)`,
	`CREATE INDEX idx_session_pending_blackboard_conclusions_created
	ON session_pending_blackboard_conclusions(session_id, created_at DESC)`,
	`CREATE UNIQUE INDEX idx_session_conclusion_dispatches_one_active
	ON session_conclusion_dispatches(obligation_id)
	WHERE delivery_state IN ('dispatch_requested','awaiting_result','validated')`,
	`CREATE INDEX idx_session_conclusion_dispatches_obligation
	ON session_conclusion_dispatches(obligation_id)`,
	`CREATE TRIGGER blackboard_v2_finish_intents_immutable_identity BEFORE UPDATE ON blackboard_v2_finish_intents WHEN NEW.continuation_id <> OLD.continuation_id OR NEW.project_id <> OLD.project_id OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.request_hash <> OLD.request_hash OR NEW.source_turn_id <> OLD.source_turn_id OR NEW.source_work_watermark <> OLD.source_work_watermark OR NEW.recorded_at <> OLD.recorded_at BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish Intent identity is immutable'); END`,
	`CREATE TRIGGER blackboard_v2_finish_intents_invalidation_forward BEFORE UPDATE ON blackboard_v2_finish_intents WHEN NEW.invalidated < OLD.invalidated BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish Intent invalidation cannot be cleared'); END`,
	`CREATE TRIGGER blackboard_v2_finish_intents_no_delete_valid BEFORE DELETE ON blackboard_v2_finish_intents WHEN OLD.invalidated = 0 BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish Intent cannot be deleted while valid'); END`,
	`CREATE TRIGGER blackboard_v2_session_finish_intents_immutable_identity BEFORE UPDATE ON blackboard_v2_session_finish_intents WHEN NEW.continuation_id <> OLD.continuation_id OR NEW.session_id <> OLD.session_id OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.request_hash <> OLD.request_hash OR NEW.source_turn_id <> OLD.source_turn_id OR NEW.source_work_watermark <> OLD.source_work_watermark OR NEW.recorded_at <> OLD.recorded_at BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Session Finish Intent identity is immutable'); END`,
	`CREATE TRIGGER blackboard_v2_session_finish_intents_invalidation_forward BEFORE UPDATE ON blackboard_v2_session_finish_intents WHEN NEW.invalidated < OLD.invalidated BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Session Finish Intent invalidation cannot be cleared'); END`,
	`CREATE TRIGGER blackboard_v2_session_finish_intents_no_delete_valid BEFORE DELETE ON blackboard_v2_session_finish_intents WHEN OLD.invalidated = 0 BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Session Finish Intent cannot be deleted while valid'); END`,
	`CREATE INDEX idx_challenge_attempts_task_status
	ON challenge_attempts (task_id, status, created_at)`,
	`CREATE INDEX idx_challenge_operations_recovery ON challenge_operations (state, updated_at)`,
	`CREATE INDEX scope_expansions_project_status_idx ON scope_expansions(project_id,status,created_at)`,
	`CREATE INDEX reason_tasks_project_created_idx ON reason_tasks(project_id,created_at)`,
	`CREATE INDEX reason_task_proposals_project_status_idx ON reason_task_proposals(project_id,status,created_at)`,
	`CREATE INDEX idx_accepted_steering_owner_queue
	ON accepted_steering(owner_kind, owner_id, queue_order ASC)`,
	`CREATE INDEX idx_accepted_steering_state
	ON accepted_steering(state ASC)`,
	`CREATE INDEX idx_blackboard_v2_operator_evidence_source_task
	ON blackboard_v2_operator_evidence_origins (source_task_id)`,
	`CREATE INDEX idx_blackboard_v2_operator_evidence_request_continuation
	ON blackboard_v2_operator_evidence_requests (source_continuation_id)`,
	`CREATE INDEX idx_working_graph_intents_settlement
 ON working_graph_intents(owner_kind,owner_id,continuation_id,state,sequence)`,
	`CREATE INDEX child_history_items_page ON child_history_items(owner_kind,owner_id,child_id,position)`,
	`CREATE INDEX child_history_items_event ON child_history_items(owner_kind,owner_id,child_id,first_seq,position)`,
	`CREATE INDEX child_history_changes_page ON child_history_changes(owner_kind,owner_id,child_id,cursor)`,
	`CREATE INDEX child_history_changes_item ON child_history_changes(owner_kind,owner_id,child_id,item_id,event_seq DESC,cursor DESC)`,
	`CREATE INDEX child_history_changes_detail ON child_history_changes(owner_kind,owner_id,child_id,item_id,cursor)`,
	`CREATE INDEX child_history_changes_owner_event ON child_history_changes(owner_kind,owner_id,event_seq)`,
	`CREATE INDEX child_history_changes_event ON child_history_changes(owner_kind,owner_id,child_id,event_seq,cursor)`,
}
