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
--
-- The unique index that makes a value impossible to hand out twice,
-- uk_wisps_arrival_seq, lives on the clone-local wisps table. The
-- ignored-series twin (ignored/0029) creates it, and is also what carries the
-- column to fresh clones whose main cursor arrives at-latest without running
-- this file. This file creates it too, guarded the same way, for the one door
-- 0029 cannot cover: this migration's down drops the column and the index with
-- it but leaves ignored/0029 recorded, so when 0073 is applied again it is the
-- only migration that runs. On an in-place upgrade the index lands here and
-- 0029's guard finds it. 0029's counter raise is not repeated: on either path
-- this file adds the column empty, so there is no stamp yet for the counter to
-- stay above.
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

-- uk_wisps_arrival_seq, on wisps only: on issues the column is always NULL.
-- Guarded on the wisps table existing and the index not, as in ignored/0029.
SET @needs_index = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'uk_wisps_arrival_seq') = 0,
    1, 0);
SET @sql = IF(@needs_index = 1,
    'CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
