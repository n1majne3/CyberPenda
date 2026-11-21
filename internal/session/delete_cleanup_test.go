package session

import (
	"testing"

	"pentest/internal/store"
)

func TestDeleteRemovesSessionOwnedRowsAcrossAuxiliaryTables(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service := NewService(db, t.TempDir())
	target, err := service.Create(CreateRequest{Input: "session to delete"})
	if err != nil {
		t.Fatalf("create target session: %v", err)
	}
	if _, err := service.Archive(target.ID); err != nil {
		t.Fatalf("archive target session: %v", err)
	}
	survivor, err := service.Create(CreateRequest{Input: "session to keep"})
	if err != nil {
		t.Fatalf("create survivor session: %v", err)
	}

	seedSessionAuxiliaryRows(t, db, "session", target.ID, "target")
	seedSessionAuxiliaryRows(t, db, "session", survivor.ID, "survivor")
	seedNonSessionAuxiliaryRows(t, db, target.ID)

	if err := service.Delete(target.ID); err != nil {
		t.Fatalf("delete target session: %v", err)
	}

	sessionScopedChecks := []struct {
		name  string
		query string
	}{
		{"child_history_context", `SELECT COUNT(*) FROM child_history_context WHERE owner_kind='session' AND owner_id=?`},
		{"child_history_blocks", `SELECT COUNT(*) FROM child_history_blocks WHERE owner_kind='session' AND owner_id=?`},
		{"child_history_aliases", `SELECT COUNT(*) FROM child_history_aliases WHERE owner_kind='session' AND owner_id=?`},
		{"child_history_observations", `SELECT COUNT(*) FROM child_history_observations WHERE owner_kind='session' AND owner_id=?`},
		{"child_history_items", `SELECT COUNT(*) FROM child_history_items WHERE owner_kind='session' AND owner_id=?`},
		{"child_history_changes", `SELECT COUNT(*) FROM child_history_changes WHERE owner_kind='session' AND owner_id=?`},
		{"fgs_boards", `SELECT COUNT(*) FROM fgs_boards WHERE kind='session' AND owner_id=?`},
		{"fgs_nodes", `SELECT COUNT(*) FROM fgs_nodes WHERE board_kind='session' AND board_id=?`},
		{"fgs_history", `SELECT COUNT(*) FROM fgs_history WHERE board_kind='session' AND board_id=?`},
		{"fgs_receipts", `SELECT COUNT(*) FROM fgs_receipts WHERE owner_kind='session' AND owner_id=?`},
		{"fgs_outbox_inventory", `SELECT COUNT(*) FROM fgs_outbox_inventory WHERE owner_kind='session' AND owner_id=?`},
		{"accepted_steering", `SELECT COUNT(*) FROM accepted_steering WHERE owner_kind='session' AND owner_id=?`},
		{"working_graph_intents", `SELECT COUNT(*) FROM working_graph_intents WHERE owner_kind='session' AND owner_id=?`},
		{"session_pending_blackboard_conclusions", `SELECT COUNT(*) FROM session_pending_blackboard_conclusions WHERE session_id=?`},
		{"session_conclusion_dispatches", `SELECT COUNT(*) FROM session_conclusion_dispatches WHERE obligation_id IN ('obl-target')`},
	}

	for _, check := range sessionScopedChecks {
		var targetCount int
		if check.name == "session_conclusion_dispatches" {
			if err := db.QueryRow(check.query).Scan(&targetCount); err != nil {
				t.Fatalf("count target %s: %v", check.name, err)
			}
		} else {
			if err := db.QueryRow(check.query, target.ID).Scan(&targetCount); err != nil {
				t.Fatalf("count target %s: %v", check.name, err)
			}
		}
		if targetCount != 0 {
			t.Errorf("%s: expected 0 rows for deleted session, got %d", check.name, targetCount)
		}
		if check.name == "session_conclusion_dispatches" {
			var survivorCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM session_conclusion_dispatches WHERE obligation_id='obl-survivor'`).Scan(&survivorCount); err != nil {
				t.Fatalf("count survivor %s: %v", check.name, err)
			}
			if survivorCount != 1 {
				t.Errorf("%s: expected 1 row for survivor session, got %d", check.name, survivorCount)
			}
			continue
		}
		var survivorCount int
		if err := db.QueryRow(check.query, survivor.ID).Scan(&survivorCount); err != nil {
			t.Fatalf("count survivor %s: %v", check.name, err)
		}
		if survivorCount != 1 {
			t.Errorf("%s: expected 1 row for survivor session, got %d", check.name, survivorCount)
		}
	}

	nonSessionChecks := []struct {
		name  string
		query string
	}{
		{"child_history_context(task)", `SELECT COUNT(*) FROM child_history_context WHERE owner_kind='task' AND owner_id=?`},
		{"child_history_blocks(task)", `SELECT COUNT(*) FROM child_history_blocks WHERE owner_kind='task' AND owner_id=?`},
		{"child_history_aliases(task)", `SELECT COUNT(*) FROM child_history_aliases WHERE owner_kind='task' AND owner_id=?`},
		{"child_history_observations(task)", `SELECT COUNT(*) FROM child_history_observations WHERE owner_kind='task' AND owner_id=?`},
		{"child_history_items(task)", `SELECT COUNT(*) FROM child_history_items WHERE owner_kind='task' AND owner_id=?`},
		{"child_history_changes(task)", `SELECT COUNT(*) FROM child_history_changes WHERE owner_kind='task' AND owner_id=?`},
		{"fgs_boards(project)", `SELECT COUNT(*) FROM fgs_boards WHERE kind='project' AND owner_id=?`},
		{"fgs_nodes(project)", `SELECT COUNT(*) FROM fgs_nodes WHERE board_kind='project' AND board_id=?`},
		{"fgs_history(project)", `SELECT COUNT(*) FROM fgs_history WHERE board_kind='project' AND board_id=?`},
		{"fgs_receipts(task)", `SELECT COUNT(*) FROM fgs_receipts WHERE owner_kind='task' AND owner_id=?`},
		{"fgs_outbox_inventory(task)", `SELECT COUNT(*) FROM fgs_outbox_inventory WHERE owner_kind='task' AND owner_id=?`},
		{"accepted_steering(task)", `SELECT COUNT(*) FROM accepted_steering WHERE owner_kind='task' AND owner_id=?`},
		{"working_graph_intents(task)", `SELECT COUNT(*) FROM working_graph_intents WHERE owner_kind='task' AND owner_id=?`},
	}
	for _, check := range nonSessionChecks {
		var count int
		if err := db.QueryRow(check.query, target.ID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 1 {
			t.Errorf("%s: expected 1 non-session row preserved, got %d", check.name, count)
		}
	}
}

func seedSessionAuxiliaryRows(t *testing.T, db *store.DB, ownerKind, ownerID, suffix string) {
	t.Helper()
	now := "2026-11-21T00:00:00Z"
	stmts := []struct {
		query string
		args  []any
	}{
		{`INSERT OR REPLACE INTO child_history_context (owner_kind, owner_id, continuation, adapter) VALUES (?, ?, 1, 'codex')`, []any{ownerKind, ownerID}},
		{`INSERT INTO child_history_blocks (owner_kind, owner_id, child_id, summary_json) VALUES (?, ?, 'child-1', '{}')`, []any{ownerKind, ownerID}},
		{`INSERT INTO child_history_aliases (owner_kind, owner_id, alias, child_id) VALUES (?, ?, 'alias-1', 'child-1')`, []any{ownerKind, ownerID}},
		{`INSERT INTO child_history_observations (owner_kind, owner_id, child_id, event_seq) VALUES (?, ?, 'child-1', 1)`, []any{ownerKind, ownerID}},
		{`INSERT INTO child_history_items (owner_kind, owner_id, child_id, item_id, first_seq) VALUES (?, ?, 'child-1', 'item-1', 1)`, []any{ownerKind, ownerID}},
		{`INSERT INTO child_history_changes (owner_kind, owner_id, child_id, item_id, event_seq, entry_json, preview_json, raw_preview_json, base_cursor) VALUES (?, ?, 'child-1', 'item-1', 1, '{}', '{}', '{}', 0)`, []any{ownerKind, ownerID}},
		{`INSERT INTO fgs_boards (kind, owner_id, revision) VALUES (?, ?, 1)`, []any{ownerKind, ownerID}},
		{`INSERT INTO fgs_nodes (board_kind, board_id, node_key, body_json) VALUES (?, ?, 'goal-1', '{}')`, []any{ownerKind, ownerID}},
		{`INSERT INTO fgs_history (board_kind, board_id, node_key, version, body_json) VALUES (?, ?, 'goal-1', 1, '{}')`, []any{ownerKind, ownerID}},
		{`INSERT INTO fgs_receipts (owner_kind, owner_id, continuation_id, intent_id, sequence, request_hash, payload_json, receipt_json) VALUES (?, ?, 'cont-1', 'intent-1', 1, 'hash', '{}', '{}')`, []any{ownerKind, ownerID}},
		{`INSERT INTO fgs_outbox_inventory (owner_kind, owner_id, continuation_id, name) VALUES (?, ?, 'cont-1', '0001.json')`, []any{ownerKind, ownerID}},
		{`INSERT INTO accepted_steering (id, owner_kind, owner_id, request_id, message, mode, state, queue_order, created_at, updated_at) VALUES (?, ?, ?, 'req-1', 'steer', 'send_turn', 'applied', 1, ?, ?)`, []any{"steer-" + suffix, ownerKind, ownerID, now, now}},
		{`INSERT INTO working_graph_intents (id, owner_kind, owner_id, continuation_id, intent_id, sequence, request_hash, kind, state, created_at, updated_at) VALUES (?, ?, ?, 'cont-1', 'intent-1', 1, 'hash', 'semantic_changes', 'applied', ?, ?)`, []any{"wgi-" + suffix, ownerKind, ownerID, now, now}},
		{`INSERT INTO session_pending_blackboard_conclusions (id, session_id, source_request_id, source_request_correlation_exact, source_continuation_id, source_session_id, source_turn_id, state, source_work_watermark, semantic_persistence_watermark, created_at, updated_at) VALUES (?, ?, 'req-1', 1, 'cont-1', 'src-sess', 'turn-1', 'clean', 1, 1, ?, ?)`, []any{"obl-" + suffix, ownerID, now, now}},
		{`INSERT INTO session_conclusion_dispatches (id, obligation_id, kind, continuation_id, source_session_id, delivery_state, created_at, updated_at) VALUES (?, ?, 'initial', 'cont-1', 'src-sess', 'dispatch_requested', ?, ?)`, []any{"disp-" + suffix, "obl-" + suffix, now, now}},
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed %s: %v", stmt.query, err)
		}
	}
}

func seedNonSessionAuxiliaryRows(t *testing.T, db *store.DB, sameID string) {
	t.Helper()
	now := "2026-11-21T00:00:00Z"
	stmts := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO child_history_context (owner_kind, owner_id, continuation, adapter) VALUES ('task', ?, 1, 'codex')`, []any{sameID}},
		{`INSERT INTO child_history_blocks (owner_kind, owner_id, child_id, summary_json) VALUES ('task', ?, 'child-1', '{}')`, []any{sameID}},
		{`INSERT INTO child_history_aliases (owner_kind, owner_id, alias, child_id) VALUES ('task', ?, 'alias-1', 'child-1')`, []any{sameID}},
		{`INSERT INTO child_history_observations (owner_kind, owner_id, child_id, event_seq) VALUES ('task', ?, 'child-1', 1)`, []any{sameID}},
		{`INSERT INTO child_history_items (owner_kind, owner_id, child_id, item_id, first_seq) VALUES ('task', ?, 'child-1', 'item-1', 1)`, []any{sameID}},
		{`INSERT INTO child_history_changes (owner_kind, owner_id, child_id, item_id, event_seq, entry_json, preview_json, raw_preview_json, base_cursor) VALUES ('task', ?, 'child-1', 'item-1', 1, '{}', '{}', '{}', 0)`, []any{sameID}},
		{`INSERT INTO fgs_boards (kind, owner_id, revision) VALUES ('project', ?, 1)`, []any{sameID}},
		{`INSERT INTO fgs_nodes (board_kind, board_id, node_key, body_json) VALUES ('project', ?, 'goal-1', '{}')`, []any{sameID}},
		{`INSERT INTO fgs_history (board_kind, board_id, node_key, version, body_json) VALUES ('project', ?, 'goal-1', 1, '{}')`, []any{sameID}},
		{`INSERT INTO fgs_receipts (owner_kind, owner_id, continuation_id, intent_id, sequence, request_hash, payload_json, receipt_json) VALUES ('task', ?, 'cont-1', 'intent-1', 1, 'hash', '{}', '{}')`, []any{sameID}},
		{`INSERT INTO fgs_outbox_inventory (owner_kind, owner_id, continuation_id, name) VALUES ('task', ?, 'cont-1', '0001.json')`, []any{sameID}},
		{`INSERT INTO accepted_steering (id, owner_kind, owner_id, request_id, message, mode, state, queue_order, created_at, updated_at) VALUES ('steer-task', 'task', ?, 'req-1', 'steer', 'send_turn', 'applied', 1, ?, ?)`, []any{sameID, now, now}},
		{`INSERT INTO working_graph_intents (id, owner_kind, owner_id, continuation_id, intent_id, sequence, request_hash, kind, state, created_at, updated_at) VALUES ('wgi-task', 'task', ?, 'cont-1', 'intent-1', 1, 'hash', 'semantic_changes', 'applied', ?, ?)`, []any{sameID, now, now}},
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed non-session %s: %v", stmt.query, err)
		}
	}
}
