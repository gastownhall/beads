-- Ignored migration 0028: ensure wisps.participation_generation exists on
-- every clone (design be-hs42e.3 §16.2-16.3 / be-h89oq / be-x8tpz).
--
-- Synced migration 0070 adds participation_generation to issues and wisps --
-- but wisps is dolt-ignored (migration 0019), so its schema is clone-local,
-- and a workspace that bootstraps or re-clones from a remote whose
-- schema_migrations cursor is already >= 0070 adopts the cursor without ever
-- executing 0070. Its wisps table would then permanently lack the column,
-- and the shared issues/wisps column lists would drift by one column between
-- the fresh-clone door and the fresh-init door. Same mechanism, same shape,
-- same fix as ignored/0013 (wisps.row_lock, wy-pt82l), ignored/0020
-- (wisps.storage_class, wy-98eh5), and ignored/0027 (wisps.current_revision).
--
-- Carried here for SHAPE only, exactly like 0070's own issues/wisps split:
-- nothing reads or writes this column on the wisps plane (design §16.2a).
-- wisps.row_lock and wisps.current_revision are the precedent for a column
-- that exists on both planes and means something on only one.
--
-- The guard makes this a no-op on in-place-upgraded workspaces where synced
-- 0070 already added the column, and on workspaces with no local wisps table
-- yet. Definition mirrors 0070's own wisps.participation_generation guard
-- exactly: BIGINT NULL, no default, no backfill.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'participation_generation') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN participation_generation BIGINT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
