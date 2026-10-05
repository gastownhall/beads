-- Converge every observed shape of bd_events_journal on the canonical one
-- (BEADS-JOURNAL-PLAN.md §4.2b, PR A2).
--
-- ignored/0023 repairs this table too, but only by probing DATA_TYPE: a
-- column that is entirely ABSENT returns no row, DATA_TYPE reads NULL, and
-- `NULL = 1` takes the no-op branch. That gap is exactly what let three
-- distinct field shapes survive 0023 unrepaired:
--
--   - gas-city-inc (gci): seq, ts, op, issue_id, issue_json, dep_json —
--     comment_json and actor both absent outright (plan §1.1). 0023 widened
--     dep_json and added the ts index (both already correct, i.e. both were
--     present with some type), so neither of the two missing-column probes
--     ever fired.
--   - the events-journal-3666e5026 release shape: issue_json LONGTEXT,
--     dep_json TEXT, no comment_json, no ts index (plan §1.1, "where that
--     shape came from"). 0023's dep_json widen DOES fire here, but the
--     missing comment_json still does not, for the same reason as gci.
--   - the enterprise fork's ignored/0017 shape (0023's own header,
--     bd-t9ovd): dep_json and comment_json both TEXT (present, so 0023's
--     widen fires for both), no ts index, no idx_wisps_defer_until. This one
--     IS fully healed by 0023 already; it is included here as a fixture
--     (TestIgnored0028ConvergesAllShapes) precisely to pin that this file
--     stays a no-op on a shape 0023 already fixed, not to add new coverage
--     for it.
--
-- This file closes the gap left open above by probing EXISTENCE first for
-- the two columns that can be missing outright, then falling through to
-- 0023's widen-if-TEXT check for both payload columns and both indexes —
-- each guarded step probes its own drift independently and does nothing when
-- it is absent, so a healthy workspace through either door (fresh init,
-- fresh clone) runs every probe below and issues no DDL. See 0023's header
-- for why that independence matters (crash-replay of an interrupted pass)
-- and for the no-DROP, no-rename invariant this file also upholds.
--
-- It never reads or writes bd_events_seq or a single journal row: the
-- counter and every row must be byte-identical before and after
-- (TestIgnored0028ConvergesAllShapes).
--
-- Two of these columns are registered as sentinels in schema.go's
-- ignoredSource (`{bd_events_journal, comment_json, replayFloor: 21}` and
-- `{bd_events_journal, actor, replayFloor: 21}`): a store whose cursor already
-- reads past 28 but is missing either column — e.g. restored from a backup
-- taken between the two doors, or damaged out of band — heals itself on the
-- next writable open instead of staying silently broken forever
-- (TestSentinelJournalColumnsReplay). Floor 21 sits below this file (28) and
-- below the table's creator (ignored/0022), per schema.go's two replay-floor
-- constraints, and above every unguarded statement in the series
-- (TestReplay22To28NoOpOnHealthyStore).
--
-- Journal tables change only on this (ignored) plane: scripts/check-
-- migration-hygiene.sh fails a main-plane migration that carries DDL against
-- any bd_events_* table, unconditionally, so this file — not a main-plane
-- twin — is the only place bd_events_journal's shape can converge.

-- comment_json: add as LONGTEXT if missing entirely; widen if it is TEXT.
-- The NULL branch is what 0023 never had: DATA_TYPE reads NULL only when the
-- column (or the table) is absent, and the table-absent case is excluded by
-- the replay-floor constraint above (0022, the table's creator, always runs
-- first in any replay this migration participates in).
SET @comment_json_type = (
    SELECT DATA_TYPE
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'comment_json'
);
SET @sql = IF(@comment_json_type IS NULL,
    'ALTER TABLE bd_events_journal ADD COLUMN comment_json LONGTEXT',
    IF(@comment_json_type = 'text',
        'ALTER TABLE bd_events_journal MODIFY COLUMN comment_json LONGTEXT',
        'SELECT 1'));
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- actor: add VARCHAR(255) NOT NULL DEFAULT '' if missing. ignored/0025
-- already does exactly this on the lineage that carries it; this is a second,
-- idempotent guard — identical definition, explicit table-exists probe as in
-- 0023/0025 — for any lineage that reaches 0028 without having run 0025 (the
-- sentinel above is what makes that heal rather than strand).
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

-- dep_json: widen if TEXT. 0023's own step, repeated here (not removed there:
-- shipped migrations are frozen, check C) for any lineage that reaches 0028
-- without ever having run it. Every creator of this table this repo or its
-- fork lineage has ever shipped declares dep_json with SOME type, so NULL
-- here can only mean "no table", which the replay floor rules out.
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

-- issue_json: widen if TEXT, same reasoning as dep_json. No observed shape
-- (gci, 3666e5026, the enterprise fork) has ever shipped this column as TEXT,
-- so this is a no-op everywhere it has been checked; included for the same
-- completeness reason as idx_bd_events_journal_issue below.
SET @issue_json_is_text = (
    SELECT IF(DATA_TYPE = 'text', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'issue_json'
);
SET @sql = IF(@issue_json_is_text = 1,
    'ALTER TABLE bd_events_journal MODIFY COLUMN issue_json LONGTEXT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- idx_bd_events_journal_ts: create if missing. 0023's own step, repeated for
-- the same reason as actor above.
SET @needs_ts_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'bd_events_journal'
          AND INDEX_NAME = 'idx_bd_events_journal_ts') = 0,
    1, 0
);
SET @sql = IF(@needs_ts_index = 1,
    'CREATE INDEX idx_bd_events_journal_ts ON bd_events_journal(ts)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- idx_bd_events_journal_issue: create if missing. Every observed shape
-- (gci, 3666e5026, the enterprise fork) already carries this index — it is
-- declared inline in every CREATE this repo or its fork lineage has ever
-- shipped — but an unconditional guard costs one more INFORMATION_SCHEMA
-- round trip and closes the same class of gap 0023 left open for
-- idx_bd_events_journal_ts, against a shape nobody has observed yet.
SET @needs_issue_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'bd_events_journal'
          AND INDEX_NAME = 'idx_bd_events_journal_issue') = 0,
    1, 0
);
SET @sql = IF(@needs_issue_index = 1,
    'CREATE INDEX idx_bd_events_journal_issue ON bd_events_journal(issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
