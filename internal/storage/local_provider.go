// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/types"
)

// LocalProvider implements types.IssueProvider using a read-only SQLite connection.
// This is used for cross-repo orphan detection when --db flag points to an external database.
type LocalProvider struct {
	db     *sql.DB
	prefix string
}

// NewLocalProvider creates a provider backed by a SQLite database at the given path.
func NewLocalProvider(dbPath string) (*LocalProvider, error) {
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return nil, err
	}

	// Get issue prefix from config
	var prefix string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&prefix)
	if err != nil || prefix == "" {
		prefix = "bd" // default
	}

	return &LocalProvider{db: db, prefix: prefix}, nil
}

// GetOpenIssues returns issues that are open or in_progress.
func (p *LocalProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	// Get all open/in_progress issues with their titles (title is optional for compatibility)
	rows, err := p.db.QueryContext(ctx, "SELECT id, title, status FROM issues WHERE status IN ('open', 'in_progress')")
	// If the query fails (e.g., no title column), fall back to simpler query
	if err != nil {
		rows, err = p.db.QueryContext(ctx, "SELECT id, '', status FROM issues WHERE status IN ('open', 'in_progress')")
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	var issues []*types.Issue
	for rows.Next() {
		var id, title, status string
		if err := rows.Scan(&id, &title, &status); err == nil {
			issues = append(issues, &types.Issue{
				ID:     id,
				Title:  title,
				Status: types.Status(status),
			})
		}
	}

	return issues, nil
}

// GetIssuePrefix returns the configured issue prefix.
func (p *LocalProvider) GetIssuePrefix() string {
	return p.prefix
}

// Close closes the underlying database connection.
func (p *LocalProvider) Close() error {
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}

// Ensure LocalProvider implements types.IssueProvider
var _ types.IssueProvider = (*LocalProvider)(nil)

// openDBReadOnly opens a SQLite database in read-only mode.
func openDBReadOnly(dbPath string) (*sql.DB, error) {
	connStr := SQLiteConnString(dbPath, true)
	return sql.Open("sqlite3", connStr)
}
