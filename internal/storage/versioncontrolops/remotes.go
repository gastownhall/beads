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
// If user is non-empty, authenticates with that user — DOLT_REMOTE_PASSWORD
// must be set in the in-process Dolt server's environment.
//
// A failed DOLT_FETCH can leave orphaned tmp_pack_* files in the
// git-remote-cache; the embedded store's connection teardown sweeps those
// (cleanGitRemoteCacheGarbage). Do NOT run DOLT_GC here: dolt_gc invalidates
// every other open session on the same engine, so a failed fetch would break
// concurrent in-flight connections (bd-6dnrw.10).
func Fetch(ctx context.Context, db DBConn, peer, user string) error {
	var err error
	if user != "" {
		_, err = db.ExecContext(ctx, "CALL DOLT_FETCH('--user', ?, ?)", user, peer)
	} else {
		_, err = db.ExecContext(ctx, "CALL DOLT_FETCH(?)", peer)
	}
	if err != nil {
		return fmt.Errorf("fetch from %s: %w", peer, err)
	}
	return nil
}

// Push pushes the given branch to the named remote.
// If user is non-empty, authenticates with that user — DOLT_REMOTE_PASSWORD
// must be set in the in-process Dolt server's environment. Required when
// pushing to a remotesapi server that enforces CLONE_ADMIN authentication.
func Push(ctx context.Context, db DBConn, remote, branch, user string) error {
	if user != "" {
		if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", user, remote, branch); err != nil {
			return fmt.Errorf("push to %s/%s: %w", remote, branch, err)
		}
		return nil
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH(?, ?)", remote, branch); err != nil {
		return fmt.Errorf("push to %s/%s: %w", remote, branch, err)
	}
	return nil
}

// ForcePush force-pushes the given branch to the named remote.
// See Push for the user/auth contract.
func ForcePush(ctx context.Context, db DBConn, remote, branch, user string) error {
	if user != "" {
		if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", user, remote, branch); err != nil {
			return fmt.Errorf("force push to %s/%s: %w", remote, branch, err)
		}
		return nil
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('--force', ?, ?)", remote, branch); err != nil {
		return fmt.Errorf("force push to %s/%s: %w", remote, branch, err)
	}
	return nil
}

// Pull pulls changes from the named remote by fetching the branch and merging
// the remote tracking ref. This is equivalent to DOLT_PULL(remote, branch) but
// avoids a nil-pointer panic in embedded Dolt when upstream branch tracking is
// not configured in repo_state.json (GH#3144). The merge runs through
// MergeAndSettle (bd-6dnrw.40), so safe conflict classes are auto-resolved and
// FK cascade violations repaired, matching server-mode pulls.
//
// db must be a single session (see MergeAndSettle). See Push for the
// user/auth contract; only the fetch step authenticates, since the merge step
// is local.
func Pull(ctx context.Context, db DBConn, remote, branch, user string) error {
	if user != "" {
		if _, err := db.ExecContext(ctx, "CALL DOLT_FETCH('--user', ?, ?, ?)", user, remote, branch); err != nil {
			return fmt.Errorf("fetch from %s/%s: %w", remote, branch, err)
		}
	} else {
		if _, err := db.ExecContext(ctx, "CALL DOLT_FETCH(?, ?)", remote, branch); err != nil {
			return fmt.Errorf("fetch from %s/%s: %w", remote, branch, err)
		}
	}
	trackingRef := remote + "/" + branch
	if err := MergeAndSettle(ctx, db, trackingRef); err != nil {
		return fmt.Errorf("merge %s: %w", trackingRef, err)
	}
	return nil
}
