package dolt

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// FederatedStorage implementation for DoltStore
// These methods enable peer-to-peer synchronization between Gas Towns.

// PushTo pushes commits to a specific peer remote.
// If credentials are stored for this peer, they are used automatically.
// For SSH remotes, uses CLI `dolt push` to avoid MySQL connection timeouts.
func (s *DoltStore) PushTo(ctx context.Context, peer string) error {
	if s.isPeerSSHRemote(ctx, peer) {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIPushToPeer(ctx, peer, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		return withEnvCredentials(creds, func() error {
			_, err := s.execContext(ctx, "CALL DOLT_PUSH(?, ?)", peer, s.branch)
			if err != nil {
				return fmt.Errorf("failed to push to peer %s: %w", peer, err)
			}
			return nil
		})
	})
}

// PullFrom pulls changes from a specific peer remote.
// If credentials are stored for this peer, they are used automatically.
// For SSH remotes, uses CLI `dolt pull` to avoid MySQL connection timeouts.
// Returns any merge conflicts if present.
func (s *DoltStore) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	var conflicts []storage.Conflict
	if s.isPeerSSHRemote(ctx, peer) {
		err := s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			if pullErr := s.doltCLIPullFromPeer(ctx, peer, creds); pullErr != nil {
				c, conflictErr := s.GetConflicts(ctx)
				if conflictErr == nil && len(c) > 0 {
					conflicts = c
					return nil
				}
				return fmt.Errorf("failed to pull from peer %s: %w", peer, pullErr)
			}
			return nil
		})
		return conflicts, err
	}
	err := s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		return withEnvCredentials(creds, func() error {
			_, pullErr := s.execContext(ctx, "CALL DOLT_PULL(?)", peer)
			if pullErr != nil {
				c, conflictErr := s.GetConflicts(ctx)
				if conflictErr == nil && len(c) > 0 {
					conflicts = c
					return nil
				}
				return fmt.Errorf("failed to pull from peer %s: %w", peer, pullErr)
			}
			return nil
		})
	})
	return conflicts, err
}

// Fetch fetches refs from a peer without merging.
// If credentials are stored for this peer, they are used automatically.
// For SSH remotes, uses CLI `dolt fetch` to avoid MySQL connection timeouts.
func (s *DoltStore) Fetch(ctx context.Context, peer string) error {
	if s.isPeerSSHRemote(ctx, peer) {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIFetchFromPeer(ctx, peer, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		return withEnvCredentials(creds, func() error {
			_, err := s.execContext(ctx, "CALL DOLT_FETCH(?)", peer)
			if err != nil {
				return fmt.Errorf("failed to fetch from peer %s: %w", peer, err)
			}
			return nil
		})
	})
}

// ListRemotes returns configured remote names and URLs.
func (s *DoltStore) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	rows, err := s.queryContext(ctx, "SELECT name, url FROM dolt_remotes")
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w", err)
	}
	defer rows.Close()

	var remotes []storage.RemoteInfo
	for rows.Next() {
		var r storage.RemoteInfo
		if err := rows.Scan(&r.Name, &r.URL); err != nil {
			return nil, fmt.Errorf("failed to scan remote: %w", err)
		}
		remotes = append(remotes, r)
	}
	return remotes, rows.Err()
}

// RemoveRemote removes a configured remote.
func (s *DoltStore) RemoveRemote(ctx context.Context, name string) error {
	_, err := s.execContext(ctx, "CALL DOLT_REMOTE('remove', ?)", name)
	if err != nil {
		return fmt.Errorf("failed to remove remote %s: %w", name, err)
	}
	return nil
}

// SyncStatus returns the sync status with a peer.
func (s *DoltStore) SyncStatus(ctx context.Context, peer string) (*storage.SyncStatus, error) {
	status := &storage.SyncStatus{
		Peer: peer,
	}

	// Get ahead/behind counts by comparing refs
	// This requires the peer to have been fetched first
	query := `
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF CONCAT(?, '/', ?))) as ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF CONCAT(?, '/', ?) WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) as behind
	`

	err := s.db.QueryRowContext(ctx, query, peer, s.branch, peer, s.branch).
		Scan(&status.LocalAhead, &status.LocalBehind)
	if err != nil {
		// If we can't get the status, return a partial result
		// This happens when the remote branch doesn't exist locally yet
		status.LocalAhead = -1
		status.LocalBehind = -1
	}

	// Check for conflicts
	conflicts, err := s.GetConflicts(ctx)
	if err == nil && len(conflicts) > 0 {
		status.HasConflicts = true
	}

	// Get last sync time from metadata
	status.LastSync = s.getLastSyncTime(ctx, peer)

	return status, nil
}

// getLastSyncTime retrieves the last sync time for a peer from metadata.
func (s *DoltStore) getLastSyncTime(ctx context.Context, peer string) time.Time {
	key := "last_sync_" + peer
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// setLastSyncTime records the last sync time for a peer in metadata.
func (s *DoltStore) setLastSyncTime(ctx context.Context, peer string) error {
	key := "last_sync_" + peer
	value := time.Now().Format(time.RFC3339)
	_, err := s.execContext(ctx,
		"REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value)
	return wrapExecError("set last sync time", err)
}

// Sync performs a full bidirectional sync with a peer:
// 1. Fetch from peer
// 2. Merge peer's changes (handling conflicts per strategy)
// 3. Push local changes to peer
//
// Returns the sync result including any conflicts encountered.
func (s *DoltStore) Sync(ctx context.Context, peer string, strategy string) (*SyncResult, error) {
	result := &SyncResult{
		Peer:      peer,
		StartTime: time.Now(),
	}

	// Step 1: Fetch from peer
	if err := s.Fetch(ctx, peer); err != nil {
		result.Error = fmt.Errorf("fetch failed: %w", err)
		return result, result.Error
	}
	result.Fetched = true

	// Step 2: Get status before merge
	beforeCommit, _ := s.GetCurrentCommit(ctx) // Best effort: empty commit hash means diff won't be logged

	// Step 3: Merge peer's branch
	remoteBranch := fmt.Sprintf("%s/%s", peer, s.branch)
	conflicts, err := s.Merge(ctx, remoteBranch)
	if err != nil {
		result.Error = fmt.Errorf("merge failed: %w", err)
		return result, result.Error
	}

	// Step 4: Handle conflicts if any
	if len(conflicts) > 0 {
		result.Conflicts = conflicts

		if strategy == "" {
			// No strategy specified, leave conflicts for manual resolution
			result.Error = fmt.Errorf("merge conflicts require resolution (use --strategy ours|theirs)")
			return result, result.Error
		}

		// Auto-resolve using strategy
		for _, c := range conflicts {
			if err := s.ResolveConflicts(ctx, c.Field, strategy); err != nil {
				result.Error = fmt.Errorf("conflict resolution failed for %s: %w", c.Field, err)
				return result, result.Error
			}
		}
		result.ConflictsResolved = true

		// Commit the resolution
		if err := s.Commit(ctx, fmt.Sprintf("Resolve conflicts from %s using %s strategy", peer, strategy)); err != nil {
			result.Error = fmt.Errorf("failed to commit conflict resolution: %w", err)
			return result, result.Error
		}
	}
	result.Merged = true

	// Count pulled commits
	afterCommit, _ := s.GetCurrentCommit(ctx) // Best effort: empty commit hash means diff won't be logged
	if beforeCommit != afterCommit {
		result.PulledCommits = 1 // Simplified - could count actual commits
	}

	// Step 5: Push our changes to peer
	if err := s.PushTo(ctx, peer); err != nil {
		// Push failure is not fatal - peer may not accept pushes
		result.PushError = err
	} else {
		result.Pushed = true
	}

	// Record last sync time
	_ = s.setLastSyncTime(ctx, peer) // Best effort: sync timestamp is advisory for scheduling

	result.EndTime = time.Now()
	return result, nil
}

// isPeerSSHRemote checks whether a specific peer remote URL uses SSH transport.
// SSH remotes require CLI-based push/pull/fetch because CALL DOLT_PUSH/PULL/FETCH
// through the SQL server times out — the MySQL connection drops before the SSH
// transfer completes.
func (s *DoltStore) isPeerSSHRemote(ctx context.Context, peer string) bool {
	remotes, err := s.ListRemotes(ctx)
	if err == nil {
		for _, r := range remotes {
			if r.Name == peer {
				return doltutil.IsSSHURL(r.URL)
			}
		}
	}
	if s.dbPath != "" {
		if url := doltutil.FindCLIRemote(s.dbPath, peer); url != "" {
			return doltutil.IsSSHURL(url)
		}
	}
	return false
}

// doltCLIPushToPeer shells out to `dolt push` for a specific peer remote.
// Used for SSH remotes where CALL DOLT_PUSH times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIPushToPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "push", peer, s.branch) // #nosec G204 -- fixed command with validated peer/branch
	cmd.Dir = s.dbPath
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push to peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIPullFromPeer shells out to `dolt pull` for a specific peer remote.
// Used for SSH remotes where CALL DOLT_PULL times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIPullFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "pull", peer, s.branch) // #nosec G204 -- fixed command with validated peer/branch
	cmd.Dir = s.dbPath
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull from peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIFetchFromPeer shells out to `dolt fetch` for a specific peer remote.
// Used for SSH remotes where CALL DOLT_FETCH times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIFetchFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "fetch", peer) // #nosec G204 -- fixed command with validated peer
	cmd.Dir = s.dbPath
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fetch from peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SyncResult contains the outcome of a Sync operation.
type SyncResult struct {
	Peer              string
	StartTime         time.Time
	EndTime           time.Time
	Fetched           bool
	Merged            bool
	Pushed            bool
	PulledCommits     int
	PushedCommits     int
	Conflicts         []storage.Conflict
	ConflictsResolved bool
	Error             error
	PushError         error // Non-fatal push error
}
