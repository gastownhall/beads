CREATE TABLE IF NOT EXISTS compaction_snapshots (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    compaction_level INT NOT NULL,
    snapshot_json BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_comp_snap_issue (issue_id, compaction_level, created_at DESC),
    CONSTRAINT fk_comp_snap_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
