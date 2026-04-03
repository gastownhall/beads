package dolt

import (
	"strings"
)

// currentSchemaVersion is bumped whenever the schema or migrations change.
// initSchemaOnDB checks this against the stored version and skips re-initialization
// when they match, avoiding ~20 DDL statements per bd invocation.
const currentSchemaVersion = 11

// schema defines the MySQL-compatible database schema for Dolt.
const schema = `
-- Issues table
CREATE TABLE IF NOT EXISTS issues (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    design TEXT NOT NULL,
    acceptance_criteria TEXT NOT NULL,
    notes TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    estimated_minutes INT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    owner VARCHAR(255) DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session VARCHAR(255) DEFAULT '',
    external_ref VARCHAR(255),
    spec_id VARCHAR(1024),
    compaction_level INT DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit VARCHAR(64),
    original_size INT,
    -- Messaging fields
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    -- NoHistory: stored in wisps table but NOT GC-eligible (gh-2619)
    no_history TINYINT(1) DEFAULT 0,
    -- Wisp classification for TTL-based compaction (gt-9br)
    wisp_type VARCHAR(32) DEFAULT '',
    -- Pinned field
    pinned TINYINT(1) DEFAULT 0,
    -- Template field
    is_template TINYINT(1) DEFAULT 0,
    -- Molecule type field
    mol_type VARCHAR(32) DEFAULT '',
    -- Work type field (mutex vs open_competition)
    work_type VARCHAR(32) DEFAULT 'mutex',
    -- Federation source system field
    source_system VARCHAR(255) DEFAULT '',
    -- Custom metadata field (GH#1406)
    metadata JSON DEFAULT (JSON_OBJECT()),
    -- Source repo for multi-repo
    source_repo VARCHAR(512) DEFAULT '',
    -- Close reason
    close_reason TEXT DEFAULT '',
    -- Event fields
    event_kind VARCHAR(32) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    target VARCHAR(255) DEFAULT '',
    payload TEXT DEFAULT '',
    -- Gate fields
    await_type VARCHAR(32) DEFAULT '',
    await_id VARCHAR(255) DEFAULT '',
    timeout_ns BIGINT DEFAULT 0,
    waiters TEXT DEFAULT '',
    -- Time-based scheduling fields
    due_at DATETIME,
    defer_until DATETIME,
    INDEX idx_issues_status (status),
    INDEX idx_issues_priority (priority),
    INDEX idx_issues_issue_type (issue_type),
    INDEX idx_issues_assignee (assignee),
    INDEX idx_issues_created_at (created_at),
    INDEX idx_issues_spec_id (spec_id),
    INDEX idx_issues_external_ref (external_ref)
);

-- Dependencies table (edge schema)
-- Note: No FK on depends_on_id to allow external references (external:<rig>:<id>).
CREATE TABLE IF NOT EXISTS dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) NOT NULL,
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (issue_id, depends_on_id),
    INDEX idx_dependencies_issue (issue_id),
    INDEX idx_dependencies_depends_on (depends_on_id),
    INDEX idx_dependencies_depends_on_type (depends_on_id, type),
    INDEX idx_dependencies_thread (thread_id),
    CONSTRAINT fk_dep_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Labels table
CREATE TABLE IF NOT EXISTS labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_labels_label (label),
    CONSTRAINT fk_labels_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Comments table
CREATE TABLE IF NOT EXISTS comments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    author VARCHAR(255) NOT NULL,
    text TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_comments_issue (issue_id),
    INDEX idx_comments_created_at (created_at),
    CONSTRAINT fk_comments_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Events table (audit trail)
CREATE TABLE IF NOT EXISTS events (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value TEXT,
    new_value TEXT,
    comment TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_events_issue (issue_id),
    INDEX idx_events_created_at (created_at),
    CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Config table
CREATE TABLE IF NOT EXISTS config (
    ` + "`key`" + ` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL
);

-- Metadata table
CREATE TABLE IF NOT EXISTS metadata (
    ` + "`key`" + ` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL
);
-- Child counters table
-- No FK constraint: parent_id may reference issues or wisps (agent beads were
-- migrated to wisps in 007_infra_to_wisps but this FK was not updated).
CREATE TABLE IF NOT EXISTS child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0
);

-- Issue snapshots table (for compaction)
CREATE TABLE IF NOT EXISTS issue_snapshots (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    snapshot_time DATETIME NOT NULL,
    compaction_level INT NOT NULL,
    original_size INT NOT NULL,
    compressed_size INT NOT NULL,
    original_content TEXT NOT NULL,
    archived_events TEXT,
    INDEX idx_snapshots_issue (issue_id),
    INDEX idx_snapshots_level (compaction_level),
    CONSTRAINT fk_snapshots_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Compaction snapshots table
CREATE TABLE IF NOT EXISTS compaction_snapshots (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    compaction_level INT NOT NULL,
    snapshot_json BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_comp_snap_issue (issue_id, compaction_level, created_at DESC),
    CONSTRAINT fk_comp_snap_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Repository mtimes table (for multi-repo)
CREATE TABLE IF NOT EXISTS repo_mtimes (
    repo_path VARCHAR(512) PRIMARY KEY,
    jsonl_path VARCHAR(512) NOT NULL,
    mtime_ns BIGINT NOT NULL,
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_repo_mtimes_checked (last_checked)
);

-- Routes table (prefix-to-path routing configuration)
CREATE TABLE IF NOT EXISTS routes (
    prefix VARCHAR(32) PRIMARY KEY,
    path VARCHAR(512) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

-- Issue counter table (for issue_id_mode=counter sequential IDs, GH#2002)
CREATE TABLE IF NOT EXISTS issue_counter (
    prefix VARCHAR(255) PRIMARY KEY,
    last_id INT NOT NULL DEFAULT 0
);

-- Interactions table (agent audit log)
CREATE TABLE IF NOT EXISTS interactions (
    id VARCHAR(32) PRIMARY KEY,
    kind VARCHAR(64) NOT NULL,
    created_at DATETIME NOT NULL,
    actor VARCHAR(255),
    issue_id VARCHAR(255),
    model VARCHAR(255),
    prompt TEXT,
    response TEXT,
    error TEXT,
    tool_name VARCHAR(255),
    exit_code INT,
    parent_id VARCHAR(32),
    label VARCHAR(64),
    reason TEXT,
    extra JSON,
    INDEX idx_interactions_kind (kind),
    INDEX idx_interactions_created_at (created_at),
    INDEX idx_interactions_issue_id (issue_id),
    INDEX idx_interactions_parent_id (parent_id)
);

-- Federation peers table (for SQL user authentication)
-- Stores credentials for peer-to-peer Dolt remotes between workspaces
CREATE TABLE IF NOT EXISTS federation_peers (
    name VARCHAR(255) PRIMARY KEY,
    remote_url VARCHAR(1024) NOT NULL,
    username VARCHAR(255),
    password_encrypted BLOB,
    sovereignty VARCHAR(8) DEFAULT '',
    last_sync DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_federation_peers_sovereignty (sovereignty)
);

-- Custom statuses table (normalized from config status.custom)
CREATE TABLE IF NOT EXISTS custom_statuses (
    name VARCHAR(64) PRIMARY KEY,
    category VARCHAR(32) NOT NULL DEFAULT 'unspecified'
);

-- Custom types table (normalized from config types.custom)
CREATE TABLE IF NOT EXISTS custom_types (
    name VARCHAR(64) PRIMARY KEY
);
`

// defaultConfig contains the default configuration values
const defaultConfig = `
INSERT IGNORE INTO config (` + "`key`" + `, value) VALUES
    ('compaction_enabled', 'false'),
    ('compact_tier1_days', '30'),
    ('compact_tier1_dep_levels', '2'),
    ('compact_tier2_days', '90'),
    ('compact_tier2_dep_levels', '5'),
    ('compact_tier2_commits', '100'),
    ('compact_batch_size', '50'),
    ('compact_parallel_workers', '5'),
    ('auto_compact_enabled', 'false');
`

// readyIssuesView is a MySQL-compatible view for ready work.
// Uses a subquery against custom_statuses table for active custom statuses.
// Note: Dolt supports recursive CTEs.
// Uses LEFT JOIN instead of NOT EXISTS to avoid Dolt mergeJoinIter panic.
// See: https://github.com/dolthub/go-mysql-server/issues/3413
const readyIssuesView = `
CREATE OR REPLACE VIEW ready_issues AS
WITH RECURSIVE
  blocked_directly AS (
    SELECT DISTINCT d.issue_id
    FROM dependencies d
    WHERE d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status NOT IN ('closed', 'pinned')
      )
  ),
  blocked_transitively AS (
    SELECT issue_id, 0 as depth
    FROM blocked_directly
    UNION ALL
    SELECT d.issue_id, bt.depth + 1
    FROM blocked_transitively bt
    JOIN dependencies d ON d.depends_on_id = bt.issue_id
    WHERE d.type = 'parent-child'
      AND bt.depth < 50
  )
SELECT i.*
FROM issues i
LEFT JOIN blocked_transitively bt ON bt.issue_id = i.id
WHERE (
    i.status = 'open'
    OR i.status IN (SELECT name FROM custom_statuses WHERE category = 'active')
  )
  AND (i.ephemeral = 0 OR i.ephemeral IS NULL)
  AND bt.issue_id IS NULL
  AND (i.defer_until IS NULL OR i.defer_until <= UTC_TIMESTAMP())
  AND NOT EXISTS (
    SELECT 1 FROM dependencies d_parent
    JOIN issues parent ON parent.id = d_parent.depends_on_id
    WHERE d_parent.issue_id = i.id
      AND d_parent.type = 'parent-child'
      AND parent.defer_until IS NOT NULL
      AND parent.defer_until > UTC_TIMESTAMP()
  );
`

// blockedIssuesView is a MySQL-compatible view for blocked issues.
// Uses subqueries against custom_statuses table for done/frozen exclusions.
// Uses subquery instead of three-table join to avoid Dolt mergeJoinIter panic.
const blockedIssuesView = `
CREATE OR REPLACE VIEW blocked_issues AS
SELECT
    i.*,
    (SELECT COUNT(*)
     FROM dependencies d
     WHERE d.issue_id = i.id
       AND d.type = 'blocks'
       AND EXISTS (
         SELECT 1 FROM issues blocker
         WHERE blocker.id = d.depends_on_id
           AND blocker.status NOT IN ('closed', 'pinned')
           AND blocker.status NOT IN (SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen'))
       )
    ) as blocked_by_count
FROM issues i
WHERE i.status NOT IN ('closed', 'pinned')
  AND i.status NOT IN (SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen'))
  AND EXISTS (
    SELECT 1 FROM dependencies d
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status NOT IN ('closed', 'pinned')
          AND blocker.status NOT IN (SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen'))
      )
  );
`

// BuildReadyIssuesView generates the ready_issues view SQL, incorporating
// custom statuses with CategoryActive from the custom_statuses table.
// The view uses a subquery against custom_statuses rather than dynamic IN clauses.
func BuildReadyIssuesView() string {
	return `
CREATE OR REPLACE VIEW ready_issues AS
WITH RECURSIVE
  blocked_directly AS (
    SELECT DISTINCT d.issue_id
    FROM dependencies d
    WHERE d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status NOT IN ('closed', 'pinned')
      )
  ),
  blocked_transitively AS (
    SELECT issue_id, 0 as depth
    FROM blocked_directly
    UNION ALL
    SELECT d.issue_id, bt.depth + 1
    FROM blocked_transitively bt
    JOIN dependencies d ON d.depends_on_id = bt.issue_id
    WHERE d.type = 'parent-child'
      AND bt.depth < 50
  )
SELECT i.*
FROM issues i
LEFT JOIN blocked_transitively bt ON bt.issue_id = i.id
WHERE (
    i.status = 'open'
    OR i.status IN (SELECT name FROM custom_statuses WHERE category = 'active')
  )
  AND (i.ephemeral = 0 OR i.ephemeral IS NULL)
  AND bt.issue_id IS NULL
  AND (i.defer_until IS NULL OR i.defer_until <= UTC_TIMESTAMP())
  AND NOT EXISTS (
    SELECT 1 FROM dependencies d_parent
    JOIN issues parent ON parent.id = d_parent.depends_on_id
    WHERE d_parent.issue_id = i.id
      AND d_parent.type = 'parent-child'
      AND parent.defer_until IS NOT NULL
      AND parent.defer_until > UTC_TIMESTAMP()
  );
`
}

// BuildBlockedIssuesView generates the blocked_issues view SQL, incorporating
// custom statuses with CategoryDone/CategoryFrozen from the custom_statuses table.
// The view uses a CTE against custom_statuses to deduplicate the subquery.
func BuildBlockedIssuesView() string {
	return `
CREATE OR REPLACE VIEW blocked_issues AS
WITH done_frozen AS (
    SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen')
)
SELECT
    i.*,
    (SELECT COUNT(*)
     FROM dependencies d
     WHERE d.issue_id = i.id
       AND d.type = 'blocks'
       AND EXISTS (
         SELECT 1 FROM issues blocker
         WHERE blocker.id = d.depends_on_id
           AND blocker.status NOT IN ('closed', 'pinned')
           AND blocker.status NOT IN (SELECT name FROM done_frozen)
       )
    ) as blocked_by_count
FROM issues i
WHERE i.status NOT IN ('closed', 'pinned')
  AND i.status NOT IN (SELECT name FROM done_frozen)
  AND EXISTS (
    SELECT 1 FROM dependencies d
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status NOT IN ('closed', 'pinned')
          AND blocker.status NOT IN (SELECT name FROM done_frozen)
      )
  );
`
}

// escapeSQL escapes a string for safe inclusion in SQL string literals.
// Deprecated: View building now uses JOINs against the custom_statuses table
// instead of string interpolation. Retained for backward compatibility and testing.
func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
