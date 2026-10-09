package externaldeps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func pageOf(hasMore bool, ids ...string) issueops.IssuePage {
	items := make([]*issueops.IssueWithCounts, 0, len(ids))
	for _, id := range ids {
		items = append(items, &issueops.IssueWithCounts{Issue: &types.Issue{ID: id}})
	}
	return issueops.IssuePage{Items: items, HasMore: hasMore}
}

func idsOf(page issueops.IssuePage) string {
	ids := make([]string, 0, len(page.Items))
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	return strings.Join(ids, ",")
}

// TestExcludeAndPage pins the client-side half of the remote ready
// exclusion: drop the blocked rows from the widened window, then cut the
// caller's own page, with an honest HasMore.
func TestExcludeAndPage(t *testing.T) {
	blocked := map[string]bool{"b1": true, "b2": true}
	for _, tc := range []struct {
		name        string
		window      issueops.IssuePage
		offset      int
		limit       int
		wantIDs     string
		wantHasMore bool
	}{
		{"blocked rows leave the page full", pageOf(false, "b1", "a", "b2", "c", "d"), 0, 2, "a,c", true},
		{"offset counts unblocked rows only", pageOf(false, "a", "b1", "c", "d"), 1, 2, "c,d", false},
		{"unlimited keeps every unblocked row", pageOf(false, "a", "b1", "c"), 0, 0, "a,c", false},
		{"a server-truncated window keeps HasMore", pageOf(true, "a", "b1"), 0, 5, "a", true},
		{"an offset past the end is an empty page", pageOf(false, "a"), 3, 2, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := excludeAndPage(tc.window, blocked, tc.offset, tc.limit)
			if idsOf(got) != tc.wantIDs || got.HasMore != tc.wantHasMore {
				t.Errorf("got %q hasMore=%v, want %q hasMore=%v", idsOf(got), got.HasMore, tc.wantIDs, tc.wantHasMore)
			}
			if got.Items == nil {
				t.Error("an empty page must be an empty slice, not nil")
			}
		})
	}
}

func TestWidenedLimit(t *testing.T) {
	if got := *widenedLimit(3, 10, 2); got != 15 {
		t.Errorf("widenedLimit(3,10,2) = %d, want 15", got)
	}
	if got := *widenedLimit(3, 0, 2); got != 0 {
		t.Errorf("an unlimited read stays unlimited, got %d", got)
	}
}

// remoteFake is a remote backend (storage.RemoteBackendStore) whose legacy
// method seam is the embedded fakeStore's and whose reader role is its own:
// remoteReader must be reached through the decorator's IssueReader, and the
// fake's legacy ready reads are never consulted by it.
type remoteFake struct {
	*fakeStore
	reader *recordingReader
}

func (r *remoteFake) IsRemoteBackendStore() bool { return true }

func (r *remoteFake) IssueReader() (issueops.Reader, error) { return r.reader, nil }

// recordingReader serves a fixed ready set in order and records the limits it
// was asked for, so the widened window is observable.
type recordingReader struct {
	ready  []string
	limits []int
	lists  int
}

func (r *recordingReader) Ready(_ context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	limit := 0
	if req.Limit != nil {
		limit = *req.Limit
	}
	r.limits = append(r.limits, limit)
	ids := r.ready[req.Offset:]
	hasMore := false
	if limit > 0 && len(ids) > limit {
		ids, hasMore = ids[:limit], true
	}
	return pageOf(hasMore, ids...), nil
}

func (r *recordingReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	r.lists++
	return pageOf(false, r.ready...), nil
}

func (r *recordingReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, issueops.ErrNotFound
}

func newRemotePolicyStore(enforced bool) (*Store, *recordingReader) {
	reader := &recordingReader{ready: []string{"be-x", "be-a", "be-b"}}
	raw := &remoteFake{
		fakeStore: &fakeStore{
			enforced: enforced,
			deps: map[string][]*types.Dependency{
				"be-x": {externalDep("be-x", "external:remote:payments", types.DepBlocks)},
			},
		},
		reader: reader,
	}
	store := New(raw, func(ProjectName) (string, bool) { return "", false }, nil)
	store.warnProject = nil
	return store, reader
}

// TestRemoteReaderReadyExcludesExternallyBlocked is the role-level half of
// S6c's ready exclusion (bd ready itself reads through the legacy seam): the
// reader the decorator hands out for a remote store drops the externally
// blocked row from a widened window and still fills the page.
func TestRemoteReaderReadyExcludesExternallyBlocked(t *testing.T) {
	store, reader := newRemotePolicyStore(false)
	rd, err := store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rd.(*remoteReader); !ok {
		t.Fatalf("IssueReader over a remote store = %T, want the inner role passed through", rd)
	}
	one := 1
	page, err := rd.Ready(t.Context(), issueops.ReadyRequest{Limit: &one})
	if err != nil {
		t.Fatal(err)
	}
	if idsOf(page) != "be-a" || !page.HasMore {
		t.Errorf("Ready(limit 1) = %q hasMore=%v, want \"be-a\" with more", idsOf(page), page.HasMore)
	}
	if len(reader.limits) != 1 || reader.limits[0] != 2 {
		t.Errorf("inner ready limits = %v, want one read widened to 2", reader.limits)
	}
	if _, err := rd.List(t.Context(), issueops.ListRequest{}); err != nil || reader.lists != 1 {
		t.Errorf("an ordinary List did not pass through: lists=%d err=%v", reader.lists, err)
	}
}

// TestRemoteReaderReadyPassesThroughWhenServerEnforces: a server that
// advertises the policy has applied it; the client neither filters nor widens.
func TestRemoteReaderReadyPassesThroughWhenServerEnforces(t *testing.T) {
	store, reader := newRemotePolicyStore(true)
	rd, err := store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	two := 2
	page, err := rd.Ready(t.Context(), issueops.ReadyRequest{Limit: &two})
	if err != nil {
		t.Fatal(err)
	}
	if idsOf(page) != "be-x,be-a" || reader.limits[0] != 2 {
		t.Errorf("Ready = %q (limits %v), want the server's page untouched", idsOf(page), reader.limits)
	}
}

type recordingClaimer struct{ claimed []string }

func (c *recordingClaimer) Claim(_ context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	c.claimed = append(c.claimed, req.IssueID)
	return issueops.ClaimResult{Issue: &types.Issue{ID: req.IssueID}, Changed: true}, nil
}

// remoteClaimFake adds the claim and edge roles a remote claim check uses.
type remoteClaimFake struct {
	*remoteFake
	claimer *recordingClaimer
}

func (r *remoteClaimFake) IssueClaimer() (issueops.Claimer, error) { return r.claimer, nil }

func (r *remoteClaimFake) EdgeReader() (issueops.EdgeReader, error) { return fakeEdges{r.deps}, nil }

type fakeEdges struct{ deps map[string][]*types.Dependency }

func (f fakeEdges) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	out := issueops.EdgeReadResult{}
	for _, id := range req.IDs {
		out.Anchors = append(out.Anchors, issueops.AnchorEdges{ID: id, Edges: f.deps[id]})
	}
	return out, nil
}

// TestRemoteClaimerRefusesExternallyBlocked pins the direct-claim guard of
// the remote composition (the CLI's own `update --claim` goes through the
// lifecycle guard instead; this role is what bd serve and library callers
// reach), reading only the claimed issue's edges.
func TestRemoteClaimerRefusesExternallyBlocked(t *testing.T) {
	store, _ := newRemotePolicyStore(false)
	raw := &remoteClaimFake{remoteFake: store.inner.(*remoteFake), claimer: &recordingClaimer{}}
	store = New(raw, func(ProjectName) (string, bool) { return "", false }, nil)
	store.warnProject = nil
	claimer, err := store.IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(t.Context(), issueops.ClaimRequest{Actor: "a", IssueID: "be-x"}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("claim of the externally blocked be-x: err = %v, want ErrCloseBlocked", err)
	}
	if _, err := claimer.Claim(t.Context(), issueops.ClaimRequest{Actor: "a", IssueID: "be-a"}); err != nil {
		t.Errorf("claim of the unblocked be-a: %v", err)
	}
	if strings.Join(raw.claimer.claimed, ",") != "be-a" {
		t.Errorf("server claims = %v, want only be-a", raw.claimer.claimed)
	}
}
