-- closed_by: the actor whose write moved the row into closed
-- (types.Issue.ClosedBy, GH#8617).
--
-- The closing actor was recorded only in the events table and the events
-- journal, both clone-local (dolt_ignored since 0062), so the issue row -- and
-- with it `bd show --json`, `bd list --json` and `bd export` -- never said who
-- closed an issue, and a fresh clone or an export/import lost it entirely. This
-- column is the close-side twin of created_by: stamped from the write's actor
-- by the close-lifecycle writers, cleared on reopen, carried verbatim by
-- insert, upsert and merge.
--
-- VARCHAR(255) DEFAULT '' matches created_by and closed_by_session (0001,
-- ignored/0001). There is no backfill: an existing row reads '' (an unknown
-- closer), so no bead row is rewritten -- the column add changes neither
-- content_hash (ComputeContentHash leaves the close group out) nor updated_at.
--
-- wisps is dolt_ignored, so its schema is clone-local: ignored/0032 carries
-- the column to a clone whose main cursor arrives already past this version.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back, and so a raw .up.sql replay onto an
-- already-migrated store is a no-op (0060/0067/0073 precedent).

-- issues.closed_by
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'closed_by'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.closed_by. Guarded on the wisps table existing (older workspaces
-- created issues-only; 0054/0060/0073 precedent).
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'closed_by') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
