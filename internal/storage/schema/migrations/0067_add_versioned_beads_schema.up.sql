-- Migration 0067: Phase 1 schema for versioned beads (be-hs42e / gastownhall/beads#6132,
-- this slice: be-hs42e.2 / #6134).
--
-- Purely additive: a per-issue version log (issue_versions), a store-global
-- epoch singleton (store_epoch), and a fast-path revision column on BOTH
-- record planes (issues.current_revision, wisps.current_revision). Nothing
-- reads or writes these surfaces yet -- CAS enforcement, dual-write and
-- epoch-bump logic land in Phase 2 (#6135) and Phase 3 (#6136). No
-- DML/backfill here: current_revision has a safe default for every existing
-- row, and both new tables start completely empty.
--
-- SEMANTIC ASYMMETRY, DELIBERATE: only the issues column is ever read or
-- written. Versioning is an issues-plane contract -- wisps are the ephemeral
-- plane and no phase of this epic (2 or 3) will CAS on, bump, or log a wisp
-- revision. The wisps column is a SHAPE obligation, not a semantic one:
-- issues and wisps share the row shape and the shared scans build one column
-- list for both tables (TestSchemaParityIssuesVsWisps in
-- internal/storage/dolt enforces strict column-name parity with no exemption
-- list), so a column on one plane only is a live drift hazard for
-- column-list-shaped code paths regardless of whether anything reads it.
-- wisps.row_lock (0054 + ignored/0013) is the precedent: carried on both
-- planes, meaningful on one.
--
-- The CREATE TABLEs are guarded with IF NOT EXISTS -- ordinary, unwrapped
-- MySQL-flavored DDL -- so a designated-migrator replay (schema_migrations
-- bookkeeping row missing, e.g. BD_ALLOW_REMOTE_MIGRATE=1 against a clone
-- that already has these tables) doesn't die on Error 1105.
--
-- The ADD COLUMNs have no equivalent IF NOT EXISTS: MySQL, which Dolt
-- follows, has never had that construct for ADD COLUMN (it's a MariaDB-only
-- extension; measured on dolt 2.2.3 -- `syntax error at position 33 near
-- 'IF'`). Neither does Dolt accept `DROP COLUMN IF EXISTS` or a top-level
-- IF. The only conditional DDL forms Dolt does accept are the PREPARE guard
-- below and a stored procedure, and a procedure body's inner semicolons
-- need a client-side DELIMITER that the CLI batch path requires and the Go
-- driver rejects -- the frozen file cannot satisfy both. So these are the
-- guarded PREPARE shape 0060 and 0066 already use for exactly this: their
-- INFORMATION_SCHEMA probe makes a raw-SQL replay of this whole file a clean
-- no-op on an already-migrated store, which is what
-- pr4107_corruption_test.go's replay harness (raw .up.sql from 0046 onward
-- onto a live store) requires of every migration >= 0046.
--
-- A PREPARE'd ADD COLUMN silently vanishes under the CLI-bundle path on a
-- pre-2.3 Dolt CLI (dolthub/dolt#11345; see cli_prepared_ddl.go), so the
-- fresh bundle gets a direct-DDL override -- cliMigration0067AddVersionedBeadsSchema
-- in cli_migrations.go, the same escape hatch 0060/0065/0066 use, guarded by
-- TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified.
--
-- wisps is dolt-ignored (clone-local), so this file's wisps ALTER never runs
-- on a fresh clone: it ships with the ignored-series twin ignored/0026, the
-- mechanism check D of scripts/check-migration-hygiene.sh enforces and
-- ignored/0013 (0054's row_lock) and ignored/0020 (0060's storage_class)
-- record.
CREATE TABLE IF NOT EXISTS issue_versions (
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
CREATE TABLE IF NOT EXISTS store_epoch (
    id TINYINT(1) NOT NULL DEFAULT 1,
    epoch INT NOT NULL DEFAULT 1,
    bumped_at DATETIME,
    bumped_reason VARCHAR(255),
    PRIMARY KEY (id),
    CONSTRAINT ck_store_epoch_singleton CHECK (id = 1)
);

-- issues.current_revision -- the fast-path CAS column, the only one any
-- later phase reads or writes.
SET @issues_cr_needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'current_revision'
);
SET @sql = IF(@issues_cr_needs_add = 1,
    'ALTER TABLE issues ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.current_revision -- shape parity only (see the asymmetry note above).
-- Guarded on the wisps table existing as well as the column: older
-- workspaces created issues-only, and a clone that never synced the
-- clone-local wisp tables must no-op rather than abort the pass (0060
-- precedent).
SET @wisps_cr_needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'current_revision') = 0,
    1, 0
);
SET @sql = IF(@wisps_cr_needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
