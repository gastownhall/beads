package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// The per-database commit lock serializes the DOLT_ADD/DOLT_COMMIT pairs bd
// issues on a shared Dolt sql-server (GH#8590 / dolthub/dolt#12258).
//
// Nothing in Dolt rejects a commit whose branch HEAD moved between the writer's
// BEGIN and its DOLT_COMMIT: prepareDoltCommit 3-way-merges the moved HEAD into
// the staged root and commits the result unexamined, so two bd processes
// committing within ~100ms store conflict artifacts in the second commit even
// though both staged the same rows from the shared working set. The symptom is
// silent (dolt_conflicts stays non-empty until the table is re-staged, and
// cherry-pick replay stops at each affected commit), and Dolt's working-root
// validation cannot catch it, so the exclusion has to happen on bd's side:
// hold a session-scoped named lock (GET_LOCK/RELEASE_LOCK, the same primitive
// schema.MigrateUpWithLock and the delete fence already use) around the
// add+commit pair on a session its holder has pinned, on every server-mode
// commit plane.
//
// Embedded mode is exempt (see WithoutCommitLock): a single-process store has
// no concurrent sessions to exclude, so its commit planes never issue the two
// lock round trips.

const (
	// commitLockPrefix namespaces the commit lock within Dolt/MySQL's
	// GET_LOCK name space, which is server-wide rather than per-database
	// (mirrors schema.MigrationLockName and deleteFenceLockPrefix). Two
	// beads databases sharing one dolt sql-server must not serialize against
	// each other, so the name carries the database.
	commitLockPrefix = "bd_commit:"
	// commitLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK name limit.
	commitLockNameMaxLength = 64
	// DefaultCommitLockWait bounds how long a commit waits to enter the
	// critical section before it proceeds unlocked (fail-open). It must
	// comfortably exceed an ordinary commit's duration without approaching
	// caller deadlines, so a stuck holder degrades serialization rather than
	// throughput. Individual stores can override it (dolt.DoltStore
	// commitLockWait) for skew-tolerant tests.
	DefaultCommitLockWait = 5 * time.Second
	// commitLockReleaseTimeout bounds the RELEASE_LOCK call, run on its own
	// context so a caller-canceled ctx cannot strand the lock held (same
	// discipline as the delete fence).
	commitLockReleaseTimeout = 5 * time.Second
	// commitLockCtxReserved keeps the effective GET_LOCK wait one second
	// inside the caller's remaining deadline, so the driver does not kill
	// the connection mid-wait at the deadline the caller meant for the
	// commit itself.
	commitLockCtxReserved = time.Second
)

// commitLockWarnInterval rate-limits the fail-open warning to roughly one per
// minute: one timed-out commit is noise, a stream of them is an incident, and
// a log line per write would bury the incident signal in the noise it exists
// to report.
var commitLockWarnInterval = int64(time.Minute / time.Second)

// commitLockWarnAt is the unix second of the last fail-open warning.
var commitLockWarnAt atomic.Int64

// commitLockTimeoutCount mirrors the bd.db.commit_lock_timeouts counter in an
// atomic, so tests can observe timeouts without installing a meter provider
// for a process-global instrument (see CommitLockTimeouts).
var commitLockTimeoutCount atomic.Uint64

// CommitLockTimeouts reports how many commit-lock acquisitions timed out and
// proceeded unlocked since process start. Test-observable counterpart of the
// bd.db.commit_lock_timeouts metric.
func CommitLockTimeouts() uint64 {
	return commitLockTimeoutCount.Load()
}

// commitLockMetrics holds the OTel instruments for the commit lock. They are
// registered against the storage/dolt meter so they sit beside the other
// bd.db.* instruments; versioncontrolops is the layer both the dolt server
// planes and the uow proxied planes reach, and it cannot import the dolt
// package (which imports this one) for its metrics struct.
var commitLockMetrics struct {
	wait               metric.Float64Histogram
	timeouts           metric.Int64Counter
	releaseUnconfirmed metric.Int64Counter
}

func init() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	commitLockMetrics.wait, _ = m.Float64Histogram("bd.db.commit_lock_wait",
		metric.WithDescription("Time spent waiting to acquire the per-database commit lock"),
		metric.WithUnit("ms"),
	)
	commitLockMetrics.timeouts, _ = m.Int64Counter("bd.db.commit_lock_timeouts",
		metric.WithDescription("Commits that waited out the commit lock and proceeded unlocked (fail-open)"),
		metric.WithUnit("{commit}"),
	)
	commitLockMetrics.releaseUnconfirmed, _ = m.Int64Counter("bd.db.commit_lock_release_unconfirmed",
		metric.WithDescription("Commit-lock RELEASE_LOCK calls that could not confirm the release; the session is discarded so the server drops its locks"),
		metric.WithUnit("{release}"),
	)
}

// CommitLockName returns the GET_LOCK key for database's commit critical
// section: "bd_commit:" plus the database name, lowercased (Dolt database
// names are case-insensitive, and the dolt and uow planes must derive the
// identical string) with any "/branch" revision suffix dropped, hashed down
// to fnv64a when the readable form would not fit the 64-byte GET_LOCK name
// limit — the scheme of schema.MigrationLockName and deleteFenceLockName.
//
// The name is a cross-version contract: a mixed-version fleet of bd binaries
// serializes against the same string, so it must never change shape.
func CommitLockName(database string) string {
	base := database
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	raw := commitLockPrefix + base
	if len(raw) <= commitLockNameMaxLength {
		return raw
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(base))
	return fmt.Sprintf("%s%016x", commitLockPrefix, h.Sum64())
}

// commitLockConfig is the per-call commit-lock configuration a server-mode
// caller threads through the context at its commit planes.
type commitLockConfig struct {
	// database is the store's database name; discovered via SELECT DATABASE()
	// on the committing session when empty.
	database string
	// wait overrides DefaultCommitLockWait; the effective wait is further
	// capped by the ctx's remaining deadline.
	wait time.Duration
	// disable turns the lock off entirely: the embedded store's commit
	// planes (single-session by construction, and exempt from the two lock
	// round trips by design).
	disable bool
}

// commitLockCtxKey keeps the context value unexported.
var commitLockCtxKey struct{}

// WithCommitLock returns ctx carrying the commit-lock configuration for a
// server-mode store: the database whose lock name to take and the store's own
// wait bound. Context-threading (the established idiom of
// issueops.VersionCommitDeferred) keeps StageAndCommit's exported signature
// stable, so mixed-version call sites compile against the same contract the
// commit message describes.
func WithCommitLock(ctx context.Context, database string, wait time.Duration) context.Context {
	return context.WithValue(ctx, commitLockCtxKey, commitLockConfig{database: database, wait: wait})
}

// WithoutCommitLock returns ctx with the commit lock disabled: the commit
// planes reached with it (embedded mode) issue no GET_LOCK or RELEASE_LOCK.
func WithoutCommitLock(ctx context.Context) context.Context {
	return context.WithValue(ctx, commitLockCtxKey, commitLockConfig{disable: true})
}

// commitLockFromContext reports the configuration a caller threaded onto ctx,
// if any.
func commitLockFromContext(ctx context.Context) (commitLockConfig, bool) {
	cfg, ok := ctx.Value(commitLockCtxKey).(commitLockConfig)
	return cfg, ok
}

// commitLockWaitSeconds resolves the GET_LOCK timeout for an acquisition on
// ctx: the configured (or default) wait, capped one second inside ctx's own
// remaining deadline so a deadline meant to bound the commit does not kill
// the connection mid-wait, truncated to the whole seconds GMS's GET_LOCK
// takes. Zero means a single attempt — a caller whose deadline is already
// breathing down the commit's neck still gets its try.
func commitLockWaitSeconds(ctx context.Context, configured time.Duration) int64 {
	wait := configured
	if wait <= 0 {
		wait = DefaultCommitLockWait
	}
	if deadline, ok := ctx.Deadline(); ok {
		if budget := time.Until(deadline) - commitLockCtxReserved; budget < wait {
			wait = budget
		}
	}
	if wait < 0 {
		wait = 0
	}
	return int64(wait / time.Second)
}

// CommitLock is a bd_commit GET_LOCK acquisition on one session. The zero
// value (and a nil pointer) is a no-op: held reports a lock that was never
// granted must never be released.
type CommitLock struct {
	name string
	held bool
}

// Held reports whether the lock was granted.
func (l *CommitLock) Held() bool {
	return l != nil && l.held
}

// Name returns the GET_LOCK key that was (or would be) taken.
func (l *CommitLock) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Release releases the lock on the same session it was acquired on, on a
// detached context, so a caller-canceled ctx cannot strand the lock held past
// this call. It reports whether the release was confirmed (the lock was
// granted and released, or was never held); an unconfirmed release means the
// session's lock state is now unknown, and the session must be discarded —
// never reused — so the server drops its locks on disconnect.
func (l *CommitLock) Release(q DBConn) (confirmed bool) {
	if l == nil || !l.held {
		return true
	}
	l.held = false
	rctx, cancel := context.WithTimeout(context.Background(), commitLockReleaseTimeout)
	defer cancel()
	var released sql.NullInt64
	err := q.QueryRowContext(rctx, "SELECT RELEASE_LOCK(?)", l.name).Scan(&released)
	if err != nil {
		log.Printf("versioncontrolops: commit lock %q release failed (session lock state unknown): %v", l.name, err)
		commitLockMetrics.releaseUnconfirmed.Add(rctx, 1)
		return false
	}
	if !released.Valid || released.Int64 != 1 {
		// NULL: this session does not hold the lock (it timed out server-side
		// or was taken over); 0: another session holds it — both mean the
		// lock is not ours to release, which is only possible if our session
		// lost it, so treat the session as suspect.
		log.Printf("versioncontrolops: commit lock %q release unconfirmed (result %v)", l.name, released)
		commitLockMetrics.releaseUnconfirmed.Add(rctx, 1)
		return false
	}
	return true
}

// AcquireCommitLock takes the per-database commit lock on the session q is
// bound to. q must be a single pinned session (*sql.Conn, an *sql.Tx whose
// connection is live for the whole hold, or a session-bound wrapper):
// GET_LOCK and the later Release are session-scoped, so acquiring and
// releasing on different sessions would not serialize anything.
//
// The wait is configured (or DefaultCommitLockWait), capped one second inside
// ctx's remaining deadline. A not-obtained lock is NOT an error: the commit
// proceeds unlocked exactly as it did before the lock existed (fail-open,
// logged rate-limited and counted) — the lock is a correctness improvement
// under contention, not a commit precondition. A query error IS an error,
// returned wrapped and handled by the caller exactly like a DOLT_ADD failure
// at that point.
func AcquireCommitLock(ctx context.Context, q DBConn, database string, wait time.Duration) (*CommitLock, error) {
	name := CommitLockName(database)
	timeout := commitLockWaitSeconds(ctx, wait)
	lock := &CommitLock{name: name}

	start := time.Now()
	var acquired sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, timeout).Scan(&acquired); err != nil {
		return lock, fmt.Errorf("acquire commit lock: %w", err)
	}
	commitLockMetrics.wait.Record(ctx, float64(time.Since(start).Milliseconds()))
	if !acquired.Valid || acquired.Int64 != 1 {
		commitLockTimeoutCount.Add(1)
		commitLockMetrics.timeouts.Add(ctx, 1)
		warnCommitLockTimeout(name, timeout)
		return lock, nil
	}
	lock.held = true
	return lock, nil
}

// connDiscarder is implemented by *sql.Conn: discarding the physical
// connection makes the pool drop the session instead of handing it to the
// next borrower, so the server releases its locks on disconnect.
type connDiscarder interface {
	Raw(func(any) error) error
}

// discardConn marks the session unusable so Close returns it to the pool as
// dead, the shape of the delete fence's releaseDiscard and schema's
// discardConn.
func discardConn(anyConn any) {
	if c, ok := anyConn.(connDiscarder); ok {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// warnCommitLockTimeout logs the fail-open degradation at most once per
// commitLockWarnInterval under this process's steady stream of commits: a
// rate-limited warning that a named holder is wedging commits on database
// `name`'s branch is the operator's signal, not a line per commit.
func warnCommitLockTimeout(name string, timeout int64) {
	now := time.Now().Unix()
	last := commitLockWarnAt.Load()
	if now-last < commitLockWarnInterval {
		return
	}
	if !commitLockWarnAt.CompareAndSwap(last, now) {
		return
	}
	log.Printf("versioncontrolops: commit lock %q not obtained after %ds; committing unlocked (fail-open; serialized commits are degraded, not lost)", name, timeout)
}

// HoldCommitLockOnConn takes the commit lock for database on the pinned conn
// and returns a release closure for it. The closure releases on conn and is
// safe to call exactly once from the critical section's exit path; a lock it
// releases unconfirmed makes it discard conn instead of leaving a suspect
// session for the pool, the delete-fence pattern. A not-obtained lock makes
// the closure a no-op. The commit's result never depends on the release.
//
// Callers must pass a *sql.Conn (or an equivalent single session): pooling
// here would let acquire and release land on different sessions.
func HoldCommitLockOnConn(ctx context.Context, conn DBConn, database string, wait time.Duration) (release func(), err error) {
	lock, err := AcquireCommitLock(ctx, conn, database, wait)
	if err != nil {
		return nil, err
	}
	return func() {
		if lock.Release(conn) {
			return
		}
		discardConn(conn)
	}, nil
}

// holdStageAndCommitLock is StageAndCommit's lock leg: the context decides
// whether and where to lock. WithoutCommitLock (embedded) skips it with no
// statements issued; a server-mode store threads its database and wait bound;
// any other caller (the proxied recompute path, a test) still gets the lock,
// discovering the database on the committing session, so the critical section
// can never silently depend on callers remembering the context.
func holdStageAndCommitLock(ctx context.Context, conn DBConn) (release func(), err error) {
	cfg, ok := commitLockFromContext(ctx)
	if ok && cfg.disable {
		return func() {}, nil
	}
	database, wait := "", DefaultCommitLockWait
	if ok {
		if cfg.database != "" {
			database = cfg.database
		}
		if cfg.wait > 0 {
			wait = cfg.wait
		}
	}
	if database == "" {
		// Same session, one round trip, only on the rare caller that did not
		// thread the store's database: a failure falls back to the empty
		// name's lock rather than skipping the critical section — one more
		// lock on one database is the safe direction to err.
		var current sql.NullString
		if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err == nil && current.Valid {
			database = current.String
		}
	}
	return HoldCommitLockOnConn(ctx, conn, database, wait)
}
