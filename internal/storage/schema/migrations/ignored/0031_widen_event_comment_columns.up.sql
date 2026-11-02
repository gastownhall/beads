-- Ignored migration 0031: ensure events.comment is LONGTEXT on every clone.
--
-- The twin of synced migration 0075, and required by check D of
-- scripts/check-migration-hygiene.sh rather than offered: events is
-- clone-local and dolt-ignored since 0062 moved it off the versioned plane,
-- so a fresh clone materializes it from ignored/0019 — whose CREATE still
-- carries `comment TEXT` — while the main cursor arrives at-latest and 0075
-- never runs there. Without this file a large audit comment (a reopen reason,
-- an AddComment payload) would write fine on an in-place-upgraded workspace
-- and fail with Error 1105 on a fresh clone, which is the same fresh-clone
-- door ignored/0019 itself and ignored/0024 (wisp_comments.text) record.
--
-- The definition mirrors 0075 exactly: nullable, no default, as 0005 created
-- it and 0062/ignored/0019 recreated it. Guarded on the same
-- COLUMN_TYPE = 'text' probe, so it is a no-op on the fresh-init door (main
-- 0075 already widened the column there before this series runs) and on an
-- already-widened clone, and idempotent on replay.
SET @events_comment_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@events_comment_needs_fix = 1,
    'ALTER TABLE events MODIFY COLUMN comment LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
