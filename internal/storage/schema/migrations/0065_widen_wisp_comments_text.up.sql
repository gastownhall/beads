-- Migration 0065: finish 0049 on the ephemeral plane — wisp_comments.text.
--
-- 0049 widened every large-content column it could name from TEXT to LONGTEXT:
-- issues and wisps description/design/acceptance_criteria/notes/close_reason,
-- and comments.text. It MISSED wisp_comments.text, which has been TEXT since
-- 0021 created the table, and the miss was invisible for as long as nothing
-- published a comment write that could carry a large body.
--
-- THE TWO COMMENT PLANES DISAGREED ABOUT A MEMBER BOUND. TEXT caps at 65535
-- bytes, so the same comment — a stack trace, a diff, a captured agent
-- transcript — writes fine against a durable issue and fails with MySQL Error
-- 1105 against a wisp. That is a wire-visible divergence on one documented
-- member: `POST /v0/beads/issues/{id}/comments` resolves its anchor across both
-- planes deliberately, so a caller cannot know which side of the bound it is on
-- until the write fails. Widening is the fix rather than a documented caveat —
-- the planes should not diverge on what a comment may contain, and the caveat
-- would have to be re-stated by every client.
--
-- Guarded on the same COLUMN_TYPE = 'text' probe 0049 uses, so this is a no-op
-- on a workspace already carrying LONGTEXT and idempotent on replay. The column
-- was created NOT NULL with no default (0021) and intentionally keeps none,
-- exactly as comments.text does in 0049 — MODIFY COLUMN replaces the whole
-- definition, so anything restated here would become the new definition.
--
-- IT SHIPS WITH AN IGNORED-SERIES TWIN (ignored/0024), which is not optional:
-- wisp_comments is a clone-local dolt-ignored table, so a fresh clone
-- materializes it from the ignored series while the main cursor arrives
-- at-latest and never runs this file. That is check D of
-- scripts/check-migration-hygiene.sh, and the mechanism ignored/0021 (the wisps
-- half of this same 0049 gap), ignored/0020 and ignored/0013 each record.
SET @wisp_comments_needs_fix = (
    SELECT IF(COLUMN_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND COLUMN_NAME = 'text'
);
SET @sql = IF(@wisp_comments_needs_fix = 1,
    'ALTER TABLE wisp_comments MODIFY COLUMN text LONGTEXT NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
