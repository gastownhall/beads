DROP INDEX idx_issues_status ON issues;
CREATE INDEX idx_issues_status_updated_at ON issues (status, updated_at);
CREATE INDEX idx_issues_defer_until ON issues (defer_until);
