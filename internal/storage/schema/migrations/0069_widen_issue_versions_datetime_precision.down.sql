-- Reverse of 0069: narrow issue_versions.change_at and .removed_at back to
-- the plain DATETIME (precision 0) type 0067 created them with. Guarded on
-- DATETIME_PRECISION so a store that never took the widen, or was already
-- rolled back, no-ops.
--
-- Unlike the widen, the narrow is lossy: at precision 0 Dolt reads a
-- sub-second value back rounded half-up to the whole second (measured on
-- dolt 2.3.3: .750000 reads back as the next second), and widening again
-- afterwards is not guaranteed to recover the fraction. That is safe under
-- the up file's premise -- issue_versions holds no rows while versioned
-- history is inactive, which it is in every build so far -- and it is the
-- thing to revisit if that premise ever stops holding.
--
-- Only migrations/*.up.sql is embedded into the CLI fresh bundle, so the
-- PREPARE hazard (cli_prepared_ddl.go) never reaches this file.
SET @issue_versions_change_at_is_widened = (
    SELECT IF(DATETIME_PRECISION = 6, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'change_at'
);
SET @sql = IF(@issue_versions_change_at_is_widened = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN change_at DATETIME NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @issue_versions_removed_at_is_widened = (
    SELECT IF(DATETIME_PRECISION = 6, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'removed_at'
);
SET @sql = IF(@issue_versions_removed_at_is_widened = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN removed_at DATETIME',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
