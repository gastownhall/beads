-- Migration 0066: record the mutating ACTOR on every events-journal row
-- (bd-mbrxm).
--
-- bd_events_journal rows record WHAT changed and in what order, but not WHO —
-- an external consumer reconciling concurrent replicas (an LWW semantic-
-- conflict detector) needs each mutation attributed to the identity that made
-- it. The writer (issueops.insertEventRow) now stamps every row with the same
-- actor the audit-events table resolves for the mutation; '' marks the paths
-- that genuinely have none (derived blocked-state maintenance, actorless
-- delete plumbing) and every row written before this column existed — the
-- NOT NULL DEFAULT '' is what makes the backfill-free upgrade read uniformly.
--
-- Guarded on an INFORMATION_SCHEMA.COLUMNS probe, so it is a no-op on a
-- workspace already carrying the column and idempotent on replay. The
-- table-exists half keeps the file replayable from any intermediate state
-- (the ignored/0023 pattern): a COLUMNS count of 0 reads the same for "no
-- column" and "no table", and ALTERing a missing table would abort the pass.
--
-- IT SHIPS WITH AN IGNORED-SERIES TWIN (ignored/0025), which is not optional:
-- bd_events_journal is a clone-local dolt-ignored table, so a fresh clone
-- materializes it from the ignored series while the main cursor arrives
-- at-latest and never runs this file. That is check D of
-- scripts/check-migration-hygiene.sh — the mechanism 0065/ignored/0024 record.
SET @needs_actor = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'bd_events_journal'
          AND COLUMN_NAME = 'actor') = 0,
    1, 0
);
SET @sql = IF(@needs_actor = 1,
    'ALTER TABLE bd_events_journal ADD COLUMN actor VARCHAR(255) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
