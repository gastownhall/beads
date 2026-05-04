-- Migration 0033: Create sync_history and sync_history_items tables.
--
-- Persistent audit log for tracker sync operations. Each sync run produces
-- one row in sync_history (summary) and zero or more rows in
-- sync_history_items (per-issue outcomes). Used for forensics, blame, and
-- recovery from bad syncs.

CREATE TABLE IF NOT EXISTS sync_history (
    sync_run_id VARCHAR(36) PRIMARY KEY,
    started_at DATETIME NOT NULL,
    completed_at DATETIME NOT NULL,
    tracker VARCHAR(64) NOT NULL DEFAULT '',
    direction VARCHAR(16) NOT NULL DEFAULT '',
    dry_run TINYINT(1) NOT NULL DEFAULT 0,
    issues_created INT NOT NULL DEFAULT 0,
    issues_updated INT NOT NULL DEFAULT 0,
    issues_skipped INT NOT NULL DEFAULT 0,
    issues_failed INT NOT NULL DEFAULT 0,
    conflicts INT NOT NULL DEFAULT 0,
    success TINYINT(1) NOT NULL DEFAULT 1,
    error_message TEXT,
    actor VARCHAR(255) DEFAULT '',
    INDEX idx_sync_history_started_at (started_at),
    INDEX idx_sync_history_tracker (tracker)
);

CREATE TABLE IF NOT EXISTS sync_history_items (
    id INT AUTO_INCREMENT PRIMARY KEY,
    sync_run_id VARCHAR(36) NOT NULL,
    bead_id VARCHAR(255) NOT NULL DEFAULT '',
    external_id VARCHAR(255) DEFAULT '',
    outcome VARCHAR(32) NOT NULL DEFAULT '',
    error_message TEXT,
    INDEX idx_sync_history_items_run_id (sync_run_id),
    INDEX idx_sync_history_items_bead_id (bead_id)
);
