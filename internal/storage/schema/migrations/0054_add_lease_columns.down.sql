DROP INDEX idx_issues_lease ON issues;
ALTER TABLE issues DROP COLUMN lease_expires_at;
ALTER TABLE issues DROP COLUMN heartbeat_at;
ALTER TABLE issues DROP COLUMN row_lock;
ALTER TABLE wisps DROP COLUMN lease_expires_at;
ALTER TABLE wisps DROP COLUMN heartbeat_at;
ALTER TABLE wisps DROP COLUMN row_lock;
