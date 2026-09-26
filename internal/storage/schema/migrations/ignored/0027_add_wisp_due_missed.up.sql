-- Ignored migration 0027: ensure due_missed exists on wisps for every clone.
--
-- Synced migration 0068 added due_missed to wisps — but wisps is dolt-ignored
-- (migration 0019), so its schema is clone-local, and a workspace that
-- bootstraps or re-clones from a remote whose schema_migrations cursor is
-- already >= 0068 adopts the cursor without ever executing 0068. Its wisps
-- table then permanently lacks the column, and every shared wisp scan from a
-- post-0068 binary fails with Error 1054. Same mechanism, same shape, same fix
-- as ignored/0026 (wisps.current_revision) and ignored/0020
-- (wisps.storage_class, bd-hs7fa).
--
-- The guards make this a no-op on in-place-upgraded workspaces where synced
-- 0068 already added the column, and on workspaces with no local wisps table
-- yet. The definition mirrors 0068 exactly.
--
-- No backfill twin: 0068 is schema-only and writes no rows at all.
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'due_missed') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN due_missed INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
