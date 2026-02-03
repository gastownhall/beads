package sqlite

const schema = `
-- Issues table
CREATE TABLE IF NOT EXISTS issues (
    id TEXT PRIMARY KEY,
    content_hash TEXT,
    title TEXT NOT NULL CHECK(length(title) <= 500),
    description TEXT NOT NULL DEFAULT '',
    design TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 0 AND priority <= 4),
    issue_type TEXT NOT NULL DEFAULT 'task',
    assignee TEXT,
    estimated_minutes INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by TEXT DEFAULT '',
    owner TEXT DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session TEXT DEFAULT '',
    external_ref TEXT,
    compaction_level INTEGER DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit TEXT,
    original_size INTEGER,
    deleted_at DATETIME,
    deleted_by TEXT DEFAULT '',
    delete_reason TEXT DEFAULT '',
    original_type TEXT DEFAULT '',
    -- Messaging fields (bd-kwro)
    sender TEXT DEFAULT '',
    ephemeral INTEGER DEFAULT 0,
    -- Wisp type for TTL-based compaction (gt-9br)
    wisp_type TEXT DEFAULT '',
    -- Pinned field (bd-7h5)
    pinned INTEGER DEFAULT 0,
    -- Template field (beads-1ra)
    is_template INTEGER DEFAULT 0,
    -- Work economics field (bd-fqze8) - HOP Decision 006
    crystallizes INTEGER DEFAULT 0,
    -- Molecule type field (bd-oxgi)
    mol_type TEXT DEFAULT '',
    -- Work type field (Decision 006: mutex vs open_competition)
    work_type TEXT DEFAULT 'mutex',
    -- HOP quality score field (0.0-1.0, set by Refineries on merge)
    quality_score REAL,
    -- Federation source system field
    source_system TEXT DEFAULT '',
    -- Custom metadata field (GH#1406)
    metadata TEXT NOT NULL DEFAULT '{}',
    -- Event fields (bd-ecmd)
    event_kind TEXT DEFAULT '',
    actor TEXT DEFAULT '',
    target TEXT DEFAULT '',
    payload TEXT DEFAULT '',
    -- NOTE: replies_to, relates_to, duplicate_of, superseded_by removed per Decision 004
    -- These relationships are now stored in the dependencies table
    -- closed_at constraint: closed issues must have it, tombstones may retain it from before deletion
    CHECK (
        (status = 'closed' AND closed_at IS NOT NULL) OR
        (status = 'tombstone') OR
        (status NOT IN ('closed', 'tombstone') AND closed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status);
CREATE INDEX IF NOT EXISTS idx_issues_priority ON issues(priority);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee);
CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at);
-- Note: idx_issues_external_ref is created in migrations/002_external_ref_column.go

-- Dependencies table (edge schema - Decision 004)
CREATE TABLE IF NOT EXISTS dependencies (
    issue_id TEXT NOT NULL,
    depends_on_id TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'blocks',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by TEXT NOT NULL,
    metadata TEXT DEFAULT '{}',    -- JSON blob for type-specific edge data
    thread_id TEXT DEFAULT '',     -- For efficient conversation threading queries
    PRIMARY KEY (issue_id, depends_on_id),
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
    FOREIGN KEY (depends_on_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dependencies_issue ON dependencies(issue_id);
CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on ON dependencies(depends_on_id);
CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on_type ON dependencies(depends_on_id, type);
CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on_type_issue ON dependencies(depends_on_id, type, issue_id);
-- NOTE: idx_dependencies_thread and idx_dependencies_thread_type are created by
-- migration 020_edge_consolidation.go after adding the thread_id column.
-- They cannot be in the schema because existing databases may not have thread_id yet.

-- Labels table
CREATE TABLE IF NOT EXISTS labels (
    issue_id TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY (issue_id, label),
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_labels_label ON labels(label);

-- Comments table
CREATE TABLE IF NOT EXISTS comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    author TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id);
CREATE INDEX IF NOT EXISTS idx_comments_created_at ON comments(created_at);

-- Events table (audit trail)
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL,
    old_value TEXT,
    new_value TEXT,
    comment TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_events_issue ON events(issue_id);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);

-- Config table (for storing settings like issue prefix)
CREATE TABLE IF NOT EXISTS config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Default compaction configuration
INSERT OR IGNORE INTO config (key, value) VALUES
    ('compaction_enabled', 'false'),
    ('compact_tier1_days', '30'),
    ('compact_tier1_dep_levels', '2'),
    ('compact_tier2_days', '90'),
    ('compact_tier2_dep_levels', '5'),
    ('compact_tier2_commits', '100'),
    ('compact_model', 'claude-3-5-haiku-20241022'),
    ('compact_batch_size', '50'),
    ('compact_parallel_workers', '5'),
    ('auto_compact_enabled', 'false');

-- Metadata table (for storing internal state like import hashes)
CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Dirty issues table (for incremental JSONL export)
-- Tracks which issues have changed since last export
CREATE TABLE IF NOT EXISTS dirty_issues (
    issue_id TEXT PRIMARY KEY,
    marked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_dirty_issues_marked_at ON dirty_issues(marked_at);

-- Tracks content hash of last export for each issue (for timestamp-only dedup, bd-164)
CREATE TABLE IF NOT EXISTS export_hashes (
    issue_id TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    exported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Child counters table (for hierarchical ID generation)
-- Tracks sequential child numbers per parent issue
CREATE TABLE IF NOT EXISTS child_counters (
    parent_id TEXT PRIMARY KEY,
    last_child INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Issue snapshots table (for compaction)
CREATE TABLE IF NOT EXISTS issue_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    snapshot_time DATETIME NOT NULL,
    compaction_level INTEGER NOT NULL,
    original_size INTEGER NOT NULL,
    compressed_size INTEGER NOT NULL,
    original_content TEXT NOT NULL,
    archived_events TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_snapshots_issue ON issue_snapshots(issue_id);
CREATE INDEX IF NOT EXISTS idx_snapshots_level ON issue_snapshots(compaction_level);

-- Compaction snapshots table (for restoration)
CREATE TABLE IF NOT EXISTS compaction_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    compaction_level INTEGER NOT NULL,
    snapshot_json BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comp_snap_issue_level_created ON compaction_snapshots(issue_id, compaction_level, created_at DESC);

-- Repository mtimes table (for multi-repo hydration optimization)
-- Tracks modification times of JSONL files to skip unchanged repos
CREATE TABLE IF NOT EXISTS repo_mtimes (
    repo_path TEXT PRIMARY KEY,  -- Absolute path to the repository root
    jsonl_path TEXT NOT NULL,    -- Absolute path to the .beads/issues.jsonl file
    mtime_ns INTEGER NOT NULL,   -- Modification time in nanoseconds since epoch
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_repo_mtimes_checked ON repo_mtimes(last_checked);

-- Ready work view (with hierarchical blocking)
-- Uses recursive CTE to propagate blocking through parent-child hierarchy
CREATE VIEW IF NOT EXISTS ready_issues AS
WITH RECURSIVE
  -- Find issues blocked directly by dependencies
  blocked_directly AS (
    SELECT DISTINCT d.issue_id
    FROM dependencies d
    JOIN issues blocker ON d.depends_on_id = blocker.id
    WHERE d.type = 'blocks'
      AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
  ),
  -- Propagate blockage to all descendants via parent-child
  blocked_transitively AS (
    -- Base case: directly blocked issues
    SELECT issue_id, 0 as depth
    FROM blocked_directly
    UNION ALL
    -- Recursive case: children of blocked issues inherit blockage
    SELECT d.issue_id, bt.depth + 1
    FROM blocked_transitively bt
    JOIN dependencies d ON d.depends_on_id = bt.issue_id
    WHERE d.type = 'parent-child'
      AND bt.depth < 50
  )
SELECT i.*
FROM issues i
WHERE i.status = 'open'
  AND (i.ephemeral = 0 OR i.ephemeral IS NULL)
  AND NOT EXISTS (
    SELECT 1 FROM blocked_transitively WHERE issue_id = i.id
  );

-- Blocked issues view
CREATE VIEW IF NOT EXISTS blocked_issues AS
SELECT
    i.*,
    COUNT(d.depends_on_id) as blocked_by_count
FROM issues i
JOIN dependencies d ON i.id = d.issue_id
JOIN issues blocker ON d.depends_on_id = blocker.id
WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
  AND d.type = 'blocks'
  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
GROUP BY i.id;
`
