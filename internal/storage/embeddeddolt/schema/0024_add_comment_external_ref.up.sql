ALTER TABLE comments ADD COLUMN external_ref VARCHAR(255) DEFAULT '';
ALTER TABLE comments ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP;
CREATE INDEX idx_comments_external_ref ON comments (external_ref);

ALTER TABLE wisp_comments ADD COLUMN external_ref VARCHAR(255) DEFAULT '';
ALTER TABLE wisp_comments ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP;
CREATE INDEX idx_wisp_comments_external_ref ON wisp_comments (external_ref);
