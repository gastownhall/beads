-- Ignored migration 0031: widen the clone-local event comment columns on
-- every clone.
--
-- The twin of synced migration 0075, and required by check D of
-- scripts/check-migration-hygiene.sh rather than offered: events and
-- wisp_events are clone-local and dolt-ignored (events since 0062), so a
-- fresh clone materializes them from ignored/0019 and ignored/0001 -- whose
-- CREATEs still carry `comment TEXT` -- while the main cursor arrives
-- at-latest and 0075 never runs there. Without this file a large reopen
-- reason or comment would write fine on an in-place-upgraded workspace and
-- fail with Error 1105 on a fresh clone, the fresh-clone door ignored/0024
-- records for wisp_comments.text. Only the event tables need a twin: the
-- snapshot tables 0075 also widens are versioned and arrive with history.
--
-- Each block mirrors 0075's for the same column, definition included (MODIFY
-- COLUMN replaces the whole definition). Guarded on the same COLUMN_TYPE =
-- 'text' probe, one guard per column, so it is a no-op on the fresh-init
-- door, on an already-widened clone and on replay, and leaves a missing table
-- alone.
SET @events_comment_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@events_comment_needs_widen = 1,
    'ALTER TABLE events MODIFY COLUMN comment LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisp_events_comment_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@wisp_events_comment_needs_widen = 1,
    'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
