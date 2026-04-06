CREATE TABLE IF NOT EXISTS labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_labels_label (label),
    CONSTRAINT fk_labels_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
