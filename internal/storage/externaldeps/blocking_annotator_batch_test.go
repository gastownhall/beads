package externaldeps

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// pageStore is a fakeStore whose local blocking state is local (an id present
// is blocked, by those blockers) and which counts the reads a page makes.
type pageStore struct {
	*fakeStore
	local      map[string][]string
	ownReads   int
	perIDCalls int
}

func (p *pageStore) GetDependencyRecordsForIssues(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	p.ownReads++
	return p.fakeStore.GetDependencyRecordsForIssues(ctx, ids)
}

func (p *pageStore) IsBlocked(_ context.Context, id string) (bool, []string, error) {
	p.perIDCalls++
	blockers, ok := p.local[id]
	return ok, slices.Clone(blockers), nil
}

func (p *pageStore) BlockingAnnotator() (publicops.BlockingAnnotator, error) {
	return pageAnnotator{}, nil
}

// pageAnnotator is the backend's own answer: no local blockers.
type pageAnnotator struct{}

func (pageAnnotator) AnnotateBlocking(_ context.Context, req publicops.BlockingRequest) (publicops.BlockingResult, error) {
	items := make([]publicops.IssueBlocking, 0, len(req.IDs))
	for _, id := range req.IDs {
		items = append(items, publicops.IssueBlocking{ID: id, BlockedBy: []string{}})
	}
	return publicops.BlockingResult{Items: items}, nil
}

// batchPageStore also answers a page's local blockers in one call
// (storage.BlockersBatchStore).
type batchPageStore struct {
	*pageStore
	batchCalls int
}

func (b *batchPageStore) IsBlockedWithBlockersBatch(_ context.Context, ids []string) (map[string][]string, error) {
	b.batchCalls++
	out := make(map[string][]string)
	for _, id := range ids {
		if blockers, ok := b.local[id]; ok {
			out[id] = slices.Clone(blockers)
		}
	}
	return out, nil
}

var _ storage.BlockersBatchStore = (*batchPageStore)(nil)

func countingPolicy(raw storage.DoltStorage, foreign *fakeStore, opens *int) *Store {
	return New(raw,
		func(project ProjectName) (string, bool) { return "/projects/remote", project == "remote" },
		func(context.Context, string) (storage.DoltStorage, error) {
			*opens++
			return foreign, nil
		},
	)
}

// TestBlockingAnnotatorReadsAPageOnce: annotating N ids reads their own edges
// once, opens the one foreign project they all name once, and asks the local
// blockers in one batch, rather than IsBlocked's reads and foreign open per
// id (9-20 ms each over a remote serve, so a 1,400-id refresh took 12 s). The
// answer is still IsBlocked's: local blockers first, then the unsatisfied
// refs; a satisfied ref is not listed.
func TestBlockingAnnotatorReadsAPageOnce(t *testing.T) {
	const n = 40
	ids := make([]string, 0, n)
	deps := make(map[string][]*types.Dependency, n)
	local := make(map[string][]string)
	for i := range n {
		id := fmt.Sprintf("be-%02d", i)
		ids = append(ids, id)
		switch i % 4 {
		case 0: // unsatisfied external only
			deps[id] = []*types.Dependency{externalDep(id, "external:remote:pending", types.DepBlocks)}
		case 1: // satisfied external only
			deps[id] = []*types.Dependency{externalDep(id, "external:remote:shipped", types.DepBlocks)}
		case 2: // local blockers plus both refs
			local[id] = []string{"be-x", "be-w (waits-for)"}
			deps[id] = []*types.Dependency{
				externalDep(id, "external:remote:pending", types.DepBlocks),
				externalDep(id, "external:remote:shipped", types.DepBlocks),
			}
		case 3: // held by its parent chain only: blocked, no direct blockers
			local[id] = nil
		}
	}
	shipped := issue("remote-shipped")
	shipped.Status = types.StatusClosed
	foreign := &fakeStore{labels: map[string][]*types.Issue{
		"provides:shipped": {shipped},
		"provides:pending": {issue("remote-pending")},
	}}
	raw := &batchPageStore{pageStore: &pageStore{fakeStore: &fakeStore{deps: deps}, local: local}}
	opens := 0
	store := countingPolicy(raw, foreign, &opens)
	annotator, err := store.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}

	result, err := annotator.AnnotateBlocking(t.Context(), publicops.BlockingRequest{IDs: ids})
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	if raw.ownReads != 1 || opens != 1 || raw.batchCalls != 1 || raw.perIDCalls != 0 {
		t.Fatalf("one page of %d ids made %d own-edge reads, %d foreign opens, %d batch and %d per-id local reads; want 1, 1, 1, 0",
			n, raw.ownReads, opens, raw.batchCalls, raw.perIDCalls)
	}

	got := make(map[string]string, n)
	for _, item := range result.Items {
		got[item.ID] = fmt.Sprint(item.BlockedBy)
	}
	want := make(map[string]string, n)
	for i, id := range ids {
		switch i % 4 {
		case 0:
			want[id] = "[external:remote:pending]"
		case 1, 3:
			want[id] = "[]"
		case 2:
			want[id] = "[be-x be-w (waits-for) external:remote:pending]"
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("AnnotateBlocking = %v\nwant %v", got, want)
	}

	// The same answer per item, through IsBlocked, which the page used to make.
	for _, item := range result.Items {
		blocked, blockers, err := store.IsBlocked(t.Context(), item.ID)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", item.ID, err)
		}
		if blocked && !slices.Equal(blockers, item.BlockedBy) {
			t.Errorf("%s: page says %v, IsBlocked says %v", item.ID, item.BlockedBy, blockers)
		}
	}
}

// TestBlockingAnnotatorFallsBackPerIDWithoutTheBatchRead: a store without
// storage.BlockersBatchStore still gets IsBlocked's answer, one local read per
// id, while the external half stays one read and one foreign open.
func TestBlockingAnnotatorFallsBackPerIDWithoutTheBatchRead(t *testing.T) {
	ids := []string{"be-a", "be-b", "be-c"}
	deps := map[string][]*types.Dependency{}
	local := map[string][]string{}
	for _, id := range ids {
		deps[id] = []*types.Dependency{externalDep(id, "external:remote:pending", types.DepBlocks)}
		local[id] = []string{"be-x"}
	}
	raw := &pageStore{fakeStore: &fakeStore{deps: deps}, local: local}
	opens := 0
	annotator, err := countingPolicy(raw, &fakeStore{}, &opens).BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	result, err := annotator.AnnotateBlocking(t.Context(), publicops.BlockingRequest{IDs: ids})
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	if raw.ownReads != 1 || opens != 1 || raw.perIDCalls != len(ids) {
		t.Fatalf("own-edge reads = %d, foreign opens = %d, per-id local reads = %d; want 1, 1, %d", raw.ownReads, opens, raw.perIDCalls, len(ids))
	}
	for _, item := range result.Items {
		if want := []string{"be-x", "external:remote:pending"}; !slices.Equal(item.BlockedBy, want) {
			t.Errorf("%s BlockedBy = %v, want %v", item.ID, item.BlockedBy, want)
		}
	}
}
