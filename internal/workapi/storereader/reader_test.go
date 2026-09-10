package storereader

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The store-backed reader answers `bd show --json` today and nothing else: the
// CLI's paging commands still reach storage through the builders directly (see
// reader.go's boundary note), so Ready and List here run in production
// only once a front door moves onto the role. That is precisely why they need
// tests — the over-fetch and trim they inherited from cmd/bd/list.go would
// otherwise be unexercised until the day something depends on them.

// fakeReaderStore is a DoltStorage whose only real methods are the five the
// reader calls. It records the filter it was handed, which is the only way to
// see the over-fetch: everything else about a page is visible in the result,
// and the +1 probe row is not.
type fakeReaderStore struct {
	storage.DoltStorage

	rows []*types.IssueWithCounts

	readyFilters  []types.WorkFilter
	searchFilters []types.IssueFilter
}

func (s *fakeReaderStore) GetReadyWorkWithCounts(_ context.Context, f types.WorkFilter) ([]*types.IssueWithCounts, error) {
	s.readyFilters = append(s.readyFilters, f)
	return s.serve(f.Limit), nil
}

func (s *fakeReaderStore) SearchIssuesWithCounts(_ context.Context, _ string, f types.IssueFilter) ([]*types.IssueWithCounts, error) {
	s.searchFilters = append(s.searchFilters, f)
	return s.serve(f.Limit), nil
}

// serve honors the filter's row limit the way a real query would, so the
// over-fetch is what actually produces the extra row the trim removes.
func (s *fakeReaderStore) serve(limit int) []*types.IssueWithCounts {
	if limit > 0 && len(s.rows) > limit {
		return slices.Clone(s.rows[:limit])
	}
	return slices.Clone(s.rows)
}

func (s *fakeReaderStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}
func (s *fakeReaderStore) GetCustomTypes(context.Context) ([]string, error) { return nil, nil }
func (s *fakeReaderStore) GetInfraTypes(context.Context) map[string]bool    { return nil }

func readerFixture(n int) []*types.IssueWithCounts {
	out := make([]*types.IssueWithCounts, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-" + string(rune('0'+i))}})
	}
	return out
}

func storeReaderFor(t *testing.T, rows []*types.IssueWithCounts) (issueops.Reader, *fakeReaderStore) {
	t.Helper()
	store := &fakeReaderStore{rows: rows}
	rd, err := New(store)
	if err != nil {
		t.Fatalf("storereader.New: %v", err)
	}
	return rd, store
}

func ids(page issueops.IssuePage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.ID)
	}
	return out
}

// TestStoreReaderReadyDetectsTruncationWithAProbeRow: the store seam has no
// HasMore of its own, so the reader asks for one row past the page and lets its
// presence be the answer. Both halves matter — asking for limit+1 and then
// cutting back to limit — because a reader that only did the first would hand
// the caller a page one row longer than it asked for.
func TestStoreReaderReadyDetectsTruncationWithAProbeRow(t *testing.T) {
	limit := 2
	rd, store := storeReaderFor(t, readerFixture(5))

	page, err := rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority", Limit: &limit})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(store.readyFilters) != 1 {
		t.Fatalf("%d queries, want 1", len(store.readyFilters))
	}
	if got := store.readyFilters[0].Limit; got != limit+1 {
		t.Errorf("query ran with Limit %d, want %d — the probe row is how this seam learns there is more", got, limit+1)
	}
	if got := ids(page); !slices.Equal(got, []string{"bd-1", "bd-2"}) {
		t.Errorf("page = %v, want the first two rows; the probe row must not be delivered", got)
	}
	if !page.HasMore {
		t.Error("HasMore = false, want true")
	}
}

// TestStoreReaderReadyReportsAnExactPageAsComplete: the probe row is the whole
// signal, so a result that exactly fills the page must not claim more.
func TestStoreReaderReadyReportsAnExactPageAsComplete(t *testing.T) {
	limit := 3
	rd, _ := storeReaderFor(t, readerFixture(3))

	page, err := rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority", Limit: &limit})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(page.Items) != 3 || page.HasMore {
		t.Errorf("page = %v has_more = %v, want all three rows and no more", ids(page), page.HasMore)
	}
}

// TestStoreReaderReadyLeavesAnUnlimitedQueryUnlimited: 0 means unlimited at
// both storage seams, and bumping it to 1 for a probe row would turn the one
// request that asks for everything into a request for one row.
func TestStoreReaderReadyLeavesAnUnlimitedQueryUnlimited(t *testing.T) {
	unlimited := 0
	rd, store := storeReaderFor(t, readerFixture(4))

	page, err := rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority", Limit: &unlimited})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if got := store.readyFilters[0].Limit; got != 0 {
		t.Errorf("query ran with Limit %d, want 0", got)
	}
	if len(page.Items) != 4 || page.HasMore {
		t.Errorf("page = %v has_more = %v, want every row and no more", ids(page), page.HasMore)
	}
}

// TestStoreReaderPagesAreNeverNil: an empty page is an empty array on every
// surface that serializes one, so no caller has to tell null from empty to
// learn that nothing matched.
func TestStoreReaderPagesAreNeverNil(t *testing.T) {
	rd, _ := storeReaderFor(t, nil)

	ready, err := rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority"})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ready.Items == nil {
		t.Error("Ready returned a nil Items on an empty result")
	}
	list, err := rd.List(context.Background(), issueops.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.Items == nil {
		t.Error("List returned a nil Items on an empty result")
	}
}

// TestStoreReaderListOverFetchesForAPushdownSort pins the other half of
// SQLLimit's decision. A sort the database CAN express keeps its row limit, so
// the reader has to add the probe row itself; the sort it cannot express
// already fetches everything, and adding one there would be meaningless.
func TestStoreReaderListOverFetchesForAPushdownSort(t *testing.T) {
	limit := 2
	for _, tc := range []struct {
		sortBy    string
		wantLimit int
	}{
		{"created", 3}, // pushdown: SQLLimit is the page limit, plus the probe row
		{"id", 0},      // no pushdown: SQLLimit is already 0, and 0 means unlimited
	} {
		t.Run(tc.sortBy, func(t *testing.T) {
			rd, store := storeReaderFor(t, readerFixture(5))
			page, err := rd.List(context.Background(), issueops.ListRequest{SortBy: tc.sortBy, Limit: &limit})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := store.searchFilters[0].Limit; got != tc.wantLimit {
				t.Errorf("--sort %s ran with Limit %d, want %d", tc.sortBy, got, tc.wantLimit)
			}
			if len(page.Items) != limit || !page.HasMore {
				t.Errorf("--sort %s: page = %v has_more = %v, want %d rows and more",
					tc.sortBy, ids(page), page.HasMore, limit)
			}
		})
	}
}

// TestStoreReaderReachesPastTheOffset pins the mechanism this body pages with,
// which the page alone cannot show. The seam renders LIMIT without OFFSET, so
// the reader has to ask for the skipped rows TOO — limit + offset + the probe
// row — and drop them itself. A body that skipped without widening would hand
// back a short page and call it complete.
//
// The filter's own Offset must come back to ZERO. The builders write the
// request's offset onto it for the callers that consume the filter and run
// their own query, so a body that widened the bound and left the offset on
// would skip the same rows twice the day this seam learns to render OFFSET.
func TestStoreReaderReachesPastTheOffset(t *testing.T) {
	limit := 2
	for _, tc := range []struct {
		name        string
		run         func(issueops.Reader) (issueops.IssuePage, error)
		queryWindow func(*fakeReaderStore) (int, int)
	}{
		{"Ready", func(rd issueops.Reader) (issueops.IssuePage, error) {
			return rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority", Limit: &limit, Offset: 1})
		}, func(s *fakeReaderStore) (int, int) { return s.readyFilters[0].Limit, s.readyFilters[0].Offset }},
		{"List", func(rd issueops.Reader) (issueops.IssuePage, error) {
			return rd.List(context.Background(), issueops.ListRequest{SortBy: "created", Limit: &limit, Offset: 1})
		}, func(s *fakeReaderStore) (int, int) { return s.searchFilters[0].Limit, s.searchFilters[0].Offset }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd, store := storeReaderFor(t, readerFixture(5))
			page, err := tc.run(rd)
			if err != nil {
				t.Fatalf("%s at Offset 1: %v", tc.name, err)
			}
			gotLimit, gotOffset := tc.queryWindow(store)
			if want := limit + 1 + 1; gotLimit != want {
				t.Errorf("query ran with Limit %d, want %d — the page, the row it skips, and the probe row", gotLimit, want)
			}
			if gotOffset != 0 {
				t.Errorf("query ran with Offset %d, want 0: the epilogue does the skipping", gotOffset)
			}
			if got := ids(page); !slices.Equal(got, []string{"bd-2", "bd-3"}) {
				t.Errorf("page = %v, want the two rows after the first", got)
			}
			if !page.HasMore {
				t.Error("HasMore = false with five rows, an offset of one and a limit of two")
			}
		})
	}
}

// TestStoreReaderOffsetPastTheEndIsAnEmptyPage: a pager that walks off the end
// gets an answer, not an error, and not the last page over again.
func TestStoreReaderOffsetPastTheEndIsAnEmptyPage(t *testing.T) {
	rd, _ := storeReaderFor(t, readerFixture(2))

	page, err := rd.List(context.Background(), issueops.ListRequest{SortBy: "created", Offset: 5})
	if err != nil {
		t.Fatalf("List at Offset 5 over two rows: %v", err)
	}
	if len(page.Items) != 0 || page.Items == nil || page.HasMore {
		t.Errorf("page = %v (nil=%v) has_more = %v, want an empty non-nil page and no more",
			ids(page), page.Items == nil, page.HasMore)
	}
}

// TestStoreReaderServesOffsetZero: zero is the absence of a page request, so it
// must not widen the query the way a real offset does. Every other test in this
// file would fail if it did, but none of them says why.
func TestStoreReaderServesOffsetZero(t *testing.T) {
	limit := 2
	rd, store := storeReaderFor(t, readerFixture(4))

	if _, err := rd.Ready(context.Background(), issueops.ReadyRequest{Sort: "priority", Limit: &limit, Offset: 0}); err != nil {
		t.Fatalf("Ready at Offset 0: %v", err)
	}
	if _, err := rd.List(context.Background(), issueops.ListRequest{SortBy: "created", Limit: &limit, Offset: 0}); err != nil {
		t.Fatalf("List at Offset 0: %v", err)
	}
	if len(store.readyFilters) != 1 || len(store.searchFilters) != 1 {
		t.Errorf("ran %d ready and %d search queries, want one each", len(store.readyFilters), len(store.searchFilters))
	}
	if got := store.readyFilters[0].Limit; got != limit+1 {
		t.Errorf("Ready at Offset 0 ran with Limit %d, want %d — the probe row and nothing else", got, limit+1)
	}
	if got := store.searchFilters[0].Limit; got != limit+1 {
		t.Errorf("List at Offset 0 ran with Limit %d, want %d — the probe row and nothing else", got, limit+1)
	}
}

// TestStoreReaderRefusesANilStore: the accessor is the only door, and a door
// that hands back a reader over nothing would fail on the first query with a
// nil dereference instead of at the seam that knows what is missing.
func TestStoreReaderRefusesANilStore(t *testing.T) {
	rd, err := New(nil)
	if err == nil {
		t.Fatalf("New(nil) = %v, want an error", rd)
	}
	if rd != nil {
		t.Errorf("New(nil) returned %T alongside its error", rd)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("New(nil) error = %v, want *storage.ErrUnsupported", err)
	}
	if unsupported.Op != "storereader.New" {
		t.Errorf("ErrUnsupported.Op = %q, want storereader.New — the Op names the function that refused", unsupported.Op)
	}
}
