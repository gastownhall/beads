-- Ignored migration 0019: ensure wisps.claim_fence exists on every clone.
--
-- Synced migration 0062 adds claim_fence to issues only. wisps is
-- dolt-ignored (by synced migration 0019_wisps_dolt_ignore), so its schema is
-- clone-local: a workspace that bootstraps or re-clones from a remote whose
-- schema_migrations cursor is already >= 0062 adopts the cursor without ever
-- executing 0062, and a wisps table materialized by ignored/0001 would lack the
-- column. Every wisp claim/unclaim then soft-fails with Error 1054, the
-- failure mode 0013 fixed for row_lock (wy-pt82l).
--
-- Wisps are claimable work — the shared claim/unclaim/update SQL routes by
-- table name and bumps the fence on either tier — so the ownership fence is
-- tier-complete even though wisps are never leased.
--
-- The guard makes this a no-op on workspaces that already carry the column
-- and on workspaces with no local wisps table yet.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'claim_fence') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN claim_fence BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
