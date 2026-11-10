-- Materialize the events audit table (bd-red8u) on clones that never ran
-- main migration 0062. events is dolt_ignored since 0062, so it lives only in
-- the working set and is never part of committed history: a fresh clone
-- arrives with the schema_migrations cursor at-latest (0062 already recorded)
-- but WITHOUT the table, exactly like leases (ignored/0012) and the
-- wisp/local-state tables (ignored/0001).
--
-- NOT the usual __temp__ + RENAME dance: events carries the named constraint
-- fk_events_issue, and constraint names are database-unique -- creating a
-- temp twin while events exists errors with "duplicate foreign key constraint
-- name" (verified on dolt 2.2.2). A PREPAREd conditional CREATE gives the
-- same create-only-when-absent contract. CREATE TABLE IF NOT EXISTS alone
-- would also work (a same-name FK short-circuits on the existing table), but
-- the explicit guard keeps the never-touch-an-existing-table intent readable.
--
-- Shape must stay identical to 0062's recreate (the post-0037 converged
-- column order, id last) -- events/wisp_events twin parity is pinned by
-- internal/storage/dolt/schema_parity_test.go.
SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'events');
SET @sql = IF(@exists = 0, 'CREATE TABLE events (
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
)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
