package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/beads/internal/storage"
	"github.com/gastownhall/beads/internal/storage/doltutil"
)

// FederatedStorage implementation for DoltStore
// These methods enable peer-to-peer synchronization between Gas Towns.

// PushTo pushes commits to a specific peer remote.
// If credentials are stored for this peer, they are used automatically.
// For git-protocol remotes, uses CLI `dolt push` to avoid MySQL connection timeouts.
func (s *DoltStore) PushTo(ctx context.Context, peer string) error {
	if s.isPeerGitProtocolRemote(ctx, peer) {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIPushToPeer(ctx, peer, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		// Credential CLI routing: when peer has credentials and server is external,
		// route through CLI subprocess (same rationale as store.go Push).
		if s.shouldUseCLIForPeerCredentials(ctx, peer, creds) {
			return s.doltCLIPushToPeer(ctx, peer, creds)
		}
		return withEnvCredentials(creds, func() error {
			if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", peer, s.branch); err != nil {
				return fmt.Errorf("failed to push to peer %s: %w", peer, err)
			}
			return nil
		})
	})
}

// PullFrom pulls changes from a specific peer remote.
// If credentials are stored for this peer, they are used automatically.
// For git-protocol remotes, uses CLI `dolt pull` to avoid MySQL connection timeouts.
// Returns any merge conflicts if present.
func (s *DoltStore) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	// GH#2474: Auto-commit pending changes before pull to prevent
	// "cannot merge with uncommitted changes" errors.
	if !s.readOnly {
		if err := s.Commit(ctx, "auto-commit before pull"); err != nil {
			if !isDoltNothingToCommit(err) {
				return nil, fmt.Errorf("failed to commit pending changes before pull: %w", err)
			}
		}
	}

	var conflicts []storage.Conflict
	if s.isPeerGitProtocolRemote(ctx, peer) {
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
		// Credential CLI routing: mirrors git-protocol peer pull path.
		if s.shouldUseCLIForPeerCredentials(ctx, peer, creds) {
			if pullErr := s.doltCLIPullFromPeer(ctx, peer, creds); pullErr != nil {
				c, conflictErr := s.GetConflicts(ctx)
				if conflictErr == nil && len(c) > 0 {
					conflicts = c
					return nil
				}
				return fmt.Errorf("failed to pull from peer %s: %w", peer, pullErr)
			}
			return nil
		}
		return withEnvCredentials(creds, func() error {
			if pullErr := s.execWithLongTimeout(ctx, "CALL DOLT_PULL(?)", peer); pullErr != nil {
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
// For git-protocol remotes, uses CLI `dolt fetch` to avoid MySQL connection timeouts.
func (s *DoltStore) Fetch(ctx context.Context, peer string) error {
	if s.isPeerGitProtocolRemote(ctx, peer) {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIFetchFromPeer(ctx, peer, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		// Credential CLI routing: route fetch through CLI subprocess.
		if s.shouldUseCLIForPeerCredentials(ctx, peer, creds) {
			return s.doltCLIFetchFromPeer(ctx, peer, creds)
		}
		return withEnvCredentials(creds, func() error {
			if err := s.execWithLongTimeout(ctx, "CALL DOLT_FETCH(?)", peer); err != nil {
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

// syncCLIRemotesToSQL re-registers CLI-level remotes into the SQL server.
// After a server restart, dolt_remotes (in-memory) is empty while CLI remotes
// (persisted in .dolt/config) survive. This is best-effort: errors are silently
// ignored because a missing remote will surface a clear error at push/pull time.
//
// GH#2118: Also checks the server root directory (dbPath) for remotes that were
// added there instead of the database subdirectory (CLIDir). Users commonly run
// `dolt remote add` in .beads/dolt/ (server root) rather than .beads/dolt/<db>/
// (database dir). When found, these remotes are propagated to the database CLI
// directory so that CLI push/pull can find them.
//
// See GH#2315.
func (s *DoltStore) syncCLIRemotesToSQL(ctx context.Context) {
	dir := s.CLIDir()
	if dir == "" {
		return
	}
	cliRemotes, err := doltutil.ListCLIRemotes(dir)
	if err != nil {
		cliRemotes = nil
	}

	// GH#2118: If the database directory has no remotes (or doesn't exist),
	// check the server root directory. Users often run `dolt remote add` in
	// .beads/dolt/ (server root) instead of .beads/dolt/<database>/.
	if len(cliRemotes) == 0 {
		cliRemotes = s.migrateServerRootRemotes(dir)
	}

	if len(cliRemotes) == 0 {
		return
	}
	sqlRemotes, err := s.ListRemotes(ctx)
	if err != nil {
		return
	}
	sqlMap := doltutil.ToRemoteNameMap(sqlRemotes)
	for _, r := range cliRemotes {
		if _, exists := sqlMap[r.Name]; !exists {
			_ = s.AddRemote(ctx, r.Name, r.URL)
		}
	}
}

// migrateServerRootRemotes checks the dolt server root directory (dbPath) for
// remotes that should be propagated to the database CLI directory (CLIDir).
// This handles GH#2118: users run `dolt remote add` in .beads/dolt/ (server root)
// instead of .beads/dolt/<database>/ (where CLI push/pull actually runs).
//
// Returns the combined CLI remotes after migration, or nil if nothing to migrate.
func (s *DoltStore) migrateServerRootRemotes(cliDir string) []storage.RemoteInfo {
	rootDir := s.dbPath
	if rootDir == "" || rootDir == cliDir {
		return nil
	}
	// Server root must have .dolt/ (from dolt init)
	if _, err := os.Stat(filepath.Join(rootDir, ".dolt")); err != nil {
		return nil
	}
	rootRemotes, err := doltutil.ListCLIRemotes(rootDir)
	if err != nil || len(rootRemotes) == 0 {
		return nil
	}
	// Database dir must have .dolt/ to accept CLI remotes
	if _, err := os.Stat(filepath.Join(cliDir, ".dolt")); err != nil {
		return nil
	}
	// Propagate server-root remotes to the database directory
	for _, r := range rootRemotes {
		if existing := doltutil.FindCLIRemote(cliDir, r.Name); existing == "" {
			_ = doltutil.AddCLIRemote(cliDir, r.Name, r.URL)
		}
	}
	combined, _ := doltutil.ListCLIRemotes(cliDir)
	return combined
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

// isPeerGitProtocolRemote checks whether a specific peer remote URL uses the git wire
// protocol and is available for CLI-based push/pull/fetch. Git-protocol remotes (SSH,
// git+https://, git://) are routed to CLI operations because the SQL server may lack
// the git credentials or SSH keys needed for network I/O to external git hosts.
// Returns false when the remote exists only on an externally-managed server's filesystem.
func (s *DoltStore) isPeerGitProtocolRemote(ctx context.Context, peer string) bool {
	remotes, err := s.ListRemotes(ctx)
	if err == nil {
		for _, r := range remotes {
			if r.Name == peer {
				if !doltutil.IsGitProtocolURL(r.URL) {
					return false
				}
				return s.CLIDir() != "" && doltutil.FindCLIRemote(s.CLIDir(), peer) != ""
			}
		}
	}
	if s.CLIDir() != "" {
		if url := doltutil.FindCLIRemote(s.CLIDir(), peer); url != "" {
			return doltutil.IsGitProtocolURL(url)
		}
	}
	return false
}

// doltCLIPushToPeer shells out to `dolt push` for a specific peer remote.
// Used for git-protocol remotes where CALL DOLT_PUSH times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIPushToPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "push", peer, s.branch) // #nosec G204 -- fixed command with validated peer/branch
	cmd.Dir = s.CLIDir()
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push to peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIPullFromPeer shells out to `dolt pull` for a specific peer remote.
// Used for git-protocol remotes where CALL DOLT_PULL times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIPullFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "pull", peer, s.branch) // #nosec G204 -- fixed command with validated peer/branch
	cmd.Dir = s.CLIDir()
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull from peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// doltCLIFetchFromPeer shells out to `dolt fetch` for a specific peer remote.
// Used for git-protocol remotes where CALL DOLT_FETCH times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIFetchFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd := exec.CommandContext(ctx, "dolt", "fetch", peer) // #nosec G204 -- fixed command with validated peer
	cmd.Dir = s.CLIDir()
	creds.applyToCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fetch from peer %s: %s: %w", peer, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SyncResult is an alias for storage.SyncResult.
type SyncResult = storage.SyncResult
