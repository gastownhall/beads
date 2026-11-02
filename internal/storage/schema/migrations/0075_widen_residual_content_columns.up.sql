-- Migration 0075: widen the residual 65,535-byte content columns (GH#3925).
--
-- 0049 widened issues/wisps/comments large-content columns to LONGTEXT, 0048
-- (+ 0057's idempotency guard) the events value columns, 0065 (with its
-- ignored/0024 twin) wisp_comments.text, and ignored/0008 the wisp_events
-- value columns. What the #3925 report named that none of those reached:
-- events.comment and wisp_events.comment are still TEXT. A reopen records
-- its reason twice — once into the LONGTEXT new_value and then again, as the
-- audit comment, into
-- events.comment via AddCommentEventInTx — so a reason over the TEXT cap
-- fails the second write and rolls back the entire reopen: a closed issue
-- cannot be reopened with a large reason at all. PR #3846 proposed widening
-- the then-TEXT columns to MEDIUMTEXT and was deferred for the storage-layer
-- redesign; the redesign has since landed (events moved to the dolt-ignored
-- plane by 0062/ignored/0019, which recreated comment as TEXT again), and the
-- widened type here is LONGTEXT to match the 0048/0049/0065 precedent rather
-- than #3846's MEDIUMTEXT: no second widening migration should ever be needed
-- for byte-size reasons.
--
-- events.comment was created nullable with no default (0005) and 0062's
-- recreate kept that shape; the MODIFY restates it exactly, because MODIFY
-- COLUMN replaces the whole definition.
--
-- Each column is guarded INDEPENDENTLY on its own COLUMN_TYPE probe, never
-- two behind one shared check: a shared guard mishandles uneven drift (one
-- column already wide, the other not) and re-issuing a MODIFY on an
-- already-LONGTEXT column re-triggers the encoding-tag flip of #4353 that
-- 0057 exists to guard against. A missing table yields NULL and the SELECT 1
-- branch, so the same text is safe to replay over a fresh clone whose main
-- cursor arrived at-latest without the dolt-ignored events table.

-- events.comment: the reopen/audit comment path (issueops reopen.go, dolt
-- events.go AddComment, embeddeddolt store.go AddComment, transaction.go).
SET @events_comment_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@events_comment_needs_fix = 1,
    'ALTER TABLE events MODIFY COLUMN comment LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisp_events.comment: the same audit path on the wisp plane, and the source
-- of the cross-plane copies. PromoteFromEphemeral copies wisp_events.comment
-- into events.comment and demotion copies it back (both INSERT IGNORE, which
-- truncates oversized values silently when the two widths differ), so the two
-- planes must hold the same width. wisp_events.comment was created nullable
-- DEFAULT '' (0021); ignored/0001 recreates it the same way on clones, and
-- ignored/0031 widens it there.
SET @wisp_events_comment_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND COLUMN_NAME = 'comment'
);
SET @sql = IF(@wisp_events_comment_needs_fix = 1,
    'ALTER TABLE wisp_events MODIFY COLUMN comment LONGTEXT DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
