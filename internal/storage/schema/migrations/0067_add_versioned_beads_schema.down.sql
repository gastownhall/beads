-- Reverse of 0067. Plain, unwrapped DDL -- see the up migration for why no
-- PREPARE/EXECUTE wrapper applies here (DROP COLUMN is in the same
-- CLI-bundle vanishing bucket as ADD COLUMN; see cli_prepared_ddl.go).
-- Every surface this reverses is still unread and unwritten in Phase 1, so
-- this is a plain schema rollback, not a data-loss concern.
ALTER TABLE issues DROP COLUMN current_revision;

DROP TABLE IF EXISTS store_epoch;

DROP TABLE IF EXISTS issue_versions;
