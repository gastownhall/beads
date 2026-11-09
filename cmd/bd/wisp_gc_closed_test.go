package main

// Unit tests for the bounded chunked closed-wisp purge (GH#4684). The
// storage behind these is a stub reduced to the calls runWispPurgeClosed
// makes, in the shape of deleteExactStubStore in delete_exact_id_test.go: the
// embedded nil storage.DoltStorage panics on any call the purge does not
// make, so a wander off the tested path fails loudly.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispClosedGCStubStore serves the closed purge the rows it selects, and
// counts the front-door reads a batch delete's per-id resolution makes
// (SearchIssues with a single id filter, then GetIssue) so a test can pin
// that the forced chunk path performs none after selection.
type wispClosedGCStubStore struct {
	storage.DoltStorage
	issues            []*types.Issue
	deleter           *wispClosedGCStubDeleter
	singletonSearches int
	singleIDSearches  int
	selectionSearches int
	getIssueCalls     int
}

func (s *wispClosedGCStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if len(filter.IDs) > 0 {
		// The resolver's exact-id probe (utils.ResolvePartialIDExact's fast
		// path): one call per id the front door resolves.
		s.singletonSearches++
		s.singleIDSearches += len(filter.IDs)
		var out []*types.Issue
		for _, want := range filter.IDs {
			for _, issue := range s.issues {
				if issue.ID == want {
					out = append(out, issue)
					break
				}
			}
		}
		return out, nil
	}
	s.selectionSearches++
	var out []*types.Issue
	for _, issue := range s.issues {
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Ephemeral != nil && issue.Ephemeral != *filter.Ephemeral {
			continue
		}
		if len(filter.ExcludeTypes) > 0 && slices.Contains(filter.ExcludeTypes, issue.IssueType) {
			continue
		}
		out = append(out, issue)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (s *wispClosedGCStubStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	s.getIssueCalls++
	for _, issue := range s.issues {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (s *wispClosedGCStubStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range ids {
		for _, issue := range s.issues {
			if issue.ID == id {
				out = append(out, issue)
				break
			}
		}
	}
	return out, nil
}

func (s *wispClosedGCStubStore) IsInfraTypeCtx(context.Context, types.IssueType) bool {
	return false
}

func (s *wispClosedGCStubStore) Deleter() (issueops.Deleter, error) {
	return s.deleter, nil
}

// wispClosedGCStubDeleter commits every request it does not refuse, removing
// the deleted ids from the store so a rerun reselects what is left. A request
// containing failID is refused whole with failErr — atomically, before any
// id is removed. orphanOn maps an id in the request to the orphan ids that
// request's deletion reports (survivors, as the role reports them).
type wispClosedGCStubDeleter struct {
	store       *wispClosedGCStubStore
	requests    []issueops.DeleteRequest
	failID      string
	failErr     error
	orphanOn    map[string][]string
	onCommitted func()
}

func (d *wispClosedGCStubDeleter) Delete(_ context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	d.requests = append(d.requests, req)
	if req.DryRun {
		// A preview mutates nothing; the forced chunk path under test never
		// sends DryRun requests.
		return issueops.DeleteResult{DryRun: true, Deleted: len(req.IDs), DeletedIDs: slices.Clone(req.IDs)}, nil
	}
	if d.failID != "" && slices.Contains(req.IDs, d.failID) {
		return issueops.DeleteResult{}, d.failErr
	}
	ids := slices.Clone(req.IDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	d.store.issues = slices.DeleteFunc(slices.Clone(d.store.issues), func(issue *types.Issue) bool {
		return slices.Contains(ids, issue.ID)
	})

	var orphans []string
	for _, id := range ids {
		orphans = append(orphans, d.orphanOn[id]...)
	}
	slices.Sort(orphans)
	orphans = slices.Compact(orphans)

	if d.onCommitted != nil {
		d.onCommitted()
	}
	return issueops.DeleteResult{
		Deleted:           len(ids),
		Dependencies:      len(ids),
		ReferencesUpdated: 1,
		Orphaned:          orphans,
		DeletedIDs:        ids,
	}, nil
}

// wispClosedGCIDs builds n closed-ephemeral wisp ids in selection order.
func wispClosedGCIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("gc-w%04d", i)
	}
	return ids
}

func wispClosedGCIssues(ids []string, pinned bool) []*types.Issue {
	issues := make([]*types.Issue, len(ids))
	for i, id := range ids {
		issues[i] = &types.Issue{
			ID:        id,
			Title:     "closed wisp " + id,
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			Pinned:    pinned,
		}
	}
	return issues
}

// useWispClosedGCStub installs a stub store holding n closed wisps as the
// global store, and restores every global the purge touches.
func useWispClosedGCStub(t *testing.T, n int) (*wispClosedGCStubStore, *wispClosedGCStubDeleter) {
	t.Helper()
	return useWispClosedGCStubIssues(t, wispClosedGCIssues(wispClosedGCIDs(n), false))
}

func useWispClosedGCStubIssues(t *testing.T, issues []*types.Issue) (*wispClosedGCStubStore, *wispClosedGCStubDeleter) {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit
	oldUOW := uowProvider
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto
		uowProvider = oldUOW
		commandDidWrite.Store(oldDidWrite)
		commandDeletedIssueIDs.reset()
	})

	stubStore := &wispClosedGCStubStore{issues: issues}
	stubStore.deleter = &wispClosedGCStubDeleter{store: stubStore, orphanOn: map[string][]string{}}
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = stubStore, context.Background(), false, false, false, "test", ""
	return stubStore, stubStore.deleter
}

// wispGCRunCaptured runs fn with stdout and stderr captured into files
// (no pipe-buffer ceiling) and returns both streams. The package's
// captureStdout/captureStderr helpers each take stdioMutex, so they cannot
// nest; this takes the lock once for both streams.
func wispGCRunCaptured(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	defer outFile.Close()
	errFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	defer errFile.Close()

	stdioMutex.Lock()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	stdioMutex.Unlock()

	outData, readErr := os.ReadFile(outFile.Name())
	if readErr != nil {
		t.Fatalf("read stdout capture: %v", readErr)
	}
	errData, readErr := os.ReadFile(errFile.Name())
	if readErr != nil {
		t.Fatalf("read stderr capture: %v", readErr)
	}
	return string(outData), string(errData)
}

// runWispPurgeClosedCaptured runs the direct closed purge with both streams
// captured, returning stdout, stderr and the command error.
func runWispPurgeClosedCaptured(t *testing.T, dryRun, force bool) (string, string, error) {
	t.Helper()
	var runErr error
	stdout, stderr := wispGCRunCaptured(t, func() {
		runErr = runWispPurgeClosed(rootCtx, dryRun, force, nil)
	})
	return stdout, stderr, runErr
}

// decodeWispPurgeJSON decodes the ENTIRE captured stdout as one JSON object,
// so a second object or stray text fails the decode rather than passing.
func decodeWispPurgeJSON(t *testing.T, stdout string) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("stdout is not exactly one JSON object: %v\nstdout:\n%s", err, stdout)
	}
	return obj
}

func wispPurgeIntField(t *testing.T, obj map[string]interface{}, key string) int {
	t.Helper()
	value, ok := obj[key].(float64)
	if !ok {
		t.Fatalf("JSON field %q = %v (%T); want a number\nobject: %v", key, obj[key], obj[key], obj)
	}
	return int(value)
}

func wispPurgeIDsField(t *testing.T, obj map[string]interface{}, key string) []string {
	t.Helper()
	raw, ok := obj[key].([]interface{})
	if !ok {
		t.Fatalf("JSON field %q = %v (%T); want an array\nobject: %v", key, obj[key], obj[key], obj)
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("JSON field %q[%d] = %v (%T); want a string", key, i, v, v)
		}
		out[i] = s
	}
	return out
}

// requestSizes returns the per-request id counts the deleter received.
func requestSizes(t *testing.T, d *wispClosedGCStubDeleter) []int {
	t.Helper()
	sizes := make([]int, len(d.requests))
	for i, req := range d.requests {
		sizes[i] = len(req.IDs)
	}
	return sizes
}

// TestWispPurgeClosedCommitsBoundedChunks pins the GH#4684 execution shape:
// a forced closed purge of 450 wisps deletes through sequential requests of
// 200, 200 and 50 ids — never one 450-id transaction.
//
// At base this is red: the purge made one 450-id request.
func TestWispPurgeClosedCommitsBoundedChunks(t *testing.T) {
	t.Run("success deletes 200 200 50", func(t *testing.T) {
		s, d := useWispClosedGCStub(t, 450)
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge: %v\nstdout:\n%s", err, stdout)
		}
		if got := requestSizes(t, d); !slices.Equal(got, []int{200, 200, 50}) {
			t.Errorf("deletion requests = %v; want sequential chunks of 200, 200, 50", got)
		}
		for _, req := range d.requests {
			if !req.Force || req.Cascade || req.DryRun {
				t.Errorf("chunk request Force=%v Cascade=%v DryRun=%v; want Force=true, Cascade=false, DryRun=false", req.Force, req.Cascade, req.DryRun)
			}
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 450 {
			t.Errorf("deleted_count = %d; want 450 (all chunks confirmed)", got)
		}
		if got := wispPurgeIDsField(t, obj, "deleted"); len(got) != 450 {
			t.Errorf("deleted lists %d ids; want all 450 confirmed ids", len(got))
		}
		if len(s.issues) != 0 {
			t.Errorf("%d wisps remain after a successful purge; want 0", len(s.issues))
		}
		if !commandDidWrite.Load() {
			t.Error("a successful purge did not mark the command as a write")
		}
		for _, id := range wispClosedGCIDs(450) {
			if !commandDeletedIssueIDs.has(id) {
				t.Errorf("id %s missing from command deletion bookkeeping after a successful purge", id)
			}
		}
	})
}

// TestWispPurgeClosedSkipsPerIDResolution pins that the forced chunk path
// performs no command-layer per-id resolution after selection: the ids the
// scan selected are exact and unique, so the front door's resolve-each-id
// loop (a SearchIssues exact-id probe plus a GetIssue per id) is redundant
// work on the HQ-sized sets that motivated the chunking.
func TestWispPurgeClosedSkipsPerIDResolution(t *testing.T) {
	s, _ := useWispClosedGCStub(t, 450)
	jsonOutput = true

	stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
	if err != nil {
		t.Fatalf("forced closed purge: %v\nstdout:\n%s", err, stdout)
	}
	if s.selectionSearches == 0 {
		t.Error("the purge never scanned for closed wisps; the test observed nothing")
	}
	if s.singletonSearches != 0 || s.getIssueCalls != 0 {
		t.Errorf("command-layer per-id resolution after selection: %d singleton id searches, %d GetIssue calls; want 0 of each",
			s.singletonSearches, s.getIssueCalls)
	}
}
