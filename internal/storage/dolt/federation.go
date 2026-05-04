package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// federationStagingBranch is the temporary branch used to filter excluded
// issue types before pushing to a federation peer.
const federationStagingBranch = "__federation_push_staging"

// FederatedStorage implementation for DoltStore
// These methods enable peer-to-peer synchronization between workspaces.

// PushTo pushes commits to a specific peer remote.
// If credentials are stored for this peer, they are used automatically.
// For git-protocol remotes, uses CLI `dolt push` to avoid MySQL connection timeouts.
func (s *DoltStore) PushTo(ctx context.Context, peer string) error {
	return s.pushRefToPeer(ctx, peer, s.branch)
}

// pushRefToPeer pushes a specific refspec to a peer remote. The refspec can be
// a simple branch name ("main") or a mapping ("staging:main").
func (s *DoltStore) pushRefToPeer(ctx context.Context, peer string, refspec string) error {
	if s.isPeerGitProtocolRemote(ctx, peer) {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIPushRefToPeer(ctx, peer, refspec, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		if s.shouldUseCLIForPeerCredentials(ctx, peer, creds) {
			return s.doltCLIPushRefToPeer(ctx, peer, refspec, creds)
		}
		return withEnvCredentials(creds, func() error {
			if err := s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", peer, refspec); err != nil {
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
	return versioncontrolops.ListRemotes(ctx, s.db)
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
	// `dolt remote -v` requires a real repo; without repo_state.json it takes
	// ~12s to fail. Multi-database server roots have only sql-server.info, so
	// skip them. (be-1he)
	if _, err := os.Stat(filepath.Join(rootDir, ".dolt", "repo_state.json")); err != nil {
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
	return versioncontrolops.RemoveRemote(ctx, s.db, name)
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

	// Step 5: Push our changes to peer, filtering excluded types.
	excludeTypes := config.GetFederationConfig().ExcludeTypes
	if err := s.filteredPushToPeer(ctx, peer, excludeTypes); err != nil {
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

// filteredPushToPeer pushes to a peer after filtering out excluded issue types.
// When excludeTypes is empty, delegates directly to PushTo (no filtering).
//
// For non-empty excludeTypes, the method creates a temporary staging branch,
// deletes matching issues, commits the filtered state, and pushes the staging
// branch to the peer using a refspec. The staging branch is always cleaned up.
//
// The special type "wisp" matches issues with ephemeral=true in the committed
// issues table. Wisps normally live in dolt_ignore'd tables and are not pushed,
// so this acts as a defense-in-depth safety net.
func (s *DoltStore) filteredPushToPeer(ctx context.Context, peer string, excludeTypes []string) error {
	if len(excludeTypes) == 0 {
		return s.PushTo(ctx, peer)
	}

	// Pin a single connection for session-scoped branch operations.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("federation filter: acquire connection: %w", err)
	}
	defer conn.Close()

	// Clean up any leftover staging branch from a previous failed run.
	_, _ = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", federationStagingBranch)

	// Create staging branch from the current branch.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", federationStagingBranch, s.branch); err != nil {
		return fmt.Errorf("federation filter: create staging branch: %w", err)
	}

	// Ensure cleanup: restore original branch and delete staging.
	defer func() {
		_, _ = conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", s.branch)
		_, _ = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", federationStagingBranch)
	}()

	// Checkout staging branch and ensure ignored tables exist.
	if err := versioncontrolops.CheckoutBranch(ctx, conn, federationStagingBranch); err != nil {
		return fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	if err := schema.EnsureIgnoredTables(ctx, conn); err != nil {
		return fmt.Errorf("federation filter: ensure ignored tables: %w", err)
	}

	// Delete excluded issues from the committed issues table.
	deleted := false
	for _, excludeType := range excludeTypes {
		var result interface{ RowsAffected() (int64, error) }
		var execErr error
		if excludeType == "wisp" {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE ephemeral = 1")
		} else {
			result, execErr = conn.ExecContext(ctx, "DELETE FROM issues WHERE issue_type = ?", excludeType)
		}
		if execErr == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				deleted = true
			}
		}
	}

	if deleted {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
			"federation: exclude private issue types"); err != nil {
			return fmt.Errorf("federation filter: commit filtered state: %w", err)
		}
	}

	// Restore original branch context before pushing.
	if err := versioncontrolops.CheckoutBranch(ctx, conn, s.branch); err != nil {
		return fmt.Errorf("federation filter: restore branch %s: %w", s.branch, err)
	}

	// Push staging branch to peer, mapped to the peer's expected branch name.
	refspec := federationStagingBranch + ":" + s.branch
	return s.pushRefToPeer(ctx, peer, refspec)
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
	return s.doltCLIPushRefToPeer(ctx, peer, s.branch, creds)
}

// doltCLIPushRefToPeer shells out to `dolt push` with a specific refspec.
// The refspec can be a branch name or a "local:remote" mapping.
func (s *DoltStore) doltCLIPushRefToPeer(ctx context.Context, peer string, refspec string, creds *remoteCredentials) error {
	if err := s.prePushFSCK(ctx); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "dolt", "push", peer, refspec) // #nosec G204 -- fixed command with validated peer/refspec
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
