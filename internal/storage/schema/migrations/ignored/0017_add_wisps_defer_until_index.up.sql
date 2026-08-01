-- Index wisps.defer_until so the ready-work deferred-parents probe
-- (getChildrenOfDeferredParentsInTx: defer_until IS NOT NULL AND
-- defer_until > UTC_TIMESTAMP() LIMIT 1) is an index range scan instead of a
-- full scan over fat wisp rows. Migration 0052 added the equivalent
-- idx_issues_defer_until on issues; wisps was left unindexed, and the probe
-- runs on every ready-work call that excludes deferred items (it is skipped
-- when IncludeDeferred is set). Guarded so it is idempotent and tolerant
-- of an absent wisps table (matches 0006_add_wisp_is_blocked).
SET @needs_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'idx_wisps_defer_until') = 0,
    1, 0
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_defer_until ON wisps(defer_until)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
