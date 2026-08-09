//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// withDBConn opens a short-lived database connection configured for the
// store's database and branch and passes it to fn. Unlike withConn, no
// transaction is started — this is required for Dolt stored procedures
// (CALL DOLT_BRANCH, CALL DOLT_MERGE, etc.) that cannot run inside
// explicit SQL transactions.
func (s *EmbeddedDoltStore) withDBConn(ctx context.Context, fn func(db versioncontrolops.DBConn) error) (err error) {
	if s.closed.Load() {
		return errClosed
	}

	var db *sql.DB
	var cleanup func() error
	db, cleanup, err = OpenSQL(ctx, s.dataDir, s.database, s.branch)
	if err != nil {
		return
	}
	defer func() {
		err = errors.Join(err, cleanup())
		// Best-effort cleanup of orphaned tmp_pack_* files left by git
		// fetch in the Dolt git-remote-cache. Rate-limited internally.
		s.cleanGitRemoteCacheGarbage()
	}()

	return fn(db)
}

// withPinnedDBConn is withDBConn pinned to a single *sql.Conn, for operation
// sequences that depend on session state spanning statements — the pull path
// sets @@dolt_allow_commit_conflicts/@@dolt_force_transaction_commit and needs
// the subsequent DOLT_MERGE and settle statements to see them (bd-6dnrw.40).
// A *sql.DB may rotate connections between statements; a pinned conn cannot.
//
// The pinned conn inherits the database/branch session setup OpenSQL applied:
// the pool holds exactly the one connection OpenSQL configured (sequential
// Ping/USE/SET on a fresh pool), and db.Conn returns it — the same invariant
// ApplySchemaMigrations relies on.
func (s *EmbeddedDoltStore) withPinnedDBConn(ctx context.Context, fn func(db versioncontrolops.DBConn) error) (err error) {
	if s.closed.Load() {
		return errClosed
	}

	var db *sql.DB
	var cleanup func() error
	db, cleanup, err = OpenSQL(ctx, s.dataDir, s.database, s.branch)
	if err != nil {
		return
	}
	defer func() {
		err = errors.Join(err, cleanup())
		// Best-effort cleanup of orphaned tmp_pack_* files left by git
		// fetch in the Dolt git-remote-cache. Rate-limited internally.
		s.cleanGitRemoteCacheGarbage()
	}()

	conn, connErr := db.Conn(ctx)
	if connErr != nil {
		return fmt.Errorf("embeddeddolt: pin connection: %w", connErr)
	}
	defer conn.Close()

	return fn(conn)
}

// withMutatingDBConn is withDBConn for operations that mutate the database
// or its version-control state (merge, push/pull, branch ops, backups, GC).
// withDBConn runs outside any SQL transaction, so withConn's commit guard
// never sees these — a read-only store satisfies the full DoltStorage
// interface and must refuse them here instead (bd-578h9.12).
func (s *EmbeddedDoltStore) withMutatingDBConn(ctx context.Context, fn func(db versioncontrolops.DBConn) error) error {
	if s.readOnly {
		return ErrReadOnly
	}
	return s.withDBConn(ctx, fn)
}

// withMutatingPinnedDBConn is withPinnedDBConn with the same read-only
// refusal as withMutatingDBConn (bd-578h9.12).
func (s *EmbeddedDoltStore) withMutatingPinnedDBConn(ctx context.Context, fn func(db versioncontrolops.DBConn) error) error {
	if s.readOnly {
		return ErrReadOnly
	}
	return s.withPinnedDBConn(ctx, fn)
}

// commitAll runs the single embedded commit statement, DOLT_COMMIT('-Am'),
// on one connection (via withConn) and reports whether a commit actually
// landed. When tolerateEmpty is true, Dolt's "nothing to commit" response is
// treated as a no-op (committed=false, err=nil) instead of an error — the
// GH#3886 parity behavior Commit relies on. When tolerateEmpty is false, that
// same response is returned as an error instead, which CommitMergeResolution
// relies on (see its doc comment).
//
// Callers that need to know whether a commit landed (CommitPending) get it
// from the returned bool instead of reading HEAD before and after: HEAD reads
// are extra engine opens and are subject to a HEAD-moved-between-reads race
// if anything else writes concurrently.
func (s *EmbeddedDoltStore) commitAll(ctx context.Context, message string, tolerateEmpty bool) (committed bool, err error) {
	err = s.withConn(ctx, true, func(tx *sql.Tx) error {
		var commitErr error
		committed, commitErr = commitAllInTx(ctx, tx, message, tolerateEmpty)
		return commitErr
	})
	return committed, err
}

func commitAllInTx(ctx context.Context, tx *sql.Tx, message string, tolerateEmpty bool) (bool, error) {
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", message); err != nil {
		if issueops.IsNothingToCommitError(err) {
			if tolerateEmpty {
				return false, nil
			}
			return false, fmt.Errorf("dolt commit: %w", err)
		}
		return false, wrapCommitIndeterminate("dolt commit", err)
	}
	return true, nil
}

// stageAndCommitAfterSQLCommit preserves the no-replay boundary for version
// publication after an already-visible SQL mutation.
func stageAndCommitAfterSQLCommit(ctx context.Context, db versioncontrolops.DBConn, dirtyTables map[string]bool, commitMsg, author string) error {
	if err := versioncontrolops.StageAndCommit(ctx, db, dirtyTables, commitMsg, author); err != nil {
		return wrapCommitIndeterminate("embeddeddolt: stage and commit after SQL commit", err)
	}
	return nil
}

// Commit stages and commits the full working set. A clean working set is not
// an error here: the server store (DoltStore.Commit et al., via
// isDoltNothingToCommit) has always tolerated Dolt's "nothing to commit"
// response, but this embedded path wrapped it as a hard failure — so `bd
// bootstrap`, which builds an embedded store unconditionally and calls
// CommitWithConfig (below) right after SetConfig on a pristine, otherwise-clean
// store, died on it (GH#3886). Tolerating it here brings embedded to parity.
func (s *EmbeddedDoltStore) Commit(ctx context.Context, message string) error {
	_, err := s.commitAll(ctx, message, true)
	return err
}

// CommitWithConfig commits all working set changes including config.
// so this is just an alias to satisfy the VersionControl interface (GH#3216).
func (s *EmbeddedDoltStore) CommitWithConfig(ctx context.Context, message string) error {
	return s.Commit(ctx, message)
}

// CommitMergeResolution concludes an operator --strategy merge resolution with
// config included. Embedded Commit already stages everything via DOLT_COMMIT
// ('-Am'), so config is never dropped here the way server-mode Commit drops it
// (GH#2455).
//
// Unlike Commit/CommitWithConfig, this does NOT alias Commit and does NOT
// tolerate a "nothing to commit" response: a merge resolution that leaves the
// working set clean is the --ours case, where our values already stood and
// resolving the conflict dirtied nothing — DoltStore.CommitMergeResolution
// handles exactly this by explicitly concluding the open merge instead of
// treating the empty diff as a no-op (concludeOpenMerge, wy-36ilm F12; see
// versioncontrolops.GetMergeBlockers, which documents the same class of gap:
// merge state that a plain commit-error check cannot see). Swallowing the
// error here without also concluding the merge would leave
// dolt_merge_status.is_merging true while reporting success, silently
// re-wedging the next pull/sync — worse than today's explicit failure.
// Whether embedded DOLT_COMMIT('-Am') already concludes an open merge on a
// clean working set (unlike the server-mode stored-procedure path, which
// requires the explicit conclude step) was not established here, so this
// keeps the pre-existing non-tolerant behavior rather than guess (GH#3886
// scope: fix bootstrap's plain-Commit path, not merge conclusion semantics).
func (s *EmbeddedDoltStore) CommitMergeResolution(ctx context.Context, message string) error {
	_, err := s.commitAll(ctx, message, false)
	return err
}

func (s *EmbeddedDoltStore) AddRemote(ctx context.Context, name, url string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		_, err := db.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
		return err
	})
}

func (s *EmbeddedDoltStore) HasRemote(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM dolt_remotes WHERE name = ?", name).Scan(&count)
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---------------------------------------------------------------------------
// Branch operations
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) Branch(ctx context.Context, name string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.CreateBranch(ctx, db, name)
	})
}

func (s *EmbeddedDoltStore) Checkout(ctx context.Context, branch string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.CheckoutBranch(ctx, db, branch)
	})
}

func (s *EmbeddedDoltStore) CurrentBranch(ctx context.Context) (string, error) {
	var branch string
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		branch, err = versioncontrolops.CurrentBranch(ctx, db)
		return err
	})
	return branch, err
}

func (s *EmbeddedDoltStore) DeleteBranch(ctx context.Context, branch string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.DeleteBranch(ctx, db, branch)
	})
}

func (s *EmbeddedDoltStore) ListBranches(ctx context.Context) ([]string, error) {
	var branches []string
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		branches, err = versioncontrolops.ListBranches(ctx, db)
		return err
	})
	return branches, err
}

// ---------------------------------------------------------------------------
// Version control operations
// ---------------------------------------------------------------------------

// commitAuthor returns the author string for merge commits.
const commitAuthor = commitName + " <" + commitEmail + ">"

func (s *EmbeddedDoltStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	var exists bool
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		exists, err = versioncontrolops.CommitExists(ctx, db, commitHash)
		return err
	})
	return exists, err
}

func (s *EmbeddedDoltStore) Status(ctx context.Context) (*storage.Status, error) {
	var status *storage.Status
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		status, err = versioncontrolops.Status(ctx, db)
		return err
	})
	return status, err
}

func (s *EmbeddedDoltStore) Log(ctx context.Context, limit int) ([]storage.CommitInfo, error) {
	var commits []storage.CommitInfo
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		commits, err = versioncontrolops.Log(ctx, db, limit)
		return err
	})
	return commits, err
}

func (s *EmbeddedDoltStore) Merge(ctx context.Context, branch string) ([]storage.Conflict, error) {
	// bd-578h9.11: like every pull path, a branch merge brings in writes that
	// bypassed the local is_blocked hooks; recompute after a conflict-free
	// merge. Conflicted merges defer to the caller's post-resolution hook
	// (Sync, bd vc merge --strategy) — recomputing over unresolved rows would
	// read garbage.
	preHead := ""
	if !s.readOnly {
		preHead = s.preMergeHead(ctx)
	}
	var conflicts []storage.Conflict
	err := s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		conflicts, err = versioncontrolops.Merge(ctx, db, branch, commitAuthor)
		return err
	})
	if err == nil && len(conflicts) == 0 && !s.readOnly {
		if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
			return conflicts, fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", rerr)
		}
	}
	return conflicts, err
}

// MergeWithStrategy implements storage.StrategicMerger for `bd vc merge
// --strategy` (#4992). Unlike Merge, it runs on a PINNED session
// (withMutatingPinnedDBConn, not withMutatingDBConn): the conflict-tolerant
// session flags versioncontrolops.MergeWithStrategy sets are session state
// and must be visible to the merge, resolve, repair, and commit statements
// that follow — a *sql.DB pool (OpenSQL allows 2 idle conns) could otherwise
// hand out a different connection mid-sequence.
//
// A resolved merge (conflicted or clean) always commits, so — unlike plain
// Merge, which skips the recompute for a still-conflicted merge — the
// is_blocked recompute always runs on success here.
func (s *EmbeddedDoltStore) MergeWithStrategy(ctx context.Context, branch, strategy string) ([]storage.Conflict, error) {
	preHead := ""
	if !s.readOnly {
		preHead = s.preMergeHead(ctx)
	}
	var conflicts []storage.Conflict
	err := s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		conflicts, err = versioncontrolops.MergeWithStrategy(ctx, db, branch, commitAuthor, strategy)
		return err
	})
	if err != nil {
		return conflicts, err
	}
	if !s.readOnly {
		if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
			return conflicts, fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", rerr)
		}
	}
	return conflicts, nil
}

// RecomputeBlockedAfterMerge recomputes the denormalized is_blocked column
// for the rows changed since fromCommit and commits the result — the hook a
// caller that resolved merge conflicts itself must run after committing the
// resolution (bd-578h9.11): conflicted merges skip the automatic recompute
// because unresolved rows would feed it garbage, and nothing else covers the
// merged-in writes. fromCommit is the pre-merge HEAD; empty degrades to a
// full-graph recompute.
func (s *EmbeddedDoltStore) RecomputeBlockedAfterMerge(ctx context.Context, fromCommit string) error {
	return s.recomputeBlockedAfterPull(ctx, fromCommit)
}

// RecomputeAllBlocked recomputes is_blocked for every issue and wisp in one full
// pass and returns the number of rows it corrected. This is the embedded path
// of the mode-independent repair (bd-6dnrw.37); see DoltStore.RecomputeAllBlocked.
func (s *EmbeddedDoltStore) RecomputeAllBlocked(ctx context.Context) (int, error) {
	var changed int64
	if err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		// One shared body across every mode: refuse to derive and commit
		// is_blocked from a dirty graph (see DoltStore.RecomputeAllBlocked),
		// checked inside the recompute tx so it sees the same working set the
		// recompute will read (bd-6dnrw.37).
		var e error
		changed, e = versioncontrolops.GuardedRecomputeAllBlockedInTx(ctx, tx)
		return e
	}); err != nil {
		return 0, err
	}
	if changed > 0 {
		// Stage only issues (wisps are dolt_ignore'd), matching the post-pull
		// recompute, so an unrelated dirty working set is not swept in.
		if err := s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return stageAndCommitAfterSQLCommit(ctx, db,
				versioncontrolops.BlockedRecomputeStagedTables(),
				versioncontrolops.BlockedRecomputeCommitMsg, commitAuthor)
		}); err != nil {
			return int(changed), err
		}
	}
	return int(changed), nil
}

func (s *EmbeddedDoltStore) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	var conflicts []storage.Conflict
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		conflicts, err = versioncontrolops.GetConflicts(ctx, db)
		return err
	})
	return conflicts, err
}

func (s *EmbeddedDoltStore) ResolveConflicts(ctx context.Context, table string, strategy string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.ResolveConflicts(ctx, db, table, strategy)
	})
}

// The CLI reaches these two methods through storage.UnwrapStore, so the
// assertion must keep holding on the concrete store.
var _ storage.ConflictInspector = (*EmbeddedDoltStore)(nil)

// GetConflictRows returns the live conflicted rows of table, per field.
// Implements storage.ConflictInspector (backs `bd conflicts list|show`).
func (s *EmbeddedDoltStore) GetConflictRows(ctx context.Context, table string) ([]storage.ConflictRow, error) {
	var rows []storage.ConflictRow
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		rows, err = versioncontrolops.GetConflictRows(ctx, db, table)
		return err
	})
	return rows, err
}

// The CLI reaches this through storage.UnwrapStore too.
var _ storage.MergeBlockerInspector = (*EmbeddedDoltStore)(nil)

// GetMergeBlockers reports schema conflicts, constraint violations, and
// whether a merge is open. Implements storage.MergeBlockerInspector.
func (s *EmbeddedDoltStore) GetMergeBlockers(ctx context.Context) (storage.MergeBlockers, error) {
	var blockers storage.MergeBlockers
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		blockers, err = versioncontrolops.GetMergeBlockers(ctx, db)
		return err
	})
	return blockers, err
}

// ResolveConflictRows resolves individual conflicted rows of table by key.
// Implements storage.ConflictInspector (backs `bd conflicts resolve <id>`).
// It runs on a PINNED connection: the resolution sets dolt's
// conflict-tolerance session flags, which the writes that follow must see.
func (s *EmbeddedDoltStore) ResolveConflictRows(ctx context.Context, table string, keys []string, strategy string) (int, error) {
	var n int
	err := s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		n, err = versioncontrolops.ResolveConflictRows(ctx, db, table, keys, strategy)
		return err
	})
	return n, err
}

// ---------------------------------------------------------------------------
// Remote operations
// ---------------------------------------------------------------------------

const defaultRemote = "origin"

// remoteAuthUser returns the username to authenticate with the remote, read
// from DOLT_REMOTE_USER. When set, push/pull/fetch invocations pass --user so
// the in-process Dolt server authenticates against the remotesapi (which
// otherwise rejects with CLONE_ADMIN). DOLT_REMOTE_PASSWORD is read by Dolt
// itself from the same process environment. Returns "" when no auth is
// configured (typical for git+ssh, file://, or unauthenticated remotes).
//
// Every remote verb reaches this through withPeerAuth, which prefers the
// credentials add-peer stored for the remote and falls back here when the
// remote has no peer entry.
func remoteAuthUser() string {
	return os.Getenv("DOLT_REMOTE_USER")
}

// The remote entry points the verbs below reach are held in variables purely
// as a test seam: credentials only change the outcome against a remotesapi
// server enforcing authentication, which a unit test cannot stand up, so the
// credential-routing tests swap these to observe the remote name and user each
// verb presents. TestRemoteEntryPointsUseVersionControlOps pins the production
// bindings.
var (
	vcPush             = versioncontrolops.Push
	vcForcePush        = versioncontrolops.ForcePush
	vcPull             = versioncontrolops.Pull
	vcPullWithStrategy = versioncontrolops.PullWithStrategy
)

func (s *EmbeddedDoltStore) RemoveRemote(ctx context.Context, name string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.RemoveRemote(ctx, db, name)
	})
}

func (s *EmbeddedDoltStore) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	var remotes []storage.RemoteInfo
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		remotes, err = versioncontrolops.ListRemotes(ctx, db)
		return err
	})
	return remotes, err
}

// GH#5080 follow-up: the verbs below resolve credentials through withPeerAuth
// rather than the environment alone, so a remote registered as a federation
// peer presents its stored credentials however it is reached (`bd sync
// --remote`, `bd dolt push|pull --remote`, or as the default remote). A remote
// with no peer entry keeps the DOLT_REMOTE_USER/DOLT_REMOTE_PASSWORD fallback
// unchanged. Routing every verb through the one resolver also narrows the
// window around withPeerAuth's mutation of that process-wide pair: a verb
// operating on a peer-backed remote now reads it holding federationEnvMutex,
// where before it read it holding no lock at all.

func (s *EmbeddedDoltStore) Push(ctx context.Context) error {
	return s.withPeerAuth(ctx, defaultRemote, func(user string) error {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcPush(ctx, db, defaultRemote, s.branch, user)
		})
	})
}

func (s *EmbeddedDoltStore) Pull(ctx context.Context) error {
	// GH#2474 / bd-578h9.2: auto-commit pending changes before pull, matching
	// server-mode pullFromRemote and PullFrom. Leftovers from a crashed
	// command would otherwise make the merge refuse to start.
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
	err := s.withPeerAuth(ctx, defaultRemote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcPull(ctx, db, defaultRemote, s.branch, user)
		})
	})
	if err != nil {
		return err
	}
	return s.recomputeBlockedAfterPull(ctx, preHead)
}

// PullWithStrategy implements storage.StrategicPuller for `bd dolt pull
// --strategy` (#4992 part 2). Identical to Pull except conflicts the
// auto-resolver declines are resolved with strategy instead of aborting the
// merge for the operator; see versioncontrolops.PullWithStrategy.
func (s *EmbeddedDoltStore) PullWithStrategy(ctx context.Context, strategy string) error {
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
	err := s.withPeerAuth(ctx, defaultRemote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcPullWithStrategy(ctx, db, defaultRemote, s.branch, user, strategy)
		})
	})
	if err != nil {
		return err
	}
	return s.recomputeBlockedAfterPull(ctx, preHead)
}

// PullRemoteWithStrategy implements storage.StrategicPuller for a named
// remote; see PullWithStrategy.
func (s *EmbeddedDoltStore) PullRemoteWithStrategy(ctx context.Context, remote, strategy string) error {
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
	err := s.withPeerAuth(ctx, remote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcPullWithStrategy(ctx, db, remote, s.branch, user, strategy)
		})
	})
	if err != nil {
		return err
	}
	return s.recomputeBlockedAfterPull(ctx, preHead)
}

func (s *EmbeddedDoltStore) ForcePush(ctx context.Context) error {
	return s.withPeerAuth(ctx, defaultRemote, func(user string) error {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcForcePush(ctx, db, defaultRemote, s.branch, user)
		})
	})
}

func (s *EmbeddedDoltStore) PushRemote(ctx context.Context, remote string, force bool) error {
	return s.withPeerAuth(ctx, remote, func(user string) error {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			if force {
				return vcForcePush(ctx, db, remote, s.branch, user)
			}
			return vcPush(ctx, db, remote, s.branch, user)
		})
	})
}

func (s *EmbeddedDoltStore) PullRemote(ctx context.Context, remote string) error {
	// GH#2474 / bd-578h9.2: see Pull.
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
	err := s.withPeerAuth(ctx, remote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return vcPull(ctx, db, remote, s.branch, user)
		})
	})
	if err != nil {
		return err
	}
	return s.recomputeBlockedAfterPull(ctx, preHead)
}

func (s *EmbeddedDoltStore) Fetch(ctx context.Context, peer string) error {
	return s.withPeerAuth(ctx, peer, func(user string) error {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return versioncontrolops.Fetch(ctx, db, peer, user)
		})
	})
}

func (s *EmbeddedDoltStore) PushTo(ctx context.Context, peer string) error {
	return s.withPeerAuth(ctx, peer, func(user string) error {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return versioncontrolops.Push(ctx, db, peer, s.branch, user)
		})
	})
}

func (s *EmbeddedDoltStore) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	// Auto-commit pending changes before pull to prevent
	// "cannot merge with uncommitted changes" errors.
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return nil, fmt.Errorf("commit pending before pull: %w", err)
	}

	preHead := s.preMergeHead(ctx)
	var conflicts []storage.Conflict
	err := s.withPeerAuth(ctx, peer, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			if pullErr := versioncontrolops.Pull(ctx, db, peer, s.branch, user); pullErr != nil {
				// bd-578h9.15: the settle machinery aborts a merge it cannot
				// auto-resolve before returning, so dolt_conflicts is already
				// empty here; the conflicts arrive captured pre-abort inside
				// MergeConflictsError instead.
				var mce *versioncontrolops.MergeConflictsError
				if errors.As(pullErr, &mce) {
					conflicts = mce.Conflicts
					return nil
				}
				return fmt.Errorf("pull from %s: %w", peer, pullErr)
			}
			return nil
		})
	})
	if err != nil || len(conflicts) > 0 {
		// Conflicted pulls skip the recompute: the operator resolves first,
		// and the next sync picks the rows up.
		return conflicts, err
	}
	if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
		return conflicts, fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
	}
	return conflicts, nil
}

// preMergeHead reads the pre-pull HEAD for the post-merge is_blocked
// recompute (bd-6dnrw.3). Empty on failure, which degrades the recompute to a
// full pass instead of skipping the hook.
func (s *EmbeddedDoltStore) preMergeHead(ctx context.Context) string {
	head, err := s.GetCurrentCommit(ctx)
	if err != nil {
		return ""
	}
	return head
}

// recomputeBlockedAfterPull recomputes the denormalized is_blocked column for
// the rows a pull's merge changed (bd-6dnrw.3) and creates a Dolt commit for
// the result. is_blocked is otherwise maintained only by local write paths, so
// a merge that brings in another clone's status or dependency changes leaves
// it stale and `bd ready` trusts it. A pull that merged nothing (HEAD
// unchanged) is a no-op; derived state converges, so committing it on every
// clone is merge-safe.
func (s *EmbeddedDoltStore) recomputeBlockedAfterPull(ctx context.Context, preHead string) error {
	if err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, preHead)
	}); err != nil {
		// The merge this recompute covers is already committed, so a plain
		// retry on the next pull would skip as "nothing merged" — leave a
		// marker so it widens its window instead (bd-578h9.11). Best-effort:
		// the recompute error is what matters.
		_ = s.withConn(ctx, true, func(tx *sql.Tx) error {
			return issueops.MarkIsBlockedRecomputePendingInTx(ctx, tx, preHead)
		})
		return err
	}
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return stageAndCommitAfterSQLCommit(ctx, db,
			map[string]bool{"issues": true}, "bd: recompute is_blocked after pull", commitAuthor)
	})
}

// ---------------------------------------------------------------------------
// Backup operations
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) BackupAdd(ctx context.Context, name, url string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.BackupAdd(ctx, db, name, url)
	})
}

func (s *EmbeddedDoltStore) BackupSync(ctx context.Context, name string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.BackupSync(ctx, db, name)
	})
}

func (s *EmbeddedDoltStore) BackupRemove(ctx context.Context, name string) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.BackupRemove(ctx, db, name)
	})
}

// BackupDatabase registers dir as a file:// Dolt backup remote and syncs
// the database to it. The dir must exist locally. This preserves full Dolt
// commit history.
func (s *EmbeddedDoltStore) BackupDatabase(ctx context.Context, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup destination does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}
	backupName := "backup_export"

	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		// Register as a backup remote (idempotent — remove first if exists).
		_ = versioncontrolops.BackupRemove(ctx, db, backupName)
		if err := versioncontrolops.BackupAdd(ctx, db, backupName, backupURL); err != nil {
			// Another backup (e.g. "default" registered by `bd backup init`) may
			// already point to this URL. In that case, sync using the existing
			// remote name rather than failing.
			if conflict := versioncontrolops.ExtractAddressConflictName(err); conflict != "" {
				if syncErr := versioncontrolops.BackupSync(ctx, db, conflict); syncErr != nil {
					return fmt.Errorf("sync to backup: %w", syncErr)
				}
				return nil
			}
			return fmt.Errorf("register backup remote: %w", err)
		}
		if err := versioncontrolops.BackupSync(ctx, db, backupName); err != nil {
			return fmt.Errorf("sync to backup: %w", err)
		}
		return nil
	})
}

// RestoreDatabase restores the database from a Dolt backup at dir.
// The dir must exist locally and contain a valid Dolt backup.
// When force is true, an existing database is overwritten.
func (s *EmbeddedDoltStore) RestoreDatabase(ctx context.Context, dir string, force bool) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup source does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}

	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.BackupRestore(ctx, db, backupURL, s.database, force)
	})
}
