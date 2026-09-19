-- Create the durable events journal tables (bd-opisf).
--
-- bd_events_journal is an append-only, seq-ordered record of every committed
-- bead mutation, written in the SAME transaction as the mutation itself at the
-- shared issueops seam (see issueops.RecordEventInTx). External tooling reads
-- it to replay the exact mutation history of the workspace.
--
-- Both tables are clone-local (registered dolt_ignored, seeded by MigrateUp's
-- doltIgnorePatterns before this migration runs): they are working-set
-- operational state, never versioned, never committed to history, and not
-- replicated. That is the point — a journal row per mutation on the versioned
-- plane would double the history churn 0062 just removed, and a per-clone seq
-- counter on a replicated table would conflict on every merge. The cost is
-- that the journal describes THIS clone's working set only.
--
-- seq is NOT AUTO_INCREMENT. AUTO_INCREMENT assigns at INSERT, not at commit,
-- so under concurrent transactions (the shared SQL server) a lower seq can
-- commit AFTER a higher seq becomes visible, and a consumer tailing
-- WHERE seq > cursor permanently skips it. Instead seq is drawn from the
-- single-row counter table bd_events_seq inside the mutation's own transaction
-- (issueops.nextEventSeq): the shared counter row makes concurrent allocators
-- conflict, so exactly one commit order survives and the surviving seqs are
-- gapless and commit-ordered (a rolled-back allocator burns no seq). The
-- counter table is dolt_ignored too, so it shares the journal's locality.
--
-- Fresh clones never run this migration (the schema_migrations cursor arrives
-- at-latest); they materialize both tables via ignored migration 0022, whose
-- shape must stay identical to this one (pinned by
-- internal/storage/embeddeddolt/migrate_ignored_plane_shape_test.go). The
-- __temp__ + conditional RENAME dance keeps each CREATE idempotent so a re-run,
-- or a run against a workspace that already has the tables, is a no-op.
--
-- comment_json and dep_json are LONGTEXT, not TEXT. TEXT caps at 65535 bytes,
-- and migration 0049 widened comments.text to LONGTEXT precisely because
-- oversized comments (embedded base64, large agent output) are real. Since the
-- journal row commits in the SAME transaction as the mutation it records, a
-- comment that overflowed a TEXT payload column would fail the journal INSERT
-- and therefore ROLL BACK THE USER'S WRITE — the journal would turn a working
-- mutation into an error. issue_json is LONGTEXT for the same reason (it
-- embeds those same widened issue columns).
--
-- idx_bd_events_journal_ts serves the retain-days floor, which resolves to
-- `SELECT MIN(seq) FROM bd_events_journal WHERE ts >= ?`
-- (issueops.EventsPruneDaysFloorQuery). Without an index on ts that is a full
-- table scan of the journal, and retention now runs it unattended on a
-- schedule — every automatic prune pass, on a table sized precisely to be
-- large. The scan grows with exactly the thing the index is there to bound.
--
-- TWO DELIBERATE DIVERGENCES from the enterprise 0016/0017 shape, both free
-- only because these tables have never shipped in OSS — retrofitting either
-- after release would need another migration. Flag both at the next enterprise
-- sync:
--   1. This ts index, which the reference shape does not carry (its age floor
--      was a per-row `ts < cutoff` predicate rather than a MIN(seq) resolution).
--   2. The LONGTEXT payload columns described above, where the reference shape
--      types both as TEXT and carries that defect latently.
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
-- Seed the counter row (idempotent), then raise it to the journal's current
-- high-water mark so seq never resets or collides (0 on a fresh table). Done as
-- VALUES + a GREATEST update rather than INSERT ... SELECT MAX(): in Dolt a
-- SELECT that mixes a literal with an aggregate over an EMPTY table yields zero
-- rows, so an INSERT ... SELECT would seed nothing on a fresh workspace.
INSERT IGNORE INTO bd_events_seq (id, next_seq) VALUES (0, 0);
UPDATE bd_events_seq
    SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(seq) FROM bd_events_journal), 0))
    WHERE id = 0;
