package eventsjournal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// fakeRunner is one plumbing's worth of maintenance transactions, backed by
// sqlmock so the driver runs the real SQL against recorded results. It counts
// transactions because the throttle's promise is about COST: a pass that is not
// due must be one query, and one only.
type fakeRunner struct {
	db      *sql.DB
	mu      sync.Mutex
	txCount int
	err     error
}

func (r *fakeRunner) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, issueops.DBTX) error) error {
	r.mu.Lock()
	r.txCount++
	failWith := r.err
	r.mu.Unlock()
	if failWith != nil {
		return failWith
	}
	return fn(ctx, r.db)
}

func (r *fakeRunner) transactions() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.txCount
}

func newFakeRunner(t *testing.T) (*fakeRunner, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &fakeRunner{db: db}, mock
}

func expectThrottleRead(mock sqlmock.Sqlmock, watermark any, head int64) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT (SELECT value FROM local_metadata")).
		WithArgs(AutoPruneSlotKey).
		WillReturnRows(sqlmock.NewRows([]string{"value", "head"}).AddRow(watermark, head))
}

func watermarkAt(t *testing.T, ts time.Time, head int64) string {
	t.Helper()
	raw, err := json.Marshal(autoPruneState{TS: ts, Head: head})
	if err != nil {
		t.Fatalf("marshal watermark: %v", err)
	}
	return string(raw)
}

// TestAutoPruneCostsOneQueryWhenNothingIsDue is the throttle's whole point. This
// runs after every journaled mutation, so the not-due answer has to be a single
// indexed read — no counter round trip, no floor resolution, and above all no
// delete transaction.
func TestAutoPruneCostsOneQueryWhenNothingIsDue(t *testing.T) {
	runner, mock := newFakeRunner(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	expectThrottleRead(mock, watermarkAt(t, now.Add(-5*time.Minute), 900), int64(1000))

	deleted, err := AutoPrune(context.Background(), runner, AutoPruneOptions{
		RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000,
	})
	if err != nil {
		t.Fatalf("auto-prune: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d rows when nothing was due, want 0", deleted)
	}
	if got := runner.transactions(); got != 1 {
		t.Errorf("opened %d maintenance transactions for a not-due check, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneRunsNothingWhenBothFloorsAreDisabled: not one read. An operator
// who disabled both floors asked for an unbounded ledger, and maintenance must
// not even ask whether it is due.
func TestAutoPruneRunsNothingWhenBothFloorsAreDisabled(t *testing.T) {
	runner, mock := newFakeRunner(t)

	deleted, err := AutoPrune(context.Background(), runner, AutoPruneOptions{})
	if err != nil {
		t.Fatalf("auto-prune: %v", err)
	}
	if deleted != 0 || runner.transactions() != 0 {
		t.Fatalf("deleted=%d transactions=%d, want a pure no-op", deleted, runner.transactions())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneDueTriggers covers both halves of "due": the interval elapsing,
// and the journal advancing past the volume threshold while the interval has
// not. The second is what keeps a bulk import bounded — an hour of a million-row
// import would otherwise be entirely unmaintained.
func TestAutoPruneDueTriggers(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		watermark any
		head      int64
		wantDue   bool
	}{
		{name: "never pruned here", watermark: nil, head: 10, wantDue: true},
		{name: "unreadable watermark self-heals", watermark: "{not json", head: 10, wantDue: true},
		{name: "interval elapsed", watermark: watermarkAt(t, now.Add(-2*time.Hour), 10), head: 11, wantDue: true},
		{name: "volume threshold crossed", watermark: watermarkAt(t, now.Add(-time.Minute), 10), head: 5011, wantDue: true},
		{name: "neither", watermark: watermarkAt(t, now.Add(-time.Minute), 10), head: 4000, wantDue: false},
		// A clock that stepped FORWARD and was then corrected leaves a stamp in
		// the future. Compared naively, `now - stamp` stays negative and the
		// interval trigger never fires again — retention stops for good on a
		// workspace that looks healthy. Small skew still throttles.
		{name: "watermark stamped far in the future", watermark: watermarkAt(t, now.Add(72*time.Hour), 10), head: 11, wantDue: true},
		{name: "watermark barely ahead still throttles", watermark: watermarkAt(t, now.Add(time.Second), 10), head: 11, wantDue: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := AutoPruneOptions{RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000}
			watermark := ""
			if s, ok := tc.watermark.(string); ok {
				watermark = s
			}
			if got := autoPruneDue(watermark, tc.head, opts); got != tc.wantDue {
				t.Errorf("due = %v, want %v", got, tc.wantDue)
			}
		})
	}
}

// TestAutoPruneStampsTheWatermarkBeforeDeleting. A pass that stamps only on
// success turns a persistently failing prune — a locked table, a full disk —
// into a retry on every single command. Maintenance that cannot succeed has to
// degrade to no maintenance.
func TestAutoPruneStampsTheWatermarkBeforeDeleting(t *testing.T) {
	runner, mock := newFakeRunner(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	expectThrottleRead(mock, nil, int64(1000))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WithArgs(AutoPruneSlotKey, watermarkAt(t, now, 1000)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(1000))
	mock.ExpectQuery(regexp.QuoteMeta(issueops.EventsPruneRowsCeilQuery())).
		WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"seq"}).AddRow(900))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM bd_events_journal")).
		WithArgs(int64(901)).WillReturnError(errors.New("table is locked"))

	deleted, err := AutoPrune(context.Background(), runner, AutoPruneOptions{
		RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000,
	})
	if err == nil {
		t.Fatal("a failing delete must be reported to the caller, which logs it")
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneKeepsTheWatermarkWhenTheBoundReadFails is the same promise one
// step earlier, and it is why the stamp gets a transaction of its own.
//
// The bound read is the step most likely to fail on a contended database, and
// it happens AFTER the stamp. Sharing a transaction with it — as the first cut
// of this did — rolls the stamp back with the failure, so the next command
// finds the pass due, fails the same way, and every command after it pays for a
// pass that cannot succeed. Here the stamp survives the failure and the second
// command does nothing but the throttle read.
func TestAutoPruneKeepsTheWatermarkWhenTheBoundReadFails(t *testing.T) {
	runner, mock := newFakeRunner(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	stamp := watermarkAt(t, now, 1000)

	// First pass: due, stamps, then the bound read fails.
	expectThrottleRead(mock, nil, int64(1000))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WithArgs(AutoPruneSlotKey, stamp).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnError(errors.New("deadlock found when trying to get lock"))

	opts := AutoPruneOptions{RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000}
	if _, err := AutoPrune(context.Background(), runner, opts); err == nil {
		t.Fatal("a failing bound read must be reported to the caller")
	}
	first := runner.transactions()
	if first != 3 {
		t.Fatalf("first pass opened %d transactions, want 3 (throttle, stamp, failed bound)", first)
	}

	// The next command reads the watermark the failed pass left behind — and
	// stops there.
	expectThrottleRead(mock, stamp, int64(1001))
	next := opts
	next.Now = now.Add(time.Minute)
	deleted, err := AutoPrune(context.Background(), runner, next)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second pass deleted %d rows; a failed pass must not re-run on the next command", deleted)
	}
	if got := runner.transactions() - first; got != 1 {
		t.Errorf("second pass opened %d transactions, want 1 (the throttle read alone)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneCapsTheBatchesItRunsPerPass is the amortization guarantee: a
// backlog larger than one pass can clear does NOT become one long transaction
// on a user's command. It stops at the cap and the rest waits for the next
// pass, which is what makes the trigger safe to hang off every write.
func TestAutoPruneCapsTheBatchesItRunsPerPass(t *testing.T) {
	runner, mock := newFakeRunner(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	full := int64(issueops.EventsAutoPruneBatchRows)

	expectThrottleRead(mock, nil, int64(10_000_000))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(10_000_000))
	mock.ExpectQuery(regexp.QuoteMeta(issueops.EventsPruneRowsCeilQuery())).
		WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"seq"}).AddRow(9_000_000))
	// Every batch comes back full, so the loop would keep going forever if the
	// cap were not enforced. Exactly EventsAutoPruneMaxBatches are allowed.
	for range issueops.EventsAutoPruneMaxBatches {
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM bd_events_journal")).
			WithArgs(int64(9_000_001)).WillReturnResult(sqlmock.NewResult(0, full))
	}

	opts := AutoPruneOptions{RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000}
	deleted, err := AutoPrune(context.Background(), runner, opts)
	if err != nil {
		t.Fatalf("auto-prune: %v", err)
	}
	perPass := full * int64(issueops.EventsAutoPruneMaxBatches)
	if deleted != perPass {
		t.Fatalf("deleted = %d, want %d (%d capped batches)", deleted, perPass, issueops.EventsAutoPruneMaxBatches)
	}
	// A fourth DELETE would show up as an unexpected call rather than an unmet
	// expectation, so the strict check is the one that proves the cap.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}

	// The remainder is not abandoned: the next pass past the throttle interval
	// picks the backlog up where this one stopped. That is the amortization —
	// a journal left unconsumed for months drains over several commands, and no
	// single command pays for all of it.
	expectThrottleRead(mock, watermarkAt(t, now, 10_000_000), int64(10_000_000))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(10_000_000))
	mock.ExpectQuery(regexp.QuoteMeta(issueops.EventsPruneRowsCeilQuery())).
		WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"seq"}).AddRow(9_000_000))
	for range issueops.EventsAutoPruneMaxBatches {
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM bd_events_journal")).
			WithArgs(int64(9_000_001)).WillReturnResult(sqlmock.NewResult(0, full))
	}

	next := opts
	next.Now = now.Add(2 * time.Hour)
	deleted, err = AutoPrune(context.Background(), runner, next)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if deleted != perPass {
		t.Fatalf("second pass deleted %d, want another %d — the backlog must keep draining", deleted, perPass)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneStopsEarlyWhenABatchComesBackShort: a partial batch means the
// prefix is gone, so a fourth transaction would delete nothing. The common case
// is one batch, and it should cost one.
func TestAutoPruneStopsEarlyWhenABatchComesBackShort(t *testing.T) {
	runner, mock := newFakeRunner(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	expectThrottleRead(mock, nil, int64(1000))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(1000))
	mock.ExpectQuery(regexp.QuoteMeta(issueops.EventsPruneRowsCeilQuery())).
		WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"seq"}).AddRow(900))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM bd_events_journal")).
		WithArgs(int64(901)).WillReturnResult(sqlmock.NewResult(0, 900))

	deleted, err := AutoPrune(context.Background(), runner, AutoPruneOptions{
		RetainRows: 100, Now: now, Interval: time.Hour, VolumeRows: 5000,
	})
	if err != nil {
		t.Fatalf("auto-prune: %v", err)
	}
	if deleted != 900 {
		t.Fatalf("deleted = %d, want 900", deleted)
	}
	// Throttle read, the watermark's own commit, the bound read, one delete.
	if got := runner.transactions(); got != 4 {
		t.Errorf("opened %d transactions, want 4", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAutoPruneReportsAFailingPlumbingWithoutPanicking. Every caller logs and
// carries on; nothing here may become a user-visible failure, so the contract
// is an ordinary error return even when the plumbing is refusing outright (a
// read-only store, a closed connection pool).
func TestAutoPruneReportsAFailingPlumbing(t *testing.T) {
	runner, _ := newFakeRunner(t)
	runner.err = errors.New("store is read-only")

	if _, err := AutoPrune(context.Background(), runner, AutoPruneOptions{RetainRows: 100}); err == nil {
		t.Fatal("a refusing plumbing must be reported, not swallowed here")
	}
	if _, err := AutoPrune(context.Background(), nil, AutoPruneOptions{RetainRows: 100}); err == nil {
		t.Fatal("a nil plumbing must be reported rather than panic")
	}
}

// TestTickAutoPruneRunsAPassPerTickAndStopsWithTheContext drives the loop bd
// serve runs, on a hand-fed clock. The server has no command boundary to hang
// maintenance off, so this loop is the trigger — and it must end when the
// process is shutting down rather than outlive the plumbing it prunes through.
func TestTickAutoPruneRunsAPassPerTickAndStopsWithTheContext(t *testing.T) {
	runner, mock := newFakeRunner(t)
	mock.MatchExpectationsInOrder(false)
	// Two ticks, each a not-due pass: one query apiece.
	for range 2 {
		expectThrottleRead(mock, watermarkAt(t, time.Now().UTC(), 10), int64(11))
	}

	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	reports := make(chan int64, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		TickAutoPrune(ctx, runner, tick, AutoPruneOptions{RetainRows: 100, Interval: time.Hour, VolumeRows: 5000},
			func(n int64, err error) {
				if err != nil {
					t.Errorf("pass reported an error: %v", err)
				}
				reports <- n
			})
	}()

	for range 2 {
		tick <- time.Now()
		select {
		case <-reports:
		case <-time.After(5 * time.Second):
			t.Fatal("a tick produced no pass")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the ticker loop outlived its context")
	}
	if got := runner.transactions(); got != 2 {
		t.Errorf("ran %d transactions across 2 ticks, want 2", got)
	}
}

// TestStartAutoPruneTickerStopsWithoutTheParentContext. The stop function must
// be able to end the loop on its own — a server that failed to bind returns
// with its signal context still live, and a stop that waited for someone else
// to cancel would hang the process there.
func TestStartAutoPruneTickerStopsWithoutTheParentContext(t *testing.T) {
	runner, mock := newFakeRunner(t)
	mock.MatchExpectationsInOrder(false)

	stop := StartAutoPruneTicker(context.Background(), runner, time.Hour, AutoPruneOptions{RetainRows: 100}, nil)
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() blocked on a context nobody was going to cancel")
	}
}

// TestAutoPruneEnabledForDefaultsToOn is the bounded-by-default promise at the
// configuration layer: a workspace that enabled the journal and said nothing
// else gets automatic retention.
func TestAutoPruneEnabledForDefaultsToOn(t *testing.T) {
	t.Setenv(AutoPruneEnvVar, "")
	if !AutoPruneEnabledFor(writeWorkspace(t, "")) {
		t.Fatal("auto-prune must default to ON for a workspace that configured nothing")
	}

	off := writeWorkspace(t, AutoPruneConfigKey+": false\n")
	if AutoPruneEnabledFor(off) {
		t.Error("a workspace that set the key to false must be respected")
	}

	t.Setenv(AutoPruneEnvVar, "1")
	if !AutoPruneEnabledFor(off) {
		t.Error("the environment override must beat the workspace file, as it does for activation")
	}
}

// TestAutoPruneEnabledForAcceptsYamlNo is a data-deletion guard, not a parsing
// nicety.
//
// `events-journal-auto-prune: no` is how a large share of people write false in
// YAML, and every value this resolver cannot read falls through to the key's
// default — which for THIS key is true. An unrecognized opt-out therefore does
// not fail safe: it goes on deleting the records the operator was writing the
// line to preserve, silently, with the file on disk saying otherwise.
func TestAutoPruneEnabledForAcceptsYamlNo(t *testing.T) {
	t.Setenv(AutoPruneEnvVar, "")
	if err := os.Unsetenv(AutoPruneEnvVar); err != nil {
		t.Fatalf("unset %s: %v", AutoPruneEnvVar, err)
	}
	for _, spelling := range []string{"no", "No", "NO", "off", "Off", "FALSE"} {
		if AutoPruneEnabledFor(writeWorkspace(t, AutoPruneConfigKey+": "+spelling+"\n")) {
			t.Errorf("%q did not read as an opt-out; bd would keep deleting records", spelling)
		}
	}
	for _, spelling := range []string{"yes", "Yes", "on", "ON", "TRUE"} {
		if !AutoPruneEnabledFor(writeWorkspace(t, AutoPruneConfigKey+": "+spelling+"\n")) {
			t.Errorf("%q did not read as enabled", spelling)
		}
	}
}

// TestResolveEnabledForArmsAgreeOnAnUnreadableValue. The no-workspace arm and
// the workspace arm answer for the same setting in the same command — one is
// simply reached earlier, before a workspace has been resolved. An unreadable
// value has to mean the same thing on both, or a key that defaults to TRUE
// resolves false on one arm and true on the other and nothing in the
// configuration explains why.
func TestResolveEnabledForArmsAgreeOnAnUnreadableValue(t *testing.T) {
	t.Setenv(AutoPruneEnvVar, "sure-why-not")
	t.Setenv(EnvVar, "sure-why-not")
	dir := writeWorkspace(t, "")

	if got, want := AutoPruneEnabledFor(""), AutoPruneEnabledFor(dir); got != want {
		t.Errorf("auto-prune: no-workspace arm = %v, workspace arm = %v — the arms disagree", got, want)
	}
	if !AutoPruneEnabledFor("") {
		t.Error("an unreadable auto-prune value must fall back to the default (on), not to false")
	}
	if got, want := EnabledFor(""), EnabledFor(dir); got != want {
		t.Errorf("activation: no-workspace arm = %v, workspace arm = %v — the arms disagree", got, want)
	}
	if EnabledFor("") {
		t.Error("an unreadable activation value must fall back to the default (off)")
	}
}
