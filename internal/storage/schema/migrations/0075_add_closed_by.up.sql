-- closed_by: the actor whose write closed the row, the close-side twin of
-- created_by (types.Issue.ClosedBy, GH#8617).
--
-- Before this column the closing actor reached only the events table and the
-- events journal (issueops.closeIssueInTx's RecordEventInTable /
-- RecordEventInTx calls), both clone-local since migration 0062, so the
-- issue row -- and with it `bd show --json`, `bd list --json` and `bd export`
-- -- never said who closed an issue, and a fresh clone lost the fact
-- entirely. closed_by_session is not a substitute: it is a Claude Code
-- session id, empty for humans and other agents.
--
-- VARCHAR(255) DEFAULT '', the exact shape of created_by and
-- closed_by_session in 0001: '' is "unknown or not closed" (a row that
-- predates this column, or an actor-less write), the empty default rewrites
-- no existing row's cell, and no bead row is touched, which keeps `bd
-- events`' "migrations never touch a bead" contract. content_hash and
-- updated_at are unchanged: ComputeContentHash excludes the close group
-- (close_reason and closed_by_session already are excluded), and ADD COLUMN
-- writes no row.
--
-- No backfill. A historical close's actor lives only in the clone-local
-- journal, so a backfill would either fork every clone or mint values a
-- fresh clone contradicts. Rows closed before this column read ''.
--
-- The wisps arm is guarded on the table existing (older workspaces created
-- issues-only; 0054/0060/0073 precedent). The ignored-series twin
-- (ignored/0032) carries the column to fresh clones whose main cursor
-- arrives already past this file -- wisps is dolt_ignored (migration 0019),
-- so this ALTER never runs there; ignored/0013 (row_lock), ignored/0020
-- (storage_class) and ignored/0027 (current_revision) are the precedent.
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

-- wisps.closed_by, when the clone-local wisps table exists.
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'closed_by') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
