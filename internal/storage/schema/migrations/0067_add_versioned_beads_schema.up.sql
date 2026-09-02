-- Migration 0067: Phase 1 schema for versioned beads (be-hs42e / gastownhall/beads#6132,
-- this slice: be-hs42e.2 / #6134).
--
-- Purely additive: a per-issue version log (issue_versions), a store-global
-- epoch singleton (store_epoch), and a fast-path revision column on issues
-- (current_revision). Nothing reads or writes these surfaces yet -- CAS
-- enforcement, dual-write and epoch-bump logic land in Phase 2 (#6135) and
-- Phase 3 (#6136). No DML/backfill here: current_revision has a safe default
-- for every existing row, and both new tables start completely empty.
--
-- Plain, unwrapped DDL throughout -- no PREPARE/EXECUTE. `issues` always
-- exists by the time any migration runs, so unlike 0066's guarded ADD COLUMN
-- (needed because bd_events_journal is a dolt-ignored table that a fresh
-- clone may materialize from the ignored series instead of via this file),
-- no idempotent-replay justification applies here, and a PREPARE'd ADD
-- COLUMN/DROP COLUMN silently vanishes under the CLI-bundle path on a
-- pre-2.3 Dolt CLI (see cli_prepared_ddl.go).
CREATE TABLE issue_versions (
    issue_id VARCHAR(255) NOT NULL,
    revision BIGINT NOT NULL,
    epoch INT NOT NULL,
    durable_state JSON,
    change_actor VARCHAR(255),
    change_agent VARCHAR(255),
    change_message TEXT,
    change_at DATETIME NOT NULL,
    removed_at DATETIME,
    removed_reason VARCHAR(255),
    PRIMARY KEY (issue_id, revision)
);

-- Singleton: exactly one row, id=1, once a later phase creates it. Phase 1
-- leaves the table empty -- the CHECK just pins the shape the lazy-init that
-- eventually seeds it (Phase 2/3) must follow.
CREATE TABLE store_epoch (
    id TINYINT(1) NOT NULL DEFAULT 1,
    epoch INT NOT NULL DEFAULT 1,
    bumped_at DATETIME,
    bumped_reason VARCHAR(255),
    PRIMARY KEY (id),
    CONSTRAINT ck_store_epoch_singleton CHECK (id = 1)
);

ALTER TABLE issues ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1;
