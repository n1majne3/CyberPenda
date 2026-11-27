package store

var baselineAssetStatements = []string{
	`CREATE TABLE project_assets (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	type TEXT NOT NULL CHECK(type IN ('root_domain','subdomain','ip','service','endpoint')),
	value TEXT NOT NULL,
	normalized_value TEXT NOT NULL,
	scheme TEXT NOT NULL DEFAULT '',
	host TEXT NOT NULL DEFAULT '',
	port INTEGER NOT NULL DEFAULT 0,
	path TEXT NOT NULL DEFAULT '',
	parent_id TEXT NOT NULL DEFAULT '',
	imported_from_project_id TEXT NOT NULL DEFAULT '',
	imported_from_asset_id TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(project_id,type,normalized_value)
)`,
	`CREATE INDEX project_assets_project_parent ON project_assets(project_id,parent_id)`,
	`CREATE INDEX project_assets_project_host ON project_assets(project_id,host)`,
	`CREATE TABLE project_asset_anchors (
	project_id TEXT NOT NULL,
	asset_id TEXT NOT NULL,
	node_key TEXT NOT NULL,
	node_type TEXT NOT NULL CHECK(node_type IN ('goal','step','fact')),
	task_id TEXT NOT NULL DEFAULT '',
	continuation_id TEXT NOT NULL DEFAULT '',
	actor_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	PRIMARY KEY(project_id,asset_id,node_key)
)`,
	`CREATE INDEX project_asset_anchors_node ON project_asset_anchors(project_id,node_key)`,
}
