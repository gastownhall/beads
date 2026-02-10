//go:build cgo

package dolt

// schema defines the MySQL-compatible database schema for Dolt.
// This mirrors the SQLite schema but uses MySQL syntax.
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
    deleted_at DATETIME,
    deleted_by VARCHAR(255) DEFAULT '',
    delete_reason TEXT DEFAULT '',
    original_type VARCHAR(32) DEFAULT '',
    -- Messaging fields
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    -- Wisp classification for TTL-based compaction (gt-9br)
    wisp_type VARCHAR(32) DEFAULT '',
    -- Pinned field
    pinned TINYINT(1) DEFAULT 0,
    -- Template field
    is_template TINYINT(1) DEFAULT 0,
    -- Work economics field (HOP Decision 006)
    crystallizes TINYINT(1) DEFAULT 0,
    -- Molecule type field
    mol_type VARCHAR(32) DEFAULT '',
    -- Work type field (Decision 006: mutex vs open_competition)
    work_type VARCHAR(32) DEFAULT 'mutex',
    -- HOP quality score field (0.0-1.0)
    quality_score DOUBLE,
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
    -- Agent fields
    hook_bead VARCHAR(255) DEFAULT '',
    role_bead VARCHAR(255) DEFAULT '',
    agent_state VARCHAR(32) DEFAULT '',
    last_activity DATETIME,
    role_type VARCHAR(32) DEFAULT '',
    rig VARCHAR(255) DEFAULT '',
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
-- See SQLite migration 025_remove_depends_on_fk.go for design context.
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
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
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
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
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

-- Export hashes table
CREATE TABLE IF NOT EXISTS export_hashes (
    issue_id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64) NOT NULL,
    exported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_export_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Child counters table
CREATE TABLE IF NOT EXISTS child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Issue snapshots table (for compaction)
CREATE TABLE IF NOT EXISTS issue_snapshots (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
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
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
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
-- Stores credentials for peer-to-peer Dolt remotes between Gas Towns
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
    ('compact_model', 'claude-haiku-4-5-20251001'),
    ('compact_batch_size', '50'),
    ('compact_parallel_workers', '5'),
    ('auto_compact_enabled', 'false');
`

// readyIssuesView is a MySQL-compatible view for ready work
// Note: Dolt supports recursive CTEs like SQLite.
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
          AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
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
WHERE i.status = 'open'
  AND (i.ephemeral = 0 OR i.ephemeral IS NULL)
  AND bt.issue_id IS NULL;
`

// blockedIssuesView is a MySQL-compatible view for blocked issues.
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
           AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
       )
    ) as blocked_by_count
FROM issues i
WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
  AND EXISTS (
    SELECT 1 FROM dependencies d
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
      )
  );
`
