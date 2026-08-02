-- Dead-worker recovery (Gas Station v1.1, wy-5r9j): give a claim a lease.
--
-- A claim was previously permanent — a worker that died mid-task stranded its
-- issue in_progress forever. These columns let a claim expire:
--
--   lease_expires_at  the wall-clock instant after which the claim is stale and
--                     a reaper (bd reclaim) may revert the issue to ready.
--   heartbeat_at      the last time the lease owner proved it was still alive.
--   row_lock          a random BIGINT rewritten by EVERY mutating path on the
--                     row. Dolt has no real row locking and merges concurrent
--                     writes cell-by-cell, so a heartbeat (touching heartbeat_at)
--                     and a close (touching status) would otherwise silently
--                     cell-merge instead of conflicting. Forcing every writer to
--                     also rewrite this one shared cell turns those into a
--                     1213/1205 serialization conflict that withRetryTx replays —
--                     the difference between exactly-once and a lost close.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back (see 0052/0046). assignee doubles as the
-- lease owner; no new owner column is needed.

-- issues.lease_expires_at
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'lease_expires_at'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN lease_expires_at DATETIME',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- issues.heartbeat_at
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'heartbeat_at'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN heartbeat_at DATETIME',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- issues.row_lock
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'row_lock'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN row_lock BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- idx_issues_lease: the reaper scans in_progress issues by lease_expires_at.
SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_lease'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_lease ON issues (status, lease_expires_at)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps mirror the issues lease columns so the shared issueops claim/heartbeat
-- SQL (which routes by table name) works uniformly. Wisps are ephemeral and are
-- never reclaimed, but the columns must exist for the shared UPDATEs to bind.
-- Guarded on the wisps table existing (older workspaces created issues-only).
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'lease_expires_at') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN lease_expires_at DATETIME',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'heartbeat_at') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN heartbeat_at DATETIME',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'row_lock') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN row_lock BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
