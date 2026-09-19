-- Reverse of 0066: drop the actor column again. The column is additive and the
-- table is clone-local (dolt_ignored), so this only forgets attribution on
-- rows this clone already holds; seq, ordering and every other member are
-- untouched. Guarded so a workspace that never gained the column no-ops.
-- Pair it with a binary rollback: a 0066-era bd names `actor` in its journal
-- INSERT and SELECT, so against a downgraded table every journaled mutation
-- would roll back and `bd events tail` would error.
SET @has_actor = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'actor'
);
SET @sql = IF(@has_actor = 1,
    'ALTER TABLE bd_events_journal DROP COLUMN actor',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
