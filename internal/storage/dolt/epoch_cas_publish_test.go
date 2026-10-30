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

	mysql "github.com/go-sql-driver/mysql"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// These tests pin WHERE and HOW this leg publishes the R20 epoch tables
// (store_epoch, epoch_minted_addresses) to Dolt history
// (gastownhall/beads#6664).
//
// The first group runs on the sequence-capturing driver from
// dolt_commit_ordering_test.go, so it needs no running Dolt server: the
// ordering contract and the staged-table lists are properties of the SQL
// this leg sends, not of Dolt. The tests after them run against a real server:
// the first reproduces bee-ghosttrack's own probe (dolt_status after the first
// mint), the next three apply it to the carrying bump, to a recorded loss and to
// the read-only CurrentAddressFor, and the last three choreograph how a mint
// behaves beside a concurrent bump, beside a concurrent loss, and beside another
// transaction's mint and loss of its own address on that server.

const epochPublishStoreID = "epoch-store"
const epochPublishMintedID = "record-a"

// epochPublishAddress is the address of epochPublishMintedID under epoch 1, the
// one the fake serves as minted. It is minted on a fake of its own rather than
// written by hand: the fake serves a row at any address, so a hand-written one
// would go stale unnoticed if the encoding moved
// (TestEpochCASTestsBuildEveryAddressWithEpochAddress in issueops scans this
// file for one).
func epochPublishAddress() string {
	f := newEpochFake()
	db := sql.OpenDB(f.connector)
	defer func() { _ = db.Close() }()
	address, err := (&DoltStore{db: db}).MintUnderEpoch(context.Background(), epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		panic(fmt.Sprintf("mint %s on a fresh epoch fake: %v", epochPublishMintedID, err))
	}
	return address
}

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
	// stagedReadErr, when set, fails the staged-set read stageAndCommit makes
	// between DOLT_ADD and DOLT_COMMIT, a failure it returns without
	// ErrCommitIndeterminate because nothing has reached Dolt history yet.
	stagedReadErr error
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
		case strings.Contains(query, "FROM dolt_status WHERE staged") && f.stagedReadErr != nil:
			return f.stagedReadErr
		}
		return nil
	}
	f.connector.rows = func(query string) (driver.Rows, bool) {
		switch {
		case strings.Contains(query, "FROM store_epoch"):
			return &valRows{cols: []string{"epoch"}, vals: [][]driver.Value{{f.epoch.Load()}}}, true
		case strings.Contains(query, "FROM epoch_minted_addresses WHERE address"):
			// Every address is already minted, so CurrentAddressFor has an
			// old address to look up and MintUnderEpoch has nothing to insert.
			// A read that asks for the survival columns gets a Live row nothing
			// carried; a read that does not gets the three-column shape.
			if strings.Contains(query, "carried_from") {
				return &valRows{
					cols: []string{"store_id", "minted_id", "minted_epoch", "carried_from", "gone_at_epoch"},
					vals: [][]driver.Value{{epochPublishStoreID, epochPublishMintedID, int64(1), nil, nil}},
				}, true
			}
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
	// staged is the exact set of tables the call stages for its Dolt commit,
	// sorted.
	staged []string
	// logsPublishFailure is true for the call that carries on past a failed Dolt
	// commit; every other call returns it.
	logsPublishFailure bool
}

func epochPublishCalls() []epochPublishCall {
	return []epochPublishCall{
		{
			name:   "BumpEpoch",
			staged: []string{"store_epoch"},
			call: func(ctx context.Context, s *DoltStore) error {
				_, err := s.BumpEpoch(ctx, epochPublishStoreID, "restore")
				return err
			},
		},
		{
			name:   "BumpEpochCarrying",
			staged: []string{"epoch_minted_addresses", "store_epoch"},
			call: func(ctx context.Context, s *DoltStore) error {
				_, err := s.BumpEpochCarrying(ctx, epochPublishStoreID, "token-scheme-change")
				return err
			},
		},
		{
			name:               "MintUnderEpoch",
			staged:             []string{"epoch_minted_addresses", "store_epoch"},
			logsPublishFailure: true,
			call: func(ctx context.Context, s *DoltStore) error {
				_, err := s.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
				return err
			},
		},
		{
			name:   "LoseVersion",
			staged: []string{"epoch_minted_addresses"},
			call: func(ctx context.Context, s *DoltStore) error {
				return s.LoseVersion(ctx, epochPublishStoreID, epochPublishAddress())
			},
		},
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

// TestEpochWritesStageExactlyTheTablesTheyChange is the server-leg half of
// gastownhall/beads#6664 Major 2 (bee-ghosttrack, review 5360880888): the
// first mint on a store lazily inserts store_epoch's singleton
// (ensureStoreEpochRow), so a mint that stages only epoch_minted_addresses
// leaves the epoch row modified and uncommitted, and the Dolt commit holds
// minted rows without the epoch row they depend on. It states the rule for
// every writer, with exact sets: a restore or reinit bump changes only the
// counter, the carrying bump and a mint change both tables, and a recorded loss
// changes only address rows. A writer that stages less leaves rows behind in
// the working set, and one that stages more commits rows it did not write.
func TestEpochWritesStageExactlyTheTablesTheyChange(t *testing.T) {
	for _, tc := range epochPublishCalls() {
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
			slices.Sort(staged)
			staged = slices.Compact(staged)
			if !slices.Equal(staged, tc.staged) {
				t.Errorf("%s staged %v, want exactly %v (Major 2: a table the call writes must be committed with it, and one it does not write must not be)", tc.name, staged, tc.staged)
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

	requirePublicationFailureReported(t, "BumpEpoch", f, err, f.publishErr)
	if epoch != 2 {
		t.Errorf("BumpEpoch returned epoch %d with the publication error, want 2: the bump applied, the error only says its Dolt commit did not", epoch)
	}
}

// requirePublicationFailureReported checks what a writer that reports a failed
// Dolt commit must show once its SQL transaction committed: the transaction was
// committed and never rolled back, and the error it returned wraps the failure
// (cause) and ErrCommitIndeterminate, so that nothing replays a write that has
// already applied.
func requirePublicationFailureReported(t *testing.T, name string, f *epochFake, err, cause error) {
	t.Helper()
	events := f.rec.snapshot()
	if !slices.Contains(events, "COMMIT") || slices.Contains(events, "ROLLBACK") {
		t.Errorf("%s: a failed Dolt commit must not undo the write: want the SQL transaction COMMITTED and never rolled back; events: %v", name, events)
	}
	if err == nil {
		t.Fatalf("%s = nil error although its Dolt commit failed: the caller would report success for a write that is committed but unpublished; events: %v", name, events)
	}
	if !errors.Is(err, cause) {
		t.Errorf("%s error = %v, want it to wrap the Dolt commit failure %v", name, err, cause)
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Errorf("%s error = %v, want it to wrap ErrCommitIndeterminate: the write has applied, and a caller that takes the error for a write that did not would replay it", name, err)
	}
}

// TestBumpEpochCarryingSurfacesAPublicationFailureAfterTheSQLCommit holds the
// carrying bump to the rule BumpEpoch is held to above. It writes the address
// rows of every live Version as well as the counter, and a token-scheme change
// must not report success over a bump that is committed but unpublished. The
// bump applied, so the epoch comes back with the error and the caller must not
// bump again to retry.
func TestBumpEpochCarryingSurfacesAPublicationFailureAfterTheSQLCommit(t *testing.T) {
	f := newEpochFake()
	f.publishErr = errors.New("dolt commit refused")
	db := sql.OpenDB(f.connector)
	defer func() { _ = db.Close() }()

	epoch, err := (&DoltStore{db: db}).BumpEpochCarrying(context.Background(), epochPublishStoreID, "token-scheme-change")

	requirePublicationFailureReported(t, "BumpEpochCarrying", f, err, f.publishErr)
	if epoch != 2 {
		t.Errorf("BumpEpochCarrying returned epoch %d with the publication error, want 2: the bump applied, the error only says its Dolt commit did not", epoch)
	}
}

// TestLoseVersionSurfacesAPublicationFailureAfterTheSQLCommit pins the same rule
// for a recorded loss: a loss its caller believes recorded must not be left
// unpublished. Unlike a mint, a loss is rare and deliberate, so a failed Dolt
// commit is an error to report, never one to log.
func TestLoseVersionSurfacesAPublicationFailureAfterTheSQLCommit(t *testing.T) {
	f := newEpochFake()
	f.publishErr = errors.New("dolt commit refused")
	db := sql.OpenDB(f.connector)
	defer func() { _ = db.Close() }()

	err := (&DoltStore{db: db}).LoseVersion(context.Background(), epochPublishStoreID, epochPublishAddress())

	requirePublicationFailureReported(t, "LoseVersion", f, err, f.publishErr)
}

// TestEpochWritesMarkAFailedStagedReadIndeterminate holds the writers that
// return a failed publication to the no-replay rule for the failure
// stageAndCommit itself returns as a plain error: its staged-set read before
// DOLT_COMMIT. Nothing has reached Dolt history at that point, but the write's
// SQL commit has landed, so a bump replayed on that error would advance the
// counter a second time. The error must say so as a failed DOLT_COMMIT does.
func TestEpochWritesMarkAFailedStagedReadIndeterminate(t *testing.T) {
	for _, tc := range epochPublishCalls() {
		if tc.logsPublishFailure {
			continue // a mint logs a failed publication and returns nil
		}
		t.Run(tc.name, func(t *testing.T) {
			f := newEpochFake()
			f.stagedReadErr = errors.New("staged-set read failed")
			db := sql.OpenDB(f.connector)
			defer func() { _ = db.Close() }()

			err := tc.call(context.Background(), &DoltStore{db: db})

			requirePublicationFailureReported(t, tc.name, f, err, f.stagedReadErr)
		})
	}
}

// TestEpochWritesReturnWhatAnIndeterminateSQLCommitMayHaveApplied pins what each
// writer hands back when its SQL commit's result is lost (ErrCommitIndeterminate,
// which withRetryTx never retries): the commit may have landed, so a bump
// returns the epoch it would have committed and a mint its address, as a bump
// does when only its Dolt commit fails. The caller then reads CurrentEpoch or
// StillServes to learn which; a zero value would read as nothing applied and
// invite the replay the sentinel forbids.
func TestEpochWritesReturnWhatAnIndeterminateSQLCommitMayHaveApplied(t *testing.T) {
	ctx := context.Background()
	open := func(t *testing.T) (*DoltStore, *epochFake) {
		t.Helper()
		f := newEpochFake()
		f.connector.commitErr = func() error { return testConnectionLoss }
		db := sql.OpenDB(f.connector)
		t.Cleanup(func() { _ = db.Close() })
		return &DoltStore{db: db}, f
	}
	requireIndeterminate := func(t *testing.T, name string, f *epochFake, err error) {
		t.Helper()
		if !errors.Is(err, ErrCommitIndeterminate) || !errors.Is(err, testConnectionLoss) {
			t.Fatalf("%s = %v, want the lost commit response wrapped in ErrCommitIndeterminate", name, err)
		}
		if events := f.rec.snapshot(); slices.ContainsFunc(events, func(ev string) bool { return strings.Contains(ev, "DOLT_COMMIT") }) {
			t.Errorf("%s published a SQL commit whose result was lost; events: %v", name, events)
		}
	}

	t.Run("BumpEpoch", func(t *testing.T) {
		s, f := open(t)
		epoch, err := s.BumpEpoch(ctx, epochPublishStoreID, "restore")
		requireIndeterminate(t, "BumpEpoch", f, err)
		if epoch != 2 {
			t.Errorf("BumpEpoch returned epoch %d with an indeterminate commit, want 2, the epoch it may have committed", epoch)
		}
	})
	t.Run("BumpEpochCarrying", func(t *testing.T) {
		s, f := open(t)
		epoch, err := s.BumpEpochCarrying(ctx, epochPublishStoreID, "token-scheme-change")
		requireIndeterminate(t, "BumpEpochCarrying", f, err)
		if epoch != 2 {
			t.Errorf("BumpEpochCarrying returned epoch %d with an indeterminate commit, want 2, the epoch it may have committed", epoch)
		}
	})
	t.Run("MintUnderEpoch", func(t *testing.T) {
		s, f := open(t)
		address, err := s.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
		requireIndeterminate(t, "MintUnderEpoch", f, err)
		if want := epochPublishAddress(); address != want {
			t.Errorf("MintUnderEpoch returned address %q with an indeterminate commit, want %s, the address it may have committed", address, want)
		}
	})
	t.Run("LoseVersion", func(t *testing.T) {
		s, f := open(t)
		requireIndeterminate(t, "LoseVersion", f, s.LoseVersion(ctx, epochPublishStoreID, epochPublishAddress()))
	})
}

// TestEpochWritesReturnNothingWhenTheSQLCommitFailsDefinitely is the other side
// of that rule: a commit error the server answered with (a typed MySQL error,
// which withRetryTx neither retries nor marks indeterminate) means nothing
// landed. Each body ran to the end and computed an epoch or an address, and
// neither may come back with the error.
func TestEpochWritesReturnNothingWhenTheSQLCommitFailsDefinitely(t *testing.T) {
	ctx := context.Background()
	refused := &mysql.MySQLError{Number: 1105, Message: "commit refused"}
	open := func(t *testing.T) *DoltStore {
		t.Helper()
		f := newEpochFake()
		f.connector.commitErr = func() error { return refused }
		db := sql.OpenDB(f.connector)
		t.Cleanup(func() { _ = db.Close() })
		return &DoltStore{db: db}
	}
	requireDefinite := func(t *testing.T, name string, err error) {
		t.Helper()
		if !errors.Is(err, refused) || errors.Is(err, ErrCommitIndeterminate) {
			t.Fatalf("%s = %v, want the refused commit, not marked ErrCommitIndeterminate", name, err)
		}
	}

	t.Run("BumpEpoch", func(t *testing.T) {
		epoch, err := open(t).BumpEpoch(ctx, epochPublishStoreID, "restore")
		requireDefinite(t, "BumpEpoch", err)
		if epoch != 0 {
			t.Errorf("BumpEpoch returned epoch %d with a refused commit, want 0: nothing applied", epoch)
		}
	})
	t.Run("BumpEpochCarrying", func(t *testing.T) {
		epoch, err := open(t).BumpEpochCarrying(ctx, epochPublishStoreID, "token-scheme-change")
		requireDefinite(t, "BumpEpochCarrying", err)
		if epoch != 0 {
			t.Errorf("BumpEpochCarrying returned epoch %d with a refused commit, want 0: nothing applied", epoch)
		}
	})
	t.Run("MintUnderEpoch", func(t *testing.T) {
		address, err := open(t).MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
		requireDefinite(t, "MintUnderEpoch", err)
		if address != "" {
			t.Errorf("MintUnderEpoch returned address %q with a refused commit, want none: nothing applied", address)
		}
	})
}

// TestMintPathsKeepTheirRowsWhenPublicationFails is the other half of that
// rule: swallow-and-log stays acceptable for the frequent, automated mint
// paths, so a failed Dolt commit does not fail a mint whose SQL transaction
// committed (the runIssueOperationTxWithMessage contract: a nil error means
// the data is durable in the working set).
func TestMintPathsKeepTheirRowsWhenPublicationFails(t *testing.T) {
	for _, tc := range epochPublishCalls() {
		if !tc.logsPublishFailure {
			continue // the bumps and the loss surface the failure instead, see above
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

// TestCurrentAddressForWritesAndPublishesNothing pins that CurrentAddressFor is a
// read on this leg: it answers from the address's own row, the carry rows and
// the counter, and sends no INSERT, UPDATE or DELETE and no DOLT_ADD or
// DOLT_COMMIT. The read path opens an ordinary transaction and rolls it back, so
// a write it made would vanish without an error; the statements it sends are the
// only place a stray write shows.
func TestCurrentAddressForWritesAndPublishesNothing(t *testing.T) {
	f := newEpochFake()
	db := sql.OpenDB(f.connector)
	defer func() { _ = db.Close() }()

	address := epochPublishAddress()
	current, err := (&DoltStore{db: db}).CurrentAddressFor(context.Background(), epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("CurrentAddressFor: %v", err)
	}
	if current != address {
		t.Errorf("CurrentAddressFor(%s) = %s, want the address itself: the fake serves it as a Live row nothing carried", address, current)
	}

	for _, ev := range f.rec.snapshot() {
		statement := strings.ToUpper(strings.TrimSpace(ev))
		for _, verb := range []string{"INSERT", "UPDATE", "DELETE", "REPLACE"} {
			if strings.HasPrefix(statement, verb) {
				t.Errorf("CurrentAddressFor sent %q: a lookup must not write", ev)
			}
		}
		if strings.Contains(statement, "DOLT_ADD") || strings.Contains(statement, "DOLT_COMMIT") {
			t.Errorf("CurrentAddressFor sent %q: a lookup has nothing to publish to Dolt history", ev)
		}
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

// TestBumpEpochCarryingLeavesTheEpochTablesCommitted applies the same probe to
// the carrying bump, which writes two tables: the counter, and one carry row per
// live address in epoch_minted_addresses. Publishing only the counter would
// leave those rows modified and uncommitted in the working set.
func TestBumpEpochCarryingLeavesTheEpochTablesCommitted(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()
	store := newMultiConnectionEpochStore(t, ctx, "epoch_carry_published")

	if _, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID); err != nil {
		t.Fatalf("MintUnderEpoch: %v", err)
	}
	newEpoch, err := store.BumpEpochCarrying(ctx, epochPublishStoreID, "token-scheme-change")
	if err != nil {
		t.Fatalf("BumpEpochCarrying: %v", err)
	}

	// The carry row is what makes the publication more than the counter: without
	// it this test would pass for a bump that wrote nothing but store_epoch.
	var carryRows int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM epoch_minted_addresses WHERE minted_id = ? AND minted_epoch = ?",
		epochPublishMintedID, newEpoch,
	).Scan(&carryRows); err != nil {
		t.Fatalf("count the carry rows: %v", err)
	}
	if carryRows != 1 {
		t.Fatalf("epoch_minted_addresses holds %d rows for %s at epoch %d after the carrying bump, want exactly 1 carry row", carryRows, epochPublishMintedID, newEpoch)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after the carrying bump the working set still holds %v: its Dolt commit must include the carry rows", dirty)
	}
}

// TestLoseVersionLeavesTheEpochTablesCommitted applies the probe to a recorded
// loss, which changes only epoch_minted_addresses: the row it marks must be in
// the Dolt commit, not left modified in the working set.
func TestLoseVersionLeavesTheEpochTablesCommitted(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()
	store := newMultiConnectionEpochStore(t, ctx, "epoch_loss_published")

	address, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("MintUnderEpoch: %v", err)
	}
	if err := store.LoseVersion(ctx, epochPublishStoreID, address); err != nil {
		t.Fatalf("LoseVersion: %v", err)
	}
	serves, err := store.StillServes(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServes: %v", err)
	}
	if serves {
		t.Fatalf("StillServes(%s) = true after LoseVersion, want false: the loss must have taken effect for the working-set check below to mean anything", address)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after LoseVersion the working set still holds %v: its Dolt commit must include the row it marked", dirty)
	}
}

// TestCurrentAddressForLeavesAnAbsentStoreEpochRowAbsent is what remains of the
// check that CurrentAddressFor committed a re-seeded singleton row. A store
// whose singleton row is absent is legitimate state — migration 0067 starts the
// table empty and treats "no row" as epoch 1 — and CurrentAddressFor used to
// re-seed it through ensureStoreEpochRow exactly as a first mint does. It only
// reads now: it answers from that default, leaves the singleton absent, and
// leaves nothing in the working set to commit.
func TestCurrentAddressForLeavesAnAbsentStoreEpochRowAbsent(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()
	store := newMultiConnectionEpochStore(t, ctx, "epoch_lookup_absent_row")

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

	current, err := store.CurrentAddressFor(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("CurrentAddressFor: %v", err)
	}
	if current != address {
		t.Errorf("CurrentAddressFor(%s) = %s, want the address itself: nothing carried it, and a lookup must not mint a replacement", address, current)
	}

	var singletonRows int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM store_epoch").Scan(&singletonRows); err != nil {
		t.Fatalf("count the store_epoch rows: %v", err)
	}
	if singletonRows != 0 {
		t.Errorf("store_epoch holds %d rows after CurrentAddressFor, want 0: a lookup must not re-seed the singleton", singletonRows)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after CurrentAddressFor the working set still holds %v: a lookup must leave nothing to commit", dirty)
	}
}

// newMultiConnectionEpochStore opens a store on its own database with several
// connections to one branch, as in production server mode. setupTestStore pins
// a single connection, which cannot overlap two transactions. A store of its own
// also never queues on the shared test database's schema-init lock, whose
// five-second acquire timeout runs out when many parallel tests set up at once
// on a loaded host, so the publication probes that need a database of their own
// for no other reason use it too.
func newMultiConnectionEpochStore(t *testing.T, ctx context.Context, name string) *DoltStore {
	t.Helper()
	store, err := New(ctx, &Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        fmt.Sprintf("%s_%d", name, time.Now().UnixNano()),
		MaxOpenConns:    4,
		CreateIfMissing: true,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestMintOverlappingABumpLeavesALiveSurvivor measures, on a real server, an
// outcome bee-ghosttrack marked "Unverified" (gastownhall/beads#6664, review
// 5360880888): a mint whose transaction overlaps a BumpEpoch reads the epoch
// before the bump, does not conflict with it (the two write different tables),
// and commits an address minted under the epoch the bump has just replaced.
//
// That address is a Live survivor, and that is the right answer. A restore or
// destructive-reinit bump writes no address rows: it advances only the counter,
// and an address's status is read from its own row, which says only that it was
// minted in an epoch that has happened. So an address minted before the bump is
// still served after it, however the two interleave, exactly as if the two
// calls had run back to back. (Before survival was recorded, this interleaving
// committed an address that already read GoneReorganization, because the bump
// voided every address nobody minted again; that outcome no longer exists.)
//
// The engine does not prevent the overlap, and this test depends on that.
// Measured: SELECT ... FOR UPDATE and LOCK IN SHARE MODE take no lock, and a
// write that changes no value (UPDATE store_epoch SET epoch = epoch matches 0
// rows) is not a write to the merge. Only a write that CHANGES a cell the bump
// also changes conflicts (Error 1213), and a mint has no cell of its own to
// change. That is also why the token-scheme-change bump, the one bump that reads
// address rows, needs its caller to hold the workspace gate exclusively, as a
// restore does: a mint cannot be made to conflict with it.
//
// If this test fails because the bump timed out or the mint's commit
// conflicted, the engine now prevents the overlap: revisit that requirement and
// this test together.
func TestMintOverlappingABumpLeavesALiveSurvivor(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, "epoch_overlap")

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
	bumpedEpoch, err := store.BumpEpoch(bumpCtx, epochPublishStoreID, "restore")
	if err != nil {
		t.Fatalf("BumpEpoch beside an open mint transaction: %v: the engine now holds the bump back, so the overlap this test documents is prevented", err)
	}
	if err := mintTx.Commit(); err != nil {
		t.Fatalf("mint commit after a concurrent bump: %v: the engine now conflicts the mint, so the overlap this test documents is prevented", err)
	}

	serves, err := store.StillServes(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServes: %v", err)
	}
	if !serves {
		t.Errorf("address %s, minted before a bump that committed first, is no longer served: a restore or reinit bump writes no address rows, so the address must survive it", address)
	}
	answer, err := store.Resolve(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if answer.Restriction != storeops.EpochRestrictionLive {
		t.Errorf("Resolve(%s) = %v, want EpochRestrictionLive: the address survived the bump", address, answer.Restriction)
	}
	if answer.Epoch == nil {
		t.Errorf("Resolve(%s).Epoch = nil, want the current epoch %d", address, bumpedEpoch)
	} else if *answer.Epoch != bumpedEpoch {
		t.Errorf("Resolve(%s).Epoch = %d, want the current epoch %d", address, *answer.Epoch, bumpedEpoch)
	}
}

// TestAMintAfterAConcurrentLossNeverRevivesTheLostAddress choreographs, with no
// timing in it, the interleaving that could bring a lost address back: one
// transaction reads the address Live, another commits the loss of it, and the
// first then mints the same id and commits.
//
// The first transaction read the address before the loss committed, so its
// snapshot holds a Live row and the mint finds nothing to do. A mint never
// writes an existing row (it inserts a root row for a new address and otherwise
// touches nothing), so the first transaction commits no write at all and has
// nothing to conflict with the loss. The loss is the only change to the row, so
// the address ends Gone: no interleaving of a mint and a loss may revive it.
//
// If this test fails because the loss was held back, or because the first
// transaction saw the loss, the engine changed the premise (an open transaction
// reads its own snapshot, and a commit that wrote nothing conflicts with
// nothing) and the interleaving needs another look.
func TestAMintAfterAConcurrentLossNeverRevivesTheLostAddress(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, "epoch_loss_overlap")

	address, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("seed mint: %v", err)
	}

	// The first transaction reads the address Live and stays open.
	mintTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the first transaction: %v", err)
	}
	defer func() { _ = mintTx.Rollback() }()
	serves, err := storeops.StillServesInTx(ctx, mintTx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServesInTx inside the first transaction: %v", err)
	}
	if !serves {
		t.Fatalf("address %s reads as not served inside the first transaction before any loss: the seed mint did not leave it Live", address)
	}

	// The loss commits on another connection while the first transaction is open.
	lossCtx, cancelLoss := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLoss()
	if err := store.LoseVersion(lossCtx, epochPublishStoreID, address); err != nil {
		t.Fatalf("LoseVersion beside an open transaction: %v (if the engine now holds the loss back, the interleaving this test choreographs is prevented)", err)
	}

	// The first transaction now mints the same id, which is the same address.
	minted, err := storeops.MintUnderEpochInTx(ctx, mintTx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("mint inside the first transaction after the loss committed: %v: the transaction should still read the address Live from its own snapshot, and if it now sees the loss this test no longer exercises the interleaving it choreographs", err)
	}
	if minted != address {
		t.Fatalf("mint returned %s, want the same address %s: minting the same id under the same epoch reproduces its address", minted, address)
	}
	if err := mintTx.Commit(); err != nil {
		t.Fatalf("commit of the first transaction: %v: a mint must not write an existing row, so it has nothing to conflict with the loss", err)
	}

	serves, err = store.StillServes(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServes: %v", err)
	}
	if serves {
		t.Errorf("address %s is served again after a mint overlapped its loss: no interleaving may revive a lost address", address)
	}
	answer, err := store.Resolve(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if answer.Restriction != storeops.EpochRestrictionGoneReorganization {
		t.Errorf("Resolve(%s) = %v, want EpochRestrictionGoneReorganization: the loss is the only change to the row", address, answer.Restriction)
	}
}

// TestAMintRacingTheMintAndLossOfItsAddressConflictsAtCommit measures what
// upsertEpochMintedAddressInTx says about Dolt. One transaction reads before
// the address exists; another mints the address and loses it, and both commit;
// the first then mints the same id. Its snapshot holds no row at the address,
// so it inserts one, and the INSERT does not fail as a duplicate key: Dolt
// checks the key against the transaction's snapshot. The two inserts conflict at
// COMMIT instead (Error 1213), which withRetryTx answers by running the mint
// again, and run again against the committed row, the mint refuses the address
// (ErrEpochAddressNotServed) rather than reviving a lost one.
//
// If the first transaction's INSERT fails as a duplicate key, the engine now
// checks keys past the snapshot, and the re-read in
// upsertEpochMintedAddressInTx answers this race instead of the commit; if the
// commit succeeds, the engine merged the two rows, and the race needs another
// look.
func TestAMintRacingTheMintAndLossOfItsAddressConflictsAtCommit(t *testing.T) {
	skipIfNoServer(t)
	ctx, cancel := testContext(t)
	defer cancel()

	store := newMultiConnectionEpochStore(t, ctx, "epoch_mint_race")

	// Seed the singleton so the first transaction's mint only reads the epoch.
	if _, err := store.MintUnderEpoch(ctx, epochPublishStoreID, "seed"); err != nil {
		t.Fatalf("seed mint: %v", err)
	}

	// The first transaction reads before the address exists and stays open.
	mintTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the first transaction: %v", err)
	}
	defer func() { _ = mintTx.Rollback() }()
	if _, err := storeops.CurrentEpochInTx(ctx, mintTx, epochPublishStoreID); err != nil {
		t.Fatalf("CurrentEpochInTx inside the first transaction: %v", err)
	}

	// Another connection mints the address and loses it while the first is open.
	otherCtx, cancelOther := context.WithTimeout(ctx, 10*time.Second)
	defer cancelOther()
	address, err := store.MintUnderEpoch(otherCtx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("MintUnderEpoch beside an open transaction: %v", err)
	}
	if err := store.LoseVersion(otherCtx, epochPublishStoreID, address); err != nil {
		t.Fatalf("LoseVersion beside an open transaction: %v", err)
	}

	// The first transaction mints the same id, which is the same address.
	minted, err := storeops.MintUnderEpochInTx(ctx, mintTx, epochPublishStoreID, epochPublishMintedID)
	if err != nil {
		t.Fatalf("mint inside the first transaction: %v: the INSERT now sees a row committed after the transaction's snapshot, so the duplicate-key re-read answers this race, not the commit", err)
	}
	if minted != address {
		t.Fatalf("mint returned %s, want the same address %s: minting the same id under the same epoch reproduces its address", minted, address)
	}
	err = mintTx.Commit()
	if err == nil {
		t.Fatalf("the first transaction committed its insert of %s over the committed, lost row: the engine merged the two inserts, and a lost address may come back", address)
	}
	if !isSerializationError(err) {
		t.Fatalf("commit of the first transaction = %v, want a serialization failure (Error 1213), the conflict withRetryTx runs the mint again on", err)
	}

	// Run again, as withRetryTx runs it, the mint reads the committed row.
	if _, err := store.MintUnderEpoch(ctx, epochPublishStoreID, epochPublishMintedID); !errors.Is(err, storeops.ErrEpochAddressNotServed) {
		t.Errorf("mint of %s run again after the conflict = %v, want ErrEpochAddressNotServed: the committed row is lost", epochPublishMintedID, err)
	}
	serves, err := store.StillServes(ctx, epochPublishStoreID, address)
	if err != nil {
		t.Fatalf("StillServes: %v", err)
	}
	if serves {
		t.Errorf("address %s is served again after a mint raced its mint and loss: no interleaving may revive a lost address", address)
	}
}
