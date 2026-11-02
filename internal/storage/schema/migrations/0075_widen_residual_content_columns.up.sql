-- Migration 0075: widen the content columns 0048/0049/0065 left at their
-- original 64 KiB cap (GH#3925).
--
-- TEXT and BLOB cap a value at 65535 bytes. 0049 (and 0048/0057 for the
-- events value columns, 0065 for wisp_comments.text) widened every
-- large-content column it named to LONGTEXT, and PR #3846 proposed the rest
-- as MEDIUMTEXT; this migration finishes the job with LONGTEXT, the type the
-- earlier widenings settled on (LONGBLOB for the one binary column). Each
-- column it covers still refuses a value the column feeding it accepts:
--
--   events.comment       a reopen reason is written to events.new_value
--                        (LONGTEXT) and then again as a commented event here,
--                        so a reason over the cap rolled the reopen back; a
--                        large AddComment on a durable issue failed the same
--                        way. 0062's recreate and ignored/0019 kept it TEXT.
--
--   wisp_events.comment  the same comment on a wisp failed the same way, and
--                        promotion and demotion copy event rows between the
--                        two planes with INSERT IGNORE, which truncates a
--                        value the destination cannot hold instead of failing.
--                        The planes must agree on the width. Its DEFAULT ''
--                        (0021, ignored/0001) is kept.
--
--   compaction_snapshots.snapshot_json
--                        SnapshotIssue marshals the issue's text fields, all
--                        LONGTEXT, into this BLOB (0010), so archiving a large
--                        issue failed and tier-1 compaction aborted. It stays
--                        binary -- LONGBLOB, as 0068 retyped durable_state --
--                        so the snapshot's bytes come back unchanged.
--
--   issue_snapshots.original_content, issue_snapshots.archived_events
--                        nothing writes this table at runtime any more (it is
--                        read, counted and deleted); both TEXT columns (0009)
--                        are widened so a snapshot row can hold what the
--                        issue it copies holds. Each keeps its nullability.
--
-- A plain INSERT of an oversized value fails with Error 1105 ("too large for
-- column"); the silent truncation GH#3925 describes is what an INSERT IGNORE
-- copy does when its source is wider than its destination.
--
-- EVERY COLUMN HAS ITS OWN GUARD AND ITS OWN ALTER, never one shared probe:
-- 0057 records the #4353 hazard (an embedded engine before dolthub/dolt#11126
-- re-derives a TEXT/LONGTEXT column's storage-encoding tag on every MODIFY,
-- even a no-op re-widen, and corrupts its rows), and a shared guard would
-- either skip a still-narrow column or re-MODIFY an already-wide one on a
-- database that widened them unevenly. Each probe reads the current
-- database's COLUMN_TYPE; a missing table or column yields NULL, takes the
-- SELECT 1 branch, and so replay is a no-op. MODIFY COLUMN replaces the whole
-- definition, so each ALTER restates the column's nullability and default
-- exactly as created.
--
-- events and wisp_events are clone-local (dolt_ignored; events since 0062),
-- so this ships with an ignored-series twin, ignored/0031: a fresh clone
-- materializes them from ignored/0019 and ignored/0001 with the main cursor
-- already at-latest and never runs this file (check D of
-- scripts/check-migration-hygiene.sh). issue_snapshots and
-- compaction_snapshots are versioned and reach every clone through this file,
-- so the twin does not touch them.
SET @events_comment_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@events_comment_needs_widen = 1,
    'ALTER TABLE events MODIFY COLUMN comment LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisp_events_comment_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@wisp_events_comment_needs_widen = 1,
    'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @issue_snapshots_original_content_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_snapshots'
      AND COLUMN_NAME = 'original_content'
);
SET @sql = IF(@issue_snapshots_original_content_needs_widen = 1,
    'ALTER TABLE issue_snapshots MODIFY COLUMN original_content LONGTEXT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @issue_snapshots_archived_events_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_snapshots'
      AND COLUMN_NAME = 'archived_events'
);
SET @sql = IF(@issue_snapshots_archived_events_needs_widen = 1,
    'ALTER TABLE issue_snapshots MODIFY COLUMN archived_events LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @compaction_snapshot_json_needs_widen = (
    SELECT IF(COLUMN_TYPE = 'blob', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'compaction_snapshots'
      AND COLUMN_NAME = 'snapshot_json'
);
SET @sql = IF(@compaction_snapshot_json_needs_widen = 1,
    'ALTER TABLE compaction_snapshots MODIFY COLUMN snapshot_json LONGBLOB NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
