-- Replace the single-column status index with a (status, updated_at) composite
-- and add a defer_until index (D4v2 query plan). Guarded against the current
-- schema so the migration is idempotent: a clone may have its schema_migrations
-- row for this version regressed without its DDL rolled back (see bd-4mpy7
-- remote_behind_schema_gates), in which case the designated migrator re-applies
-- this file and it must not error on already-present/already-absent indexes.

-- Drop the old single-column status index only if it is still present.
SET @has_old = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_status'
);
SET @sql = IF(@has_old = 1, 'DROP INDEX idx_issues_status ON issues', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Create the (status, updated_at) composite index only if missing.
SET @needs_su = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_status_updated_at'
);
SET @sql = IF(@needs_su = 1, 'CREATE INDEX idx_issues_status_updated_at ON issues (status, updated_at)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Create the defer_until index only if missing.
SET @needs_du = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_defer_until'
);
SET @sql = IF(@needs_du = 1, 'CREATE INDEX idx_issues_defer_until ON issues (defer_until)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
