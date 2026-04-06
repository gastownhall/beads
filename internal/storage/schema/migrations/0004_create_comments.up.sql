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
