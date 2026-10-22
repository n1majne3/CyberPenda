package store

// baselineFGSStatements holds part of the baseline schema.
var baselineFGSStatements = []string{
	`CREATE TABLE fgs_boards (
 kind TEXT NOT NULL CHECK(kind IN ('project','session')),
 owner_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(kind,owner_id)
)`,
	`CREATE TABLE fgs_nodes (
 board_kind TEXT NOT NULL,
 board_id TEXT NOT NULL,
 node_key TEXT NOT NULL,
 body_json TEXT NOT NULL,
 PRIMARY KEY(board_kind,board_id,node_key)
)`,
	`CREATE TABLE fgs_history (
 board_kind TEXT NOT NULL,
 board_id TEXT NOT NULL,
 node_key TEXT NOT NULL,
 version INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 PRIMARY KEY(board_kind,board_id,node_key,version)
)`,
	`CREATE TABLE fgs_receipts (
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
)`,
	`CREATE TABLE fgs_outbox_inventory (
 owner_kind TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 continuation_id TEXT NOT NULL,
 name TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,continuation_id,name)
)`,
	`CREATE TABLE child_history_context (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, continuation INTEGER NOT NULL, adapter TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id)
)`,
	`CREATE TABLE child_history_blocks (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, summary_json TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,child_id)
)`,
	`CREATE TABLE child_history_aliases (
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, alias TEXT NOT NULL, child_id TEXT NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,alias)
)`,
	`CREATE TABLE child_history_observations (
 owner_kind TEXT NOT NULL,owner_id TEXT NOT NULL,child_id TEXT NOT NULL,event_seq INTEGER NOT NULL,
 PRIMARY KEY(owner_kind,owner_id,event_seq,child_id)
)`,
	`CREATE TABLE child_history_items (
 position INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, item_id TEXT NOT NULL, first_seq INTEGER NOT NULL,
 UNIQUE(owner_kind,owner_id,child_id,item_id)
)`,
	`CREATE TABLE child_history_changes (
 cursor INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_kind TEXT NOT NULL, owner_id TEXT NOT NULL, child_id TEXT NOT NULL, item_id TEXT NOT NULL,
 event_seq INTEGER NOT NULL, entry_json TEXT NOT NULL, preview_json TEXT NOT NULL, raw_preview_json TEXT NOT NULL, base_cursor INTEGER NOT NULL
)`,
}
