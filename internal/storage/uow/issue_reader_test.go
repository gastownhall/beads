package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi/storereader"
	publicops "github.com/steveyegge/beads/issueops"
)

// The Reader role has two implementations — one over a store handle, one over a
// unit of work — and they answer the SAME contract method. A test that drives
// only one of them cannot see the failure this file exists for: an epilogue
// (sort, then trim) applied on one implementation's branch and not the other's.
// So these run one request through both and compare.
//
// WHAT MOVED TO THE CONTRACT. The --ready arm's display order is now pinned at
// all three backends by conformance.RunReaderListReadyFlagAnswersTheBlockerAware-
// Set, against real rows; so is the never-nil page, by
// RunReaderListEmptyPageIsWellFormed. What stays here is the thing no
// single-implementation suite can see: both bodies answering the SAME request
// from the SAME stub rows in the SAME order, with the database taken out of the
// comparison. A contract leg asserts one implementation against one engine's
// row order; this asserts the two implementations against each other.

// readerRows is the fixture: three ids whose natural-numeric order (bd-1, bd-2,
// bd-10) differs from both their input order and their lexical order, so an
// implementation that skips the sort is visible rather than lucky.
func readerRows() []*types.IssueWithCounts {
	return []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "bd-10", Title: "ten"}},
		{Issue: &types.Issue{ID: "bd-2", Title: "two"}},
		{Issue: &types.Issue{ID: "bd-1", Title: "one"}},
	}
}

// readerIssues answers both queries with the same rows in the same unsorted
// order, so any difference between the two implementations is the epilogue.
type readerIssues struct {
	domain.IssueUseCase
	rows []*types.IssueWithCounts
}

func (f readerIssues) GetReadyWorkWithCounts(context.Context, types.WorkFilter) (domain.SearchCountsPage, error) {
	return domain.SearchCountsPage{Items: f.rows}, nil
}

func (f readerIssues) SearchIssuesWithCounts(context.Context, string, types.IssueFilter) (domain.SearchCountsPage, error) {
	return domain.SearchCountsPage{Items: f.rows}, nil
}

// The ready arm's defer-wake sweep reaches this before the read; a workspace
// with nothing expired is the steady state the fixture models.
func (readerIssues) WakeExpiredDefers(context.Context) (issues, wisps int, err error) {
	return 0, 0, nil
}

// readerConfig is a workspace with no custom vocabulary, which is all
// BuildListFilter needs to run.
type readerConfig struct{ domain.ConfigUseCase }

func (readerConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) { return nil, nil }
func (readerConfig) GetCustomTypes(context.Context) ([]string, error)                { return nil, nil }
func (readerConfig) GetInfraTypes(context.Context) (map[string]bool, error)          { return nil, nil }

// readerStore is the store-shaped half of the comparison: a DoltStorage whose
// only real methods are the ones the store reader calls. Anything else panics
// rather than returning a zero value.
type readerStore struct {
	storage.DoltStorage
	rows []*types.IssueWithCounts
}

func (s readerStore) GetReadyWorkWithCounts(context.Context, types.WorkFilter) ([]*types.IssueWithCounts, error) {
	return s.rows, nil
}

func (s readerStore) SearchIssuesWithCounts(context.Context, string, types.IssueFilter) ([]*types.IssueWithCounts, error) {
	return s.rows, nil
}

func (s readerStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}
func (s readerStore) GetCustomTypes(context.Context) ([]string, error) { return nil, nil }
func (s readerStore) GetInfraTypes(context.Context) map[string]bool    { return nil }

func pageIDs(page publicops.IssuePage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.ID)
	}
	return out
}

// TestBothReaderImplementationsAgreeOnTheListEpilogue pins the property the
// role exists for: one request, two implementations, one answer.
//
// The --ready arm is where they diverged. The uow implementation returned the
// storage order untouched and untrimmed there, while its store-backed sibling
// sorted and trimmed both arms — so `--ready --sort id --limit N` came back in
// a different order, and with a different number of rows, depending on which
// implementation the front door happened to reach. `--sort id` is the case
// that makes it visible: SQL cannot express natural-numeric id order, so the
// builder leaves the query unlimited and the trim is the only thing bounding
// the page.
func TestBothReaderImplementationsAgreeOnTheListEpilogue(t *testing.T) {
	limit := 2
	for _, ready := range []bool{false, true} {
		name := "search"
		if ready {
			name = "ready"
		}
		t.Run(name, func(t *testing.T) {
			req := publicops.ListRequest{ReadyFlag: ready, SortBy: "id", Limit: &limit}

			uw := &mockUnitOfWork{
				issueUseCase:  readerIssues{rows: readerRows()},
				configUseCase: readerConfig{},
			}
			// The ready arm opens two units of work — the defer-wake
			// sweep, then the read span — and the provider hands out
			// zero-valued mocks once the pool runs dry.
			provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw, uw}}
			overUOW, err := NewIssueReader(provider)
			if err != nil {
				t.Fatalf("NewIssueReader: %v", err)
			}
			overStore, err := storereader.New(readerStore{rows: readerRows()})
			if err != nil {
				t.Fatalf("storereader.New: %v", err)
			}

			fromUOW, err := overUOW.List(context.Background(), req)
			if err != nil {
				t.Fatalf("uow reader List: %v", err)
			}
			fromStore, err := overStore.List(context.Background(), req)
			if err != nil {
				t.Fatalf("store reader List: %v", err)
			}

			want := []string{"bd-1", "bd-2"}
			if got := pageIDs(fromStore); !slices.Equal(got, want) {
				t.Errorf("store reader returned %v, want %v (sorted by natural id, trimmed to the page limit)", got, want)
			}
			if got := pageIDs(fromUOW); !slices.Equal(got, want) {
				t.Errorf("uow reader returned %v, want %v (sorted by natural id, trimmed to the page limit)", got, want)
			}
			if !slices.Equal(pageIDs(fromUOW), pageIDs(fromStore)) {
				t.Errorf("the two implementations of one contract method disagree\n uow: %v\nstore: %v",
					pageIDs(fromUOW), pageIDs(fromStore))
			}
			if !fromUOW.HasMore || !fromStore.HasMore {
				t.Errorf("HasMore: uow = %v, store = %v; the trim removed a row on both", fromUOW.HasMore, fromStore.HasMore)
			}
		})
	}
}

// readySnapshotRunner advances a second connection only after the active-name
// rows have been consumed, at the next read on the first connection.
type readySnapshotRunner struct {
	db.Runner
	afterNames func() error
	namesRead  bool
	statements []string
	arguments  [][]any
	events     []string
}

func (r *readySnapshotRunner) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if r.namesRead && r.afterNames != nil {
		advance := r.afterNames
		r.afterNames = nil
		if err := advance(); err != nil {
			return nil, err
		}
	}
	r.statements = append(r.statements, q)
	r.arguments = append(r.arguments, append([]any(nil), args...))
	rows, err := r.Runner.QueryContext(ctx, q, args...)
	if err == nil && strings.HasPrefix(q, "SELECT id, src FROM (") {
		r.events = append(r.events, "selection")
	}
	if q == sqlbuild.ActiveCustomStatusesSQL && err == nil {
		r.namesRead = true
		r.events = append(r.events, "names")
	}
	return rows, err
}

type readySnapshotTx struct {
	Tx
	runner        *readySnapshotRunner
	closes        int
	commits       int
	closeErr      error
	closeDeadline bool
}

func (t *readySnapshotTx) Runner() db.Runner { return t.runner }
func (t *readySnapshotTx) Commit(ctx context.Context, message string) error {
	t.commits++
	return t.Tx.Commit(ctx, message)
}
func (t *readySnapshotTx) RollbackUnlessCommitted(ctx context.Context) {
	t.closes++
	t.closeErr = ctx.Err()
	_, t.closeDeadline = ctx.Deadline()
	t.runner.events = append(t.runner.events, "close")
	t.Tx.RollbackUnlessCommitted(ctx)
}

type readySnapshotProvider struct {
	UnitOfWorkProvider
	advance    func() error
	autocommit bool
	readers    []*readySnapshotTx
	failure    error
	cleanup    func(Tx)
}

func (p *readySnapshotProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	base, ok := uw.(*baseUOW)
	if !ok {
		uw.Close(ctx)
		return nil, fmt.Errorf("unexpected UOW %T", uw)
	}
	runner := &readySnapshotRunner{Runner: base.tx.Runner()}
	runner.afterNames = func() error {
		if p.failure != nil {
			runner.events = append(runner.events, "prepare-error")
			return p.failure
		}
		// The negative control deliberately removes ONLY this read transaction.
		// The active names were read from the old configuration in either arm.
		if p.autocommit {
			if _, err := runner.Runner.ExecContext(ctx, "COMMIT"); err != nil {
				return err
			}
		}
		runner.events = append(runner.events, "writer-entry")
		if err := p.advance(); err != nil {
			return err
		}
		runner.events = append(runner.events, "writer-commit")
		return nil
	}
	tx := &readySnapshotTx{Tx: base.tx, runner: runner}
	if p.cleanup != nil {
		p.cleanup(base.tx)
	}
	base.tx = tx
	p.readers = append(p.readers, tx)
	return base, nil
}

// TestReadyReaderCustomActiveSnapshot drives the public UOW Reader, including
// its separate wake sweep, against a real server. The synchronized writer
// changes names AND candidate rows between lookup and selection. This proves
// the exercised read span, not a snapshot shared by separate Ready calls.
func TestReadyReaderCustomActiveSnapshot(t *testing.T) {
	provider := newUOWRoleFixtureProvider(t, t.Context(), "snap")
	kit := newUOWRoleFixtureKit(provider, "snap")
	for _, tc := range []struct {
		name       string
		autocommit bool
		failure    bool
	}{
		{"transaction", false, false}, {"transaction_ended_negative_control", true, false}, {"preparation_error", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			prefix := "snap-" + tc.name
			if err := kit.SetConfig(ctx, "status.custom", "review:active,triage:wip"); err != nil {
				t.Fatal(err)
			}
			for i, row := range []struct {
				suffix string
				status types.Status
				wisp   bool
			}{
				{"old", "review", false}, {"oldw", "review", true},
				{"new", "triage", false}, {"neww", "triage", true},
			} {
				issue := &types.Issue{ID: prefix + "-" + row.suffix, Title: row.suffix,
					Status: row.status, Priority: i, IssueType: types.TypeTask, Labels: []string{prefix}, Ephemeral: row.wisp}
				create := kit.CreateIssue
				if row.wisp {
					create = kit.CreateWisp
				}
				if err := create(ctx, issue, "tester"); err != nil {
					t.Fatal(err)
				}
			}
			advanced := 0
			wrapped := &readySnapshotProvider{UnitOfWorkProvider: provider, autocommit: tc.autocommit}
			sentinel := errors.New("ready preparation sentinel")
			if tc.failure {
				wrapped.failure = sentinel
			}
			wrapped.cleanup = func(tx Tx) {
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					tx.RollbackUnlessCommitted(ctx)
				})
			}
			wrapped.advance = func() error {
				advanced++
				writer, err := provider.NewUOW(ctx)
				if err != nil {
					return err
				}
				defer writer.Close(ctx)
				runner := writer.(*baseUOW).tx.Runner()
				if _, err := runner.ExecContext(ctx, "UPDATE custom_statuses SET category = CASE name WHEN 'review' THEN 'wip' ELSE 'active' END WHERE name IN ('review','triage')"); err != nil {
					return err
				}
				for _, table := range []string{"issues", "wisps"} {
					if _, err := runner.ExecContext(ctx, "UPDATE "+table+" SET status='closed' WHERE id IN (?, ?)", prefix+"-old", prefix+"-oldw"); err != nil {
						return err
					}
				}
				return writer.Commit(ctx, "")
			}
			reader, err := NewIssueReader(wrapped)
			if err != nil {
				t.Fatal(err)
			}
			limit := 1
			req := publicops.ReadyRequest{Sort: "priority", Labels: []string{prefix}, IncludeEphemeral: true, Limit: &limit}
			page, err := reader.Ready(ctx, req)
			if tc.failure {
				if !errors.Is(err, sentinel) || len(page.Items) != 0 || page.HasMore {
					t.Fatalf("failure page=%+v error=%v, want sentinel and no successful page", page, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want, more := []string{prefix + "-old"}, true
			if tc.autocommit || tc.failure {
				want, more = []string{}, false
			}
			if !slices.Equal(pageIDs(page), want) || page.HasMore != more {
				t.Fatalf("interleaved page=%v more=%v, want %v more=%v", pageIDs(page), page.HasMore, want, more)
			}
			wantAdvances := 1
			if tc.failure {
				wantAdvances = 0
			}
			if advanced != wantAdvances {
				t.Fatalf("writer advances=%d, want%d", advanced, wantAdvances)
			}
			var readTx *readySnapshotTx
			for _, tx := range wrapped.readers {
				r := tx.runner
				if !r.namesRead {
					continue
				}
				if readTx != nil {
					t.Fatal("active names read in more than one UOW")
				}
				readTx = tx
				names, selections := 0, 0
				for i, q := range r.statements {
					if q == sqlbuild.ActiveCustomStatusesSQL {
						names++
						t.Logf("names SQL=%s args=%v", q, r.arguments[i])
					}
					if strings.HasPrefix(q, "SELECT id, src FROM (") {
						selections++
						t.Logf("selection SQL=%s args=%v", q, r.arguments[i])
						if strings.Contains(q, "category = 'active'") {
							t.Fatal("selection retained custom-status subquery")
						}
					}
				}
				expected := []string{"names", "writer-entry", "writer-commit", "selection", "close"}
				wantSelections := 1
				if tc.failure {
					expected = []string{"names", "prepare-error", "close"}
					wantSelections = 0
				}
				if names != 1 || selections != wantSelections || !slices.Equal(r.events, expected) {
					t.Fatalf("read UOW names=%d selections=%d events=%v, want1/%d/%v", names, selections, r.events, wantSelections, expected)
				}
			}
			if readTx == nil {
				t.Fatal("no identified ready read UOW")
			}
			if readTx.closes != 1 || readTx.commits != 0 || readTx.closeErr != nil || !readTx.closeDeadline {
				t.Fatalf("read close=%d commit=%d error=%v deadline=%v, want exactly one live bounded close", readTx.closes, readTx.commits, readTx.closeErr, readTx.closeDeadline)
			}
			realTx, ok := readTx.Tx.(*doltServerTx)
			if !ok || !realTx.done || realTx.conn != nil {
				t.Fatalf("read transaction not released: %T", readTx.Tx)
			}
			t.Logf("outcome ids=%v hasMore=%v writerCommits=%d events=%v", pageIDs(page), page.HasMore, advanced, readTx.runner.events)
			if tc.failure {
				return
			}
			// A new public request must observe the committed new configuration and rows.
			fresh, err := NewIssueReader(provider)
			if err != nil {
				t.Fatal(err)
			}
			page, err = fresh.Ready(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(pageIDs(page), []string{prefix + "-new"}) || !page.HasMore {
				t.Fatalf("fresh page=%v more=%v, want new durable row and another wisp", pageIDs(page), page.HasMore)
			}
		})
	}
}
