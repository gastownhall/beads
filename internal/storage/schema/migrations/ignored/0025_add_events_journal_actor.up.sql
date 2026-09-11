-- Ignored migration 0025: ensure bd_events_journal carries the actor column on
-- every clone (bd-mbrxm).
--
-- The twin of synced migration 0066, and required by check D of
-- scripts/check-migration-hygiene.sh rather than offered: bd_events_journal is
-- clone-local and dolt-ignored, so a fresh clone materializes it from
-- ignored/0022 — whose CREATE carries no actor column — while the main cursor
-- arrives at-latest and 0066 never runs there. Without this file an upgraded
-- workspace would journal attributed rows while a fresh clone's journal INSERT
-- fails on the unknown column — and because the journal row commits in the
-- mutation's own transaction, that failure would roll back the user's write.
-- Same fresh-clone door ignored/0024 (wisp_comments.text), ignored/0021,
-- ignored/0020 and ignored/0013 each record.
--
-- The definition mirrors 0066 exactly: actor is the acting identity the
-- audit-events table resolves for the mutation, '' for the genuinely
-- actorless paths and for rows written before the column existed. Guarded on
-- the same INFORMATION_SCHEMA probe (with 0023's explicit table-exists half,
-- since a COLUMNS count of 0 cannot tell "no column" from "no table"), so it
-- is a no-op on the fresh-init door and on an already-altered clone, and
-- idempotent on replay. Shape convergence across the two doors is pinned by
-- internal/storage/embeddeddolt/migrate_ignored_plane_shape_test.go.
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
