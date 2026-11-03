package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// purgeBatchStore covers only the store calls `bd mol wisp gc --closed
// --force` makes. The embedded nil interface makes any other call panic
// loudly rather than return a silent zero value.
type purgeBatchStore struct {
	storage.DoltStorage
	issues  []*types.Issue
	deleter *recordingDeleter
}

func (s *purgeBatchStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if len(filter.IDs) == 0 {
		return s.issues, nil
	}
	var out []*types.Issue
	for _, issue := range s.issues {
		if slices.Contains(filter.IDs, issue.ID) {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (s *purgeBatchStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (s *purgeBatchStore) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }

func (s *purgeBatchStore) Deleter() (issueops.Deleter, error) { return s.deleter, nil }

// recordingDeleter records each delete request, the unit one transaction
// commits, and fails the failOn-th request (1-based; 0 never fails).
type recordingDeleter struct {
	requests [][]string
	reqs     []issueops.DeleteRequest
	failOn   int
	orphans  func(batch []string) []string
}

func (d *recordingDeleter) Delete(_ context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	d.requests = append(d.requests, slices.Clone(req.IDs))
	d.reqs = append(d.reqs, req)
	if len(d.requests) == d.failOn {
		return issueops.DeleteResult{}, errors.New("context deadline exceeded")
	}
	result := issueops.DeleteResult{DryRun: req.DryRun, Deleted: len(req.IDs), Labels: 2 * len(req.IDs)}
	if d.orphans != nil {
		result.Orphaned = d.orphans(req.IDs)
	}
	return result, nil
}

// usePurgeBatchStore installs n closed wisps behind the package store for the
// test and returns their ids in candidate order.
func usePurgeBatchStore(t *testing.T, n int, d *recordingDeleter) []string {
	t.Helper()
	ids := make([]string, n)
	issues := make([]*types.Issue, n)
	for i := range n {
		ids[i] = fmt.Sprintf("closed-%04d", i)
		issues[i] = &types.Issue{ID: ids[i], Status: types.StatusClosed, Ephemeral: true, IssueType: types.TypeTask}
	}
	oldStore, oldJSON := store, jsonOutput
	store = &purgeBatchStore{issues: issues, deleter: d}
	jsonOutput = false
	t.Cleanup(func() { store, jsonOutput = oldStore, oldJSON })
	return ids
}

// TestWispPurgeClosedForceCommitsInBoundedBatches is the regression test for
// GH#4684: `bd mol wisp gc --closed --force` timed out on a large store at
// 120s and again at 300s. The whole candidate set (up to 5000 wisps) went to
// the deleter as ONE request, so one transaction: a run cut short by the
// caller's deadline rolled back to nothing, and every retry started over.
// Each request must stay within the 200 wisps deleteWispBatch commits at once,
// and together the requests must delete every candidate exactly once.
func TestWispPurgeClosedForceCommitsInBoundedBatches(t *testing.T) {
	d := &recordingDeleter{}
	ids := usePurgeBatchStore(t, 401, d)

	captureStdout(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})

	if len(d.requests) < 2 {
		t.Fatalf("401 closed wisps went out in %d delete request(s); want one per batch of at most 200", len(d.requests))
	}
	var got []string
	for i, batch := range d.requests {
		if len(batch) > 200 {
			t.Errorf("request %d carries %d wisps; one transaction must carry at most 200", i+1, len(batch))
		}
		req := d.reqs[i]
		if !req.Force || req.Cascade || req.DryRun {
			t.Errorf("request %d: Force=%v Cascade=%v DryRun=%v; want a forced, non-cascading, real delete", i+1, req.Force, req.Cascade, req.DryRun)
		}
		got = append(got, batch...)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("batches deleted %d ids; want each of the %d candidates once, in order", len(got), len(ids))
	}
}

// TestWispPurgeClosedForceReportsProgressOnFailure pins what a cut-short run
// says: the batches before the failure are committed, the command stops there,
// and the error tells the caller how far it got and that a rerun continues.
func TestWispPurgeClosedForceReportsProgressOnFailure(t *testing.T) {
	d := &recordingDeleter{failOn: 2}
	usePurgeBatchStore(t, 401, d)

	var err error
	stderr := captureStderr(t, func() {
		captureStdoutIgnoringError(t, func() {
			err = runWispPurgeClosed(context.Background(), false, true, nil)
		})
	})

	if err == nil {
		t.Fatal("a failed batch must fail the command")
	}
	if len(d.requests) != 2 {
		t.Errorf("sent %d request(s) after the second failed; want the run to stop at the failure", len(d.requests))
	}
	for _, want := range []string{"deleted 200 of 401 closed wisp(s)", "run the command again", "context deadline exceeded"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not say %q", stderr, want)
		}
	}
}

// TestWispPurgeClosedForceOrphansOnlySurvivors: a batch reports as orphaned a
// dependent that a later batch of the same purge deletes. The summary must list
// only the dependents that survive the whole purge.
func TestWispPurgeClosedForceOrphansOnlySurvivors(t *testing.T) {
	d := &recordingDeleter{orphans: func(batch []string) []string {
		out := []string{"live-step"}
		if !slices.Contains(batch, "closed-0200") {
			out = append(out, "closed-0200")
		}
		return out
	}}
	usePurgeBatchStore(t, 201, d)
	jsonOutput = true

	out := captureStdout(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})

	var answer struct {
		DeletedCount int      `json:"deleted_count"`
		Labels       int      `json:"labels_removed"`
		Orphaned     []string `json:"orphaned_issues"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("want one JSON answer for the whole purge, got %q: %v", out, err)
	}
	if answer.DeletedCount != 201 || answer.Labels != 402 {
		t.Errorf("deleted_count=%d labels_removed=%d; want the batches summed (201, 402)", answer.DeletedCount, answer.Labels)
	}
	if !slices.Equal(answer.Orphaned, []string{"live-step"}) {
		t.Errorf("orphaned_issues = %v; want only the surviving dependent [live-step]", answer.Orphaned)
	}
}
