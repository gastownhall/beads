-- Migration 0075: widen the content columns 0048/0049/0065 left at their
-- original 64 KiB cap (GH#3925).
--
-- TEXT and BLOB cap a value at 65535 bytes. 0049 (and 0048/0057 for the
-- events value columns, 0065 for wisp_comments.text) widened every
-- large-content column it named to LONGTEXT, and PR #3846 proposed the rest
-- as MEDIUMTEXT; this migration finishes the job with LONGTEXT, the type the
-- earlier widenings settled on. The columns it covers each still refuse a
-- value the column feeding them already accepts:
--
--   events.comment       a reopen reason is written to events.new_value
--                        (LONGTEXT) and then again as a commented event here,
--                        so a reason over the cap rolled the reopen back; a
--                        large AddComment on a durable issue failed the same
--                        way. 0062's recreate and ignored/0019 kept it TEXT.
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
-- events is clone-local (dolt_ignored since 0062), so this ships with an
-- ignored-series twin, ignored/0031: a fresh clone materializes it from
-- ignored/0019 with the main cursor already at-latest and never runs this
-- file (check D of scripts/check-migration-hygiene.sh).
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
