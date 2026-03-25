CREATE TABLE IF NOT EXISTS attachments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    external_ref VARCHAR(255) NOT NULL DEFAULT '',
    filename VARCHAR(500) NOT NULL DEFAULT '',
    url TEXT NOT NULL,
    mime_type VARCHAR(255) NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    source VARCHAR(64) NOT NULL DEFAULT '',
    creator VARCHAR(255) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_attachments_issue (issue_id),
    INDEX idx_attachments_external_ref (external_ref),
    CONSTRAINT fk_attachments_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
