-- Migration 0051: drop DEFAULT (UUID()) from the aux history tables' primary
-- keys (events, comments, issue_snapshots, compaction_snapshots).
--
-- Follow-up to bd-6dnrw.18 (bd-2rd37), completing for the aux tables what 0050
-- did for dependencies. Every insert site now mints the id app-side
-- (issueops.NewEventID / UUIDv7 comment ids), and the snapshot tables have no
-- live insert path at all. The 0004/0005/0009/0010-era DEFAULT (UUID()) is
-- therefore dormant -- but a future code path that omitted id would silently
-- mint per-clone-random keys instead of failing loudly, the exact failure
-- class behind the #4259 dependencies corruption. Drop the defaults so such a
-- path fails with a NOT NULL violation instead.
--
-- Guarded on COLUMN_DEFAULT so re-running (or running on a database that never
-- had the default) is a no-op. The wisp_ twins (wisp_events, wisp_comments,
-- wisp_dependencies) are dolt-ignored and live in the ignored migration chain;
-- ignored migration 0010 drops their defaults.

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE events ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'comments'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE comments ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_snapshots'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE issue_snapshots ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'compaction_snapshots'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE compaction_snapshots ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
