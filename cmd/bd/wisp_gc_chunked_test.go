package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispGCAgeStubStore is the store the age-based half of `bd mol wisp gc` reads
// on the direct route: the ephemeral-wisp scan with an age filter, the
// blocked/status/infra protection reads, dependent expansion, and the Deleter
// role. It answers the front door's per-id resolution (SearchIssues with IDs,
// SearchIssueIDs, GetIssue) and counts those calls in lookups: a forced age GC
// hands the ids it just selected straight to the role, so after the scan it
// must make none. Any other call panics on the embedded nil DoltStorage.
type wispGCAgeStubStore struct {
	storage.DoltStorage
	wisps   []*types.Issue
	blocked map[string]bool
	deleter *wispPurgeStubDeleter
	lookups int
}

func (s *wispGCAgeStubStore) issue(id string) *types.Issue {
	for _, w := range s.wisps {
		if w.ID == id {
			return w
		}
	}
	return nil
}

func (s *wispGCAgeStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if len(filter.IDs) > 0 {
		s.lookups++
		for _, id := range filter.IDs {
			if issue := s.issue(id); issue != nil && !s.deleter.deleted[id] {
				out = append(out, issue)
			}
		}
		return out, nil
	}
	for _, w := range s.wisps {
		if s.deleter.deleted[w.ID] {
			continue
		}
		if filter.Limit > 0 && len(out) == filter.Limit {
			break
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *wispGCAgeStubStore) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	s.lookups++
	return nil, nil
}

func (s *wispGCAgeStubStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	s.lookups++
	if issue := s.issue(id); issue != nil && !s.deleter.deleted[id] {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

func (s *wispGCAgeStubStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range ids {
		if issue := s.issue(id); issue != nil && !s.deleter.deleted[id] {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (s *wispGCAgeStubStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "fx", nil
	}
	return "", nil
}

func (s *wispGCAgeStubStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{"issue_prefix": "fx"}, nil
}

func (s *wispGCAgeStubStore) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	var out []*types.BlockedIssue
	for _, w := range s.wisps {
		if s.blocked[w.ID] && !s.deleter.deleted[w.ID] {
			out = append(out, &types.BlockedIssue{ID: w.ID})
		}
	}
	return out, nil
}

func (s *wispGCAgeStubStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

func (s *wispGCAgeStubStore) IsInfraTypeCtx(context.Context, types.IssueType) bool {
	return false
}

func (s *wispGCAgeStubStore) FindWispDependentsRecursive(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}

func (s *wispGCAgeStubStore) Deleter() (issueops.Deleter, error) {
	return s.deleter, nil
}

// useWispGCAgeStub installs a store holding n open, unblocked ephemeral wisps
// — all abandoned under --age 0 — and restores every global the command
// touches.
func useWispGCAgeStub(t *testing.T, n int) *wispGCAgeStubStore {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor
		commandDidWrite.Store(oldDidWrite)
		commandDeletedIssueIDs.reset()
	})
	commandDidWrite.Store(false)
	commandDeletedIssueIDs.reset()

	ids := wispPurgeIDs(n)
	wisps := make([]*types.Issue, n)
	for i := range wisps {
		wisps[i] = &types.Issue{
			ID:        ids[i],
			Title:     "abandoned wisp " + fmt.Sprintf("%04d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}
	}
	s := &wispGCAgeStubStore{
		wisps:   wisps,
		blocked: map[string]bool{},
		deleter: &wispPurgeStubDeleter{deleted: map[string]bool{}, dependents: map[string][]string{}},
	}
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = s, context.Background(), false, false, false, "test"
	return s
}

// runWispGCAgeForced runs the age GC against the installed stub with --age 0
// (every wisp abandoned) and no --dry-run.
func runWispGCAgeForced() error {
	return runWispGCAge(context.Background(), false, 0, false, nil)
}

// TestWispGCChunkLoop pins the chunk executor's own contract, independent of
// either route: sizes (empty, one chunk, one over, several), selection order,
// cancellation between chunks, stopping at the first error, and discarding
// whatever a chunk reported beside its error (GH#6246: an error at a commit
// boundary proves nothing about what was written).
func TestWispGCChunkLoop(t *testing.T) {
	t.Run("chunk sizes", func(t *testing.T) {
		for _, tc := range []struct {
			in   int
			want []int
		}{
			{0, nil},
			{200, []int{200}},
			{201, []int{200, 1}},
			{450, []int{200, 200, 50}},
		} {
			var got []int
			var sent []string
			ids := wispPurgeIDs(tc.in)
			out := runWispPurgeChunks(context.Background(), ids, func(_ context.Context, chunk []string) (wispPurgeChunk, error) {
				got = append(got, len(chunk))
				sent = append(sent, chunk...)
				return wispPurgeChunk{deleted: len(chunk), deletedIDs: slices.Clone(chunk)}, nil
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("%d ids went out in chunks %v, want %v", tc.in, got, tc.want)
			}
			if !slices.Equal(sent, ids) {
				t.Errorf("%d ids: chunks carried %d ids, want the selection once each in order", tc.in, len(sent))
			}
			if out.err != nil {
				t.Errorf("%d ids: err = %v, want nil", tc.in, out.err)
			}
			if len(out.confirmed) != tc.in {
				t.Errorf("%d ids: confirmed %d, want %d", tc.in, len(out.confirmed), tc.in)
			}
		}
	})

	t.Run("first error stops with nothing confirmed", func(t *testing.T) {
		boom := fmt.Errorf("injected failure")
		var calls int
		out := runWispPurgeChunks(context.Background(), wispPurgeIDs(450), func(_ context.Context, _ []string) (wispPurgeChunk, error) {
			calls++
			return wispPurgeChunk{}, boom
		})
		if calls != 1 {
			t.Errorf("sent %d chunks after the first error, want 1", calls)
		}
		if len(out.confirmed) != 0 {
			t.Errorf("confirmed %d ids beside the failed first chunk, want 0", len(out.confirmed))
		}
		if out.remaining() != 450 {
			t.Errorf("remaining = %d, want 450", out.remaining())
		}
	})

	t.Run("later error keeps the earlier chunks and discards the failed chunk's report", func(t *testing.T) {
		boom := fmt.Errorf("injected later failure")
		var calls int
		out := runWispPurgeChunks(context.Background(), wispPurgeIDs(450), func(_ context.Context, chunk []string) (wispPurgeChunk, error) {
			calls++
			if calls == 2 {
				// A chunk that reports deletions AND an error: the report
				// must not survive the error.
				return wispPurgeChunk{deleted: 99, deletedIDs: chunk}, boom
			}
			return wispPurgeChunk{deleted: len(chunk), deletedIDs: slices.Clone(chunk)}, nil
		})
		if calls != 2 {
			t.Errorf("sent %d chunks, want 2 (none after the failed one)", calls)
		}
		if len(out.confirmed) != 200 {
			t.Errorf("confirmed %d ids, want the first chunk's 200", len(out.confirmed))
		}
		if out.deleted != 200 {
			t.Errorf("deleted = %d, want 200 (the failed chunk's report discarded)", out.deleted)
		}
		if len(out.deletedIDs) != 200 {
			t.Errorf("deleted_ids holds %d ids, want 200", len(out.deletedIDs))
		}
		if out.remaining() != 250 {
			t.Errorf("remaining = %d, want 250", out.remaining())
		}
	})

	t.Run("canceled before the first chunk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var calls int
		out := runWispPurgeChunks(ctx, wispPurgeIDs(450), func(_ context.Context, _ []string) (wispPurgeChunk, error) {
			calls++
			return wispPurgeChunk{deleted: 1}, nil
		})
		if calls != 0 {
			t.Errorf("sent %d chunks to a canceled context, want 0", calls)
		}
		if out.err == nil {
			t.Error("err = nil, want the cancellation")
		}
		if len(out.confirmed) != 0 {
			t.Errorf("confirmed %d ids beside a cancellation, want 0", len(out.confirmed))
		}
	})

	t.Run("canceled between chunks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls int
		out := runWispPurgeChunks(ctx, wispPurgeIDs(450), func(_ context.Context, chunk []string) (wispPurgeChunk, error) {
			calls++
			cancel()
			return wispPurgeChunk{deleted: len(chunk), deletedIDs: slices.Clone(chunk)}, nil
		})
		if calls != 1 {
			t.Errorf("sent %d chunks, want 1 (canceled after it committed)", calls)
		}
		if len(out.confirmed) != 200 {
			t.Errorf("confirmed %d ids, want the committed chunk's 200", len(out.confirmed))
		}
		if out.err == nil {
			t.Error("err = nil, want the cancellation")
		}
	})
}

// TestWispGCDeletesInBoundedTransactions pins the age-based half of GH#6246:
// a forced age GC that selects 450 wisps must not submit them as one
// deletion request — one transaction whose post-delete is_blocked recompute
// can outrun the client read timeout on a large database and roll the whole
// selection back. It sends sequential requests of 200, 200 and 50, each
// forced and uncascaded, skips the front door's per-id resolution, and
// reports one aggregate success result.
func TestWispGCDeletesInBoundedTransactions(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		s := useWispGCAgeStub(t, 450)

		stdout, stderr, err := captureWispGC(t, runWispGCAgeForced)
		if err != nil {
			t.Fatalf("runWispGCAge: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if got, want := requestSizes(s.deleter.requests), []int{200, 200, 50}; !slices.Equal(got, want) {
			t.Fatalf("request sizes = %v, want %v (one 450-id transaction is the GH#6246 failure mode)", got, want)
		}
		var sent []string
		for i, req := range s.deleter.requests {
			if !req.Force || req.Cascade || req.DryRun {
				t.Errorf("request %d = Force %v Cascade %v DryRun %v, want a forced, uncascaded, real delete",
					i+1, req.Force, req.Cascade, req.DryRun)
			}
			sent = append(sent, req.IDs...)
		}
		if !slices.Equal(sent, wispPurgeIDs(450)) {
			t.Error("requests did not carry the selection once each, in order")
		}
		if len(s.deleter.deleted) != 450 {
			t.Errorf("deleted %d wisps, want all 450", len(s.deleter.deleted))
		}
		if !containsAll(stdout, "Deleted 450 issue(s)") {
			t.Errorf("stdout is not one aggregate success result:\n%s", stdout)
		}
		assertExportBookkeeping(t, wispPurgeIDs(450), nil)
	})

	t.Run("json", func(t *testing.T) {
		s := useWispGCAgeStub(t, 450)
		jsonOutput = true

		stdout, stderr, err := captureWispGC(t, runWispGCAgeForced)
		if err != nil {
			t.Fatalf("runWispGCAge: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if got, want := requestSizes(s.deleter.requests), []int{200, 200, 50}; !slices.Equal(got, want) {
			t.Fatalf("request sizes = %v, want %v", got, want)
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(450) {
			t.Errorf("deleted_count = %v, want 450", got)
		}
		if got, _ := obj["deleted"].([]interface{}); len(got) != 450 {
			t.Errorf("deleted names %d ids, want 450", len(got))
		}
		if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 450 {
			t.Errorf("deleted_ids names %d ids, want 450", len(got))
		}
		if got, ok := obj["candidates_capped"]; !ok || got != false {
			t.Errorf("candidates_capped = %v (present %v), want false (the age scan is not capped-reporting)", got, ok)
		}
	})

	t.Run("skips per-id resolution", func(t *testing.T) {
		s := useWispGCAgeStub(t, 450)

		stdout, stderr, err := captureWispGC(t, runWispGCAgeForced)
		if err != nil {
			t.Fatalf("runWispGCAge: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if s.lookups != 0 {
			t.Errorf("front door made %d per-id lookups after the scan, want 0", s.lookups)
		}
	})
}

// TestWispGCKeepsCommittedChunksWhenALaterChunkFails pins the age route's
// retained-progress contract (GH#6246): a request that fails after earlier
// ones committed stops the GC without discarding them, no further request is
// sent, the command exits nonzero naming the confirmed deletions and the
// unconfirmed rest, and a rerun without the fault deletes the rest.
func TestWispGCKeepsCommittedChunksWhenALaterChunkFails(t *testing.T) {
	s := useWispGCAgeStub(t, 450)
	// The second request (ids 200..399) is refused before it deletes
	// anything, as a server-side timeout refuses a transaction.
	s.deleter.fail = failRequestContaining(s.wisps[250].ID, context.DeadlineExceeded)

	stdout, stderr, err := captureWispGC(t, runWispGCAgeForced)
	if err == nil {
		t.Fatalf("runWispGCAge succeeded, want a nonzero exit\nstdout:\n%s", stdout)
	}
	if code, ok := exitCodeFromError(err); !ok || code == 0 {
		t.Errorf("error = %v, want an already-reported nonzero exit", err)
	}
	if got, want := requestSizes(s.deleter.requests), []int{200, 200}; !slices.Equal(got, want) {
		t.Errorf("request sizes = %v, want %v (no request after the failed one)", got, want)
	}
	if len(s.deleter.deleted) != 200 {
		t.Errorf("deleted %d wisps, want the first chunk's 200", len(s.deleter.deleted))
	}
	if !containsAll(stderr, "200", "250", "context deadline exceeded", "Rerun the same command") {
		t.Errorf("stderr does not name the 200 confirmed, the 250 unconfirmed, the cause and the rerun hint:\n%s", stderr)
	}
	if !containsAll(stdout, "Deleted 200 issue(s)") {
		t.Errorf("stdout does not report the 200 confirmed deletions:\n%s", stdout)
	}
	assertExportBookkeeping(t, wispPurgeIDs(200), wispPurgeIDs(450)[200:])

	// Rerun without the fault: the store reselects the 250 still there and
	// deletes them.
	s.deleter.fail = nil
	stdout, stderr, err = captureWispGC(t, runWispGCAgeForced)
	if err != nil {
		t.Fatalf("rerun: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if len(s.deleter.deleted) != 450 {
		t.Errorf("after the rerun %d of 450 wisps are deleted, want all", len(s.deleter.deleted))
	}
}

// TestWispGCReportsOneAggregateResult pins the --json half of a stopped age
// GC: exactly one stdout object, describing the committed chunks with
// unconfirmed_count (GH#6246's name for the selected ids no committed chunk
// confirmed — the errored and unattempted ones), with no second error
// envelope on stdout and no JSON on stderr, and the command's write/export
// bookkeeping still recording the confirmed deletions although the command
// fails.
func TestWispGCReportsOneAggregateResult(t *testing.T) {
	s := useWispGCAgeStub(t, 450)
	s.deleter.fail = failRequestContaining(s.wisps[250].ID, context.DeadlineExceeded)
	jsonOutput = true

	stdout, stderr, err := captureWispGC(t, runWispGCAgeForced)
	if err == nil {
		t.Fatalf("runWispGCAge succeeded, want a nonzero exit\nstdout:\n%s", stdout)
	}
	if !isReportedExit(err) {
		t.Errorf("error = %v, want an already-reported exit (the caller must not print it again)", err)
	}
	if strings.Contains(stderr, "{") {
		t.Errorf("stderr carries a JSON value; the answer belongs in the one stdout object:\n%s", stderr)
	}
	obj := decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(200) {
		t.Errorf("deleted_count = %v, want 200", got)
	}
	if got, _ := obj["deleted"].([]interface{}); len(got) != 200 {
		t.Errorf("deleted names %d ids, want 200", len(got))
	}
	if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 200 {
		t.Errorf("deleted_ids names %d ids, want 200", len(got))
	}
	if got := obj["unconfirmed_count"]; got != float64(250) {
		t.Errorf("unconfirmed_count = %v, want 250", got)
	}
	if got := obj["remaining_count"]; got != float64(250) {
		t.Errorf("remaining_count = %v, want 250 (the published spelling stays)", got)
	}
	if got, _ := obj["error"].(string); !containsAll(got, "context deadline exceeded") {
		t.Errorf("error = %q, want the cause", got)
	}
	if got, _ := obj["hint"].(string); got != wispPurgeRerunHint {
		t.Errorf("hint = %q, want the same-command rerun guidance", got)
	}
	if _, ok := obj["dependencies_removed"].(float64); !ok {
		t.Errorf("dependencies_removed missing from the partial-failure object: %v", obj)
	}
	assertExportBookkeeping(t, wispPurgeIDs(200), wispPurgeIDs(450)[200:])
}

// TestWispGCAgePreviewDoesNotMutate pins that chunking reaches only the
// forced age GC: a --dry-run deletes nothing, keeps the preview answer it
// always gave, and sends no deletion request (GH#6246).
func TestWispGCAgePreviewDoesNotMutate(t *testing.T) {
	s := useWispGCAgeStub(t, 3)
	jsonOutput = true

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispGCAge(context.Background(), true, 0, false, nil)
	})
	if err != nil {
		t.Fatalf("runWispGCAge dry run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	obj := decodeSingleJSONObject(t, stdout)
	if got, _ := obj["cleaned_ids"].([]interface{}); len(got) != 3 {
		t.Errorf("cleaned_ids names %d wisps, want 3", len(got))
	}
	if obj["cleaned_count"] != float64(0) || obj["dry_run"] != true {
		t.Errorf("preview = %v, want cleaned_count 0 and dry_run true (the shape is unchanged)", obj)
	}
	if len(s.deleter.requests) != 0 {
		t.Errorf("dry run sent %d deletion requests", len(s.deleter.requests))
	}
	if len(s.deleter.deleted) != 0 {
		t.Errorf("dry run deleted %d wisps", len(s.deleter.deleted))
	}
}

// TestDeleteBatchStaysOneRequest pins the other side of GH#6246's scoping:
// ordinary multi-id `bd delete --force` is NOT chunked — the front door
// submits one atomic request for its whole resolved selection, exactly as
// before the wisp GCs gained bounded transactions.
func TestDeleteBatchStaysOneRequest(t *testing.T) {
	s := useWispPurgeStub(t, 250)

	stdout, stderr, err := captureWispGC(t, func() error {
		return deleteBatch(nil, s.closed, true, false, false, false, false, nil)
	})
	if err != nil {
		t.Fatalf("deleteBatch: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if len(s.deleter.requests) != 1 {
		t.Fatalf("deleteBatch sent %d requests for 250 ids, want exactly one atomic request", len(s.deleter.requests))
	}
	req := s.deleter.requests[0]
	if len(req.IDs) != 250 {
		t.Errorf("the one request carries %d ids, want 250", len(req.IDs))
	}
	if !req.Force || req.Cascade || req.DryRun {
		t.Errorf("request = Force %v Cascade %v DryRun %v, want a forced, uncascaded, real delete", req.Force, req.Cascade, req.DryRun)
	}
	if !slices.Equal(req.IDs, s.closed) {
		t.Error("the request did not carry the resolved selection once each, in order")
	}
}

// wispGCAgeProxiedProvider is the proxied route's unit-of-work provider for
// the age GC: every NewUOW opens a unit of work, the abandoned-wisp scan
// reads through one, and each bounded deletion chunk is one more. Only a unit
// of work that commits applies its deletions.
type wispGCAgeProxiedProvider struct {
	wisps   []*types.Issue
	deleted map[string]bool
	// units is every unit of work opened, in order.
	units []*wispGCAgeProxiedUOW
	// fail, when it returns an error, refuses a DeleteIssues call.
	fail func(ids []string) error
}

func (p *wispGCAgeProxiedProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	u := &wispGCAgeProxiedUOW{p: p}
	p.units = append(p.units, u)
	return u, nil
}

func (p *wispGCAgeProxiedProvider) Close(context.Context) error { return nil }

// deletionUnits is the units of work that asked to delete, in order.
func (p *wispGCAgeProxiedProvider) deletionUnits() []*wispGCAgeProxiedUOW {
	var out []*wispGCAgeProxiedUOW
	for _, u := range p.units {
		if u.params != nil {
			out = append(out, u)
		}
	}
	return out
}

type wispGCAgeProxiedUOW struct {
	uow.UnitOfWork
	p         *wispGCAgeProxiedProvider
	params    *domain.DeleteIssuesParams
	committed bool
}

func (u *wispGCAgeProxiedUOW) Close(context.Context) {}

func (u *wispGCAgeProxiedUOW) Commit(_ context.Context, _ string) error {
	u.committed = true
	if u.params != nil {
		for _, id := range u.params.IDs {
			u.p.deleted[id] = true
		}
	}
	return nil
}

func (u *wispGCAgeProxiedUOW) IssueUseCase() domain.IssueUseCase {
	return wispGCAgeProxiedIssues{u: u}
}

func (u *wispGCAgeProxiedUOW) ConfigUseCase() domain.ConfigUseCase {
	return wispGCAgeProxiedConfig{}
}

type wispGCAgeProxiedIssues struct {
	domain.IssueUseCase
	u *wispGCAgeProxiedUOW
}

func (s wispGCAgeProxiedIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var page domain.SearchPage
	for _, w := range s.u.p.wisps {
		if s.u.p.deleted[w.ID] {
			continue
		}
		if filter.Limit > 0 && len(page.Items) == filter.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, w)
	}
	return page, nil
}

func (wispGCAgeProxiedIssues) GetBlockedIssues(context.Context, types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}

func (wispGCAgeProxiedIssues) FindWispDependentsRecursive(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}

func (s wispGCAgeProxiedIssues) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range ids {
		for _, w := range s.u.p.wisps {
			if w.ID == id && !s.u.p.deleted[id] {
				out = append(out, w)
			}
		}
	}
	return out, nil
}

func (s wispGCAgeProxiedIssues) DeleteIssues(_ context.Context, params domain.DeleteIssuesParams, _ string) (domain.DeleteIssuesResult, error) {
	s.u.params = &params
	if s.u.p.fail != nil {
		if err := s.u.p.fail(params.IDs); err != nil {
			return domain.DeleteIssuesResult{}, err
		}
	}
	return domain.DeleteIssuesResult{
		DeletedCount:      len(params.IDs),
		DependenciesCount: len(params.IDs),
		LabelsCount:       2 * len(params.IDs),
		EventsCount:       3 * len(params.IDs),
		ReferencesUpdated: 1,
		DeletedIDs:        slices.Clone(params.IDs),
	}, nil
}

type wispGCAgeProxiedConfig struct {
	domain.ConfigUseCase
}

func (wispGCAgeProxiedConfig) IsInfraTypeCtx(context.Context, types.IssueType) (bool, error) {
	return false, nil
}

func (wispGCAgeProxiedConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

// useWispGCAgeProxied installs a proxied provider holding n open ephemeral
// wisps, all abandoned under --age 0.
func useWispGCAgeProxied(t *testing.T, n int) *wispGCAgeProxiedProvider {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	oldProvider, oldJSON, oldQuiet, oldActor := uowProvider, jsonOutput, quietFlag, actor
	t.Cleanup(func() {
		uowProvider, jsonOutput, quietFlag, actor = oldProvider, oldJSON, oldQuiet, oldActor
	})
	ids := wispPurgeIDs(n)
	wisps := make([]*types.Issue, n)
	for i := range wisps {
		wisps[i] = &types.Issue{
			ID:        ids[i],
			Title:     "abandoned proxied wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}
	}
	p := &wispGCAgeProxiedProvider{wisps: wisps, deleted: map[string]bool{}}
	uowProvider, jsonOutput, quietFlag, actor = p, false, false, "test"
	return p
}

// TestWispGCAgeProxiedBoundsEachUnitOfWork pins the proxied half of the age
// GC's bounded transactions (GH#6246): every deletion unit of work carries at
// most 200 of the selected ids, uncascaded, with reference rewriting on, and
// a deterministic second-unit failure stops further units while the first
// unit's confirmed progress stays deleted and stays counted in the one JSON
// answer, under the keys the proxied route has always printed.
func TestWispGCAgeProxiedBoundsEachUnitOfWork(t *testing.T) {
	runForced := func() error {
		return runWispGCAgeProxiedServer(context.Background(), false, 0, false, nil)
	}

	t.Run("success", func(t *testing.T) {
		p := useWispGCAgeProxied(t, 450)
		jsonOutput = true

		stdout, stderr, err := captureWispGC(t, runForced)
		if err != nil {
			t.Fatalf("runWispGCAgeProxiedServer: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		units := p.deletionUnits()
		var sizes []int
		var sent []string
		for i, u := range units {
			sizes = append(sizes, len(u.params.IDs))
			sent = append(sent, u.params.IDs...)
			if u.params.Cascade || !u.params.UpdateTextReferences || !u.committed {
				t.Errorf("unit %d: Cascade %v UpdateTextReferences %v committed %v, want uncascaded, rewriting references, committed",
					i, u.params.Cascade, u.params.UpdateTextReferences, u.committed)
			}
		}
		if want := []int{200, 200, 50}; !slices.Equal(sizes, want) {
			t.Fatalf("deletion unit sizes = %v, want %v (one 450-id unit of work is one transaction)", sizes, want)
		}
		if !slices.Equal(sent, wispPurgeIDs(450)) {
			t.Error("deletion units did not carry the selection once each in selection order")
		}
		if len(p.deleted) != 450 {
			t.Errorf("%d wisps deleted, want 450", len(p.deleted))
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(450) {
			t.Errorf("deleted_count = %v, want 450", got)
		}
		if got := obj["references_updated"]; got != float64(3) {
			t.Errorf("references_updated = %v, want 3 (one per unit, summed)", got)
		}
		if _, ok := obj["deleted_ids"]; ok {
			t.Error("proxied answer gained deleted_ids, which that route has never printed")
		}
	})

	t.Run("second unit fails", func(t *testing.T) {
		p := useWispGCAgeProxied(t, 450)
		jsonOutput = true
		boom := fmt.Errorf("injected proxied deletion failure")
		p.fail = func(ids []string) error {
			if slices.Contains(ids, wispPurgeIDs(450)[250]) {
				return boom
			}
			return nil
		}

		stdout, _, err := captureWispGC(t, runForced)
		if err == nil || !isReportedExit(err) {
			t.Fatalf("error = %v, want an already-reported nonzero exit\nstdout:\n%s", err, stdout)
		}
		units := p.deletionUnits()
		if len(units) != 2 {
			t.Fatalf("opened %d deletion units, want 2 (none after the failed one)", len(units))
		}
		if units[1].committed {
			t.Error("the failed unit of work committed")
		}
		if len(p.deleted) != 200 {
			t.Errorf("%d wisps deleted, want the first unit's 200", len(p.deleted))
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(200) {
			t.Errorf("deleted_count = %v, want 200 (the failed unit's counts are not published)", got)
		}
		if got := obj["unconfirmed_count"]; got != float64(250) {
			t.Errorf("unconfirmed_count = %v, want 250", got)
		}
		if got, _ := obj["error"].(string); !containsAll(got, boom.Error()) {
			t.Errorf("error = %q, want the cause", got)
		}
		if _, ok := obj["deleted_ids"]; ok {
			t.Error("proxied answer gained deleted_ids, which that route has never printed")
		}
	})
}

// containsAll reports whether s contains every want as a substring.
func containsAll(s string, want ...string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
