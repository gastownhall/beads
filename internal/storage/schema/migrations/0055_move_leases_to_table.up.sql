-- Move claim leases out of the versioned issues table into an ephemeral
-- leases table (bd-lrgn1).
--
-- lease_expires_at / heartbeat_at as issues columns meant every claim and
-- every heartbeat was an issues-row UPDATE -> a Dolt commit. At fleet scale
-- that coordination chatter was the dominant source of unbounded reachable
-- history and of the constant write traffic that starves large catch-up
-- merges. The new leases table is registered dolt_ignored (seeded by
-- MigrateUp's doltIgnorePatterns before this migration runs), so claims mint
-- exactly one commit (the status/assignee transition — history-worthy) and
-- heartbeats mint none.
--
-- Leases are deliberately node-local: dolt_ignored tables do not replicate,
-- which matches what leases already were in reality — only enforceable on the
-- replica that granted them. Cross-machine claim VISIBILITY still rides
-- status/assignee on issues. row_lock STAYS on issues/wisps: it is the
-- general write-serialization cell for status/ownership races, not a lease
-- column (see issueops/lease.go freshRowLock).
--
-- Fresh clones never run this migration (the schema_migrations cursor arrives
-- at-latest); they materialize the leases table via ignored migration 0012.
-- Everything here is guarded so a re-run, or a run against a workspace in any
-- intermediate state, is a no-op (see 0052/0046 precedent).

-- 1. Create the leases table if this workspace does not have one yet. The
-- __temp__ + conditional RENAME dance (see ignored/0001) keeps the CREATE
-- idempotent; __temp__leases never survives this migration step.
DROP TABLE IF EXISTS __temp__leases;
CREATE TABLE __temp__leases (
    issue_id VARCHAR(255) PRIMARY KEY,
    holder VARCHAR(255) NOT NULL,
    granted_at DATETIME NOT NULL,
    lease_expires_at DATETIME NOT NULL,
    heartbeat_at DATETIME NOT NULL,
    INDEX idx_leases_expires (lease_expires_at)
);
SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'leases');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__leases TO leases', 'DROP TABLE __temp__leases');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 2. Move live lease values out of the issues columns. Only rows that are an
-- actual live claim carry a lease; granted_at/heartbeat_at fall back to
-- updated_at when heartbeat_at was never stamped. INSERT IGNORE: an existing
-- lease row (e.g. from a partially-applied earlier run) wins.
SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'lease_expires_at'
);
SET @sql = IF(@has_col > 0,
    'INSERT IGNORE INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at)
     SELECT id, assignee, COALESCE(heartbeat_at, updated_at), lease_expires_at, COALESCE(heartbeat_at, updated_at)
     FROM issues
     WHERE lease_expires_at IS NOT NULL
       AND status = ''in_progress''
       AND COALESCE(assignee, '''') != ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 3. Drop the lease index and columns from issues. One combined ALTER (each
-- ALTER is a table rewrite; fresh chains run 0054 add + 0055 drop, so keep
-- this cheap). Clauses are guarded individually so a partially-applied
-- earlier run re-runs as a no-op.
SET @has_index = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_lease'
);
SET @has_lease_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'lease_expires_at'
);
SET @has_hb_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'heartbeat_at'
);
SET @clauses = CONCAT_WS(', ',
    IF(@has_index > 0, 'DROP INDEX idx_issues_lease', NULL),
    IF(@has_lease_col > 0, 'DROP COLUMN lease_expires_at', NULL),
    IF(@has_hb_col > 0, 'DROP COLUMN heartbeat_at', NULL));
SET @sql = IF(@clauses = '', 'SELECT 1', CONCAT('ALTER TABLE issues ', @clauses));
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 4. Drop the (never-reclaimed, now entirely unread) lease columns from wisps.
-- Wisps are never leased work; their claims get no lease row. row_lock stays:
-- the shared claim/close/update SQL still rewrites it on wisps rows. Guarded
-- on the wisps table existing AND carrying the columns — fresh clones build
-- wisps from ignored/0001, which never had them.
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @has_lease_col = IF(@has_wisps > 0,
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'lease_expires_at'),
    0);
SET @has_hb_col = IF(@has_wisps > 0,
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'heartbeat_at'),
    0);
SET @clauses = CONCAT_WS(', ',
    IF(@has_lease_col > 0, 'DROP COLUMN lease_expires_at', NULL),
    IF(@has_hb_col > 0, 'DROP COLUMN heartbeat_at', NULL));
SET @sql = IF(@clauses = '', 'SELECT 1', CONCAT('ALTER TABLE wisps ', @clauses));
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
