package store

import "database/sql"

// The index starts with new Events. Historical records keep their old projection.
const migration78SQL = `
CREATE TABLE IF NOT EXISTS child_history_context (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, continuation INTEGER NOT NULL, adapter TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id)
);
CREATE TABLE IF NOT EXISTS child_history_blocks (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, summary_json TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,child_id)
);
CREATE TABLE IF NOT EXISTS child_history_aliases (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, alias TEXT NOT NULL, child_id TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,alias)
);
CREATE TABLE IF NOT EXISTS child_history_observations (
 owner_kind TEXT NOT NULL,owner_id TEXT NOT NULL,child_id TEXT NOT NULL,event_seq INTEGER NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,event_seq,child_id)
);
CREATE TABLE IF NOT EXISTS child_history_items (
 position INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, item_id TEXT NOT NULL, first_seq INTEGER NOT NULL,
 UNIQUE(owner_kind,owner_id,child_id,item_id)
);
CREATE INDEX IF NOT EXISTS child_history_items_page ON child_history_items(owner_kind,owner_id,child_id,position);
CREATE INDEX IF NOT EXISTS child_history_items_event ON child_history_items(owner_kind,owner_id,child_id,first_seq,position);
CREATE TABLE IF NOT EXISTS child_history_changes (
 cursor INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, item_id TEXT NOT NULL,
 event_seq INTEGER NOT NULL, entry_json TEXT NOT NULL, preview_json TEXT NOT NULL, raw_preview_json TEXT NOT NULL, base_cursor INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS child_history_changes_page ON child_history_changes(owner_kind,owner_id,child_id,cursor);
CREATE INDEX IF NOT EXISTS child_history_changes_item ON child_history_changes(owner_kind,owner_id,child_id,item_id,event_seq DESC,cursor DESC);
CREATE INDEX IF NOT EXISTS child_history_changes_detail ON child_history_changes(owner_kind,owner_id,child_id,item_id,cursor);
CREATE INDEX IF NOT EXISTS child_history_changes_owner_event ON child_history_changes(owner_kind,owner_id,event_seq);
CREATE INDEX IF NOT EXISTS child_history_changes_event ON child_history_changes(owner_kind,owner_id,child_id,event_seq,cursor);
`

func migration78Up(tx *sql.Tx) error { return execStatements(tx, migration78SQL) }
