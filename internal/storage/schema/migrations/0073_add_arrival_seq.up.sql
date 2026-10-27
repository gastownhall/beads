-- arrival_seq: the commit-ordered create sequence stamped on a row
-- (types.Issue.ArrivalSeq, issueops.RecordEventInTx).
--
-- The value is the events-journal seq of the `create` record written by the
-- transaction that inserted the row, stored on the row by that transaction. The
-- journal seq is drawn from the bd_events_seq counter inside the mutation's own
-- transaction, so it is gapless and in commit order; carrying it on the row is
-- what lets a consumer recover the order rows were created in after the journal
-- records themselves have been pruned.
--
-- THE COLUMN ONLY MEANS SOMETHING ON wisps. wisps, the journal and the counter
-- are all clone-local (dolt_ignored), so a wisps value is always one this
-- clone's counter assigned. issues replicates through Dolt: a per-clone number
-- there would reach clones whose counter never produced it, and the same row
-- imported in two clones would become an add/add merge conflict. issues carries
-- the column for SHAPE only and it stays NULL -- the shared issues/wisps scans
-- select one column list from both planes, and TestSchemaParityIssuesVsWisps
-- holds the two tables to the same column names. wisps.row_lock (0054 +
-- ignored/0013) and wisps.current_revision (0067 + ignored/0027) are the
-- precedent for a column carried on both planes and meaningful on one.
--
-- BIGINT, nullable, no default: NULL means "never stamped" (the writer did not
-- journal, or the row predates this column), so no existing row is rewritten.
-- The unique index that makes a value impossible to hand out twice lives on the
-- clone-local wisps table and is created by the ignored-series twin
-- (ignored/0029), which is also what carries the column to fresh clones whose
-- main cursor arrives at-latest without running this file.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back, and so a raw .up.sql replay onto an
-- already-migrated store is a no-op (0060/0067 precedent).

-- issues.arrival_seq (shape only; always NULL)
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'arrival_seq'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN arrival_seq BIGINT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.arrival_seq. Guarded on the wisps table existing (older workspaces
-- created issues-only; 0054/0060 precedent).
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'arrival_seq') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
