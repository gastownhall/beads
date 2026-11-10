-- Materialize the clone-local events journal tables (bd-opisf) on clones that
-- never ran main migration 0064. A fresh clone arrives with the
-- schema_migrations cursor at-latest (0064 already recorded) but WITHOUT the
-- tables, exactly like the leases (ignored/0012) and events (ignored/0019)
-- precedents: dolt_ignored tables live only in the working set, and a working
-- set is not what a clone receives.
--
-- bd_events_journal and bd_events_seq are dolt_ignored (seeded by MigrateUp's
-- doltIgnorePatterns before this migration runs): operational, never versioned
-- or replicated, so the seq stays monotonic with no merge conflict. seq is NOT
-- AUTO_INCREMENT — it is drawn from the single-row bd_events_seq counter inside
-- the mutation's own transaction so concurrent allocators conflict and the
-- surviving seqs are gapless and commit-ordered (see main migration 0064 and
-- issueops.nextEventSeq).
--
-- Shape must stay identical to 0064's CREATE; the two doors are diffed by
-- internal/storage/embeddeddolt/migrate_ignored_plane_shape_test.go. Same
-- __temp__ + conditional RENAME pattern for both tables: create only when
-- absent, never touch an existing table. See issueops.RecordEventInTx for the
-- writer.
--
-- The payload columns are LONGTEXT rather than TEXT: a journal row commits in
-- the same transaction as the mutation it records, so an oversized comment or
-- edge payload overflowing a 64KB TEXT column would fail the journal INSERT and
-- roll back the user's write. idx_bd_events_journal_ts serves the retain-days
-- floor's `MIN(seq) WHERE ts >= ?` resolution, which automatic retention runs
-- unattended on every pass. See 0064 for the full reasoning on both, and for
-- the two deliberate divergences from the enterprise 0016/0017 shape.
DROP TABLE IF EXISTS __temp__bd_events_journal;
CREATE TABLE __temp__bd_events_journal (
    seq BIGINT NOT NULL PRIMARY KEY,
    ts DATETIME NOT NULL,
    op VARCHAR(32) NOT NULL,
    issue_id VARCHAR(255) NOT NULL,
    issue_json LONGTEXT,
    dep_json LONGTEXT,
    comment_json LONGTEXT,
    INDEX idx_bd_events_journal_issue (issue_id),
    INDEX idx_bd_events_journal_ts (ts)
);
SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__bd_events_journal TO bd_events_journal', 'DROP TABLE __temp__bd_events_journal');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS __temp__bd_events_seq;
CREATE TABLE __temp__bd_events_seq (
    id INT NOT NULL PRIMARY KEY,
    next_seq BIGINT NOT NULL
);
SET @seq_exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_seq');
SET @seq_sql = IF(@seq_exists = 0, 'RENAME TABLE __temp__bd_events_seq TO bd_events_seq', 'DROP TABLE __temp__bd_events_seq');
PREPARE stmt FROM @seq_sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
-- Seed (idempotent), then raise to the journal high-water mark. See main
-- migration 0064 for why this is VALUES + GREATEST rather than INSERT ... SELECT.
INSERT IGNORE INTO bd_events_seq (id, next_seq) VALUES (0, 0);
UPDATE bd_events_seq
    SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(seq) FROM bd_events_journal), 0))
    WHERE id = 0;
