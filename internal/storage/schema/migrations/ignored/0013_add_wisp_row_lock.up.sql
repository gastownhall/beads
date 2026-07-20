-- Ignored migration 0013: ensure wisps.row_lock exists on every clone
-- (bd-rotq2).
--
-- Synced migration 0054 added row_lock to wisps — but wisps is dolt-ignored
-- (migration 0019), so its schema is clone-local, and a workspace that
-- bootstraps or re-clones from a remote whose schema_migrations cursor is
-- already >= 0054 adopts the cursor without ever executing 0054. Its wisps
-- table (materialized by ignored/0001, which predates row_lock) then
-- permanently lacks the column, and every wisp INSERT from a post-0054 binary
-- soft-fails with Error 1054 (observed in prod on claude-code-vm, wy-pt82l).
--
-- Carrying the column on the ignored track fixes both fronts: fresh
-- bootstraps get it right after ignored/0001 runs, and already-affected
-- workspaces self-heal on their next store open. The guard makes this a
-- no-op on in-place-upgraded workspaces where synced 0054 already added the
-- column, and on workspaces with no local wisps table yet.
--
-- Deliberately NOT healed: 0054's other two wisps columns
-- (lease_expires_at, heartbeat_at). 0055 moved leases to their own table
-- and dropped those columns from issues/wisps — wisps are never leased, and
-- nothing post-cutover reads or writes them on wisps — so bootstrapped
-- workspaces that never had them are already in the correct final shape.
-- row_lock is the only 0054 wisps column that survives 0055.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'row_lock') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN row_lock BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
