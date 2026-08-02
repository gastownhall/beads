-- Migration 0057: idempotency guard for the events value-column widening
-- (residual of #4353).
--
-- 0048_widen_event_value_columns unconditionally runs
-- `ALTER TABLE events MODIFY old_value/new_value LONGTEXT` with no
-- INFORMATION_SCHEMA guard, unlike 0049 (which widens every other
-- large-content column and guards each MODIFY individually). #4353: an
-- embedded-dolt engine pinned before dolthub/dolt#11126 re-derives a
-- TEXT/LONGTEXT column's on-disk storage-encoding tag -- without rewriting
-- rows -- on every MODIFY, even a logical no-op that re-widens an
-- already-LONGTEXT column. An unguarded re-application flips the tag and
-- corrupts every row: "invalid hash length: 19" on read afterwards, or
-- "invalid hash length: 1" on insert/merge if the flip goes the other way.
--
-- 0048 itself is shipped and frozen (scripts/check-migration-hygiene.sh
-- Check C forbids editing a migration file that already exists on main;
-- there is no hash-reconciliation override for that check), so it cannot be
-- given this guard in place. This migration cannot intercept a raw
-- re-execution of 0048's frozen SQL text by tooling outside bd's own
-- cursor-gated migrate chain -- that is a separate bug in whatever does
-- that. What it closes: within the normal migrate path (fresh DB, or a DB
-- resuming mid-chain) events.old_value/new_value converge to LONGTEXT
-- exactly once, and any further pass through this version is a guarded
-- no-op instead of an unconditional MODIFY, matching the idempotent pattern
-- 0049 already established for the other large-content columns.
--
-- old_value and new_value are guarded INDEPENDENTLY, not behind one shared
-- check: a single check on old_value's type covering a combined MODIFY of
-- both columns would either silently skip converting new_value (if
-- old_value already reads LONGTEXT but new_value is still TEXT -- e.g. a
-- database that picked up 0048 unevenly through some other historical
-- drift) or re-issue a MODIFY on a column that is already LONGTEXT (if
-- old_value is still TEXT but new_value is already LONGTEXT), re-triggering
-- the exact encoding-flip risk this migration exists to guard against.
SET @old_value_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'old_value'
);
SET @sql = IF(@old_value_needs_fix = 1,
    'ALTER TABLE events MODIFY COLUMN old_value LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @new_value_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'new_value'
);
SET @sql = IF(@new_value_needs_fix = 1,
    'ALTER TABLE events MODIFY COLUMN new_value LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
