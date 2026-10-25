package versioncontrolops

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// DoltClone clones a Dolt database from a remote URL.
// conn must be a non-transactional database connection.
// The database parameter specifies the local database name for the clone.
// If user is non-empty, authenticates with that user. Dolt reads the remote
// password from DOLT_REMOTE_PASSWORD.
func DoltClone(ctx context.Context, conn DBConn, remoteURL, database, user string) error {
	return DoltCloneWithRef(ctx, conn, remoteURL, database, user, "")
}

// DoltCloneWithRef is DoltClone for a git-backed remote whose Dolt data lives
// on the git ref ref. A non-empty ref is passed as DOLT_CLONE's --ref option;
// Dolt reads the data from that ref and records it on the clone's origin
// remote, so later push and pull use it. An empty ref is DoltClone.
func DoltCloneWithRef(ctx context.Context, conn DBConn, remoteURL, database, user, ref string) error {
	var parts []string
	var args []any
	if user != "" {
		parts = append(parts, "'--user', ?")
		args = append(args, user)
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		parts = append(parts, "'--ref', ?")
		args = append(args, ref)
	}
	parts = append(parts, "?, ?")
	args = append(args, remoteURL, database)
	query := "CALL DOLT_CLONE(" + strings.Join(parts, ", ") + ")"

	// GH#4272: the initial fetch runs git hooks just like push/pull; disable
	// them for the clone window too (see remotes.go for the full rationale).
	return withRemoteEnvGuards(func() error {
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
