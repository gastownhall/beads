package uow

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
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
