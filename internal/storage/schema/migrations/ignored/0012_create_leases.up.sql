-- Materialize the ephemeral leases table (bd-lrgn1) on clones that never ran
-- main migration 0055. The leases table is dolt_ignored, so it lives only in
-- the working set and is never part of committed history: a fresh clone
-- arrives with the schema_migrations cursor at-latest (0055 already recorded)
-- but WITHOUT the table, exactly like the wisp/local-state tables from
-- ignored/0001. Same __temp__ + conditional RENAME pattern: create only when
-- absent, never touch an existing table.
--
-- One row per live claim granted through this node; see
-- issueops.UpsertLeaseInTx for the lease-row invariant.
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
