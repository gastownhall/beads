package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// federationStagingBranchPrefix identifies temporary filtered-push branches.
// Each operation appends a UUID so concurrent pushes cannot collide.
const federationStagingBranchPrefix = "__federation_push_staging_"

// federationKeepBranchPrefix names the temporary branch on which a filtered
// push reverts its filter commit before merging that revert into the source.
const federationKeepBranchPrefix = "__federation_push_keep_"

// federationStagingCleanupTimeout bounds best-effort cleanup after the caller
// has canceled or its deadline has expired.
const federationStagingCleanupTimeout = 30 * time.Second

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
	var conflicts []storage.Conflict
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		var err error
		conflicts, err = s.pullFromPeer(ctx, peer)
		return err
	})
	return conflicts, err
}

func (s *DoltStore) pullFromPeer(ctx context.Context, peer string) ([]storage.Conflict, error) {
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
// Unlike HasPersistedRemote, it reads those directories whatever server the
// store dials: for this gate a remote recorded there is a reason for caution,
// not a claim about the server's remotes that a caller acts on.
//
// The probe reads repo_state.json itself (no dolt CLI subprocess), so a
// missing dolt binary can no longer disable the gate. A directory that is not
// a dolt repository is a definite "no remote here"; a read/parse failure still
// fails open (migration is not wedged on unrelated corruption) but is logged,
// never swallowed (bd-6dnrw.33).
func (s *DoltStore) hasPersistedCLIRemote() bool {
	_, remotes := s.persistedRemotesOnDisk()
	return len(remotes) > 0
}

// HasPersistedRemote is the exported on-disk probe for callers that must not
// trust an empty dolt_remotes table at cold start: the push/pull "no remote
// configured" exit-0 skip (bd-578h9.10). Like PersistedRemoteInfos, it counts
// only remotes recorded where the workspace's Dolt root is proven served by
// the server bd dials.
func (s *DoltStore) HasPersistedRemote() bool {
	return len(s.PersistedRemoteInfos()) > 0
}

// PersistedRemoteInfos returns the remotes persisted on disk in
// .dolt/repo_state.json — names AND urls — searching the same directories in
// the same order as HasPersistedRemote: the database CLI directory first,
// then the dolt server root (GH#2118). The first directory that yields any
// remotes wins, mirroring HasPersistedRemote's first-hit semantics.
//
// Callers in the GH#2118 cold-start window use this to RECOVER the invisible
// remote rather than merely detect it: a freshly (auto-)started sql-server
// can report an empty dolt_remotes even though the remote is persisted on
// disk, so an empty listing is a reporting artifact, not an unconfigured rig
// (wy-6k7f7). Read/parse failures are logged and skipped, matching the old
// HasPersistedRemote behavior — callers only consult this after a SUCCESSFUL
// (empty) ListRemotes, so a read failure here never masquerades as evidence.
//
// Those are the server's remotes only where the workspace's Dolt root is
// proven served by the server bd dials (cliDirServesServer). Elsewhere, as
// for a server bd does not run or a copy no live server on the dialed port
// serves, a database there is at best a copy whose remotes say nothing about
// the server's, so none are returned: a copy that lists origin must not make
// `bd dolt remote add` or `bd config apply` take the remote as already
// configured on a server that lists none. LeftoverRemotes returns such a
// copy's remotes instead.
func (s *DoltStore) PersistedRemoteInfos() []storage.RemoteInfo {
	if !s.cliDirServesServer() {
		return nil
	}
	_, remotes := s.persistedRemotesOnDisk()
	return remotes
}

// LeftoverRemotes returns the remotes PersistedRemoteInfos leaves out, those
// recorded where the workspace's Dolt root is not proven served by the
// server bd dials (cliDirServesServer), and the directory that records them;
// none where it is. They are not the server's remotes, but they are the
// remotes the operator set up, such as the origin of the clone bd init
// --server --remote leaves for a gateway: a server that lists no remote
// beside them is not a workspace without one, so the push/pull no-remote
// decision (hasConfiguredRemote in cmd/bd) names them and the command that
// adds each on the server instead of reporting that no remote is configured.
func (s *DoltStore) LeftoverRemotes() (string, []storage.RemoteInfo) {
	if s.cliDirServesServer() {
		return "", nil
	}
	return s.persistedRemotesOnDisk()
}

// persistedRemotesOnDisk is PersistedRemoteInfos for any server: the remotes
// recorded in the database CLI directory, else in the dolt server root, and
// the directory that records them.
func (s *DoltStore) persistedRemotesOnDisk() (string, []storage.RemoteInfo) {
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
			return dir, remotes
		}
	}
	return "", nil
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

	// Step 3: Merge peer's branch through the settle machinery PullFrom uses. A
	// plain DOLT_MERGE rolls back on any conflict or FK violation, so a peer's
	// delete of a published issue linked to a withheld one (the kept filter
	// revert makes those links local changes) would fail every later sync.
	remoteBranch := fmt.Sprintf("%s/%s", peer, s.branch)
	conflicts, err := s.mergeFetchedPeer(ctx, remoteBranch, beforeCommit)
	if err != nil {
		result.Error = fmt.Errorf("merge failed: %w", err)
		return result, result.Error
	}

	// Step 4: Handle the conflicts the auto-resolver left for the operator.
	if len(conflicts) > 0 {
		result.Conflicts = conflicts

		if strategy == "" {
			// No strategy specified, leave conflicts for manual resolution
			result.Error = fmt.Errorf("merge conflicts require resolution (use --strategy ours|theirs)")
			return result, result.Error
		}

		// The settle rolled the merge back: merge again, resolving every
		// conflicted table with the strategy.
		if err := s.mergeFetchedPeerWithStrategy(ctx, remoteBranch, strategy, beforeCommit); err != nil {
			result.Error = fmt.Errorf("conflict resolution failed: %w", err)
			return result, result.Error
		}
		result.ConflictsResolved = true
	}
	result.Merged = true

	// Count pulled commits
	afterCommit, _ := s.GetCurrentCommit(ctx) // Best effort: empty commit hash means diff won't be logged
	if beforeCommit != afterCommit {
		result.PulledCommits = 1 // Simplified - could count actual commits
	}

	// Step 5: Push our changes to peer, filtering excluded types.
	excludeTypes := config.GetFederationConfig().ExcludeTypes
	pushRes, err := s.filteredPublishToPeer(ctx, peer, excludeTypes)
	if err != nil {
		// Push failure is not fatal - peer may not accept pushes
		result.PushError = err
	}
	// Pushed means a publication landed: an unchanged payload pushes nothing,
	// and a cleanup failure after the push is reported but does not undo it.
	result.Pushed = pushRes.Pushed
	result.PushFiltered = pushRes.Filtered

	// Record last sync time
	_ = s.setLastSyncTime(ctx, peer) // Best effort: sync timestamp is advisory for scheduling

	result.EndTime = time.Now()
	return result, nil
}

// mergeFetchedPeer merges ref, the peer branch Fetch brought in, through the
// settle machinery PullFrom's SQL route uses (settleMergeInTx): safe conflict
// classes are auto-resolved and FK cascade violations repaired in one
// transaction, then is_blocked is recomputed from preHead. Conflicts left for
// the operator come back with the transaction rolled back (bd-578h9.15).
func (s *DoltStore) mergeFetchedPeer(ctx context.Context, ref, preHead string) ([]storage.Conflict, error) {
	var conflicts []storage.Conflict
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.withStoreBranchConn(ctx, func(conn *sql.Conn) error {
			err := s.mergeWithAutoResolve(ctx, conn, ref)
			var mce *versioncontrolops.MergeConflictsError
			if errors.As(err, &mce) {
				conflicts = mce.Conflicts
				return nil
			}
			return err
		})
	})
	if err != nil || len(conflicts) > 0 {
		return conflicts, err
	}
	return nil, s.recomputeAfterPeerMerge(ctx, preHead)
}

// mergeFetchedPeerWithStrategy merges ref again with MergeWithStrategy (bd vc
// merge --strategy): every conflicted table is resolved with strategy, FK
// cascade violations are repaired and the merge is committed. Then is_blocked
// is recomputed from preHead.
func (s *DoltStore) mergeFetchedPeerWithStrategy(ctx context.Context, ref, strategy, preHead string) error {
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.withStoreBranchConn(ctx, func(conn *sql.Conn) error {
			_, err := versioncontrolops.MergeWithStrategy(ctx, conn, ref, s.commitAuthorString(), strategy)
			return err
		})
	})
	if err != nil {
		return err
	}
	return s.recomputeAfterPeerMerge(ctx, preHead)
}

// recomputeAfterPeerMerge recomputes is_blocked for the rows a peer merge
// changed since preHead (bd-6dnrw.3).
func (s *DoltStore) recomputeAfterPeerMerge(ctx context.Context, preHead string) error {
	if s.readOnly {
		return nil
	}
	if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
		return fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", err)
	}
	return nil
}

// withStoreBranchConn runs fn on a dedicated long-timeout connection checked
// out to the store's branch: a fresh connection starts on the database's
// default branch (be-b0am), and the session flags a merge sets die with the
// connection instead of staying on a pooled one.
func (s *DoltStore) withStoreBranchConn(ctx context.Context, fn func(conn *sql.Conn) error) error {
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire long-timeout connection: %w", err)
	}
	defer conn.Close()
	if err := s.pinStoreBranch(ctx, conn); err != nil {
		return err
	}
	return fn(conn)
}

// mergeWithAutoResolve runs DOLT_MERGE of ref on conn and settles it in one
// transaction, with the session flags pullWithAutoResolve sets so a conflicted
// or FK-violating merge lands in the working set for settleMergeInTx.
func (s *DoltStore) mergeWithAutoResolve(ctx context.Context, conn *sql.Conn, ref string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	for _, flag := range []string{"SET @@dolt_allow_commit_conflicts = 1", "SET @@dolt_force_transaction_commit = 1"} {
		if _, err := tx.ExecContext(ctx, flag); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", flag, err)
		}
	}
	baseline, err := versioncontrolops.ReadMergeBaseline(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	mergeErr := schema.DrainCall(ctx, tx, "CALL DOLT_MERGE('--author', ?, ?)", s.commitAuthorString(), ref)
	if mergeErr != nil && strings.Contains(mergeErr.Error(), "up to date") {
		mergeErr = nil
	}
	return s.settleMergeInTx(ctx, tx, baseline, mergeErr)
}

// filteredPushToPeer pushes to a peer after filtering out excluded issue types.
// When excludeTypes is empty, delegates directly to PushTo (no filtering).
//
// For non-empty excludeTypes, versioncontrolops.RunFilteredPush stages the
// source branch, withholds excluded issues the peer's fetched tip does not
// already hold, keeps the filter commit's ancestry locally without losing the
// withheld rows, and pushes the staging branch to the peer using a refspec. The
// temporary branches are always cleaned up.
//
// The special type "wisp" matches issues with ephemeral=true in the committed
// issues table. Wisps normally live in dolt_ignore'd tables and are not pushed,
// so this acts as a defense-in-depth safety net.
func (s *DoltStore) filteredPushToPeer(ctx context.Context, peer string, excludeTypes []string) error {
	_, err := s.filteredPublishToPeer(ctx, peer, excludeTypes)
	return err
}

// filteredPublishToPeer is filteredPushToPeer that also reports what the
// publication did; res.Pushed stays set when cleanup fails after the push.
func (s *DoltStore) filteredPublishToPeer(ctx context.Context, peer string, excludeTypes []string) (res versioncontrolops.FilteredPushResult, retErr error) {
	if len(excludeTypes) == 0 {
		err := s.PushTo(ctx, peer)
		return versioncontrolops.FilteredPushResult{Pushed: err == nil}, err
	}
	sourceBranch := s.branch
	operationID, err := uuid.NewRandom()
	if err != nil {
		return res, fmt.Errorf("federation filter: generate staging branch: %w", err)
	}
	stagingBranch := federationStagingBranchPrefix + operationID.String()
	keepBranch := federationKeepBranchPrefix + operationID.String()

	// Pin a single long-timeout connection for the session-scoped staging
	// operation. RecomputeAllIsBlockedInTx can exceed the shared pool's
	// 10-second I/O deadline, and branch state is connection-scoped.
	//
	// Audited for be-b0am's fresh-connection branch hazard: safe. Unlike
	// recomputeAllBlocked/recomputeBlockedTx, this never depends on the
	// fresh connection's default checkout — RunFilteredPush checks out
	// sourceBranch by name and creates stagingBranch from its captured head,
	// so the source ref is named, not inherited.
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return res, fmt.Errorf("federation filter: open long-timeout connection: %w", err)
	}
	defer db.Close()
	db.SetMaxIdleConns(0)
	conn, err := db.Conn(ctx)
	if err != nil {
		return res, fmt.Errorf("federation filter: acquire long-timeout connection: %w", err)
	}
	defer conn.Close()

	// Arm cleanup before creating the branches: a lost create response can make
	// a branch exist even when the call fails, and canceling an in-flight
	// query makes go-sql-driver/mysql invalidate the operation connection, so
	// cleanup reopens a fresh one.
	defer func() {
		if cleanupErr := cleanupFilteredStaging(ctx, db, conn, sourceBranch, stagingBranch, keepBranch); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("federation filter: cleanup: %w", cleanupErr))
		}
	}()

	peerTip, err := versioncontrolops.RemoteBranchTip(ctx, conn, peer, sourceBranch)
	if err != nil {
		return res, fmt.Errorf("federation filter: %w", err)
	}
	return versioncontrolops.RunFilteredPush(ctx, conn, versioncontrolops.FilteredPushRequest{
		Source:       sourceBranch,
		Staging:      stagingBranch,
		Keep:         keepBranch,
		PeerTip:      peerTip,
		ExcludeTypes: excludeTypes,
		Author:       s.commitAuthorString(),
		Push: func(ctx context.Context, refspec string) error {
			return s.pushRefToPeer(ctx, peer, refspec)
		},
	})
}

// cleanupFilteredStaging discards the operation connection (an in-flight cancel
// invalidates it), then restores sourceBranch and deletes the temporary
// branches on a fresh, uncanceled, bounded connection. Errors are joined; a
// non-nil result is wrapped by the caller with the "federation filter: cleanup"
// context.
func cleanupFilteredStaging(ctx context.Context, db *sql.DB, conn *sql.Conn, sourceBranch string, branches ...string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), federationStagingCleanupTimeout)
	defer cancel()
	var cleanupErr error
	if err := conn.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("discard operation connection: %w", err))
	}
	cleanupConn, err := db.Conn(cleanupCtx)
	if err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("acquire cleanup connection: %w", err))
	} else {
		defer cleanupConn.Close()
		if err := versioncontrolops.FilteredPushStage(cleanupCtx, "cleanup", cleanupConn, versioncontrolops.FilteredPushRequest{Source: sourceBranch}); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := schema.DrainCall(cleanupCtx, cleanupConn, "CALL DOLT_CHECKOUT(?)", sourceBranch); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore branch %s: %w", sourceBranch, err))
		}
		for _, branch := range branches {
			if err := deleteFederationStagingBranch(cleanupCtx, cleanupConn, branch); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete temporary branch %s: %w", branch, err))
			}
		}
	}
	return cleanupErr
}

func deleteFederationStagingBranch(ctx context.Context, conn *sql.Conn, stagingBranch string) error {
	err := schema.DrainCall(ctx, conn, "CALL DOLT_BRANCH('-Df', ?)", stagingBranch)
	var mysqlErr *mysql.MySQLError
	// Dolt reports a missing branch as generic error 1105; match the message as
	// a case-insensitive substring (as RemoteRefUnavailableErr does) because
	// Dolt may append the branch/ref name to this class of error.
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1105 &&
		strings.Contains(strings.ToLower(mysqlErr.Message), "branch not found") {
		return nil
	}
	return err
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
			if err := s.ensureMatchingCLIRemote(peer, r.URL, r.Ref); err != nil {
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
	if err := requireCLIDoltBinary(); err != nil {
		return fmt.Errorf("dolt push: %w", err)
	}
	if err := s.prePushFSCK(ctx); err != nil {
		return err
	}
	cmd, transferCtx, cancel, err := s.prepareDoltCLITransfer(ctx, peer, creds, "push", peer, refspec)
	if err != nil {
		return err
	}
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
	cmd, transferCtx, cancel, err := s.prepareDoltCLITransfer(ctx, peer, creds, "pull", peer, s.branch)
	if err != nil {
		return err
	}
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
	cmd, transferCtx, cancel, err := s.prepareDoltCLITransfer(ctx, peer, creds, "fetch", peer)
	if err != nil {
		return err
	}
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError(fmt.Sprintf("fetch from peer %s", peer), peer, transferCtx, out, err)
	}
	return nil
}

// SyncResult is an alias for storage.SyncResult.
type SyncResult = storage.SyncResult
