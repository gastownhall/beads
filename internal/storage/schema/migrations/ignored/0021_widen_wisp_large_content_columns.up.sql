-- Ignored migration 0021: ensure the wisps large-content columns are
-- LONGTEXT on every clone (bd-hs7fa, second finding).
--
-- Synced migration 0049 widened description/design/acceptance_criteria/notes
-- and close_reason from TEXT to LONGTEXT on issues AND wisps — but wisps is
-- dolt-ignored, so a fresh clone materializes it from ignored/0001, whose
-- CREATE still carries the original TEXT columns, and the at-latest main
-- cursor means 0049 never runs there. TEXT caps at 65535 bytes: a wisp
-- with an embedded-image description or a large agent payload that writes
-- fine on an in-place-upgraded workspace fails with Error 1105 on a fresh
-- clone. Same fresh-clone-door mechanism as ignored/0020 (storage_class)
-- and ignored/0013 (row_lock); found by the shape audit in
-- internal/storage/embeddeddolt/migrate_ignored_plane_shape_test.go.
--
-- Definitions mirror 0049's wisps section exactly, including the restated
-- DEFAULTs (MODIFY COLUMN replaces the whole definition; dropping the
-- defaults would regress inserts that omit the column). Guarded on the same
-- COLUMN_TYPE = 'text' probe as 0049, so this is a no-op on
-- in-place-upgraded workspaces and on the fresh-init door, and idempotent
-- on replay.
SET @wisps_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'description'
);
SET @sql = IF(@wisps_needs_fix = 1,
    'ALTER TABLE wisps MODIFY COLUMN description LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN design LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN notes LONGTEXT NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisps_cr_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'close_reason'
);
SET @sql = IF(@wisps_cr_needs_fix = 1,
    'ALTER TABLE wisps MODIFY COLUMN close_reason LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
