export interface Dependency {
  issue_id: string;
  depends_on_id: string;
  type: "parent-child" | "blocks" | "blocked-by" | string;
  created_at: string;
  created_by: string;
  metadata: string;
}

export interface BeadMetadata {
  ibead?: boolean;
  domain?: string;
  depth?: number;
  gitnexus_pass?: number;
  generated_by?: string;
  parent_title?: string;
  task_list?: string[];
  [key: string]: unknown;
}

export interface Bead {
  id: string;
  title: string;
  description?: string;
  acceptance_criteria?: string;
  notes?: string;
  status: BeadStatus;
  priority: number;
  issue_type: string;
  assignee?: string;
  owner?: string;
  created_at: string;
  created_by?: string;
  updated_at: string;
  labels: string[];
  dependencies: Dependency[];
  dependency_count: number;
  dependent_count: number;
  comment_count: number;
  parent?: string;
  metadata?: BeadMetadata;
}

export type BeadStatus = "open" | "in_progress" | "closed" | "blocked" | "deferred" | string;

export interface Stats {
  total_issues: number;
  open_issues: number;
  in_progress_issues: number;
  closed_issues: number;
  blocked_issues: number;
  ready_issues?: number;
}
