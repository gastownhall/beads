-- Migration 0037: Convert BIGINT AUTO_INCREMENT primary keys to CHAR(36) UUID
-- on the six tables that previously used auto-increment IDs.
--
-- Why: independent AUTO_INCREMENT counters across federated clones produce
-- conflicting IDs on push/pull. UUID-defaulted PKs eliminate the collision.
--
-- Fresh DBs already declare these tables with CHAR(36) PKs (migrations 0004,
-- 0005, 0009, 0010, 0021), so this is a no-op for them. Backfilled legacy DBs
-- with BIGINT PKs get converted in place via the seven-step add/copy/drop/
-- rename dance below (Dolt requires removing AUTO_INCREMENT before dropping a
-- PK, so MODIFY precedes DROP PRIMARY KEY).
--
-- Per-table guard via INFORMATION_SCHEMA + PREPARE/EXECUTE: when the id column
-- is not BIGINT, every ALTER becomes a `SELECT 1` no-op. The whole migration
-- file runs in a single Exec on a single connection, so @needs_migration and
-- @sql persist across statements.

-- ============================================================
-- events
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE events SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE events ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- comments
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'comments'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE comments SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE comments ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- issue_snapshots
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_snapshots'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE issue_snapshots SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE issue_snapshots ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- compaction_snapshots
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'compaction_snapshots'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE compaction_snapshots SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE compaction_snapshots ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- wisp_events
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE wisp_events SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_events ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ============================================================
-- wisp_comments
-- ============================================================
SET @needs_migration = (
    SELECT IF(LOWER(DATA_TYPE) = 'bigint', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND COLUMN_NAME = 'id'
);

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments ADD COLUMN uuid_id CHAR(36) NOT NULL DEFAULT (UUID())',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'UPDATE wisp_comments SET uuid_id = UUID() WHERE uuid_id = '''' OR uuid_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments MODIFY id BIGINT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments DROP COLUMN id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments RENAME COLUMN uuid_id TO id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migration = 1,
    'ALTER TABLE wisp_comments ADD PRIMARY KEY (id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
