DROP INDEX idx_issues_defer_until ON issues;
DROP INDEX idx_issues_status_updated_at ON issues;
CREATE INDEX idx_issues_status ON issues (status);
