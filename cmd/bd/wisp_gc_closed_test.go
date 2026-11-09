package main

// Unit tests for the bounded chunked closed-wisp purge (GH#4684). The
// storage behind these is a stub reduced to the calls runWispPurgeClosed
// makes, in the shape of deleteExactStubStore in delete_exact_id_test.go: the
// embedded nil storage.DoltStorage panics on any call the purge does not
// make, so a wander off the tested path fails loudly.
//
// The real-store regression (actual persisted chunks against a Dolt server)
// lives in wisp_gc_closed_dolt_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
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

// runWispPurgeClosedProxiedCaptured is runWispPurgeClosedCaptured for the
// proxied route.
func runWispPurgeClosedProxiedCaptured(t *testing.T, dryRun, force bool) (string, string, error) {
	t.Helper()
	var runErr error
	stdout, stderr := wispGCRunCaptured(t, func() {
		runErr = runWispPurgeClosedProxiedServer(rootCtx, dryRun, force, nil)
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
// 200, 200 and 50 ids — never one 450-id transaction — and a request that
// fails mid-run leaves the earlier chunks committed instead of discarding
// the whole purge with the transaction that failed.
//
// At base this is red twice over: the purge made one 450-id request, so the
// injected failure (the request containing the final candidate) erased all
// progress, and no chunk boundary existed to stop at.
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

	t.Run("third chunk failure keeps the first 400", func(t *testing.T) {
		ids := wispClosedGCIDs(450)
		_, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		// The fake deletion operation refuses, atomically, any request
		// containing the final selected candidate.
		d.failID = ids[len(ids)-1]
		d.failErr = context.DeadlineExceeded
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatalf("a purge whose third chunk failed exited nil\nstdout:\n%s", stdout)
		}
		if got := requestSizes(t, d); !slices.Equal(got, []int{200, 200, 50}) {
			t.Fatalf("deletion requests = %v; want two committed chunks of 200 plus the refused 50 — no chunk after the failure", got)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400 confirmed by the two committed chunks", got)
		}
		if got := wispPurgeIntField(t, obj, "remaining_count"); got != 50 {
			t.Errorf("remaining_count = %d; want the 50 unconfirmed ids of the refused chunk", got)
		}
		if _, ok := obj["error"]; !ok {
			t.Errorf("partial-failure JSON has no error field\nobject: %v", obj)
		}
		if hint, ok := obj["hint"].(string); !ok || !strings.Contains(strings.ToLower(hint), "rerun") {
			t.Errorf("partial-failure JSON hint = %v; want rerun guidance", obj["hint"])
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Errorf("exit code = %v, %v; want nonzero", err, code)
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

// TestWispPurgeClosedJSONReportsPartialProgress drives the partial-failure
// outcome through the command boundary and decodes the ENTIRE stdout stream
// as one object: exactly one JSON document, carrying the success fields, the
// confirmed count, the unconfirmed remainder, the cause and a rerun hint, with
// a nonzero exit. A missing-id refusal mid-run is diagnosed the same way —
// no retry, no shrink, no phantom deletes.
func TestWispPurgeClosedJSONReportsPartialProgress(t *testing.T) {
	ids := wispClosedGCIDs(450)

	t.Run("injected deadline failure", func(t *testing.T) {
		_, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		d.failID = ids[420]
		d.failErr = context.DeadlineExceeded
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatalf("a mid-run failure exited nil\nstdout:\n%s", stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400", got)
		}
		if got := wispPurgeIDsField(t, obj, "deleted_ids"); len(got) != 400 {
			t.Errorf("deleted_ids lists %d ids; want the 400 confirmed ones", len(got))
		}
		if got := wispPurgeIntField(t, obj, "remaining_count"); got != 50 {
			t.Errorf("remaining_count = %d; want 50", got)
		}
		if msg, _ := obj["error"].(string); !strings.Contains(msg, "context deadline exceeded") {
			t.Errorf("error = %q; want the original cause", msg)
		}
		if hint, _ := obj["hint"].(string); !strings.Contains(strings.ToLower(hint), "rerun") {
			t.Errorf("hint = %q; want rerun guidance", hint)
		}
		if _, ok := obj["dependencies_removed"].(float64); !ok {
			t.Errorf("dependencies_removed missing from the partial-failure object: %v", obj)
		}
	})

	t.Run("missing id stops without retries", func(t *testing.T) {
		_, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		d.failID = ids[401]
		d.failErr = &issueops.NotFoundError{IDs: []string{ids[401]}}
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatalf("a missing-id refusal exited nil\nstdout:\n%s", stdout)
		}
		if got := len(d.requests); got != 3 {
			t.Errorf("deleter saw %d requests; want exactly 3 (two committed, one refused) — no shrink-and-retry loop", got)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400 confirmed before the refusal", got)
		}
		if msg, _ := obj["error"].(string); !strings.Contains(msg, ids[401]) {
			t.Errorf("error = %q; want it to name the missing id", msg)
		}
	})
}

// TestWispPurgeClosedStopsBeforeNextChunkOnCancellation pins the
// between-chunks context check: a cancellation that lands after a committed
// chunk stops the purge before any further request is issued, the committed
// ids stay in the command's deletion bookkeeping (the auto-export handoff,
// GH#5896), and the error reports the confirmed progress with rerun
// guidance. A cancellation ahead of the first chunk stops the purge with no
// request at all.
func TestWispPurgeClosedStopsBeforeNextChunkOnCancellation(t *testing.T) {
	ids := wispClosedGCIDs(450)

	t.Run("cancelled between chunks", func(t *testing.T) {
		_, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		ctx, cancel := context.WithCancel(context.Background())
		rootCtx = ctx
		d.onCommitted = cancel

		_, stderr, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatal("a cancelled purge exited nil")
		}
		if got := len(d.requests); got != 1 {
			t.Errorf("deleter saw %d requests; want exactly 1 — no further request after cancellation", got)
		}
		for _, id := range ids[:200] {
			if !commandDeletedIssueIDs.has(id) {
				t.Errorf("confirmed id %s missing from command deletion bookkeeping", id)
			}
		}
		if !commandDidWrite.Load() {
			t.Error("a purge with one committed chunk did not mark the command as a write")
		}
		if !strings.Contains(stderr, "Confirmed 200 deletion(s)") || !strings.Contains(stderr, "250 of 450") {
			t.Errorf("stderr does not report the confirmed progress and the remainder:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Hint:") || !strings.Contains(strings.ToLower(stderr), "rerun") {
			t.Errorf("stderr does not carry rerun guidance:\n%s", stderr)
		}
	})

	t.Run("cancelled before the first chunk", func(t *testing.T) {
		_, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rootCtx = ctx

		_, stderr, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatal("a purge cancelled before its first chunk exited nil")
		}
		if got := len(d.requests); got != 0 {
			t.Errorf("deleter saw %d requests; want 0 — the cancelled context stops the purge before the first request", got)
		}
		if !strings.Contains(stderr, "Confirmed 0 deletion(s)") {
			t.Errorf("stderr does not report zero confirmed progress:\n%s", stderr)
		}
		if commandDidWrite.Load() {
			t.Error("a purge that deleted nothing marked the command as a write")
		}
	})
}

// TestWispPurgeClosedOrphanAccountingSpansChunks pins AC-4's orphan report
// across chunk boundaries: a survivor orphaned by an early chunk that a later
// chunk goes on to delete leaves the FINAL orphan report, while one the
// later failing chunk never reached stays in it. Reference-update counts are
// per-operation across chunks, not distinct-survivor counts.
func TestWispPurgeClosedOrphanAccountingSpansChunks(t *testing.T) {
	ids := wispClosedGCIDs(450)
	liveSurvivor := "gc-live" // never selected: stays orphaned for good

	newCase := func(t *testing.T) (*wispClosedGCStubStore, *wispClosedGCStubDeleter) {
		t.Helper()
		s, d := useWispClosedGCStubIssues(t, wispClosedGCIssues(ids, false))
		// Deleting the first chunk's head orphans a later-chunk id and a
		// never-selected survivor, the way the role reports live dependents.
		d.orphanOn[ids[0]] = []string{ids[250], liveSurvivor}
		return s, d
	}

	t.Run("later chunk deletes an earlier chunk's orphan", func(t *testing.T) {
		_, d := newCase(t)
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		orphans := wispPurgeIDsField(t, obj, "orphaned_issues")
		// ids[250] was deleted by its own chunk; only the survivor remains.
		if !slices.Equal(orphans, []string{liveSurvivor}) {
			t.Errorf("orphaned_issues = %v; want only %s (the id a later chunk deleted must leave the report)", orphans, liveSurvivor)
		}
		if got := wispPurgeIntField(t, obj, "references_updated"); got != len(d.requests) {
			t.Errorf("references_updated = %d; want %d (per-operation across chunks)", got, len(d.requests))
		}
	})

	t.Run("failing later chunk leaves its orphan in the report", func(t *testing.T) {
		_, d := newCase(t)
		d.failID = ids[len(ids)-1]
		d.failErr = context.DeadlineExceeded
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatal("a purge whose last chunk failed exited nil")
		}
		obj := decodeWispPurgeJSON(t, stdout)
		orphans := wispPurgeIDsField(t, obj, "orphaned_issues")
		// ids[250] was deleted by the second chunk, so it left the report;
		// the unselected survivor the first chunk orphaned stays.
		if !slices.Equal(orphans, []string{liveSurvivor}) {
			t.Errorf("orphaned_issues = %v; want %q only — chunk 2 deleted the other orphan", orphans, liveSurvivor)
		}
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400", got)
		}
	})
}

// TestWispPurgeClosedReportsScanCap pins AC-6: a forced scan that reaches
// the raw 5000-row limit says so in the outcome (candidates_capped, plus a
// human hint that more candidates MAY remain) without claiming an exact
// total, and never deletes beyond the window. A window entirely occupied by
// protected rows says so too. Below the cap, nothing changes.
func TestWispPurgeClosedReportsScanCap(t *testing.T) {
	t.Run("cap hit reports candidates_capped", func(t *testing.T) {
		_, _ = useWispClosedGCStubIssues(t, wispClosedGCIssues(wispClosedGCIDs(5000), false))
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge at the cap: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if capped, ok := obj["candidates_capped"].(bool); !ok || !capped {
			t.Errorf("candidates_capped = %v; want true after a 5000-row scan", obj["candidates_capped"])
		}
	})

	t.Run("cap hit adds the human hint without a total", func(t *testing.T) {
		// 5200 rows, of which the scan selects 5000: the hint says more MAY
		// remain, and the outcome never claims the full 5200.
		_, _ = useWispClosedGCStubIssues(t, wispClosedGCIssues(wispClosedGCIDs(5200), false))

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge at the cap: %v\nstdout:\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "more closed wisps may remain") {
			t.Errorf("text outcome does not say more candidates may remain:\n%s", stdout)
		}
		if strings.Contains(stdout, "5200") {
			t.Errorf("text outcome claims the exact remaining total:\n%s", stdout)
		}
	})

	t.Run("below the cap reports not capped", func(t *testing.T) {
		_, _ = useWispClosedGCStub(t, 450)
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge below the cap: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if capped, ok := obj["candidates_capped"].(bool); !ok || capped {
			t.Errorf("candidates_capped = %v; want false below the cap", obj["candidates_capped"])
		}
	})

	t.Run("deletes no more than the window", func(t *testing.T) {
		// 5200 rows: the scan stops at 5000 and the purge deletes exactly
		// that window — never the rows beyond it.
		all := wispClosedGCIDs(5200)
		s, _ := useWispClosedGCStubIssues(t, wispClosedGCIssues(all, false))
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge at the cap: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 5000 {
			t.Errorf("deleted_count = %d; want 5000 (the window, not the database)", got)
		}
		if len(s.issues) != 200 {
			t.Errorf("%d wisps remain; want the 200 beyond the window", len(s.issues))
		}
	})

	t.Run("all-protected capped window still says so", func(t *testing.T) {
		_, _ = useWispClosedGCStubIssues(t, wispClosedGCIssues(wispClosedGCIDs(5000), true))

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge of an all-pinned window: %v\nstdout:\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "No closed wisps to delete") {
			t.Errorf("the all-protected window lost its empty-selection message:\n%s", stdout)
		}
		if !strings.Contains(stdout, "more closed wisps may remain") {
			t.Errorf("the all-protected capped window does not say more candidates may remain:\n%s", stdout)
		}
	})

	t.Run("all-protected capped window JSON", func(t *testing.T) {
		_, _ = useWispClosedGCStubIssues(t, wispClosedGCIssues(wispClosedGCIDs(5000), true))
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("forced closed purge of an all-pinned window: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 0 {
			t.Errorf("deleted_count = %d; want 0", got)
		}
		if capped, ok := obj["candidates_capped"].(bool); !ok || !capped {
			t.Errorf("candidates_capped = %v; want true — the raw scan hit the limit even though every row was protected", obj["candidates_capped"])
		}
	})
}

// ---- proxied route: a recording transactional provider ----

// wispGCProxiedStubProvider records every unit of work the proxied purge
// opens and every deletion it attempts, and can refuse the unit whose
// chunk contains a given id — the proxied twin of the direct route's
// injected chunk failure.
type wispGCProxiedStubProvider struct {
	issues      []*types.Issue
	deleteCalls [][]string
	failOnID    string
	failErr     error
	newUOWCalls int
}

func (p *wispGCProxiedStubProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.newUOWCalls++
	return &wispGCProxiedStubUOW{provider: p}, nil
}

func (p *wispGCProxiedStubProvider) Close(context.Context) error { return nil }

type wispGCProxiedStubUOW struct {
	uow.UnitOfWork
	provider   *wispGCProxiedStubProvider
	commitMsgs []string
}

func (u *wispGCProxiedStubUOW) IssueUseCase() domain.IssueUseCase {
	return &wispGCProxiedStubIssues{provider: u.provider}
}

func (u *wispGCProxiedStubUOW) ConfigUseCase() domain.ConfigUseCase {
	return wispGCProxiedStubConfig{}
}

func (u *wispGCProxiedStubUOW) Commit(_ context.Context, message string) error {
	u.commitMsgs = append(u.commitMsgs, message)
	return nil
}

func (u *wispGCProxiedStubUOW) Close(context.Context) {}

type wispGCProxiedStubConfig struct {
	domain.ConfigUseCase
}

func (wispGCProxiedStubConfig) IsInfraTypeCtx(context.Context, types.IssueType) (bool, error) {
	return false, nil
}

type wispGCProxiedStubIssues struct {
	domain.IssueUseCase
	provider *wispGCProxiedStubProvider
}

func (u *wispGCProxiedStubIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var items []*types.Issue
	for _, issue := range u.provider.issues {
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Ephemeral != nil && issue.Ephemeral != *filter.Ephemeral {
			continue
		}
		items = append(items, issue)
		if filter.Limit > 0 && len(items) >= filter.Limit {
			break
		}
	}
	return domain.SearchPage{Items: items}, nil
}

func (u *wispGCProxiedStubIssues) DeleteIssues(_ context.Context, params domain.DeleteIssuesParams, _ string) (domain.DeleteIssuesResult, error) {
	u.provider.deleteCalls = append(u.provider.deleteCalls, slices.Clone(params.IDs))
	if u.provider.failOnID != "" && slices.Contains(params.IDs, u.provider.failOnID) {
		return domain.DeleteIssuesResult{}, u.provider.failErr
	}
	ids := slices.Clone(params.IDs)
	slices.Sort(ids)
	return domain.DeleteIssuesResult{
		DeletedCount:      len(ids),
		DependenciesCount: len(ids),
		DeletedIDs:        ids,
	}, nil
}

// useWispGCProxiedStub installs the recording provider as the proxied
// UOW provider with n closed wisps behind it.
func useWispGCProxiedStub(t *testing.T, n int) *wispGCProxiedStubProvider {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit
	oldUOW := uowProvider
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto
		uowProvider = oldUOW
	})

	p := &wispGCProxiedStubProvider{issues: wispClosedGCIssues(wispClosedGCIDs(n), false)}
	rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = context.Background(), false, false, true, "test", ""
	uowProvider = p
	return p
}

// TestWispPurgeClosedProxiedCommitsBoundedChunks pins AC-3: the proxied
// forced closed purge runs one unit of work per bounded chunk — never a
// single 450-id transaction — and when a later unit fails, the units that
// committed stay accounted for: the outcome reports their confirmed
// deletions and the unconfirmed remainder, and the failed unit's
// intermediate counters are not published.
func TestWispPurgeClosedProxiedCommitsBoundedChunks(t *testing.T) {
	t.Run("success one unit per 200", func(t *testing.T) {
		p := useWispGCProxiedStub(t, 450)
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedProxiedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("proxied forced closed purge: %v\nstdout:\n%s", err, stdout)
		}
		sizes := make([]int, len(p.deleteCalls))
		for i, ids := range p.deleteCalls {
			sizes[i] = len(ids)
		}
		if !slices.Equal(sizes, []int{200, 200, 50}) {
			t.Errorf("deletion units = %v; want 200, 200, 50", sizes)
		}
		if p.newUOWCalls != 1+len(p.deleteCalls) {
			t.Errorf("units of work opened = %d; want 1 selection read + %d mutation units", p.newUOWCalls, len(p.deleteCalls))
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 450 {
			t.Errorf("deleted_count = %d; want 450", got)
		}
	})

	t.Run("failing third unit keeps the first two", func(t *testing.T) {
		p := useWispGCProxiedStub(t, 450)
		p.failOnID = wispClosedGCIDs(450)[449]
		p.failErr = context.DeadlineExceeded
		jsonOutput = true

		stdout, _, err := runWispPurgeClosedProxiedCaptured(t, false, true)
		if err == nil {
			t.Fatalf("a proxied purge whose third unit failed exited nil\nstdout:\n%s", stdout)
		}
		sizes := make([]int, len(p.deleteCalls))
		for i, ids := range p.deleteCalls {
			sizes[i] = len(ids)
		}
		if !slices.Equal(sizes, []int{200, 200, 50}) {
			t.Fatalf("deletion units = %v; want two committed 200s plus the refused 50 — no unit after the failure", sizes)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400 — the committed units stay accounted for", got)
		}
		if got := wispPurgeIntField(t, obj, "remaining_count"); got != 50 {
			t.Errorf("remaining_count = %d; want 50", got)
		}
		if got := wispPurgeIntField(t, obj, "dependencies_removed"); got != 400 {
			t.Errorf("dependencies_removed = %d; want 400 — the failed unit's counters are not published", got)
		}
	})
}
