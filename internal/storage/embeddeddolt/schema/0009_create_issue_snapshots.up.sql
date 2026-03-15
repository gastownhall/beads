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
