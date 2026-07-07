DROP INDEX idx_issues_is_blocked ON issues;
ALTER TABLE issues DROP COLUMN is_blocked;
