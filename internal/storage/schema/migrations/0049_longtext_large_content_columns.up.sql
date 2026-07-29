-- Migration 0049: Use LONGTEXT for large-content columns.
--
-- issues/wisps: description, design, acceptance_criteria, notes, close_reason
-- comments: text
--
-- TEXT is capped at 65535 bytes. Bead descriptions with embedded base64
-- images or large agent outputs exceed that limit, causing MySQL Error 1105
-- on bd import. LONGTEXT removes the practical size ceiling. Each MODIFY is
-- guarded by an INFORMATION_SCHEMA check so the migration is idempotent.
--
-- MODIFY COLUMN replaces the entire column definition, so each restated
-- column must carry its original DEFAULT. The wisps large-content columns
-- were created NOT NULL DEFAULT '' (0020) and close_reason on both tables is
-- nullable DEFAULT '' (0001/0020); those defaults are preserved here.
-- Dropping them regresses inserts that omit the column to
-- "Field '<col>' doesn't have a default value". The issues
-- description/design/acceptance_criteria/notes and comments.text columns were
-- created NOT NULL with no default (0001/0004) and intentionally keep none.

-- issues: description, design, acceptance_criteria, notes (NOT NULL, no default)
SET @issues_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'description'
);
SET @sql = IF(@issues_needs_fix = 1,
    'ALTER TABLE issues MODIFY COLUMN description LONGTEXT NOT NULL, MODIFY COLUMN design LONGTEXT NOT NULL, MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL, MODIFY COLUMN notes LONGTEXT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- issues: close_reason (nullable, DEFAULT '')
SET @issues_cr_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'close_reason'
);
SET @sql = IF(@issues_cr_needs_fix = 1,
    'ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps: description, design, acceptance_criteria, notes (NOT NULL DEFAULT '')
SET @wisps_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'description'
);
SET @sql = IF(@wisps_needs_fix = 1,
    'ALTER TABLE wisps MODIFY COLUMN description LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN design LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL DEFAULT '''', MODIFY COLUMN notes LONGTEXT NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps: close_reason (nullable, DEFAULT '')
SET @wisps_cr_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'close_reason'
);
SET @sql = IF(@wisps_cr_needs_fix = 1,
    'ALTER TABLE wisps MODIFY COLUMN close_reason LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- comments: text (NOT NULL, no default)
SET @comments_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'comments'
      AND COLUMN_NAME = 'text'
);
SET @sql = IF(@comments_needs_fix = 1,
    'ALTER TABLE comments MODIFY COLUMN text LONGTEXT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
