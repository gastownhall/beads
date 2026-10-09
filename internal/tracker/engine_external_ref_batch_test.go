package tracker

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type refsHistoryStore struct {
	storage.DoltStorage

	refs        map[string]string
	singleErr   error
	singleCalls int
}

func (s *refsHistoryStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	s.singleCalls++
	if s.singleErr != nil {
		return "", false, s.singleErr
	}
	ref, ok := s.refs[id]
	return ref, ok, nil
}

type refsBatchStore struct {
	*refsHistoryStore

	batchErr   error
	batchCalls int
	batchIDs   []string
}

func (s *refsBatchStore) PreviousExternalRefs(_ context.Context, ids []string, _ time.Time) (map[string]string, error) {
	s.batchCalls++
	s.batchIDs = ids
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	out := make(map[string]string)
	for _, id := range ids {
		if ref, ok := s.refs[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}

type prelinkedOutcome struct {
	hydrated   []string
	hydratedBy map[string]bool
	fetches    int
	warnings   int
	err        error
}

func runPrelinked(t *testing.T, store Store) prelinkedOutcome {
	t.Helper()
	const prefix = "https://tracker.test/"
	ref := func(s string) *string { return &s }
	locals := []*types.Issue{
		nil,
		{ID: "bd-nil"},
		{ID: "bd-other", ExternalRef: ref("https://elsewhere.test/X")},
		{ID: "bd-same", ExternalRef: ref(prefix + "EXT-1")},
		{ID: "bd-moved", ExternalRef: ref(prefix + "EXT-2")},
		{ID: "bd-new", ExternalRef: ref(" " + prefix + "EXT-3")},
		{ID: "bd-nullprev", ExternalRef: ref(prefix + "EXT-4")},
		{ID: "bd-padded", ExternalRef: ref(prefix + "EXT-5")},
		{ID: "bd-seen", ExternalRef: ref(prefix + "EXT-6")},
		{ID: "bd-gone-remote", ExternalRef: ref(prefix + "EXT-7")},
	}
	base := newMockTracker("test")
	for _, id := range []string{"EXT-1", "EXT-2", "EXT-3", "EXT-4", "EXT-5", "EXT-6"} {
		base.issues = append(base.issues, TrackerIssue{ID: "internal-" + id, Identifier: id})
	}
	tr := &mockExternalRefTracker{
		mockTracker: base,
		buildRef:    func(issue *TrackerIssue) string { return prefix + issue.Identifier },
		extract:     func(r string) string { return strings.TrimPrefix(r, prefix) },
		isRef:       func(r string) bool { return strings.HasPrefix(r, prefix) },
	}
	e := &Engine{Tracker: tr, Store: store}
	lastSync := time.Now()
	hydrated, by, err := e.fetchPrelinkedIssues(context.Background(), []TrackerIssue{{Identifier: "EXT-6"}}, locals, &lastSync)
	out := prelinkedOutcome{hydratedBy: by, fetches: base.fetchCalls, warnings: len(e.warnings), err: err}
	for _, issue := range hydrated {
		out.hydrated = append(out.hydrated, issue.Identifier)
	}
	return out
}

func prelinkedRefs() map[string]string {
	return map[string]string{
		"bd-same":     "https://tracker.test/EXT-1",
		"bd-moved":    "https://tracker.test/EXT-OLD",
		"bd-nullprev": "",
		"bd-padded":   "  https://tracker.test/EXT-5  ",
	}
}

const prelinkedCount = 7

func sameOutcome(t *testing.T, got, want prelinkedOutcome) {
	t.Helper()
	if !slices.Equal(got.hydrated, want.hydrated) || !maps.Equal(got.hydratedBy, want.hydratedBy) || got.fetches != want.fetches {
		t.Fatalf("outcome = %+v, per-issue outcome = %+v", got, want)
	}
}

func TestFetchPrelinkedIssuesBatchMatchesPerIssue(t *testing.T) {
	single := &refsHistoryStore{refs: prelinkedRefs()}
	want := runPrelinked(t, single)
	if want.err != nil || single.singleCalls != prelinkedCount {
		t.Fatalf("per-issue run: err=%v, %d PreviousExternalRef calls, want %d", want.err, single.singleCalls, prelinkedCount)
	}
	if !slices.Equal(want.hydrated, []string{"EXT-2", "EXT-3", "EXT-4"}) {
		t.Fatalf("per-issue run hydrated %v, want EXT-2, EXT-3, EXT-4", want.hydrated)
	}

	batch := &refsBatchStore{refsHistoryStore: &refsHistoryStore{refs: prelinkedRefs()}}
	sameOutcome(t, runPrelinked(t, batch), want)
	if batch.batchCalls != 1 || batch.singleCalls != 0 || len(batch.batchIDs) != prelinkedCount {
		t.Fatalf("batch run: %d PreviousExternalRefs calls for %d ids, %d PreviousExternalRef calls; want 1 call for %d ids, 0", batch.batchCalls, len(batch.batchIDs), batch.singleCalls, prelinkedCount)
	}
}

func TestFetchPrelinkedIssuesBatchErrorFallsBackPerIssue(t *testing.T) {
	want := runPrelinked(t, &refsHistoryStore{refs: prelinkedRefs()})

	batch := &refsBatchStore{refsHistoryStore: &refsHistoryStore{refs: prelinkedRefs()}, batchErr: errors.New("i/o timeout")}
	got := runPrelinked(t, batch)
	sameOutcome(t, got, want)
	if batch.batchCalls != 1 || batch.singleCalls != prelinkedCount {
		t.Fatalf("%d PreviousExternalRefs and %d PreviousExternalRef calls, want 1 and %d", batch.batchCalls, batch.singleCalls, prelinkedCount)
	}
	if want.warnings != 0 || got.warnings != 1 {
		t.Fatalf("warnings: per-issue run %d, fallback run %d; want 0 and 1", want.warnings, got.warnings)
	}

	perIssueErr := errors.New("boom")
	batch = &refsBatchStore{refsHistoryStore: &refsHistoryStore{singleErr: perIssueErr}, batchErr: errors.New("i/o timeout")}
	got = runPrelinked(t, batch)
	if !errors.Is(got.err, perIssueErr) || !strings.Contains(got.err.Error(), "checking pre-linked local issue bd-same") {
		t.Fatalf("err = %v, want the per-issue error for bd-same", got.err)
	}
}

func TestFetchPrelinkedIssuesBatchThroughDecorator(t *testing.T) {
	want := runPrelinked(t, &refsHistoryStore{refs: prelinkedRefs()})

	inner := &refsBatchStore{refsHistoryStore: &refsHistoryStore{refs: prelinkedRefs()}}
	store := NewStore(&fakeStoreDecorator{DoltStorage: inner, inner: inner})
	if _, ok := externalRefHistoryBatchQuerier(store); !ok {
		t.Fatalf("expected %T over a decorator to resolve the batch external_ref history capability", store)
	}
	sameOutcome(t, runPrelinked(t, store), want)
	if inner.batchCalls != 1 || inner.singleCalls != 0 {
		t.Fatalf("%d PreviousExternalRefs and %d PreviousExternalRef calls, want 1 and 0", inner.batchCalls, inner.singleCalls)
	}
}

func TestFetchPrelinkedIssuesWithoutLastSyncSkipsBatch(t *testing.T) {
	batch := &refsBatchStore{refsHistoryStore: &refsHistoryStore{refs: prelinkedRefs()}}
	e := &Engine{Tracker: newMockTracker("test"), Store: batch}
	if _, _, err := e.fetchPrelinkedIssues(context.Background(), nil, []*types.Issue{{ID: "bd-1"}}, nil); err != nil {
		t.Fatalf("fetchPrelinkedIssues: %v", err)
	}
	if batch.batchCalls != 0 {
		t.Fatalf("%d PreviousExternalRefs calls without a last sync, want 0", batch.batchCalls)
	}
}
