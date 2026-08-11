-- Ignored migration 0024: ensure wisp_comments.text is LONGTEXT on every clone.
--
-- The twin of synced migration 0065, and required by check D of
-- scripts/check-migration-hygiene.sh rather than offered: wisp_comments is
-- clone-local and dolt-ignored, so a fresh clone materializes it from
-- ignored/0001 — whose CREATE still carries `text TEXT NOT NULL` — while the
-- main cursor arrives at-latest and 0065 never runs there. Without this file a
-- large comment on a wisp would write fine on an in-place-upgraded workspace
-- and fail with Error 1105 on a fresh clone, which is the same fresh-clone door
-- ignored/0021 (the wisps half of the same 0049 gap), ignored/0020
-- (storage_class) and ignored/0013 (row_lock) each record.
--
-- The definition mirrors 0065 exactly, including the absent default: the column
-- was created NOT NULL with no default and MODIFY COLUMN replaces the whole
-- definition. Guarded on the same COLUMN_TYPE = 'text' probe, so it is a no-op
-- on the fresh-init door and on an already-widened clone, and idempotent on
-- replay.
SET @wisp_comments_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND COLUMN_NAME = 'text'
);
SET @sql = IF(@wisp_comments_needs_fix = 1,
    'ALTER TABLE wisp_comments MODIFY COLUMN text LONGTEXT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
