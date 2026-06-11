CREATE TABLE IF NOT EXISTS attachments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    hash_algorithm VARCHAR(32) NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    original_filename VARCHAR(1024) NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    byte_size BIGINT NOT NULL,
    storage_relpath VARCHAR(2048) NOT NULL,
    created_by VARCHAR(255) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_attachments_issue (issue_id),
    INDEX idx_attachments_issue_filename (issue_id, original_filename),
    INDEX idx_attachments_hash (content_hash),
    UNIQUE KEY uniq_attachments_issue_hash (issue_id, hash_algorithm, content_hash),
    CONSTRAINT fk_attachments_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE
);
