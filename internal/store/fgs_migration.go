package store

import "database/sql"

// FGS uses separate tables. This migration does not convert legacy records.
const migration74SQL = `
CREATE TABLE IF NOT EXISTS fgs_boards (
 kind TEXT NOT NULL CHECK(kind IN ('project','session')),
 owner_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(kind,owner_id)
);
CREATE TABLE IF NOT EXISTS fgs_nodes (
 board_kind TEXT NOT NULL,
 board_id TEXT NOT NULL,
 node_key TEXT NOT NULL,
 body_json TEXT NOT NULL,
 PRIMARY KEY(board_kind,board_id,node_key)
);
CREATE TABLE IF NOT EXISTS fgs_history (
 board_kind TEXT NOT NULL,
 board_id TEXT NOT NULL,
 node_key TEXT NOT NULL,
 version INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 PRIMARY KEY(board_kind,board_id,node_key,version)
);
CREATE TABLE IF NOT EXISTS fgs_receipts (
 owner_kind TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 continuation_id TEXT NOT NULL,
 intent_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 request_hash TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 receipt_json TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,continuation_id,intent_id),
 UNIQUE(owner_kind,owner_id,continuation_id,sequence)
);
`

func migration74Up(tx *sql.Tx) error { return execStatements(tx, migration74SQL) }

const migration75SQL = `ALTER TABLE projects ADD COLUMN blackboard_protocol TEXT NOT NULL DEFAULT 'legacy'; ALTER TABLE sessions ADD COLUMN blackboard_protocol TEXT NOT NULL DEFAULT 'legacy';`

func migration75Up(tx *sql.Tx) error {
	for _, table := range []string{"projects", "sessions"} {
		if err := ensureColumn(tx, table, "blackboard_protocol", "TEXT NOT NULL DEFAULT 'legacy'"); err != nil {
			return err
		}
	}
	return nil
}
