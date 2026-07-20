package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
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
	if useCLI, err := s.prepareCLIRouteForPeerGitProtocol(ctx, peer); err != nil {
		return err
	} else if useCLI {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIPushRefToPeer(ctx, peer, refspec, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		if useCLI, err := s.prepareCLIRouteForPeerCredentials(ctx, peer, creds); err != nil {
			return err
		} else if useCLI {
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
		if err := s.commitBeforePull(ctx, "auto-commit before pull"); err != nil {
			if !isDoltNothingToCommit(err) {
				return nil, fmt.Errorf("failed to commit pending changes before pull: %w", err)
			}
		}
	}

	// bd-6dnrw.3: pre-pull HEAD for the post-merge is_blocked recompute; an
	// unreadable HEAD degrades to a full recompute.
	preHead := ""
	if !s.readOnly {
		if h, err := s.GetCurrentCommit(ctx); err == nil {
			preHead = h
		}
	}

	// bd-578h9.3: every peer-pull route funnels through the same settle
	// machinery as the default-remote pull (pullTransport): the CLI routes
	// through finishCLIPull, the SQL route through pullWithAutoResolve. A bare
	// peer pull used to leave non-convergent merges behind — an FK
	// delete-vs-insert divergence rolls the merge back with nothing in
	// dolt_conflicts, and mixed-vintage schema_migrations rows conflict on
	// every retry.
	var conflicts []storage.Conflict
	var err error
	if useCLI, routeErr := s.prepareCLIRouteForPeerGitProtocol(ctx, peer); routeErr != nil {
		return nil, routeErr
	} else if useCLI {
		err = s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			pullErr := s.finishCLIPull(ctx, s.doltCLIPullFromPeer(ctx, peer, creds))
			return s.peerPullOutcome(ctx, peer, pullErr, &conflicts)
		})
		return s.finishPeerPull(ctx, conflicts, err, preHead)
	}
	err = s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		// Credential CLI routing: mirrors git-protocol peer pull path.
		if useCLI, err := s.prepareCLIRouteForPeerCredentials(ctx, peer, creds); err != nil {
			return err
		} else if useCLI {
			pullErr := s.finishCLIPull(ctx, s.doltCLIPullFromPeer(ctx, peer, creds))
			return s.peerPullOutcome(ctx, peer, pullErr, &conflicts)
		}
		return withEnvCredentials(creds, func() error {
			pullErr := s.pullWithAutoResolve(ctx, peer, "CALL DOLT_PULL(?)", peer)
			return s.peerPullOutcome(ctx, peer, pullErr, &conflicts)
		})
	})
	return s.finishPeerPull(ctx, conflicts, err, preHead)
}

// peerPullOutcome converts a settled peer pull's result into PullFrom's
// contract: conflicts the settle machinery could not auto-resolve are returned
// as data for the caller, anything else stays an error. The SQL route rolls
// the conflicted merge back before returning, so its conflicts arrive only via
// MergeConflictsError, captured pre-rollback (bd-578h9.15); the CLI route's
// subprocess writes conflicts to the on-disk working set where GetConflicts
// still sees them.
func (s *DoltStore) peerPullOutcome(ctx context.Context, peer string, pullErr error, conflicts *[]storage.Conflict) error {
	if pullErr == nil {
		return nil
	}
	var mce *versioncontrolops.MergeConflictsError
	if errors.As(pullErr, &mce) {
		*conflicts = mce.Conflicts
		return nil
	}
	if c, conflictErr := s.GetConflicts(ctx); conflictErr == nil && len(c) > 0 {
		*conflicts = c
		return nil
	}
	return fmt.Errorf("failed to pull from peer %s: %w", peer, pullErr)
}

// finishPeerPull runs the post-merge is_blocked recompute (bd-6dnrw.3) after a
// successful, conflict-free peer pull and passes the pull result through
// otherwise. Conflicted pulls skip the recompute: the caller resolves the
// conflicts first, and the next sync picks the rows up.
func (s *DoltStore) finishPeerPull(ctx context.Context, conflicts []storage.Conflict, pullErr error, preHead string) ([]storage.Conflict, error) {
	if pullErr != nil || len(conflicts) > 0 || s.readOnly {
		return conflicts, pullErr
	}
	if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
		return conflicts, fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
	}
	return conflicts, nil
}

// Fetch fetches refs from a peer without merging.
// If credentials are stored for this peer, they are used automatically.
// For git-protocol remotes, uses CLI `dolt fetch` to avoid MySQL connection timeouts.
func (s *DoltStore) Fetch(ctx context.Context, peer string) error {
	if useCLI, err := s.prepareCLIRouteForPeerGitProtocol(ctx, peer); err != nil {
		return err
	} else if useCLI {
		return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
			return s.doltCLIFetchFromPeer(ctx, peer, creds)
		})
	}
	return s.withPeerCredentials(ctx, peer, func(creds *remoteCredentials) error {
		// Credential CLI routing: route fetch through CLI subprocess.
		if useCLI, err := s.prepareCLIRouteForPeerCredentials(ctx, peer, creds); err != nil {
			return err
		} else if useCLI {
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

// hasPersistedCLIRemote reports whether a Dolt remote is persisted on disk in
// .dolt/repo_state.json — in the database CLI directory (CLIDir) or the dolt
// server root (Path, per GH#2118). A freshly (auto-)started sql-server can
// report an empty dolt_remotes table at store open even though remotes are
// persisted on disk. The #4259 remote-migrate gate therefore consults this
// directly so a cold-start open cannot miss the remote and migrate the shared
// database in place.
//
// The probe reads repo_state.json itself (no dolt CLI subprocess), so a
// missing dolt binary can no longer disable the gate. A directory that is not
// a dolt repository is a definite "no remote here"; a read/parse failure still
// fails open (migration is not wedged on unrelated corruption) but is logged,
// never swallowed (bd-6dnrw.33).
func (s *DoltStore) hasPersistedCLIRemote() bool {
	return s.HasPersistedRemote()
}

// HasPersistedRemote is the exported on-disk probe for callers that must not
// trust an empty dolt_remotes table at cold start: the remote-migrate gate
// and the push/pull "no remote configured" exit-0 skip (bd-578h9.10).
func (s *DoltStore) HasPersistedRemote() bool {
	cliDir := s.CLIDir()
	dirs := []string{cliDir}
	if s.dbPath != "" && s.dbPath != cliDir {
		dirs = append(dirs, s.dbPath)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		remotes, err := doltutil.PersistedRemotes(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"Warning: remote-migrate gate could not inspect %s for persisted remotes (assuming none): %v\n",
				dir, err)
			continue
		}
		if len(remotes) > 0 {
			return true
		}
	}
	return false
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

	// Get ahead/behind counts by comparing refs.
	// This requires the peer to have been fetched first.
	// Dolt's AS OF requires a literal ref: bind parameters (even inside CONCAT)
	// fail server-side with `unbound variable "v1" in query`, so validate the
	// ref and interpolate it (same pattern as embeddeddolt SyncStatus).
	remoteRef := peer + "/" + s.branch
	if err := issueops.ValidateRef(remoteRef); err != nil {
		status.LocalAhead = -1
		status.LocalBehind = -1
	} else {
		//nolint:gosec // G201: remoteRef is validated by issueops.ValidateRef above — AS OF requires a literal
		query := fmt.Sprintf(`
			SELECT
				(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
					(SELECT commit_hash FROM dolt_log AS OF '%s')) as ahead,
				(SELECT COUNT(*) FROM dolt_log AS OF '%s' WHERE commit_hash NOT IN
					(SELECT commit_hash FROM dolt_log)) as behind
		`, remoteRef, remoteRef)
		if err := s.db.QueryRowContext(ctx, query).
			Scan(&status.LocalAhead, &status.LocalBehind); err != nil {
			// If we can't get the status, return a partial result.
			// This happens when the remote branch doesn't exist locally yet.
			status.LocalAhead = -1
			status.LocalBehind = -1
		}
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

	// GH#2474: match PullFrom — commit pending changes before the merge,
	// INCLUDING config (where kv.memory.* rows live). Plain Commit excludes
	// config (GH#2455), so federation metadata writes such as add-peer plus any
	// persistent memories would otherwise leave the working set dirty and wedge
	// DOLT_MERGE ("cannot merge with uncommitted changes").
	if !s.readOnly {
		if err := s.commitBeforePull(ctx, "auto-commit before sync"); err != nil {
			if !isDoltNothingToCommit(err) {
				result.Error = fmt.Errorf("failed to commit pending changes before sync: %w", err)
				return result, result.Error
			}
		}
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

		// Commit the resolution INCLUDING config: the operator chose this
		// strategy, and plain Commit excludes config (GH#2455). A config-only
		// conflict — routine now that kv.memory.* memories sync through config —
		// would otherwise resolve but never commit, leaving the merge
		// unconcluded and re-wedging the next sync.
		if err := s.CommitMergeResolution(ctx, fmt.Sprintf("Resolve conflicts from %s using %s strategy", peer, strategy)); err != nil {
			result.Error = fmt.Errorf("failed to commit conflict resolution: %w", err)
			return result, result.Error
		}

		// bd-578h9.11: the conflicted merge skipped the automatic is_blocked
		// recompute (unresolved rows would have fed it garbage); now that the
		// resolution is committed, cover the whole merge+resolution window.
		if err := s.RecomputeBlockedAfterMerge(ctx, beforeCommit); err != nil {
			result.Error = fmt.Errorf("conflicts resolved but is_blocked recompute failed: %w", err)
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

	// Checkout staging branch.
	if err := versioncontrolops.CheckoutBranch(ctx, conn, federationStagingBranch); err != nil {
		return fmt.Errorf("federation filter: checkout staging: %w", err)
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

// prepareCLIRouteForPeerGitProtocol reports whether the SQL-visible peer
// remote uses git wire protocol and prepares the matching local CLI remote
// before routing.
func (s *DoltStore) prepareCLIRouteForPeerGitProtocol(ctx context.Context, peer string) (bool, error) {
	if s.CLIDir() == "" {
		return false, nil
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before git-protocol routing for peer %q: %w", peer, err)
	}
	for _, r := range remotes {
		if r.Name == peer {
			if !doltutil.IsGitProtocolURL(r.URL) {
				return false, nil
			}
			if err := s.ensureMatchingCLIRemote(peer, r.URL); err != nil {
				return false, fmt.Errorf("peer remote %q uses git protocol and requires CLI routing: %w", peer, err)
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *DoltStore) shouldUseCLIForPeerGitProtocol(ctx context.Context, peer string) (bool, error) {
	return s.prepareCLIRouteForPeerGitProtocol(ctx, peer)
}

// doltCLIPushRefToPeer shells out to `dolt push` with a specific refspec.
// The refspec can be a branch name or a "local:remote" mapping.
func (s *DoltStore) doltCLIPushRefToPeer(ctx context.Context, peer string, refspec string, creds *remoteCredentials) error {
	if err := s.prePushFSCK(ctx); err != nil {
		return err
	}
	cmd, transferCtx, cancel := s.prepareDoltCLITransfer(ctx, peer, creds, "push", peer, refspec)
	defer cancel()
	applyNoGitHooksToCmd(cmd) // GH#3724
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError(fmt.Sprintf("push to peer %s", peer), peer, transferCtx, out, err)
	}
	return nil
}

// doltCLIPullFromPeer shells out to `dolt pull` for a specific peer remote.
// Used for git-protocol remotes where CALL DOLT_PULL times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIPullFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd, transferCtx, cancel := s.prepareDoltCLITransfer(ctx, peer, creds, "pull", peer, s.branch)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError(fmt.Sprintf("pull from peer %s", peer), peer, transferCtx, out, err)
	}
	return nil
}

// doltCLIFetchFromPeer shells out to `dolt fetch` for a specific peer remote.
// Used for git-protocol remotes where CALL DOLT_FETCH times out through the SQL connection.
// Credentials are set on the subprocess environment only via cmd.Env.
func (s *DoltStore) doltCLIFetchFromPeer(ctx context.Context, peer string, creds *remoteCredentials) error {
	cmd, transferCtx, cancel := s.prepareDoltCLITransfer(ctx, peer, creds, "fetch", peer)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError(fmt.Sprintf("fetch from peer %s", peer), peer, transferCtx, out, err)
	}
	return nil
}

// SyncResult is an alias for storage.SyncResult.
type SyncResult = storage.SyncResult
