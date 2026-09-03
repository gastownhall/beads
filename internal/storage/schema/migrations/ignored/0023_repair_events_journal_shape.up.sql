-- Repair the events-journal shape on workspaces provisioned from the fork
-- lineage that created those tables under a different ignored slot (bd-t9ovd).
--
-- A fork branch (bd-enterprise PR #32, feat/native-postgres-addon) shipped its
-- own ignored/0017_create_events_journal -- the slot upstream had already
-- assigned to 0017_add_wisps_defer_until_index. A workspace provisioned from
-- that lineage and later opened by an upstream binary converges SILENTLY
-- WRONG: its ignored cursor already reads past 17, so upstream's 0017..0022
-- are treated as applied without ever having run, and
--
--   (a) idx_wisps_defer_until is permanently missing, so the ready-work
--       deferred-parents probe full-scans wisps on every call (0017's whole
--       reason for existing);
--   (b) ignored/0022's guarded create finds bd_events_journal already present
--       and correctly leaves it alone -- so the fork's TEXT dep_json and
--       comment_json survive (TEXT caps at 65535 bytes: an oversized comment
--       fails the journal INSERT and therefore ROLLS BACK THE USER'S MUTATION,
--       since the journal row commits in that mutation's own transaction), and
--       idx_bd_events_journal_ts is absent, so the retain-days floor's
--       `MIN(seq) WHERE ts >= ?` full-scans on every unattended prune pass.
--
-- Nothing already in the tree detects any of this. The cursor-reality
-- sentinels (schema.go's cursorContradictedBySchema) test table EXISTENCE
-- only, and the content_hash skew comparison structurally cannot reach it:
-- ignored_schema_migrations is itself dolt_ignored, so two clones never see
-- each other's rows.
--
-- This is insurance, not the repair of a known population -- such a workspace
-- is believed rare and may well not exist. The point is to turn an
-- undetectable divergence into a no-op question: every step below probes for
-- its own drift independently and does nothing when it is absent, so a healthy
-- workspace arriving through either door (fresh init, fresh clone) runs the
-- probes and no DDL at all. Independent guards also make the file replayable
-- from any intermediate state, which crash-replay of an interrupted pass
-- requires.
--
-- Nothing is dropped or renamed (the ignored plane's no-DROP invariant), and
-- neither bd_events_seq nor a single journal row is read or written: the
-- counter value is the one piece of state a shape repair must never perturb.

-- The two payload columns, probed separately. ignored/0021's LONGTEXT widening
-- is the model, with DATA_TYPE in place of its COLUMN_TYPE: these columns carry
-- no length or attributes, so the bare type name is the whole comparison. An
-- absent table yields an empty result set, the variable is NULL, and `NULL = 1`
-- routes IF to its no-op branch -- which is why "the table exists" stays
-- implicit here. The index steps below cannot borrow that trick (a STATISTICS
-- count of 0 reads the same for "no index" and "no table") and spell the TABLES
-- probe out, as ignored/0017 does. The definitions mirror 0022's CREATE
-- exactly: nullable, no default.
SET @dep_json_is_text = (
    SELECT IF(DATA_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'dep_json'
);
SET @sql = IF(@dep_json_is_text = 1,
    'ALTER TABLE bd_events_journal MODIFY COLUMN dep_json LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @comment_json_is_text = (
    SELECT IF(DATA_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'comment_json'
);
SET @sql = IF(@comment_json_is_text = 1,
    'ALTER TABLE bd_events_journal MODIFY COLUMN comment_json LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- The retention index. 0022 and main 0064 declare it inline in their CREATE,
-- so only a journal table built elsewhere can be missing it.
SET @needs_journal_ts_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'bd_events_journal'
          AND INDEX_NAME = 'idx_bd_events_journal_ts') = 0,
    1, 0
);
SET @sql = IF(@needs_journal_ts_index = 1,
    'CREATE INDEX idx_bd_events_journal_ts ON bd_events_journal(ts)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Casualty (a): the index ignored/0017 would have created had the fork not
-- consumed its slot. Guard and index definition are 0017's, verbatim, so a
-- workspace repaired here and one that ran 0017 normally are indistinguishable.
SET @needs_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'idx_wisps_defer_until') = 0,
    1, 0
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_defer_until ON wisps(defer_until)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
