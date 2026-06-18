-- Ignored migration 0010: drop DEFAULT (UUID()) from the wisp tables' primary
-- keys (wisp_events, wisp_comments, wisp_dependencies).
--
-- Companion to main migration 0051 (bd-2rd37, follow-up to bd-6dnrw.18): every
-- wisp insert site mints its id app-side, so the defaults are dormant, and a
-- future path that omitted id should fail loudly rather than silently mint a
-- random key. Wisp rows are clone-local (dolt-ignored, never merged), but
-- promote copies their rows id-preserving into the synced events/comments
-- tables, so a DB-minted id here would outlive the wisp.
--
-- 0050 left wisp_dependencies' default in place, citing recreation by
-- EnsureIgnoredTables -- a mechanism since replaced by this ignored migration
-- chain, which replays in full on every clone (ignored/0001 recreates the
-- tables with the default; this migration, later in the chain, drops it).
-- The COLUMN_DEFAULT guard makes re-running a no-op.

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE wisp_events ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE wisp_comments ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE wisp_dependencies ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
