
export type FGSNode = {
  key: string;
  type: "goal" | "step" | "fact";
  version: number;
  state?: string;
  title?: string;
  action?: string;
  summary?: string;
  body?: string;
  reason?: string;
  success_criteria?: string;
  executor?: string;
  parent_goal?: string;
  goal?: string;
  step?: string;
  facts?: string[];
  outputs?: string[];
  inputs?: string[];
};

export type FGSEdge = { from: string; to: string; relation: string };

export type FGSGraph = {
  revision: number;
  next_cursor?: string;
  nodes: FGSNode[];
  edges: FGSEdge[];
};

export type FGSStatus = {
  action_required: number;
  last_accepted_at?: string;
  receipts: { id: string; continuation_id: string; state: string; revision?: number; message?: string }[];
};

export type FGSReport = { revision: number; markdown: string };
