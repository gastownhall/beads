-- Ignored migration 0026: ensure wisps.current_revision exists on every clone
-- (be-hs42e.2 / gastownhall/beads#6134).
--
-- Synced migration 0067 adds current_revision to issues and wisps — but wisps
-- is dolt-ignored (migration 0019), so its schema is clone-local, and a
-- workspace that bootstraps or re-clones from a remote whose
-- schema_migrations cursor is already >= 0067 adopts the cursor without ever
-- executing 0067. Its wisps table (materialized by ignored/0001, which
-- predates current_revision) would then permanently lack the column, and the
-- shared issues/wisps column lists would drift by one column between the
-- fresh-clone door and the fresh-init door. Same mechanism, same shape, same
-- fix as ignored/0013 (wisps.row_lock, wy-pt82l) and ignored/0020
-- (wisps.storage_class, bd-hs7fa).
--
-- Carried here for SHAPE only. Nothing in Phase 2 (#6135) or Phase 3 (#6136)
-- reads or writes the wisps column: versioning is an issues-plane contract
-- and wisps are the ephemeral plane. wisps.row_lock is the precedent for a
-- column that exists on both planes and means something on one — see 0067's
-- header for the full asymmetry note.
--
-- The guard makes this a no-op on in-place-upgraded workspaces where synced
-- 0067 already added the column, and on workspaces with no local wisps table
-- yet. Definition mirrors 0067 exactly: BIGINT NOT NULL DEFAULT 1, so every
-- pre-existing row reads as revision 1 with no backfill.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'current_revision') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
