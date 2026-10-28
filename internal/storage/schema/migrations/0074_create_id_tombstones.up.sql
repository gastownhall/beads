-- Create the id tombstone tables (retention sweep with permanent id
-- tombstones).
--
-- A retaining retention sweep (issueops.SweepRequest.RetainIDs) writes one row
-- here per row it deletes, in the SAME transaction as the delete. From then on
-- no bd write path commits a row with that id again: a create naming it is
-- refused with issueops.IdempotencyExpiredError (HTTP 410
-- idempotency_expired), an import skips it and reports it, and the id
-- generators treat it as taken. The tables are what lets an orchestrator
-- sweep rows whose ids are idempotency keys without a late retry of the
-- operation re-creating the row it already created.
--
-- TWO TABLES, MATCHING THE ROW'S OWN PLANE, so a tombstone is exactly as
-- durable as the row it replaces:
--   - id_tombstones is VERSIONED, like issues: a durable row's tombstone
--     replicates with the history the row was part of.
--   - wisp_id_tombstones is CLONE-LOCAL, like wisps: the existing wisp_%
--     dolt_ignore pattern (seeded by MigrateUp before this migration runs)
--     covers it, so it lands ignored with no new pattern. Fresh clones never
--     run this migration (the cursor arrives at-latest) and materialize it
--     through ignored migration 0030 instead, whose shape must stay identical
--     to the one below.
--
-- COMPACT AND DETERMINISTIC: exactly the id and the swept row's closed_at,
-- nothing else from the row. The same tombstone written on two clones is the
-- same row, so it merges without conflict. Rows are written with INSERT
-- IGNORE and never updated or deleted by any bd API: a tombstone is permanent.
--
-- The main-series version bump is LOAD-BEARING: it is what makes an older bd
-- binary — one that does not check tombstones and would happily re-create a
-- retired id — refuse to open a database that carries them
-- (schema.checkSchemaSkew).
--
-- Both CREATEs are plain IF NOT EXISTS (0063/0067's shape): ordinary
-- unwrapped DDL, so a re-run, a run against a workspace that already has the
-- tables, and the Dolt CLI bundle path (which no-ops prepared statements on
-- pre-2.3 CLIs) all land the same schema with no override.
CREATE TABLE IF NOT EXISTS id_tombstones (
    id VARCHAR(255) NOT NULL,
    closed_at DATETIME NOT NULL,
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS wisp_id_tombstones (
    id VARCHAR(255) NOT NULL,
    closed_at DATETIME NOT NULL,
    PRIMARY KEY (id)
);
