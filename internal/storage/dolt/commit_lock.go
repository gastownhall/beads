package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// Server-mode commit serialization (GH#8590). Every commit plane of a
// server-mode store holds versioncontrolops.CommitLockName(s.database) from
// just before its first DOLT_ADD until its DOLT_COMMIT returns; see
// versioncontrolops/commit_lock.go for why and for the fail-open contract.
//
// Lock order: a guarded delete takes its bd_delete:<db>:<id> fence before the
// commit lock, never the reverse, and migration commits under bd_schema_init
// never take it, so the named locks cannot form a cycle.

// commitLockConfig is the store's commit-lock configuration, with the
// observers that feed its metrics.
func (s *DoltStore) commitLockConfig() versioncontrolops.CommitLockConfig {
	return versioncontrolops.CommitLockConfig{
		Database: s.database,
		Wait:     s.commitLockWait,
		OnAcquire: func(ctx context.Context, l *versioncontrolops.CommitLock) {
			doltMetrics.commitLockWait.Record(ctx, float64(l.Waited.Milliseconds()))
			if l.TimedOut {
				s.commitLockTimeouts.Add(1)
				doltMetrics.commitLockTimeouts.Add(ctx, 1)
			}
		},
		OnReleaseUnconfirmed: func() {
			doltMetrics.commitLockReleaseUnconfirmed.Add(context.Background(), 1)
		},
	}
}

// holdCommitLockOnConn takes the commit lock on conn, a connection the
// caller pinned for its whole add+commit, and returns its release. Outside
// server mode it takes nothing: an embedded engine has no concurrent
// sessions to serialize.
func (s *DoltStore) holdCommitLockOnConn(ctx context.Context, conn *sql.Conn) (func(), error) {
	if !s.serverMode {
		return func() {}, nil
	}
	return versioncontrolops.HoldCommitLockOnConn(ctx, conn, s.commitLockConfig())
}

// stageAndCommitSerialized is versioncontrolops.StageAndCommitSerialized in
// server mode and plain StageAndCommit otherwise.
func (s *DoltStore) stageAndCommitSerialized(ctx context.Context, conn *sql.Conn, dirtyTables map[string]bool, commitMsg, author string) error {
	if !s.serverMode {
		return versioncontrolops.StageAndCommit(ctx, conn, dirtyTables, commitMsg, author)
	}
	return versioncontrolops.StageAndCommitSerialized(ctx, conn, s.commitLockConfig(), dirtyTables, commitMsg, author)
}
