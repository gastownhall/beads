-- Ignored migration 0020: ensure wisps.storage_class exists on every clone
-- (bd-hs7fa).
--
-- Synced migration 0060 added storage_class to wisps — but wisps is
-- dolt-ignored (migration 0019), so its schema is clone-local, and a
-- workspace that bootstraps or re-clones from a remote whose
-- schema_migrations cursor is already >= 0060 adopts the cursor without ever
-- executing 0060. Its wisps table (materialized by ignored/0001, which
-- predates storage_class) then permanently lacks the column, and every
-- shared wisp scan from a post-0060 binary fails with Error 1054 (observed
-- in prod on the wy-98eh5 VM re-clone). Same mechanism, same shape, same fix
-- as ignored/0013 (wisps.row_lock, wy-pt82l).
--
-- The guard makes this a no-op on in-place-upgraded workspaces where synced
-- 0060 already added the column, and on workspaces with no local wisps table
-- yet. Definition mirrors 0060 exactly: VARCHAR(16), nullable, no default —
-- NULL means unset and resolves per Protocol v0.1 C1.2
-- (types.Issue.EffectiveStorageClass).
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'storage_class') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN storage_class VARCHAR(16)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
