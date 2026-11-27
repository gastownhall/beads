-- Ignored migration 0032: ensure wisps.closed_by exists on every clone.
--
-- Synced migration 0075 adds closed_by to issues and wisps -- but wisps is
-- dolt-ignored (migration 0019), so its schema is clone-local, and a workspace
-- that bootstraps or re-clones from a remote whose schema_migrations cursor is
-- already >= 0075 adopts the cursor without ever executing 0075. Its wisps
-- table (materialized by ignored/0001, which predates closed_by) would then
-- permanently lack the column, and every shared wisp scan would fail. Same
-- mechanism, same shape, same fix as ignored/0013 (wisps.row_lock),
-- ignored/0027 (wisps.current_revision) and ignored/0029 (wisps.arrival_seq).
--
-- Guarded: a no-op on in-place-upgraded workspaces where synced 0075 already
-- added the column, and on workspaces with no local wisps table yet.
-- Definition mirrors 0075 exactly: VARCHAR(255) DEFAULT ''.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'closed_by') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
