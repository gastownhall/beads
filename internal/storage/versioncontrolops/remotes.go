package versioncontrolops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// ListRemotes returns all configured Dolt remotes (name and URL).
func ListRemotes(ctx context.Context, db DBConn) ([]storage.RemoteInfo, error) {
	rows, err := db.QueryContext(ctx, "SELECT name, url FROM dolt_remotes")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	defer rows.Close()

	var remotes []storage.RemoteInfo
	for rows.Next() {
		var r storage.RemoteInfo
		if err := rows.Scan(&r.Name, &r.URL); err != nil {
			return nil, fmt.Errorf("scan remote: %w", err)
		}
		remotes = append(remotes, r)
	}
	return remotes, rows.Err()
}

// RemoveRemote removes a configured Dolt remote.
func RemoveRemote(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_REMOTE('remove', ?)", name); err != nil {
		return fmt.Errorf("remove remote %s: %w", name, err)
	}
	return nil
}

// Fetch fetches refs from a remote without merging.
//
// On failure, a best-effort GC is run to clean up any orphaned tmp_pack_*
// files that DOLT_FETCH may have left in the git-remote-cache. These files
// accumulate unboundedly across repeated failures and can consume hundreds of
// gigabytes over time.
func Fetch(ctx context.Context, db DBConn, peer string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_FETCH(?)", peer); err != nil {
		// Best-effort: ignore GC errors — the original fetch error is what matters.
		// DoltGC requires a non-transactional connection; if db is a tx it will
		// fail silently here, which is acceptable.
		_ = DoltGC(ctx, db)
		return fmt.Errorf("fetch from %s: %w", peer, err)
	}
	return nil
}

// Push pushes the given branch to the named remote.
func Push(ctx context.Context, db DBConn, remote, branch string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH(?, ?)", remote, branch); err != nil {
		return fmt.Errorf("push to %s/%s: %w", remote, branch, err)
	}
	return nil
}

// ForcePush force-pushes the given branch to the named remote.
func ForcePush(ctx context.Context, db DBConn, remote, branch string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('--force', ?, ?)", remote, branch); err != nil {
		return fmt.Errorf("force push to %s/%s: %w", remote, branch, err)
	}
	return nil
}

// Pull pulls changes from the named remote.
func Pull(ctx context.Context, db DBConn, remote string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_PULL(?)", remote); err != nil {
		return fmt.Errorf("pull from %s: %w", remote, err)
	}
	return nil
}
