-- IDEMPOTENCY (2026-08-31): INSERT IGNORE, not INSERT.
--
-- This migration is not atomic: it interleaves four INSERTs with four
-- DOLT_COMMIT calls, so a connection drop partway through leaves some rows
-- committed while the migration itself is never recorded as applied. The
-- next open re-runs it from the top and the first INSERT dies with
--   Error 1062: duplicate primary key given: [wisps]
-- which aborts schema init and BRICKS the database. That is the long-standing
-- `gt rig add` failure: reproduced 2026-08-31 creating the igr rig, alongside
-- `busy buffer` and `release migration lock: bad connection` — i.e. exactly
-- the partial-application path, and it shows up under load.
--
-- INSERT IGNORE makes the re-run converge instead of abort. Nothing is
-- destroyed: 0041 clears this table wholesale as its first statement, so
-- these four rows are transient registrations, not durable state.
--
-- --allow-empty is REQUIRED alongside it, and testing on the real bricked
-- igr database is what proved that: once INSERT IGNORE turns the insert
-- into a no-op, the following DOLT_COMMIT has nothing staged and fails with
-- `Error 1105: nothing to commit`. The migration still aborted, just one
-- line further down. Both halves are needed for the re-run to converge.
INSERT IGNORE INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('wisps', 'main', 'immediate');
CALL DOLT_COMMIT('--allow-empty', '-Am', 'create nonlocal table wisps');
INSERT IGNORE INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('wisp_*', 'main', 'immediate');
CALL DOLT_COMMIT('--allow-empty', '-Am', 'create nonlocal table wisp_*');
INSERT IGNORE INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('repo_mtimes', 'main', 'immediate');
CALL DOLT_COMMIT('--allow-empty', '-Am', 'create nonlocal table repo_mtimes');
INSERT IGNORE INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('local_metadata', 'main', 'immediate');
CALL DOLT_COMMIT('--allow-empty', '-Am', 'create nonlocal table local_metadata');

