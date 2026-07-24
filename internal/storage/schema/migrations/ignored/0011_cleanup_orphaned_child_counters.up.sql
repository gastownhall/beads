-- Clean up child_counters rows whose parent row no longer exists (#4534).
--
-- Migration 0039 dropped fk_counter_parent and ignored migration 0002
-- re-added it under FOREIGN_KEY_CHECKS = 0, so counter rows orphaned during
-- the FK-less window (a create that incremented the counter but rolled back
-- before inserting the child, or a parent deleted with no cascade) survive
-- the constraint's return. Dolt then fails constraint validation on
-- subsequent writes, so one legacy orphan bricks every bd create on an
-- otherwise healthy database.
--
-- First move counters that belong to live wisps into wisp_child_counters
-- (same semantics as ignored migration 0002, kept idempotent), then delete
-- rows dangling from issues — exactly what the FK's ON DELETE CASCADE would
-- have done had it been in force when the parent went away.

SET @has_child_counters = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'child_counters'
);
SET @has_wisps = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
);
SET @has_wisp_child_counters = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_child_counters'
);

SET @sql = IF(@has_child_counters > 0 AND @has_wisps > 0 AND @has_wisp_child_counters > 0,
    'INSERT IGNORE INTO wisp_child_counters (parent_id, last_child) SELECT cc.parent_id, cc.last_child FROM child_counters cc INNER JOIN wisps w ON w.id = cc.parent_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_child_counters > 0 AND @has_wisps > 0 AND @has_wisp_child_counters > 0,
    'UPDATE wisp_child_counters wcc JOIN child_counters cc ON cc.parent_id = wcc.parent_id JOIN wisps w ON w.id = cc.parent_id SET wcc.last_child = GREATEST(wcc.last_child, cc.last_child)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_child_counters > 0 AND @has_wisps > 0 AND @has_wisp_child_counters > 0,
    'DELETE cc FROM child_counters cc INNER JOIN wisps w ON w.id = cc.parent_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_child_counters > 0,
    'DELETE cc FROM child_counters cc LEFT JOIN issues i ON i.id = cc.parent_id WHERE i.id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
