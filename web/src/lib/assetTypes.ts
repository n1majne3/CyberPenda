export type AssetType = "root_domain" | "subdomain" | "ip" | "service" | "endpoint";
export type AssetScopeStatus = "in_scope" | "out_of_scope" | "excluded";

export interface AssetAnchor {
  project_id: string;
  asset_id: string;
  node_key: string;
  node_type: "goal" | "step" | "fact" | string;
  task_id?: string;
  continuation_id?: string;
  actor_id?: string;
  created_at: string;
}

export interface ProjectAsset {
  id: string;
  project_id: string;
  type: AssetType;
  value: string;
  normalized_value: string;
  scheme?: string;
  host?: string;
  port?: number;
  path?: string;
  parent_id?: string;
  imported_from_project_id?: string;
  imported_from_asset_id?: string;
  first_seen_at: string;
  last_seen_at: string;
  created_at: string;
  updated_at: string;
  scope_status: AssetScopeStatus;
  covered: boolean;
  goal_count: number;
  step_count: number;
  fact_count: number;
  anchors: AssetAnchor[];
}

export interface AssetCoverage {
  schema: string;
  project_id: string;
  total_assets: number;
  in_scope_assets: number;
  covered_in_scope_assets: number;
  coverage_percent: number;
  out_of_scope_assets: number;
  excluded_assets: number;
  uncovered_in_scope_asset_ids: string[];
}

export interface AssetBatchResult {
  assets: ProjectAsset[];
  created: number;
  updated: number;
}
