-- Migration 0038: Drop HOP-specific quality_score and crystallizes columns.
--
-- These were Gas Town agent credibility fields that don't belong in beads.
-- No version of beads' embedded schema migrations ever created them; legacy
-- DBs may have them from the pre-migration Go schema. Each DROP is guarded by
-- an INFORMATION_SCHEMA check so missing columns are a clean no-op rather
-- than relying on Dolt's "does not have column" error wording.

-- issues.quality_score
SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'quality_score'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE issues DROP COLUMN quality_score',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- issues.crystallizes
SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'crystallizes'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE issues DROP COLUMN crystallizes',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.quality_score
SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'quality_score'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisps DROP COLUMN quality_score',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.crystallizes
SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'crystallizes'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisps DROP COLUMN crystallizes',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
