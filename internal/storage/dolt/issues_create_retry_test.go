package dolt

// Regression tests for the create retry-replay id leak (GH#7289 / PR #6831).
//
// withRetryTx replays a create's whole closure when its transaction loses
// Dolt's optimistic commit-time merge. The create mints ids into CALLER-OWNED
// objects: issue.ID for an auto-minted id, dependency IssueIDs backfilled to
// that mint, and the content-derived id of a fresh comment. An aborted
// attempt's mint survived in those objects, so a replay treated it as an
// explicit id — no re-mint — and the default INSERT ... ON DUPLICATE KEY
// UPDATE row write then upserted over a concurrent winner that had taken the
// id in between (with dependency edges and comments stranded on it).
//
// The interleaving is forced deterministically instead of by scheduler luck:
// the store-under-test's pool is swapped for one whose first create
// transaction commit is intercepted at the driver boundary. The interception
// performs the real server-side rollback a MySQL 1213 guarantees, commits the
// competitor's write on the ORIGINAL pool, and only then returns the 1213, so
// withRetryTx replays against a database that already holds the competitor's
// row. No row-lock assumptions, no sleeps.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// createCommitFault intercepts the first armCount transaction commits on a
// pool: each interception rolls the transaction back for real, runs onIntercept
// (the competitor's write, on another pool), and returns MySQL 1213 — the
// rollback-guaranteed serialization error withRetryTx replays on. Later
// commits delegate normally.
type createCommitFault struct {
	mu          sync.Mutex
	armCount    int
	armed       int
	commits     int // successful (delegated) commits observed
	onIntercept func() error
}

func (f *createCommitFault) tryArm() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.armed >= f.armCount {
		return false
	}
	f.armed++
	return true
}

func (f *createCommitFault) recordCommit() {
	f.mu.Lock()
	f.commits++
	f.mu.Unlock()
}

func (f *createCommitFault) counts() (armed, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.armed, f.commits
}

type faultingTx struct {
	inner driver.Tx
	fault *createCommitFault
}

func (t *faultingTx) Commit() error {
	if t.fault.tryArm() {
		// Do what the 1213 we are about to return promises the server already
		// did: roll the transaction back. A 1213 that lied about the rollback
		// would let the replay double-apply; keeping the rollback real makes
		// the replayed database state exactly what production sees.
		if err := t.inner.Rollback(); err != nil {
			return fmt.Errorf("fault-injected create conflict: rollback failed: %w", err)
		}
		// Competitor errors are NOT masked as 1213: surfacing them raw makes
		// withRetryTx refuse permanently so the test fails loudly instead of
		// replaying around a broken fixture.
		if t.fault.onIntercept != nil {
			if err := t.fault.onIntercept(); err != nil {
				return err
			}
		}
		return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction (fault-injected)"}
	}
	if err := t.inner.Commit(); err != nil {
		return err
	}
	t.fault.recordCommit()
	return nil
}

func (t *faultingTx) Rollback() error { return t.inner.Rollback() }

// faultingConnector wraps a driver.Connector (mysql.NewConnector, the same
// construction point the server backend uses) so the connections it hands out
// intercept transaction commits. Everything else forwards untouched, exactly
// like the sqlcount wrapper: optional modern interfaces delegate when the
// inner connection implements them and fall back — or return driver.ErrSkip —
// when it does not, so database/sql behaves as it would against the inner
// driver alone.
type faultingConnector struct {
	inner driver.Connector
	fault *createCommitFault
}

func (c *faultingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &faultingConn{inner: inner, fault: c.fault}, nil
}

func (c *faultingConnector) Driver() driver.Driver { return c.inner.Driver() }

type faultingConn struct {
	inner driver.Conn
	fault *createCommitFault
}

func (c *faultingConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(query)
}

func (c *faultingConn) Close() error { return c.inner.Close() }

//nolint:staticcheck // legacy driver.Conn.Begin, required by the interface; BeginTx below is preferred when available.
func (c *faultingConn) Begin() (driver.Tx, error) {
	tx, err := c.inner.Begin() //nolint:staticcheck
	if err != nil {
		return nil, err
	}
	return &faultingTx{inner: tx, fault: c.fault}, nil
}

func (c *faultingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.inner.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Prepare(query)
}

func (c *faultingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *faultingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *faultingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.inner.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &faultingTx{inner: tx, fault: c.fault}, nil
	}
	return c.Begin()
}

func (c *faultingConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *faultingConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *faultingConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *faultingConn) CheckNamedValue(nv *driver.NamedValue) error {
	if chk, ok := c.inner.(driver.NamedValueChecker); ok {
		return chk.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// armCreateCommitFault swaps store's pool for a faulting one built from the
// store's own connStr (same DSN and session config as the store's pool — see
// openCountedDoltConn for the same construction) and returns the fault, the
// ORIGINAL pool (the competitor's connection and the post-hoc verification
// reader) and an idempotent restore func. Restore is also registered via
// t.Cleanup, so the faulting pool is always closed before the store's own
// cleanup.
func armCreateCommitFault(t *testing.T, store *DoltStore, armCount int, onIntercept func() error) (*createCommitFault, *sql.DB, func()) {
	t.Helper()
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse store connStr: %v", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("build mysql connector: %v", err)
	}
	fault := &createCommitFault{armCount: armCount, onIntercept: onIntercept}
	faultDB := sql.OpenDB(&faultingConnector{inner: connector, fault: fault})
	faultDB.SetMaxOpenConns(2)
	faultDB.SetMaxIdleConns(0)
	faultDB.SetConnMaxIdleTime(time.Second)
	origDB := store.db
	store.db = faultDB
	var once sync.Once
	restore := func() {
		once.Do(func() {
			store.db = origDB
			faultDB.Close()
		})
	}
	t.Cleanup(restore)
	return fault, origDB, restore
}

// createOnPool creates issue through issueops on an independent pool: the
// concurrent winner's session. SkipPrefixValidation matches the singular store
// path so an explicit competitor id (e.g. the aborted mint) is accepted
// verbatim.
func createOnPool(ctx context.Context, db *sql.DB, issue *types.Issue, actor string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := issueops.CreateIssueInTxWithResult(ctx, tx, bc, issue, actor); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// issueOnPool reads one issue (either plane) through the original pool.
func issueOnPool(ctx context.Context, db *sql.DB, id string) (*types.Issue, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return issueops.GetIssueInTx(ctx, tx, id)
}

func countOnPool(ctx context.Context, db *sql.DB, query string, args ...interface{}) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var n int
	err = conn.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// commentIDOnPool reads the stored id of the one comment attached to issueID
// in table (comments or wisp_comments).
func commentIDOnPool(ctx context.Context, db *sql.DB, table, issueID string) (string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var id string
	//nolint:gosec // G201: table is one of two hardcoded routing constants.
	err = conn.QueryRowContext(ctx, fmt.Sprintf("SELECT id FROM %s WHERE issue_id = ?", table), issueID).Scan(&id)
	return id, err
}

// derivedCommentID computes the id issueops.InsertDerivedComment derives for a
// fresh comment from its final owner, author, text and formatted timestamp.
func derivedCommentID(issueID, author, text string, createdAt time.Time) string {
	return rowid.New("comments", 0, rowid.Digest([]sql.NullString{
		{String: issueID, Valid: true},
		{String: author, Valid: true},
		{String: text, Valid: true},
		{String: issueops.FormatAuxTime(createdAt), Valid: true},
	}))
}

// TestCreateIssueReplayRemintsAutoMintedID (AC-1): a singular auto-id
// CreateIssue whose transaction is rolled back before a competitor auto-mints
// the aborted id must, on replay, mint a FRESH id: the competitor keeps its
// row and the caller's different final id names its own row.
func TestCreateIssueReplayRemintsAutoMintedID(t *testing.T) {
	for _, tc := range []struct {
		name           string
		prefixOverride string
	}{
		{name: "configured prefix"},
		{name: "prefix override outside allowed prefixes", prefixOverride: "zzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
				t.Fatalf("set issue_id_mode=counter: %v", err)
			}

			issue := &types.Issue{
				Title:     "singular loser",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if tc.prefixOverride != "" {
				issue.PrefixOverride = tc.prefixOverride
			}

			var staleID string
			var competitorErr error
			// origDB must be declared before the arming statement: the
			// interceptor closure runs inside it.
			var fault *createCommitFault
			var origDB *sql.DB
			fault, origDB, _ = armCreateCommitFault(t, store, 1, func() error {
				// The create's first attempt has minted and written its row;
				// the real rollback above undid it. The competitor now
				// auto-mints under the same (now unoccupied) id deterministically:
				// the counter update was rolled back with the transaction.
				staleID = issue.ID
				competitor := &types.Issue{
					Title:     "concurrent winner",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
				}
				if tc.prefixOverride != "" {
					competitor.PrefixOverride = tc.prefixOverride
				}
				competitorErr = createOnPool(ctx, origDB, competitor, "winner")
				return nil
			})

			if err := store.CreateIssue(ctx, issue, "loser"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			if competitorErr != nil {
				t.Fatalf("concurrent winner create: %v", competitorErr)
			}
			if staleID == "" {
				t.Fatal("fault never observed the aborted mint: interleaving not forced")
			}
			if armed, commits := fault.counts(); armed != 1 || commits != 1 {
				t.Fatalf("fault intercepted %d commits (want 1) and observed %d successful commits (want 1: the replay)", armed, commits)
			}

			winner, err := issueOnPool(ctx, origDB, staleID)
			if err != nil {
				t.Fatalf("read competitor %s: %v", staleID, err)
			}
			if winner.Title != "concurrent winner" {
				t.Errorf("competitor %s was overwritten by the replayed create: title = %q, want %q", staleID, winner.Title, "concurrent winner")
			}
			if issue.ID == staleID {
				t.Fatalf("replay reused the aborted auto-minted id %s instead of re-minting", staleID)
			}
			loser, err := issueOnPool(ctx, origDB, issue.ID)
			if err != nil {
				t.Fatalf("read caller issue %s: %v", issue.ID, err)
			}
			if loser.Title != "singular loser" {
				t.Errorf("caller issue %s title = %q, want %q", issue.ID, loser.Title, "singular loser")
			}
		})
	}
}

// TestCreateIssuesReplayMovesBackfilledDependencyToRemint (AC-2): a durable
// auto-id CreateIssues batch whose transaction rolls back after the row
// write, the dependency backfill and the comment insert, with a competitor
// taking the aborted issue id before the replay. The replay must re-mint the
// issue id, re-backfill the dependency to the NEW id and re-derive the
// comment id from its final owner/content/timestamp — and nothing of the
// caller's may attach to the competitor's row.
func TestCreateIssuesReplayMovesBackfilledDependencyToRemint(t *testing.T) {
	fixedCommentAt, err := time.ParseInLocation("2006-01-02 15:04:05", "2024-05-01 12:00:00", time.UTC)
	if err != nil {
		t.Fatalf("parse fixed comment timestamp: %v", err)
	}

	// issueCount drives the batch shape: 1 takes the per-row path, 2 enables
	// the create batch cache. armCount drives repeated rollbacks: each extra
	// interception rolls one more replay back before the final one commits.
	run := func(t *testing.T, issueCount, armCount int) {
		store, cleanup := setupConcurrentTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		target := &types.Issue{ID: "test-ac2-target", Title: "dep target", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, target, "seeder"); err != nil {
			t.Fatalf("seed dep target: %v", err)
		}

		head := &types.Issue{
			Title:     "batch head loser",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "test-ac2-target",
				Type:        types.DepBlocks,
			}},
			Comments: []*types.Comment{{
				Author:    "author",
				Text:      "head comment",
				CreatedAt: fixedCommentAt,
			}},
		}
		issues := []*types.Issue{head}
		for i := 1; i < issueCount; i++ {
			issues = append(issues, &types.Issue{
				Title: fmt.Sprintf("batch sibling %d", i), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			})
		}

		var staleHeadID string
		var competitorErr error
		competitorDone := false
		// Declared before the arming statement: the interceptor closure runs
		// inside it.
		var fault *createCommitFault
		var origDB *sql.DB
		fault, origDB, _ = armCreateCommitFault(t, store, armCount, func() error {
			// Occupy the aborted id once, on the first interception: after the
			// real rollback the id is free, so the competitor's explicit-id
			// create commits. Later rollbacks replay against it.
			if competitorDone {
				return nil
			}
			competitorDone = true
			staleHeadID = head.ID
			competitorErr = createOnPool(ctx, origDB, &types.Issue{
				ID: staleHeadID, Title: "concurrent winner", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
			}, "winner")
			return nil
		})

		if err := store.CreateIssues(ctx, issues, "loser"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}
		if competitorErr != nil {
			t.Fatalf("concurrent winner create: %v", competitorErr)
		}
		if staleHeadID == "" {
			t.Fatal("fault never observed the aborted mint: interleaving not forced")
		}
		if armed, commits := fault.counts(); armed != armCount || commits != 1 {
			t.Fatalf("fault intercepted %d commits (want %d) and observed %d successful commits (want 1: the final replay)", armed, armCount, commits)
		}

		winner, err := issueOnPool(ctx, origDB, staleHeadID)
		if err != nil {
			t.Fatalf("read competitor %s: %v", staleHeadID, err)
		}
		if winner.Title != "concurrent winner" {
			t.Errorf("competitor %s was overwritten by the replayed batch: title = %q, want %q", staleHeadID, winner.Title, "concurrent winner")
		}
		if head.ID == staleHeadID {
			t.Fatalf("replay reused the aborted auto-minted id %s instead of re-minting", staleHeadID)
		}
		if got := head.Dependencies[0].IssueID; got != head.ID {
			t.Errorf("backfilled dependency IssueID = %q, want re-backfilled to the reminted id %q", got, head.ID)
		}

		// No caller edge or comment may attach to the competitor's row.
		if n, err := countOnPool(ctx, origDB, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", staleHeadID); err != nil {
			t.Fatalf("count competitor edges: %v", err)
		} else if n != 0 {
			t.Errorf("competitor %s carries %d caller edge(s), want none", staleHeadID, n)
		}
		if n, err := countOnPool(ctx, origDB, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", staleHeadID); err != nil {
			t.Fatalf("count competitor comments: %v", err)
		} else if n != 0 {
			t.Errorf("competitor %s carries %d caller comment(s), want none", staleHeadID, n)
		}

		// The edge persists under the reminted id.
		if n, err := countOnPool(ctx, origDB, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", head.ID, "test-ac2-target"); err != nil {
			t.Fatalf("count caller edges: %v", err)
		} else if n != 1 {
			t.Errorf("reminted id %s has %d edge(s) to the target, want 1", head.ID, n)
		}

		// The stored comment id derives from its final owner/content/timestamp
		// (issueops.InsertDerivedComment's derivation).
		wantCommentID := derivedCommentID(head.ID, "author", "head comment", fixedCommentAt)
		if got := head.Comments[0].ID; got != wantCommentID {
			t.Errorf("caller comment id = %q, want re-derived %q", got, wantCommentID)
		}
		storedID, err := commentIDOnPool(ctx, origDB, "comments", head.ID)
		if err != nil {
			t.Fatalf("read stored comment under %s: %v", head.ID, err)
		}
		if storedID != wantCommentID {
			t.Errorf("stored comment id = %q, want %q", storedID, wantCommentID)
		}

		// Siblings name their own rows.
		for i := 1; i < len(issues); i++ {
			got, err := issueOnPool(ctx, origDB, issues[i].ID)
			if err != nil {
				t.Fatalf("read sibling %s: %v", issues[i].ID, err)
			}
			if got.Title != issues[i].Title {
				t.Errorf("sibling %s title = %q, want %q", issues[i].ID, got.Title, issues[i].Title)
			}
		}
	}

	t.Run("one issue", func(t *testing.T) { run(t, 1, 1) })
	t.Run("two issues (batch cache)", func(t *testing.T) { run(t, 2, 1) })
	t.Run("repeated rollback", func(t *testing.T) { run(t, 1, 2) })
}

// TestCreateIssueReplayKeepsCounterAheadOfReplayedID (AC-3): with the counter
// pre-seeded, a rolled-back singular auto-id create with NO competitor
// re-mints on replay (deterministically obtaining its own unoccupied id), and
// the counter must be left AHEAD of the replayed row: the next auto-id create
// gets a distinct id instead of reusing — and overwriting — the replayed one.
func TestCreateIssueReplayKeepsCounterAheadOfReplayedID(t *testing.T) {
	t.Run("counter mode", func(t *testing.T) {
		store, cleanup := setupConcurrentTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set issue_id_mode=counter: %v", err)
		}
		// Seed the counter with a committed row so replay-time allocation is
		// UPDATE-driven. Without a seeded row the next mint re-seeds from the
		// live rows and hides the leak.
		seed := &types.Issue{Title: "counter seed", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, seed, "seeder"); err != nil {
			t.Fatalf("seed counter: %v", err)
		}

		issue := &types.Issue{Title: "replayed create", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		fault, origDB, restore := armCreateCommitFault(t, store, 1, func() error {
			return nil // no competitor: the aborted id stays unoccupied
		})
		if err := store.CreateIssue(ctx, issue, "loser"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		restore()
		if armed, commits := fault.counts(); armed != 1 || commits != 1 {
			t.Fatalf("fault intercepted %d commits (want 1) and observed %d successful commits (want 1: the replay)", armed, commits)
		}
		if issue.ID == "" {
			t.Fatal("replayed create holds no id")
		}

		later := &types.Issue{Title: "later distinct create", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, later, "later"); err != nil {
			t.Fatalf("subsequent CreateIssue: %v", err)
		}
		if later.ID == issue.ID {
			t.Errorf("subsequent auto-id create reused the replayed id %s: the counter was left behind the replayed row", issue.ID)
		}
		for _, want := range []struct{ id, title string }{
			{issue.ID, "replayed create"},
			{later.ID, "later distinct create"},
		} {
			got, err := issueOnPool(ctx, origDB, want.id)
			if err != nil {
				t.Fatalf("read %s: %v", want.id, err)
			}
			if got.Title != want.title {
				t.Errorf("issue %s title = %q, want %q", want.id, got.Title, want.title)
			}
		}
	})

	t.Run("hash mode reuses its own unoccupied id", func(t *testing.T) {
		// No competitor and no counter: the replay legitimately re-derives the
		// SAME hash id, because the aborted id is unoccupied. Guards against a
		// fix that forces a different id per attempt.
		store, cleanup := setupConcurrentTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		issue := &types.Issue{Title: "hash replay", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		var staleID string
		fault, origDB, restore := armCreateCommitFault(t, store, 1, func() error {
			staleID = issue.ID
			return nil
		})
		if err := store.CreateIssue(ctx, issue, "loser"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		restore()
		if armed, commits := fault.counts(); armed != 1 || commits != 1 {
			t.Fatalf("fault intercepted %d commits (want 1) and observed %d successful commits (want 1: the replay)", armed, commits)
		}
		if staleID == "" {
			t.Fatal("fault never observed the aborted mint: interleaving not forced")
		}
		if issue.ID != staleID {
			t.Errorf("replay minted %s; the aborted id %s was unoccupied, so the deterministic hash mint legitimately reuses it", issue.ID, staleID)
		}
		got, err := issueOnPool(ctx, origDB, issue.ID)
		if err != nil {
			t.Fatalf("read %s: %v", issue.ID, err)
		}
		if got.Title != "hash replay" {
			t.Errorf("issue %s title = %q, want %q", issue.ID, got.Title, "hash replay")
		}
	})
}
