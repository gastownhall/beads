DROP INDEX idx_comments_external_ref ON comments;
ALTER TABLE comments DROP COLUMN updated_at;
ALTER TABLE comments DROP COLUMN external_ref;

DROP INDEX idx_wisp_comments_external_ref ON wisp_comments;
ALTER TABLE wisp_comments DROP COLUMN updated_at;
ALTER TABLE wisp_comments DROP COLUMN external_ref;
