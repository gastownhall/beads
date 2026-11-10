-- Move the events audit table off the versioned plane (bd-red8u, Protocol
-- v0.1 C2.2: no per-write history cost for unversioned records).
--
-- events is the dominant history churner: every issue op appends audit rows,
-- so at fleet tempo the table is touched by ~80% of dolt commits and is the
-- primary driver of unbounded reachable-history growth (bd-7vb13
-- measurements). Like leases (0055) and repo_mtimes (0028), the rows keep
-- full SQL durability in the working set; what changes is that they stop
-- minting versioned history. Unlike those precedents, events is an
-- already-committed SYNCED table, and dolt_ignore only exempts tables that
-- have never been committed -- a drop+recreate netting out in one working set
-- leaves the table tracked-at-HEAD and permanently dirty. The drop must
-- therefore be committed on its own BEFORE the recreate, which makes this the
-- first self-committing table-plane migration (0040/0041 are the
-- self-commit precedent; --skip-empty keeps the replay idempotent).
--
-- Replication note (deliberate, per the 2026-07-30 mitigation-first ruling):
-- dolt_ignored tables do not replicate. Events are single-writer audit rows
-- with low cross-machine value; the C2.3 newest-wins replication leg
-- (bd-1quyq) re-adds unversioned sync later. Comments stay versioned until
-- that leg exists. The pre-flip events history remains in remotes until the
-- next squash window; only NEW growth stops.
--
-- Fresh clones never run this migration (their schema_migrations cursor
-- arrives at-latest); they materialize the table via ignored migration 0019.
-- Every step is guarded so a re-run from any intermediate state converges
-- (verified: full-replay and mid-crash states all land in the final shape).

-- Phase 0: Heal. A replay that crashed after the drop finds no events table;
-- recreate it empty so the copy below cannot fail. No-op on the normal path.
CREATE TABLE IF NOT EXISTS events (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_events_created_at (created_at),
    INDEX idx_events_issue (issue_id),
    CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

-- Phase 1: Preserve rows in a temp table. IF NOT EXISTS + INSERT IGNORE keep
-- a copy made by an interrupted earlier run intact (id is the PK, so the
-- union of the old copy and the current rows converges).
CREATE TABLE IF NOT EXISTS __temp__events_flip (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id)
);
INSERT IGNORE INTO __temp__events_flip (issue_id, event_type, actor, old_value, new_value, comment, created_at, id)
    SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at, id FROM events;

-- Phase 2: Drop the tracked table and commit the drop out of HEAD. The
-- DOLT_ADD is conditional on dolt_status actually showing a pending events
-- change: on first run the drop of the tracked table is pending; on a replay
-- the drop of the recreated (never-committed) table leaves no status row, and
-- an unconditional add of a table that exists nowhere would error. The add
-- uses '-f' because plain DOLT_ADD of a dolt_ignore'd table is a silent no-op:
-- on the normal path 'events' is not in dolt_ignore yet (Phase 3 adds it), but
-- a database that already carries the pattern while events is still tracked at
-- HEAD (branch-per-test databases materialize exactly that state; so would any
-- out-of-band-seeded pattern) needs the force or the drop never commits and
-- the recreate nets out to a permanently-dirty tracked table. The commit
-- is a bare statement so the drain path picks it up; --skip-empty makes the
-- replay a no-op instead of "nothing to commit" (the 0040/0041 lesson).
--
-- The bare COMMIT before the dolt_status read is load-bearing on the
-- sql-server path: migrations run there on a transaction-pinned connection,
-- and dolt_status reflects the WORKING SET, which an open SQL transaction has
-- not updated yet -- without it the probe reads 0, the DOLT_ADD is skipped,
-- and the drop+recreate nets out to a permanently-dirty tracked table.
-- Mid-body transaction commits are already this pass's semantics: CALL
-- DOLT_COMMIT (here and in 0040/0041) implicitly commits the session
-- transaction too, and every statement in this file is replay-safe from any
-- intermediate state. On autocommit paths (embedded engine, dolt CLI) the
-- bare COMMIT is a no-op.
DROP TABLE events;
COMMIT;
SET @flip_pending = (SELECT COUNT(*) FROM dolt_status WHERE table_name = 'events');
SET @sql = IF(@flip_pending > 0, 'CALL DOLT_ADD(''-f'', ''events'')', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: drop versioned events table for ignored-plane flip (bd-red8u)');

-- Phase 3: Register the ignore pattern (committed with the pass, so clones
-- inherit it) and recreate events in the working set, now dolt-ignored.
-- MigrateUp's cursor-gated doltIgnorePatterns seeding re-asserts the pattern
-- for databases materialized out-of-band.
REPLACE INTO dolt_ignore VALUES ('events', true);
CREATE TABLE IF NOT EXISTS events (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_events_created_at (created_at),
    INDEX idx_events_issue (issue_id),
    CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

-- Phase 4: Restore rows and clean up. __temp__events_flip was never
-- committed, so dropping it leaves no tracked changes.
INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id)
    SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at, id FROM __temp__events_flip;
DROP TABLE IF EXISTS __temp__events_flip;
