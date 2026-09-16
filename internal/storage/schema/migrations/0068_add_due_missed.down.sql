-- Roll back the missed-deadline counter. Guarded so an issues-only or
-- partially-applied workspace rolls back as safely as it migrated up
-- (0054/0060 precedent).
--
-- Any PRIORITY the escalation step raised is deliberately not reversed: after
-- rolling back nothing records which bump came from an escalation, so lowering
-- "the escalated ones" would be a guess that could bury real work. Leaving the
-- priorities is the safe direction.

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'due_missed'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE issues DROP COLUMN due_missed', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'due_missed'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE wisps DROP COLUMN due_missed', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
