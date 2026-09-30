package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// These tests pin WHERE and HOW this leg publishes the R20 epoch tables
// (store_epoch, epoch_minted_addresses) to Dolt history
// (gastownhall/beads#6664).
//
// The first group runs on the sequence-capturing driver from
// dolt_commit_ordering_test.go, so it needs no running Dolt server: the
// ordering contract and the staged-table lists are properties of the SQL
// this leg sends, not of Dolt. The next two tests reproduce bee-ghosttrack's
// own probe against a real server (dolt_status after the first mint), and the
// last documents how a mint behaves beside a concurrent bump on that server.

const epochPublishStoreID = "epoch-store"
const epochPublishMintedID = "record-a"

// epochFake is a *DoltStore's driver for the epoch methods: it serves the
// reads the shared epoch Tx functions make, keeps the singleton's epoch moving
// when the bump's UPDATE runs, and can refuse every DOLT_COMMIT.
type epochFake struct {
	rec       *seqRecorder
	connector *seqConnector
	epoch     atomic.Int64
	// publishErr, when set before the call under test, fails every
	// CALL DOLT_COMMIT: the SQL transaction is fine, only publication is not.
	publishErr error
}

func newEpochFake() *epochFake {
	f := &epochFake{rec: &seqRecorder{}}
	f.epoch.Store(1)
	f.connector = &seqConnector{rec: f.rec}
	f.connector.queryErr = func(query string) error {
		switch {
		case strings.Contains(query, "UPDATE store_epoch"):
			f.epoch.Add(1)
		case strings.Contains(query, "DOLT_COMMIT") && f.publishErr != nil:
			return f.publishErr
		}
		return nil
	}
	f.connector.rows = func(query string) (driver.Rows, bool) {
		switch {
		case strings.Contains(query, "FROM store_epoch"):
			return &valRows{cols: []string{"epoch"}, vals: [][]driver.Value{{f.epoch.Load()}}}, true
		case strings.Contains(query, "FROM epoch_minted_addresses WHERE address"):
			// Every address is already minted, so CurrentAddressFor has an
			// old address to re-mint and MintUnderEpoch has nothing to insert.
			return &valRows{
				cols: []string{"store_id", "minted_id", "minted_epoch"},
				vals: [][]driver.Value{{epochPublishStoreID, epochPublishMintedID, int64(1)}},
			}, true
		}
		return nil, false
	}
	return f
}

// epochArgConnector is the sequence-capturing connector plus the table each
// DOLT_ADD stages, which the sequence recorder drops.
type epochArgConnector struct{ *seqConnector }

func (c epochArgConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.seqConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &epochArgConn{seqConn: conn.(*seqConn)}, nil
}

type epochArgConn struct{ *seqConn }

func (c *epochArgConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "DOLT_ADD") && len(args) == 1 {
		c.parent.rec.add(c.id, fmt.Sprintf("STAGE %v", args[0].Value))
	}
	return c.seqConn.QueryContext(ctx, query, args)
}

// epochPublishCall is one epoch method that writes.
type epochPublishCall struct {
	name string
	call func(ctx context.Context, s *DoltStore) error
}

func epochPublishCalls() []epochPublishCall {
	return []epochPublishCall{
		{"BumpEpoch", func(ctx context.Context, s *DoltStore) error {
			_, err := s.BumpEpoch(ctx, epochPublishStoreID, "restore")
			return err
		}},
		{"MintUnderEpoch", func(ctx context.Context, s *DoltStore) error {
			_, err := s.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
			return err
		}},
		{"CurrentAddressFor", func(ctx context.Context, s *DoltStore) error {
			_, err := s.CurrentAddressFor(ctx, epochPublishStoreID, "epch:11:epoch-store:8:record-a:1")
			return err
		}},
	}
}

func epochPublishIndex(events []string, pred func(string) bool) int {
	for i, ev := range events {
		if pred(ev) {
			return i
		}
	}
	return -1
}

// TestEpochWritesPublishToDoltOnlyAfterTheSQLCommit is the ordering contract
// of dolt_commit_ordering_test.go applied to the epoch methods. Staging and
// Dolt-committing store_epoch or epoch_minted_addresses inside the still-open
// SQL transaction is the lost-update hazard documented on doltAddAndCommitInTx
// (LatentLabsSpace/NEXUS#92), and it costs more here than on issues: both
// tables are append-only, so a row reverted to its BEGIN-time value is never
// rewritten.
func TestEpochWritesPublishToDoltOnlyAfterTheSQLCommit(t *testing.T) {
	for _, tc := range epochPublishCalls() {
		t.Run(tc.name, func(t *testing.T) {
			f := newEpochFake()
			db := sql.OpenDB(f.connector)
			defer func() { _ = db.Close() }()

			if err := tc.call(context.Background(), &DoltStore{db: db}); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			events := f.rec.snapshot()
			commitIdx := epochPublishIndex(events, func(ev string) bool { return ev == "COMMIT" })
			addIdx := epochPublishIndex(events, func(ev string) bool { return strings.Contains(ev, "DOLT_ADD") })
			doltCommitIdx := epochPublishIndex(events, func(ev string) bool { return strings.Contains(ev, "DOLT_COMMIT") })
			if commitIdx == -1 || addIdx == -1 || doltCommitIdx == -1 {
				t.Fatalf("%s: want a SQL COMMIT, a DOLT_ADD and a DOLT_COMMIT; events: %v", tc.name, events)
			}
			if addIdx < commitIdx {
				t.Errorf("%s: DOLT_ADD ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering hazard; events: %v",
					tc.name, addIdx, commitIdx, events)
			}
			if doltCommitIdx < commitIdx {
				t.Errorf("%s: DOLT_COMMIT ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering hazard; events: %v",
					tc.name, doltCommitIdx, commitIdx, events)
			}
		})
	}
}

// TestEpochMintPathsStageStoreEpochWithTheMintedRows is the server-leg half of
// gastownhall/beads#6664 Major 2 (bee-ghosttrack, review 5360880888): the
// first mint on a store lazily inserts store_epoch's singleton
// (ensureStoreEpochRow), so a mint that stages only epoch_minted_addresses
// leaves the epoch row modified and uncommitted, and the Dolt commit holds
// minted rows without the epoch row they depend on.
func TestEpochMintPathsStageStoreEpochWithTheMintedRows(t *testing.T) {
	for _, tc := range epochPublishCalls() {
		if tc.name == "BumpEpoch" {
			continue // stages store_epoch already; only the mint paths omitted it
		}
		t.Run(tc.name, func(t *testing.T) {
			f := newEpochFake()
			db := sql.OpenDB(epochArgConnector{f.connector})
			defer func() { _ = db.Close() }()

			if err := tc.call(context.Background(), &DoltStore{db: db}); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			var staged []string
			for _, ev := range f.rec.snapshot() {
				if table, ok := strings.CutPrefix(ev, "STAGE "); ok {
					staged = append(staged, table)
				}
			}
			for _, want := range []string{"epoch_minted_addresses", "store_epoch"} {
				if !slices.Contains(staged, want) {
					t.Errorf("%s staged %v, want %q among them (Major 2: the singleton row a first mint inserts must be committed with the rows that depend on it)", tc.name, staged, want)
				}
			}
		})
	}
}

// TestBumpEpochSurfacesAPublicationFailureAfterTheSQLCommit pins how a bump
// reports a failed publication: a bump whose SQL transaction
// committed but whose Dolt commit failed must tell its caller, because a
// restore trigger must not report a restore complete over an unpublished
// bump. The bump itself must survive: it is durable in the working set, so a
// failed history commit is an error to report, never a reason to roll back.
func TestBumpEpochSurfacesAPublicationFailureAfterTheSQLCommit(t *testing.T) {
	f := newEpochFake()
	f.publishErr = errors.New("dolt commit refused")
	db := sql.OpenDB(f.connector)
	defer func() { _ = db.Close() }()

	epoch, err := (&DoltStore{db: db}).BumpEpoch(context.Background(), epochPublishStoreID, "restore")

	events := f.rec.snapshot()
	if !slices.Contains(events, "COMMIT") || slices.Contains(events, "ROLLBACK") {
		t.Errorf("a failed Dolt commit must not undo the bump: want the SQL transaction COMMITTED and never rolled back; events: %v", events)
	}
	if err == nil {
		t.Fatalf("BumpEpoch = nil error although its Dolt commit failed: the caller would report success for a bump that is committed but unpublished; events: %v", events)
	}
	if !errors.Is(err, f.publishErr) {
		t.Errorf("BumpEpoch error = %v, want it to wrap the Dolt commit failure %v", err, f.publishErr)
	}
	if epoch != 2 {
		t.Errorf("BumpEpoch returned epoch %d with the publication error, want 2: the bump applied, the error only says its Dolt commit did not", epoch)
	}
}

// TestMintPathsKeepTheirRowsWhenPublicationFails is the other half of that
// rule: swallow-and-log stays acceptable for the frequent, automated mint
// paths, so a failed Dolt commit does not fail a mint whose SQL transaction
// committed (the runIssueOperationTxWithMessage contract: a nil error means
// the data is durable in the working set).
func TestMintPathsKeepTheirRowsWhenPublicationFails(t *testing.T) {
	for _, tc := range epochPublishCalls() {
		if tc.name == "BumpEpoch" {
			continue // Bump surfaces the failure instead, see above
		}
		t.Run(tc.name, func(t *testing.T) {
			f := newEpochFake()
			f.publishErr = errors.New("dolt commit refused")
			db := sql.OpenDB(f.connector)
			defer func() { _ = db.Close() }()

			err := tc.call(context.Background(), &DoltStore{db: db})

			events := f.rec.snapshot()
			if !slices.Contains(events, "COMMIT") || slices.Contains(events, "ROLLBACK") {
				t.Errorf("%s: a failed Dolt commit must not roll the mint back: want the SQL transaction COMMITTED; events: %v", tc.name, events)
			}
			if err != nil {
				t.Errorf("%s = %v, want nil: the mint's data is durable, so a failed history commit is logged, not returned", tc.name, err)
			}
		})
	}
}

// epochTablesInWorkingSet lists the R20 epoch tables that still appear in
// dolt_status, staged or not: after a write has been published, neither may.
func epochTablesInWorkingSet(status *DoltStatus) []string {
	var dirty []string
	for _, entry := range append(slices.Clone(status.Staged), status.Unstaged...) {
		if entry.Table == "store_epoch" || entry.Table == "epoch_minted_addresses" {
			dirty = append(dirty, fmt.Sprintf("%s (%s)", entry.Table, entry.Status))
		}
	}
	return dirty
}

// TestFirstMintLeavesTheEpochTablesCommitted reproduces bee-ghosttrack's probe
// for gastownhall/beads#6664 Major 2 on a real server: Status() after the
// first mint on a fresh store read unstaged=[{store_epoch modified}].
func TestFirstMintLeavesTheEpochTablesCommitted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// A fresh branch: store_epoch has no row, so this mint is what seeds it.
	if _, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID); err != nil {
		t.Fatalf("MintUnderEpoch on a fresh store: %v", err)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after the first mint the working set still holds %v: the mint's Dolt commit must include the singleton row it seeded", dirty)
	}
}

// TestCurrentAddressForCommitsAReseededStoreEpochRow covers the same omission
// on the CurrentAddressFor path, which the first-mint probe cannot reach (the
// mint that minted the old address already seeded the row). A store whose
// singleton row is absent is legitimate state — migration 0067 starts the
// table empty and treats "no row" as epoch 1 — and CurrentAddressFor re-seeds
// it through ensureStoreEpochRow exactly as a first mint does.
func TestCurrentAddressForCommitsAReseededStoreEpochRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	address, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("MintUnderEpoch: %v", err)
	}
	// Settle everything so far, then leave the singleton row absent with a
	// clean working set.
	if _, err := store.CommitAll(ctx, "test: settle the first mint"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM store_epoch"); err != nil {
		t.Fatalf("delete the singleton row: %v", err)
	}
	if _, err := store.CommitAll(ctx, "test: absent singleton row"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	if _, err := store.CurrentAddressFor(ctx, epochPublishStoreID, address); err != nil {
		t.Fatalf("CurrentAddressFor: %v", err)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after CurrentAddressFor re-seeded the singleton the working set still holds %v: this path must commit store_epoch too", dirty)
	}
}

// TestMintOverlappingABumpCommitsAnAddressThatIsAlreadyGone documents an
// outcome bee-ghosttrack marked "Unverified" (gastownhall/beads#6664, review
// 5360880888), here measured on a real server: a
// mint whose transaction overlaps a BumpEpoch reads the epoch before the bump,
// does not conflict with it (the two write different tables), and commits an
// address that already reads GoneReorganization.
//
// It is left as it is, on purpose. It is a valid serial order (the mint ran
// before the bump, so the caller holds an address the bump then voided, exactly
// as if the two calls had run back to back) and it is recoverable
// (CurrentAddressFor). Preventing it needs the mint to conflict with a
// concurrent bump, and Dolt has no cheap way to do that. Measured: SELECT ...
// FOR UPDATE and LOCK IN SHARE MODE take no lock, and a write that changes no
// value (UPDATE store_epoch SET epoch = epoch matches 0 rows) is not a write to
// the merge. Only a write that CHANGES a cell the bump also changes conflicts
// (Error 1213), and the mint has no cell of its own to change: it would have to
// overwrite bumped_at or bumped_reason, and every mint would then conflict with
// every other mint on the shared row (the footprint version_history.go avoids
// where it reads store_epoch). A guarantee needs a schema-level mechanism,
// which is a design decision beyond this change.
//
// If this test fails because the bump timed out or the mint's commit
// conflicted, the engine now prevents the overlap: close the gap in
// MintUnderEpoch and delete this test.
func TestMintOverlappingABumpCommitsAnAddressThatIsAlreadyGone(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	// Several sessions on one branch, as in production server mode:
	// setupTestStore pins a single connection, which cannot overlap.
	store, err := New(ctx, &Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        fmt.Sprintf("epoch_overlap_%d", time.Now().UnixNano()),
		MaxOpenConns:    4,
		CreateIfMissing: true,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed the singleton so the mint below only reads the epoch.
	if _, err := store.MintUnderEpoch(ctx, epochPublishStoreID, "seed"); err != nil {
		t.Fatalf("seed mint: %v", err)
	}

	mintTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin mint transaction: %v", err)
	}
	defer func() { _ = mintTx.Rollback() }()
	address, err := storeops.MintUnderEpochInTx(ctx, mintTx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("mint inside the open transaction: %v", err)
	}

	// The bump runs on another connection while the mint transaction is open.
	bumpCtx, cancelBump := context.WithTimeout(ctx, 10*time.Second)
	defer cancelBump()
	if _, err := store.BumpEpoch(bumpCtx, epochPublishStoreID, "restore"); err != nil {
		t.Fatalf("BumpEpoch beside an open mint transaction: %v: the engine now holds the bump back, so the overlap this test documents is prevented", err)
	}
	if err := mintTx.Commit(); err != nil {
		t.Fatalf("mint commit after a concurrent bump: %v: the engine now conflicts the mint, so the overlap this test documents is prevented", err)
	}

	serves, err := store.StillServes(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServes: %v", err)
	}
	if serves {
		t.Errorf("address %s, minted before a bump that committed first, is still served: the documented overlap outcome changed", address)
	}
}
