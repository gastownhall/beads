//go:build cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage/dolt"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispGCRecordingStore is a real store whose deletion role records every
// request it is handed and, while failAt is set, refuses the failAt-th one
// (1-based) before it reaches the store, so the refused request changes
// nothing.
type wispGCRecordingStore struct {
	*dolt.DoltStore
	requests []issueops.DeleteRequest
	failAt   int
}

// errWispGCInjected is the stable, nonretryable refusal the recording store
// injects.
var errWispGCInjected = errors.New("injected deletion refusal")

func (s *wispGCRecordingStore) Deleter() (issueops.Deleter, error) {
	inner, err := s.DoltStore.Deleter()
	if err != nil {
		return nil, err
	}
	return wispGCRecordingDeleter{s: s, inner: inner}, nil
}

type wispGCRecordingDeleter struct {
	s     *wispGCRecordingStore
	inner issueops.Deleter
}

func (d wispGCRecordingDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	req.IDs = slices.Clone(req.IDs)
	d.s.requests = append(d.s.requests, req)
	if d.s.failAt > 0 && len(d.s.requests) == d.s.failAt {
		return issueops.DeleteResult{}, errWispGCInjected
	}
	return d.inner.Delete(ctx, req)
}

func (s *wispGCRecordingStore) requestSizes() []int {
	sizes := make([]int, 0, len(s.requests))
	for _, req := range s.requests {
		sizes = append(sizes, len(req.IDs))
	}
	return sizes
}

// useWispGCRealStore opens a fresh Dolt-backed store, installs it (behind the
// recording deletion role) as the store the direct gc route reads, and
// restores every global the command touches.
func useWispGCRealStore(t *testing.T) (*dolt.DoltStore, *wispGCRecordingStore) {
	t.Helper()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	rec := &wispGCRecordingStore{DoltStore: s}

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
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = rec, context.Background(), false, false, false, "test"
	return s, rec
}

// seedWisps creates n ephemeral tasks with the given status.
func seedWisps(t *testing.T, s *dolt.DoltStore, n int, status types.Status) []*types.Issue {
	t.Helper()
	now := time.Now()
	wisps := make([]*types.Issue, n)
	for i := range wisps {
		wisps[i] = &types.Issue{
			Title:     fmt.Sprintf("patrol step %d", i),
			Status:    status,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if status == types.StatusClosed {
			wisps[i].ClosedAt = &now
		}
	}
	if err := s.CreateIssues(context.Background(), wisps, "test"); err != nil {
		t.Fatalf("creating %d %s wisps: %v", n, status, err)
	}
	return wisps
}

// wispCount is how many wisps are left in s.
func wispCount(t *testing.T, s *dolt.DoltStore) int {
	t.Helper()
	ephemeral := true
	found, err := s.SearchIssues(context.Background(), "", types.IssueFilter{Ephemeral: &ephemeral, Limit: 10000})
	if err != nil {
		t.Fatalf("listing wisps: %v", err)
	}
	return len(found)
}

// wispGCClosedFlags is `bd mol wisp gc --closed --force`; wispGCAgeFlags is
// `bd mol wisp gc --age 0s`, which selects every unprotected open wisp.
var (
	wispGCClosedFlags = map[string]string{"closed": "true", "force": "true"}
	wispGCAgeFlags    = map[string]string{"age": "0s"}
)

// runWispGCCommand runs `bd mol wisp gc` with flags through the command's own
// RunE, capturing stdout and stderr, and puts every flag back afterwards.
func runWispGCCommand(t *testing.T, flags map[string]string) (stdout, stderr string, err error) {
	t.Helper()
	t.Cleanup(func() {
		wispGCCmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				if sv, ok := f.Value.(pflag.SliceValue); ok {
					_ = sv.Replace(nil)
				} else {
					_ = f.Value.Set(f.DefValue)
				}
				f.Changed = false
			}
		})
	})
	for name, value := range flags {
		if err := wispGCCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("setting --%s=%s: %v", name, value, err)
		}
	}
	return captureWispGC(t, func() error { return runWispGC(wispGCCmd, nil) })
}

// TestWispGCDeletesInBoundedTransactions is the regression test for GH#6246
// on both direct gc modes: 450 eligible wisps go to the deletion role in
// three uncascaded, forced requests of 200, 200 and 50 (one transaction
// each), every one is deleted, and the command prints one aggregate result.
// The age mode handed its whole selection to one request, so one transaction
// carried the blocked-state recompute of every wisp it deleted.
func TestWispGCDeletesInBoundedTransactions(t *testing.T) {
	modes := []struct {
		name   string
		status types.Status
		flags  map[string]string
	}{
		{"closed", types.StatusClosed, wispGCClosedFlags},
		{"age", types.StatusOpen, wispGCAgeFlags},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			s, rec := useWispGCRealStore(t)
			seedWisps(t, s, 450, mode.status)
			jsonOutput = true

			stdout, stderr, err := runWispGCCommand(t, mode.flags)
			if err != nil {
				t.Fatalf("gc: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if got, want := rec.requestSizes(), []int{200, 200, 50}; !slices.Equal(got, want) {
				t.Errorf("deletion request sizes = %v, want %v", got, want)
			}
			seen := map[string]bool{}
			for i, req := range rec.requests {
				if !req.Force || req.Cascade || req.DryRun {
					t.Errorf("request %d: force=%v cascade=%v dry_run=%v, want a forced, uncascaded, real delete", i+1, req.Force, req.Cascade, req.DryRun)
				}
				for _, id := range req.IDs {
					if seen[id] {
						t.Errorf("request %d repeats %s", i+1, id)
					}
					seen[id] = true
				}
			}
			if left := wispCount(t, s); left != 0 {
				t.Errorf("%d wisps remain, want 0", left)
			}
			obj := decodeSingleJSONObject(t, stdout)
			if got := obj["deleted_count"]; got != float64(450) {
				t.Errorf("deleted_count = %v, want 450", got)
			}
			if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 450 {
				t.Errorf("deleted_ids names %d ids, want 450", len(got))
			}
			if got, _ := obj["deleted"].([]interface{}); len(got) != 450 {
				t.Errorf("deleted names %d ids, want 450", len(got))
			}
		})
	}
}

// TestWispGCKeepsCommittedChunksWhenALaterChunkFails pins that an age gc
// whose second deletion request is refused before it changes anything keeps
// the first chunk deleted, sends no third request, exits nonzero naming the
// 200 confirmed deletions and the 250 selected wisps it could not confirm,
// with the age command's own wording, and that rerunning without the fault
// deletes the rest. With the whole selection in one request, a failure took
// every deletion of the run with it.
func TestWispGCKeepsCommittedChunksWhenALaterChunkFails(t *testing.T) {
	s, rec := useWispGCRealStore(t)
	wisps := seedWisps(t, s, 450, types.StatusOpen)
	rec.failAt = 2

	stdout, stderr, err := runWispGCCommand(t, wispGCAgeFlags)
	if err == nil {
		t.Fatalf("gc succeeded, want the injected refusal\nstdout:\n%s", stdout)
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Errorf("error = %v, want an already-reported exit 1", err)
	}
	if got, want := rec.requestSizes(), []int{200, 200}; !slices.Equal(got, want) {
		t.Errorf("deletion request sizes = %v, want %v (no request after the refused one)", got, want)
	}
	if left := wispCount(t, s); left != 250 {
		t.Errorf("%d wisps remain, want the 250 the first chunk did not carry", left)
	}
	for _, id := range rec.requests[0].IDs {
		if _, err := s.GetIssue(context.Background(), id); err == nil {
			t.Errorf("%s of the committed first chunk is still present", id)
			break
		}
	}
	if !bytes.Contains([]byte(stdout), []byte("Deleted 200 issue(s)")) {
		t.Errorf("stdout lacks the 200 confirmed deletions:\n%s", stdout)
	}
	for _, want := range []string{
		"wisp gc stopped after deleting 200 of 450 selected wisp(s); 250 not confirmed deleted",
		errWispGCInjected.Error(),
		"Rerun the same command to reselect current data and delete the remaining abandoned wisps.",
	} {
		if !bytes.Contains([]byte(stderr), []byte(want)) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if bytes.Contains([]byte(stderr), []byte("--closed")) {
		t.Errorf("an age gc failure names the closed purge:\n%s", stderr)
	}
	assertExportBookkeeping(t, rec.requests[0].IDs, rec.requests[1].IDs)

	rec.failAt, rec.requests = 0, nil
	stdout, stderr, err = runWispGCCommand(t, wispGCAgeFlags)
	if err != nil {
		t.Fatalf("rerun: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got, want := rec.requestSizes(), []int{200, 50}; !slices.Equal(got, want) {
		t.Errorf("rerun request sizes = %v, want %v", got, want)
	}
	if left := wispCount(t, s); left != 0 {
		t.Errorf("%d of %d wisps remain after the rerun, want 0", left, len(wisps))
	}
}

// TestWispGCReportsPartialFailureAsOneAggregateAndOneError pins the --json
// half of a stopped gc: stdout is exactly one aggregate object counting only
// the committed chunk, with unconfirmed_count (selected ids no committed
// chunk confirmed: the refused chunk and any never sent); stderr is exactly
// one error object naming the cause; the exit is already reported, so the
// caller prints nothing more; and the command's write and export bookkeeping
// holds the confirmed deletions even though the command fails.
func TestWispGCReportsPartialFailureAsOneAggregateAndOneError(t *testing.T) {
	s, rec := useWispGCRealStore(t)
	seedWisps(t, s, 450, types.StatusOpen)
	rec.failAt = 2
	jsonOutput = true

	stdout, stderr, err := runWispGCCommand(t, wispGCAgeFlags)
	if err == nil {
		t.Fatalf("gc succeeded, want the injected refusal\nstdout:\n%s", stdout)
	}
	if !isReportedExit(err) {
		t.Errorf("error = %v, want an already-reported exit (the caller must not print it again)", err)
	}
	obj := decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(200) {
		t.Errorf("deleted_count = %v, want 200", got)
	}
	if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 200 {
		t.Errorf("deleted_ids names %d ids, want 200", len(got))
	}
	if got := obj["unconfirmed_count"]; got != float64(250) {
		t.Errorf("unconfirmed_count = %v, want 250", got)
	}
	errObj := decodeSingleJSONObject(t, stderr)
	if msg, _ := errObj["error"].(string); !bytes.Contains([]byte(msg), []byte(errWispGCInjected.Error())) ||
		!bytes.Contains([]byte(msg), []byte("250 not confirmed deleted")) {
		t.Errorf("stderr error = %q, want the cause and the unconfirmed count", msg)
	}
	if left := wispCount(t, s); left != 250 {
		t.Errorf("%d wisps remain, want 250", left)
	}
	assertExportBookkeeping(t, rec.requests[0].IDs, rec.requests[1].IDs)
}

// TestWispGCReportsOneAggregateResult runs an age gc of 450 wisps on a real
// store whose dependencies and text references cross the 200-id chunk
// boundaries, and pins what the run leaves and what its one aggregate result
// says:
//
//   - every selected wisp is gone, and the protected in_progress wisp and the
//     durable issues survive;
//   - no surviving row keeps a dependency on a deleted one;
//   - blocked state agrees with the graph: the durable issue whose only
//     blocker was a chunk-3 wisp is unblocked, the one with a surviving
//     blocker too stays blocked, the in_progress wisp whose blocker sat in
//     chunk 1 is unblocked, and the store's own consistency count is zero;
//   - the durable text citing a chunk-1 wisp and a chunk-3 wisp is rewritten
//     for both, and references_updated counts the two rewrite operations
//     (one per chunk), not distinct rows;
//   - the orphan report names each surviving dependent once, and not the
//     chunk-2 wisp that chunk 1 orphaned before chunk 2 deleted it.
func TestWispGCReportsOneAggregateResult(t *testing.T) {
	ctx := context.Background()
	s, rec := useWispGCRealStore(t)
	seeded := seedWisps(t, s, 450, types.StatusOpen)

	// The protected live step and the durable rows are created before the
	// selection order is read, so creating them cannot reorder it.
	live := &types.Issue{Title: "live patrol step", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	blocker := &types.Issue{Title: "durable blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	freed := &types.Issue{Title: "durable follow-up", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	held := &types.Issue{Title: "durable held", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{live, blocker, freed, held} {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("creating %q: %v", issue.Title, err)
		}
	}

	seededSet := map[string]bool{}
	for _, w := range seeded {
		seededSet[w.ID] = true
	}
	ephemeral := true
	found, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &ephemeral, Limit: 5000})
	if err != nil {
		t.Fatalf("listing wisps: %v", err)
	}
	var order []string
	for _, issue := range found {
		if seededSet[issue.ID] {
			order = append(order, issue.ID)
		}
	}
	if len(order) != 450 {
		t.Fatalf("selection holds %d seeded wisps, want 450", len(order))
	}
	chunk1, chunk2, chunk3 := order[10], order[300], order[420]

	freed.Description = "follows " + chunk1 + " and " + chunk3
	if err := s.UpdateIssue(ctx, freed.ID, map[string]interface{}{"description": freed.Description}, "test"); err != nil {
		t.Fatalf("citing wisps in %s: %v", freed.ID, err)
	}
	for _, dep := range []*types.Dependency{
		// related, so the chunk-2 wisp stays unblocked and selected.
		{IssueID: chunk2, DependsOnID: chunk1, Type: types.DepRelated},
		{IssueID: live.ID, DependsOnID: chunk1, Type: types.DepBlocks},
		{IssueID: freed.ID, DependsOnID: chunk3, Type: types.DepBlocks},
		// The rewrite reaches the rows a deletion's own edges connect, so
		// the text citing chunk 1 needs an edge to it.
		{IssueID: freed.ID, DependsOnID: chunk1, Type: types.DepRelated},
		{IssueID: held.ID, DependsOnID: chunk3, Type: types.DepBlocks},
		{IssueID: held.ID, DependsOnID: blocker.ID, Type: types.DepBlocks},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("adding %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	jsonOutput = true

	stdout, stderr, err := runWispGCCommand(t, wispGCAgeFlags)
	if err != nil {
		t.Fatalf("gc: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if len(rec.requests) != 3 {
		t.Fatalf("gc sent %d deletion requests (sizes %v), want 3 chunks so the fixture crosses chunk boundaries", len(rec.requests), rec.requestSizes())
	}
	for i, id := range []string{chunk1, chunk2, chunk3} {
		if !slices.Contains(rec.requests[i].IDs, id) {
			t.Fatalf("%s is not in request %d; the fixture's chunk positions are wrong", id, i+1)
		}
	}

	if left := wispCount(t, s); left != 1 {
		t.Errorf("%d wisps remain, want only the in_progress step", left)
	}
	survivors := []string{live.ID, blocker.ID, freed.ID, held.ID}
	survivorSet := map[string]bool{}
	for _, id := range survivors {
		survivorSet[id] = true
		if _, err := s.GetIssue(ctx, id); err != nil {
			t.Errorf("%s did not survive: %v", id, err)
		}
		deps, err := s.GetDependencyRecords(ctx, id)
		if err != nil {
			t.Fatalf("dependencies of %s: %v", id, err)
		}
		for _, dep := range deps {
			if seededSet[dep.DependsOnID] {
				t.Errorf("%s still depends on deleted wisp %s", id, dep.DependsOnID)
			}
		}
	}

	blocked, err := s.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("blocked issues: %v", err)
	}
	var blockedIDs []string
	for _, b := range blocked {
		blockedIDs = append(blockedIDs, b.ID)
	}
	if !slices.Equal(blockedIDs, []string{held.ID}) {
		t.Errorf("blocked = %v, want only %s (still blocked by %s)", blockedIDs, held.ID, blocker.ID)
	}
	if stale, err := storageissueops.CountIsBlockedInconsistenciesInTx(ctx, s.DB()); err != nil || stale != 0 {
		t.Errorf("is_blocked inconsistencies = %d (err %v), want 0", stale, err)
	}

	got, err := s.GetIssue(ctx, freed.ID)
	if err != nil {
		t.Fatalf("reading %s: %v", freed.ID, err)
	}
	for _, id := range []string{chunk1, chunk3} {
		if want := "[deleted:" + id + "]"; !bytes.Contains([]byte(got.Description), []byte(want)) {
			t.Errorf("description %q lacks %s: its citation was not rewritten", got.Description, want)
		}
	}

	obj := decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(450) {
		t.Errorf("deleted_count = %v, want 450", got)
	}
	if got := obj["references_updated"]; got != float64(2) {
		t.Errorf("references_updated = %v, want 2 (one rewrite of %s in chunk 1, one in chunk 3)", got, freed.ID)
	}
	var orphans []string
	for _, o := range obj["orphaned_issues"].([]interface{}) {
		orphans = append(orphans, o.(string))
	}
	want := []string{live.ID, freed.ID, held.ID}
	slices.Sort(want)
	if !slices.Equal(orphans, want) {
		t.Errorf("orphaned_issues = %v, want %v (each surviving dependent once, not %s)", orphans, want, chunk2)
	}
	for _, o := range orphans {
		if !survivorSet[o] {
			t.Errorf("orphaned_issues names %s, which this run deleted", o)
		}
	}
}

// TestWispGCPreviewsWriteNothing pins that chunking reaches only a gc that
// deletes: an age --dry-run, a closed preview without --force and a closed
// --dry-run send no deleting request, leave every wisp in place, and keep
// their existing answers.
func TestWispGCPreviewsWriteNothing(t *testing.T) {
	previews := []struct {
		name   string
		status types.Status
		flags  map[string]string
		// keys is the preview's existing --json answer.
		keys []string
		text string
	}{
		{"age dry run", types.StatusOpen, map[string]string{"age": "0s", "dry-run": "true"},
			[]string{"candidates", "cleaned_count", "cleaned_ids", "dry_run", "schema_version"},
			"Dry run: would clean 450 abandoned wisp(s)"},
		{"closed without force", types.StatusClosed, map[string]string{"closed": "true"},
			[]string{"candidates", "dry_run", "schema_version"},
			"Use --force to proceed"},
		{"closed dry run", types.StatusClosed, map[string]string{"closed": "true", "dry-run": "true"},
			nil, "DRY RUN - no changes will be made"},
	}
	for _, preview := range previews {
		for _, asJSON := range []bool{false, true} {
			name := preview.name + "/text"
			if asJSON {
				name = preview.name + "/json"
			}
			t.Run(name, func(t *testing.T) {
				s, rec := useWispGCRealStore(t)
				seedWisps(t, s, 450, preview.status)
				jsonOutput = asJSON

				stdout, stderr, err := runWispGCCommand(t, preview.flags)
				if err != nil {
					t.Fatalf("preview: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
				}
				for i, req := range rec.requests {
					if !req.DryRun {
						t.Errorf("request %d of a preview is a real delete", i+1)
					}
				}
				if left := wispCount(t, s); left != 450 {
					t.Errorf("%d wisps remain after a preview, want 450", left)
				}
				if commandDidWrite.Load() {
					t.Error("a preview marked the command as a write")
				}
				if !asJSON {
					if !strings.Contains(stdout, preview.text) {
						t.Errorf("stdout lacks %q:\n%s", preview.text, stdout)
					}
					return
				}
				if preview.keys == nil {
					return
				}
				obj := decodeSingleJSONObject(t, stdout)
				var keys []string
				for k := range obj {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, preview.keys) {
					t.Errorf("preview keys = %v, want the existing %v", keys, preview.keys)
				}
				if got := obj["candidates"]; got != float64(450) {
					t.Errorf("candidates = %v, want 450", got)
				}
			})
		}
	}
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
