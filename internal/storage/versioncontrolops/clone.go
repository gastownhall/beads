package versioncontrolops

import (
	"context"
	"fmt"
	"net/url"

	"github.com/steveyegge/beads/internal/githooksenv"
)

// DoltClone clones a Dolt database from a remote URL.
// conn must be a non-transactional database connection.
// The database parameter specifies the local database name for the clone.
// If user is non-empty, authenticates with that user. Dolt reads the remote
// password from DOLT_REMOTE_PASSWORD.
func DoltClone(ctx context.Context, conn DBConn, remoteURL, database, user string) error {
	query := "CALL DOLT_CLONE(?, ?)"
	args := []any{remoteURL, database}
	if user != "" {
		query = "CALL DOLT_CLONE('--user', ?, ?, ?)"
		args = []any{user, remoteURL, database}
	}

	// GH#4272: the initial fetch runs git hooks just like push/pull; disable
	// them for the clone window too (see remotes.go for the full rationale).
	return githooksenv.WithDisabled(func() error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("dolt clone %s: %w", sanitizeURL(remoteURL), err)
		}
		return nil
	})
}

// sanitizeURL removes credentials from a URL for safe error reporting.
func sanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
