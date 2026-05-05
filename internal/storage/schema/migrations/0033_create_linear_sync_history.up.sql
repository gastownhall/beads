-- Migration 0033: Create linear_sync_history tables for persistent sync audit log.
--
-- Two tables store sync results: linear_sync_runs (one row per invocation)
-- and linear_sync_items (one row per issue per sync). Together they enable
-- audit, debugging, and rollback scripting.

CREATE TABLE IF NOT EXISTS linear_sync_runs (
    sync_run_id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    started_at DATETIME NOT NULL,
    completed_at DATETIME NOT NULL,
    direction VARCHAR(16) NOT NULL,
    dry_run TINYINT(1) NOT NULL DEFAULT 0,
    issues_created INT NOT NULL DEFAULT 0,
    issues_updated INT NOT NULL DEFAULT 0,
    issues_skipped INT NOT NULL DEFAULT 0,
    issues_failed INT NOT NULL DEFAULT 0,
    issues_archived INT NOT NULL DEFAULT 0,
    conflict_resolution VARCHAR(16) DEFAULT '',
    error_message TEXT DEFAULT '',
    INDEX idx_sync_runs_started (started_at),
    INDEX idx_sync_runs_direction (direction)
);

CREATE TABLE IF NOT EXISTS linear_sync_items (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    sync_run_id CHAR(36) NOT NULL,
    bead_id VARCHAR(255) NOT NULL DEFAULT '',
    linear_id VARCHAR(255) NOT NULL DEFAULT '',
    direction VARCHAR(16) NOT NULL,
    attempt_number INT NOT NULL DEFAULT 1,
    outcome VARCHAR(32) NOT NULL,
    status_code INT NOT NULL DEFAULT 0,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    before_values JSON DEFAULT NULL,
    after_values JSON DEFAULT NULL,
    error_message TEXT DEFAULT '',
    INDEX idx_sync_items_run (sync_run_id),
    INDEX idx_sync_items_bead (bead_id),
    INDEX idx_sync_items_outcome (outcome)
);
