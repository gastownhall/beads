-- Materialize the clone-local wisp_id_tombstones table on clones that never
-- ran main migration 0074. The table is dolt_ignored (wisp_% pattern),
-- so it lives only in the working set and is never part of committed history:
-- a fresh clone arrives with the schema_migrations cursor at-latest (0074
-- already recorded) but WITHOUT the table, exactly like leases (ignored/0012)
-- and the events journal (ignored/0022). CREATE TABLE IF NOT EXISTS gives the
-- create-only-when-absent contract: it never touches an existing table, and
-- the table carries no named constraint that would collide (ignored/0019). The
-- shape must stay identical to 0074's.
--
-- A table materialized here on a fresh clone starts EMPTY: the tombstones of
-- the wisps-plane rows the source clone swept did not travel, exactly as the
-- swept rows themselves never did. A consumer relying on them must treat a
-- fresh clone (or a lost clone-local plane) as a new logical scope.
CREATE TABLE IF NOT EXISTS wisp_id_tombstones (
    id VARCHAR(255) NOT NULL,
    closed_at DATETIME NOT NULL,
    PRIMARY KEY (id)
);
