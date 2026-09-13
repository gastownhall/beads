-- Escalation counter for missed deadlines.
--
--   due_missed  how many times this bead's due date has ARRIVED with the work
--               still open. 0 on every row this migration touches.
--
-- The due sweep re-dates a missed deadline forward so it nags again instead of
-- going silent (issueops.DueMissGrace). That alone repeats forever at the same
-- priority: a bead missed once and a bead missed twenty times are
-- indistinguishable, so nothing ever escalates and the nag becomes noise. The
-- counter is what makes a repeated miss legible, and what the one escalation
-- step stands on — at issueops.DueMissEscalateAt misses the bead's priority is
-- raised once.
--
-- It is SYSTEM-MAINTAINED: the sweep is the only writer, there is no flag that
-- sets it, and nothing infers a value for history. A count is state about what
-- the clock observed, not a field describing the work.
--
-- This migration is SCHEMA ONLY: it adds the column and writes no rows.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back (0052/0054/0060 precedent).

-- issues.
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'due_missed'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN due_missed INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps: the same column. Guarded on the table existing (older workspaces
-- created issues-only; 0054/0060 precedent). The clone-local twin that
-- carries this through the fresh-clone door is ignored/0027.
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
