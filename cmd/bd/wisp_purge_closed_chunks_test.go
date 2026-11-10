package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispPurgeStubStore is the store a forced `bd mol wisp gc --closed` reads on
// the direct route: the closed-wisp scan (SearchIssues with a status filter),
// the infra-type check, and Deleter. It also answers the front door's per-id
// resolution (SearchIssues with IDs, SearchIssueIDs, GetIssue) so the base
// path runs to an assertion instead of a panic, and counts those calls in
// lookups: a forced purge hands the ids it just selected straight to the role,
// so after the scan it must make none. The embedded nil DoltStorage makes any
// other call panic, the same shape as deleteExactStubStore.
type wispPurgeStubStore struct {
	storage.DoltStorage
	closed  []string
	infra   map[string]bool
	deleter *wispPurgeStubDeleter
	scanned bool
	lookups int
}

func (s *wispPurgeStubStore) issue(id string) *types.Issue {
	issueType := types.TypeTask
	if s.infra[id] {
		issueType = "agent"
	}
	return &types.Issue{ID: id, Title: "wisp " + id, Status: types.StatusClosed, Ephemeral: true, IssueType: issueType}
}

func (s *wispPurgeStubStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if len(filter.IDs) > 0 {
		s.lookups++
		for _, id := range filter.IDs {
			if slices.Contains(s.closed, id) && !s.deleter.deleted[id] {
				out = append(out, s.issue(id))
			}
		}
		return out, nil
	}
	s.scanned = true
	for _, id := range s.closed {
		if s.deleter.deleted[id] {
			continue
		}
		if filter.Limit > 0 && len(out) == filter.Limit {
			break
		}
		out = append(out, s.issue(id))
	}
	return out, nil
}

func (s *wispPurgeStubStore) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	s.lookups++
	return nil, nil
}

func (s *wispPurgeStubStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	s.lookups++
	if slices.Contains(s.closed, id) && !s.deleter.deleted[id] {
		return s.issue(id), nil
	}
	return nil, storage.ErrNotFound
}

func (s *wispPurgeStubStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "fx", nil
	}
	return "", nil
}

func (s *wispPurgeStubStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{"issue_prefix": "fx"}, nil
}

func (s *wispPurgeStubStore) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return t == "agent"
}

func (s *wispPurgeStubStore) Deleter() (issueops.Deleter, error) {
	return s.deleter, nil
}

// wispPurgeStubDeleter is the deletion role: each request is atomic, so a
// request that fails deletes nothing and one that succeeds deletes all of its
// ids. dependents models the edges the role reports orphans from: a surviving
// row that depended on something a request deleted.
type wispPurgeStubDeleter struct {
	requests   []issueops.DeleteRequest
	deleted    map[string]bool
	dependents map[string][]string
	// fail, when it returns an error, refuses the request before it deletes.
	fail func(req issueops.DeleteRequest) error
	// committed runs after each request that deleted its rows.
	committed func(req issueops.DeleteRequest)
}

func (d *wispPurgeStubDeleter) Delete(_ context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	d.requests = append(d.requests, req)
	if req.DryRun {
		return issueops.DeleteResult{DryRun: true, Deleted: len(req.IDs)}, nil
	}
	if d.fail != nil {
		if err := d.fail(req); err != nil {
			return issueops.DeleteResult{}, err
		}
	}
	for _, id := range req.IDs {
		d.deleted[id] = true
	}
	orphaned := map[string]bool{}
	for _, id := range req.IDs {
		for _, dep := range d.dependents[id] {
			if !d.deleted[dep] {
				orphaned[dep] = true
			}
		}
	}
	var orphans []string
	for id := range orphaned {
		orphans = append(orphans, id)
	}
	sort.Strings(orphans)
	deletedIDs := slices.Clone(req.IDs)
	sort.Strings(deletedIDs)
	if d.committed != nil {
		d.committed(req)
	}
	return issueops.DeleteResult{
		Deleted:      len(req.IDs),
		Dependencies: len(req.IDs),
		Labels:       2 * len(req.IDs),
		Events:       3 * len(req.IDs),
		Orphaned:     orphans,
		DeletedIDs:   deletedIDs,
	}, nil
}

// wispPurgeIDs names n closed wisps in selection order.
func wispPurgeIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("fx-wisp-%04d", i)
	}
	return ids
}

// useWispPurgeStub installs a store holding n closed wisps as the one the
// direct route reads, and restores every global the command touches.
func useWispPurgeStub(t *testing.T, n int) *wispPurgeStubStore {
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

	s := &wispPurgeStubStore{
		closed:  wispPurgeIDs(n),
		infra:   map[string]bool{},
		deleter: &wispPurgeStubDeleter{deleted: map[string]bool{}, dependents: map[string][]string{}},
	}
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = s, context.Background(), false, false, false, "test"
	return s
}

// captureWispGC runs fn with stdout and stderr captured, draining both while
// fn runs so a large --json answer cannot fill a pipe and block it.
func captureWispGC(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	rOut, wOut, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	rErr, wErr, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	drain := func(r *os.File) <-chan string {
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r)
			done <- buf.String()
		}()
		return done
	}
	outDone, errDone := drain(rOut), drain(rErr)

	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	func() {
		defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
		err = fn()
	}()
	_ = wOut.Close()
	_ = wErr.Close()
	stdout, stderr = <-outDone, <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return stdout, stderr, err
}

// decodeSingleJSONObject decodes the whole of out as exactly one JSON object:
// a second object (an error envelope printed after the answer) or any stray
// text around it fails.
func decodeSingleJSONObject(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("stdout is not exactly one JSON object: %v\nstdout:\n%s", err, out)
	}
	return obj
}

func requestSizes(reqs []issueops.DeleteRequest) []int {
	sizes := make([]int, 0, len(reqs))
	for _, req := range reqs {
		sizes = append(sizes, len(req.IDs))
	}
	return sizes
}

// assertExportBookkeeping pins the GH#5896 handoff to auto-export: a write
// happened, and exactly the committed ids are recorded as deleted. Without
// it the export's orphan guard sees them as JSONL-only records and wedges.
// (From GH#8103.)
func assertExportBookkeeping(t *testing.T, committed, notCommitted []string) {
	t.Helper()
	if !commandDidWrite.Load() {
		t.Error("commandDidWrite is false after committed batches; auto-export would skip the write")
	}
	for _, id := range committed {
		if !commandDeletedIssueIDs.has(id) {
			t.Errorf("committed id %s is not in commandDeletedIssueIDs; the export orphan guard would wedge on it", id)
			break
		}
	}
	for _, id := range notCommitted {
		if commandDeletedIssueIDs.has(id) {
			t.Errorf("id %s of an uncommitted batch is in commandDeletedIssueIDs", id)
			break
		}
	}
}

// TestWispPurgeClosedForceCommitsInBoundedBatches is GH#8103's regression
// test for GH#4684, on a selection that is not a multiple of the chunk size:
// every request stays within the 200 wisps deleteWispBatch commits at once,
// is a forced, non-cascading, real delete, and together the requests delete
// every candidate exactly once, each recorded for auto-export.
func TestWispPurgeClosedForceCommitsInBoundedBatches(t *testing.T) {
	s := useWispPurgeStub(t, 401)

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})
	if err != nil {
		t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if len(s.deleter.requests) < 2 {
		t.Fatalf("401 closed wisps went out in %d delete request(s); want one per batch of at most 200", len(s.deleter.requests))
	}
	var got []string
	for i, req := range s.deleter.requests {
		if len(req.IDs) > 200 {
			t.Errorf("request %d carries %d wisps; one transaction must carry at most 200", i+1, len(req.IDs))
		}
		if !req.Force || req.Cascade || req.DryRun {
			t.Errorf("request %d: Force=%v Cascade=%v DryRun=%v; want a forced, non-cascading, real delete", i+1, req.Force, req.Cascade, req.DryRun)
		}
		got = append(got, req.IDs...)
	}
	if !slices.Equal(got, s.closed) {
		t.Errorf("batches deleted %d ids; want each of the %d candidates once, in order", len(got), len(s.closed))
	}
	assertExportBookkeeping(t, s.closed, nil)
}

// TestWispPurgeClosedSubmitsBoundedChunks pins that a forced closed purge on
// the direct route hands the deletion role the selection in sequential
// requests of at most 200 ids, in selection order, each forced and uncascaded,
// and reports every row the requests deleted (bd-d2zqqc, GH#4684). One request
// for the whole selection is one transaction for up to 5000 wisps.
func TestWispPurgeClosedSubmitsBoundedChunks(t *testing.T) {
	s := useWispPurgeStub(t, 450)

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})
	if err != nil {
		t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if got, want := requestSizes(s.deleter.requests), []int{200, 200, 50}; !slices.Equal(got, want) {
		t.Fatalf("request sizes = %v, want %v", got, want)
	}
	var sent []string
	for i, req := range s.deleter.requests {
		if !req.Force || req.Cascade || req.DryRun || req.ExpectedVersion != nil {
			t.Errorf("request %d = Force %v Cascade %v DryRun %v ExpectedVersion %v, want forced, uncascaded, not a dry run, unguarded",
				i, req.Force, req.Cascade, req.DryRun, req.ExpectedVersion)
		}
		sent = append(sent, req.IDs...)
	}
	if !slices.Equal(sent, s.closed) {
		t.Errorf("requests did not carry the selection once each in selection order")
	}
	if len(s.deleter.deleted) != 450 {
		t.Errorf("deleted %d wisps, want 450", len(s.deleter.deleted))
	}
	if !bytes.Contains([]byte(stdout), []byte("Deleted 450 issue(s)")) {
		t.Errorf("stdout does not confirm 450 deletions:\n%s", stdout)
	}
	assertExportBookkeeping(t, s.closed, nil)
}

// TestWispPurgeClosedSkipsPerIDResolution pins that the forced purge does not
// resolve each selected id again through the front door: the ids came from
// this store's own scan a moment earlier, and resolving 5000 of them one at a
// time (exact-id search plus GetIssue each) before deleting anything is cost
// with no information in it.
func TestWispPurgeClosedSkipsPerIDResolution(t *testing.T) {
	s := useWispPurgeStub(t, 450)

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})
	if err != nil {
		t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !s.scanned {
		t.Fatal("the closed-wisp scan never ran")
	}
	if s.lookups != 0 {
		t.Errorf("front door made %d per-id lookups after the scan, want 0", s.lookups)
	}
}

// failRequestContaining refuses, atomically, the request that carries id.
func failRequestContaining(id string, err error) func(issueops.DeleteRequest) error {
	return func(req issueops.DeleteRequest) error {
		if slices.Contains(req.IDs, id) {
			return err
		}
		return nil
	}
}

// TestWispPurgeClosedCommitsBoundedChunks pins that a deletion request that
// fails after earlier ones committed stops the purge without discarding them:
// the two committed chunks stay deleted, no later request is sent, the command
// exits nonzero, and its text names the confirmed deletions, the selected wisps
// still unconfirmed, the cause and how to continue. With the whole selection in
// one request, the failure took every deletion of the run with it.
func TestWispPurgeClosedCommitsBoundedChunks(t *testing.T) {
	s := useWispPurgeStub(t, 450)
	s.deleter.fail = failRequestContaining(s.closed[449], context.DeadlineExceeded)

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispPurgeClosed(context.Background(), false, true, nil)
	})
	if err == nil {
		t.Fatalf("runWispPurgeClosed succeeded, want a nonzero exit\nstdout:\n%s", stdout)
	}
	if code, ok := exitCodeFromError(err); !ok || code == 0 {
		t.Errorf("error = %v, want an already-reported nonzero exit", err)
	}
	if got, want := requestSizes(s.deleter.requests), []int{200, 200, 50}; !slices.Equal(got, want) {
		t.Errorf("request sizes = %v, want %v (no request after the failed one)", got, want)
	}
	if len(s.deleter.deleted) != 400 {
		t.Errorf("deleted %d wisps, want the 400 of the two committed chunks", len(s.deleter.deleted))
	}
	for _, want := range []string{"Deleted 400 issue(s)"} {
		if !bytes.Contains([]byte(stdout), []byte(want)) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{"50", "context deadline exceeded", "Rerun the same command"} {
		if !bytes.Contains([]byte(stderr), []byte(want)) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	assertExportBookkeeping(t, s.closed[:400], s.closed[400:])
}

// TestWispPurgeClosedJSONReportsPartialProgress pins the --json half: a purge
// stopped by a failed request prints exactly one JSON object on stdout, with
// the success keys counting only the committed chunks plus remaining_count,
// error and hint, and exits nonzero without a second error envelope. A
// missing-id refusal mid-run is diagnosed the same way: no retry, no shrink,
// no phantom deletes.
func TestWispPurgeClosedJSONReportsPartialProgress(t *testing.T) {
	t.Run("injected deadline failure", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		s.deleter.fail = failRequestContaining(s.closed[449], context.DeadlineExceeded)
		jsonOutput = true

		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, true, nil)
		})
		if err == nil {
			t.Fatalf("runWispPurgeClosed succeeded, want a nonzero exit\nstdout:\n%s", stdout)
		}
		if !isReportedExit(err) {
			t.Errorf("error = %v, want an already-reported exit (the caller must not print it again)", err)
		}
		if bytes.Contains([]byte(stderr), []byte("{")) {
			t.Errorf("stderr carries a JSON value; the answer belongs in the one stdout object:\n%s", stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(400) {
			t.Errorf("deleted_count = %v, want 400", got)
		}
		if got, _ := obj["deleted"].([]interface{}); len(got) != 400 {
			t.Errorf("deleted names %d ids, want 400", len(got))
		}
		if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 400 {
			t.Errorf("deleted_ids names %d ids, want 400", len(got))
		}
		if got := obj["remaining_count"]; got != float64(50) {
			t.Errorf("remaining_count = %v, want 50", got)
		}
		if got, _ := obj["error"].(string); !bytes.Contains([]byte(got), []byte("context deadline exceeded")) {
			t.Errorf("error = %q, want the cause", got)
		}
		if got, _ := obj["hint"].(string); got != wispPurgeRerunHint {
			t.Errorf("hint = %q, want the same-command rerun guidance %q", got, wispPurgeRerunHint)
		}
		if _, ok := obj["dependencies_removed"].(float64); !ok {
			t.Errorf("dependencies_removed missing from the partial-failure object: %v", obj)
		}
		assertExportBookkeeping(t, s.closed[:400], s.closed[400:])
	})

	t.Run("missing id stops without retries", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		missing := s.closed[401]
		s.deleter.fail = failRequestContaining(missing, &issueops.NotFoundError{IDs: []string{missing}})
		jsonOutput = true

		stdout, _, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, true, nil)
		})
		if err == nil {
			t.Fatalf("a missing-id refusal exited nil\nstdout:\n%s", stdout)
		}
		if got := len(s.deleter.requests); got != 3 {
			t.Errorf("deleter saw %d requests; want exactly 3 (two committed, one refused): no shrink-and-retry loop", got)
		}
		if len(s.deleter.deleted) != 400 {
			t.Errorf("deleted %d wisps, want the 400 of the two committed chunks", len(s.deleter.deleted))
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(400) {
			t.Errorf("deleted_count = %v, want 400 confirmed before the refusal", got)
		}
		if got := obj["remaining_count"]; got != float64(50) {
			t.Errorf("remaining_count = %v, want 50", got)
		}
		if msg, _ := obj["error"].(string); !bytes.Contains([]byte(msg), []byte(missing)) {
			t.Errorf("error = %q; want it to name the missing id", msg)
		}
		assertExportBookkeeping(t, s.closed[:400], s.closed[400:])
	})
}

// wispPurgeProxiedProvider is the proxied route's unit-of-work provider. Every
// NewUOW opens a unit of work; the closed-wisp scan reads through one, and
// each deletion is one more. Only a unit of work that commits applies its
// deletions, so an aborted transaction leaves no rows gone and no counts.
type wispPurgeProxiedProvider struct {
	closed  []string
	deleted map[string]bool
	// units is every unit of work opened, in order.
	units []*wispPurgeProxiedUOW
	// fail, when it returns an error, refuses a DeleteIssues call.
	fail func(ids []string) error
}

func (p *wispPurgeProxiedProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	u := &wispPurgeProxiedUOW{p: p}
	p.units = append(p.units, u)
	return u, nil
}

func (p *wispPurgeProxiedProvider) Close(context.Context) error { return nil }

// deletionUnits is the units of work that asked to delete, in order.
func (p *wispPurgeProxiedProvider) deletionUnits() []*wispPurgeProxiedUOW {
	var out []*wispPurgeProxiedUOW
	for _, u := range p.units {
		if u.params != nil {
			out = append(out, u)
		}
	}
	return out
}

type wispPurgeProxiedUOW struct {
	uow.UnitOfWork
	p         *wispPurgeProxiedProvider
	params    *domain.DeleteIssuesParams
	committed bool
	message   string
}

func (u *wispPurgeProxiedUOW) Close(context.Context) {}

func (u *wispPurgeProxiedUOW) Commit(_ context.Context, message string) error {
	u.committed, u.message = true, message
	if u.params != nil {
		for _, id := range u.params.IDs {
			u.p.deleted[id] = true
		}
	}
	return nil
}

func (u *wispPurgeProxiedUOW) IssueUseCase() domain.IssueUseCase {
	return wispPurgeProxiedIssues{u: u}
}

func (u *wispPurgeProxiedUOW) ConfigUseCase() domain.ConfigUseCase {
	return wispPurgeProxiedConfig{}
}

type wispPurgeProxiedIssues struct {
	domain.IssueUseCase
	u *wispPurgeProxiedUOW
}

func (s wispPurgeProxiedIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var page domain.SearchPage
	for _, id := range s.u.p.closed {
		if s.u.p.deleted[id] {
			continue
		}
		if filter.Limit > 0 && len(page.Items) == filter.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, &types.Issue{ID: id, Status: types.StatusClosed, Ephemeral: true, IssueType: types.TypeTask})
	}
	return page, nil
}

func (s wispPurgeProxiedIssues) DeleteIssues(_ context.Context, params domain.DeleteIssuesParams, _ string) (domain.DeleteIssuesResult, error) {
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
	}, nil
}

type wispPurgeProxiedConfig struct {
	domain.ConfigUseCase
}

func (wispPurgeProxiedConfig) IsInfraTypeCtx(_ context.Context, t types.IssueType) (bool, error) {
	return t == "agent", nil
}

// useWispPurgeProxied installs a proxied provider holding n closed wisps.
func useWispPurgeProxied(t *testing.T, n int) *wispPurgeProxiedProvider {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	oldProvider, oldJSON, oldQuiet, oldActor := uowProvider, jsonOutput, quietFlag, actor
	t.Cleanup(func() {
		uowProvider, jsonOutput, quietFlag, actor = oldProvider, oldJSON, oldQuiet, oldActor
	})
	p := &wispPurgeProxiedProvider{closed: wispPurgeIDs(n), deleted: map[string]bool{}}
	uowProvider, jsonOutput, quietFlag, actor = p, false, false, "test"
	return p
}

// TestWispPurgeClosedProxiedBoundsEachUnitOfWork pins the proxied route's
// half of bd-d2zqqc: every deletion unit of work carries at most 200 of the
// selected ids, uncascaded, and when a later unit fails the ones that
// committed before it stay deleted and stay counted in the one JSON answer.
func TestWispPurgeClosedProxiedBoundsEachUnitOfWork(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		p := useWispPurgeProxied(t, 450)
		jsonOutput = true

		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosedProxiedServer(context.Background(), false, true, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosedProxiedServer: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
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
			t.Fatalf("deletion unit sizes = %v, want %v", sizes, want)
		}
		if !slices.Equal(sent, p.closed) {
			t.Errorf("deletion units did not carry the selection once each in selection order")
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(450) {
			t.Errorf("deleted_count = %v, want 450", got)
		}
		if got := obj["references_updated"]; got != float64(3) {
			t.Errorf("references_updated = %v, want 3 (one per unit, summed)", got)
		}
	})

	t.Run("later unit fails", func(t *testing.T) {
		p := useWispPurgeProxied(t, 450)
		jsonOutput = true
		boom := fmt.Errorf("injected deletion failure")
		last := p.closed[449]
		p.fail = func(ids []string) error {
			if slices.Contains(ids, last) {
				return boom
			}
			return nil
		}

		stdout, _, err := captureWispGC(t, func() error {
			return runWispPurgeClosedProxiedServer(context.Background(), false, true, nil)
		})
		if err == nil || !isReportedExit(err) {
			t.Fatalf("error = %v, want an already-reported nonzero exit\nstdout:\n%s", err, stdout)
		}
		units := p.deletionUnits()
		if len(units) != 3 {
			t.Fatalf("opened %d deletion units, want 3 (none after the failed one)", len(units))
		}
		if units[2].committed {
			t.Error("the failed unit of work committed")
		}
		if len(p.deleted) != 400 {
			t.Errorf("%d wisps deleted, want the 400 of the two committed units", len(p.deleted))
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["deleted_count"]; got != float64(400) {
			t.Errorf("deleted_count = %v, want 400 (the failed unit's counts are not published)", got)
		}
		if got := obj["remaining_count"]; got != float64(50) {
			t.Errorf("remaining_count = %v, want 50", got)
		}
		if got, _ := obj["error"].(string); !bytes.Contains([]byte(got), []byte(boom.Error())) {
			t.Errorf("error = %q, want the cause", got)
		}
		if _, ok := obj["deleted_ids"]; ok {
			t.Error("proxied answer gained deleted_ids, which that route has never printed")
		}
	})
}

// TestWispPurgeClosedOrphanReportDropsLaterChunkDeletions pins the orphan
// report across chunk boundaries. Each request names the survivors that
// depended on what it deleted, and a selected wisp in a later chunk is such a
// survivor when its dependency sits in an earlier one; once its own chunk
// deletes it, it is no longer orphaned and must leave the report. A live
// dependent stays, once, however many chunks orphan it; a selected wisp whose
// chunk never committed stays too, because it is still there.
func TestWispPurgeClosedOrphanReportDropsLaterChunkDeletions(t *testing.T) {
	const live = "fx-live-step"

	t.Run("all chunks commit", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		jsonOutput = true
		// closed[300] (chunk 2) and the live step depend on closed[10]
		// (chunk 1); the live step depends on closed[420] (chunk 3) too.
		s.deleter.dependents[s.closed[10]] = []string{s.closed[300], live}
		s.deleter.dependents[s.closed[420]] = []string{live}

		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, true, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		got, _ := obj["orphaned_issues"].([]interface{})
		if len(got) != 1 || got[0] != live {
			t.Errorf("orphaned_issues = %v, want only the live step %s, once", got, live)
		}
	})

	t.Run("later chunk fails", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		jsonOutput = true
		s.deleter.dependents[s.closed[10]] = []string{s.closed[420], live}
		s.deleter.fail = failRequestContaining(s.closed[449], context.Canceled)

		stdout, _, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, true, nil)
		})
		if err == nil {
			t.Fatalf("runWispPurgeClosed succeeded, want the injected failure\nstdout:\n%s", stdout)
		}
		obj := decodeSingleJSONObject(t, stdout)
		got, _ := obj["orphaned_issues"].([]interface{})
		want := []interface{}{s.closed[420], live}
		sort.Slice(got, func(i, j int) bool { return got[i].(string) < got[j].(string) })
		sort.Slice(want, func(i, j int) bool { return want[i].(string) < want[j].(string) })
		if !slices.Equal(got, want) {
			t.Errorf("orphaned_issues = %v, want %v (the unconfirmed wisp survived)", got, want)
		}
	})
}

// TestWispPurgeClosedStopsOnCancellationBetweenChunks pins that a purge whose
// context is canceled after a chunk committed sends no further request, keeps
// the committed ids in this command's deletion bookkeeping (what its own
// auto-export reads, GH#5896), and reports the confirmed progress with the
// rerun hint. A role that does not happen to check the context must not be
// the only thing standing between a canceled command and another write. A
// cancellation ahead of the first chunk stops the purge with no request at
// all and nothing recorded.
func TestWispPurgeClosedStopsOnCancellationBetweenChunks(t *testing.T) {
	t.Run("canceled after the first chunk", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.deleter.committed = func(issueops.DeleteRequest) { cancel() }

		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(ctx, false, true, nil)
		})
		if err == nil {
			t.Fatalf("runWispPurgeClosed succeeded after cancellation\nstdout:\n%s", stdout)
		}
		if got := len(s.deleter.requests); got != 1 {
			t.Fatalf("sent %d requests, want 1 (none after the cancellation)", got)
		}
		// Every id of the committed chunk is recorded, none of the rest.
		assertExportBookkeeping(t, s.closed[:200], s.closed[200:])
		if !bytes.Contains([]byte(stdout), []byte("Deleted 200 issue(s)")) {
			t.Errorf("stdout does not report the 200 confirmed deletions:\n%s", stdout)
		}
		for _, want := range []string{"250", "context canceled", "Rerun the same command"} {
			if !bytes.Contains([]byte(stderr), []byte(want)) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
	})

	t.Run("canceled before the first chunk", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(ctx, false, true, nil)
		})
		if err == nil {
			t.Fatalf("a purge canceled before its first chunk exited nil\nstdout:\n%s", stdout)
		}
		if got := len(s.deleter.requests); got != 0 {
			t.Errorf("sent %d requests, want 0: the canceled context stops the purge before the first request", got)
		}
		if len(s.deleter.deleted) != 0 {
			t.Errorf("deleted %d wisps, want 0", len(s.deleter.deleted))
		}
		if !bytes.Contains([]byte(stdout), []byte("Deleted 0 issue(s)")) {
			t.Errorf("stdout does not report zero confirmed deletions:\n%s", stdout)
		}
		for _, want := range []string{"0 of 450", "context canceled"} {
			if !bytes.Contains([]byte(stderr), []byte(want)) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if commandDidWrite.Load() {
			t.Error("a purge that deleted nothing marked the command as a write")
		}
		for _, id := range s.closed {
			if commandDeletedIssueIDs.has(id) {
				t.Errorf("unsent id %s recorded as deleted", id)
				break
			}
		}
	})
}

// TestWispPurgeClosedReportsCappedScan pins that a forced purge whose scan
// came back with the full 5000 rows says so: candidates_capped in --json, and
// a text hint that more closed wisps may remain. It deletes nothing beyond
// that window and claims no total. The cap is read from the raw scan, before
// pinned and infra wisps are set aside, so a window those fill entirely still
// reports it rather than "nothing to delete" alone.
func TestWispPurgeClosedReportsCappedScan(t *testing.T) {
	run := func(t *testing.T, s *wispPurgeStubStore, asJSON bool) (string, string) {
		t.Helper()
		jsonOutput = asJSON
		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, true, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		return stdout, stderr
	}
	const mayRemain = "more closed wisps may remain"

	t.Run("scan at the limit json", func(t *testing.T) {
		s := useWispPurgeStub(t, 6000)
		stdout, _ := run(t, s, true)
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["candidates_capped"]; got != true {
			t.Errorf("candidates_capped = %v, want true", got)
		}
		if got := obj["deleted_count"]; got != float64(5000) {
			t.Errorf("deleted_count = %v, want 5000 (the scanned window only)", got)
		}
		if len(s.deleter.deleted) != 5000 {
			t.Errorf("deleted %d wisps, want the 5000 of the window", len(s.deleter.deleted))
		}
	})

	t.Run("scan at the limit text", func(t *testing.T) {
		s := useWispPurgeStub(t, 5000)
		stdout, _ := run(t, s, false)
		if !bytes.Contains([]byte(stdout), []byte(mayRemain)) {
			t.Errorf("stdout lacks %q:\n%s", mayRemain, stdout)
		}
	})

	t.Run("scan below the limit", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		stdout, _ := run(t, s, true)
		obj := decodeSingleJSONObject(t, stdout)
		if got, ok := obj["candidates_capped"]; !ok || got != false {
			t.Errorf("candidates_capped = %v (present %v), want false", got, ok)
		}
		s2 := useWispPurgeStub(t, 450)
		stdout, _ = run(t, s2, false)
		if bytes.Contains([]byte(stdout), []byte(mayRemain)) {
			t.Errorf("uncapped scan says more may remain:\n%s", stdout)
		}
	})

	t.Run("window entirely protected", func(t *testing.T) {
		s := useWispPurgeStub(t, 5001)
		for _, id := range s.closed[:5000] {
			s.infra[id] = true
		}
		stdout, _ := run(t, s, true)
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["candidates_capped"]; got != true {
			t.Errorf("candidates_capped = %v, want true", got)
		}
		if got := obj["deleted_count"]; got != float64(0) {
			t.Errorf("deleted_count = %v, want 0", got)
		}
		if len(s.deleter.requests) != 0 {
			t.Errorf("sent %d deletion requests for a fully protected window", len(s.deleter.requests))
		}
	})

	t.Run("proxied scan at the limit", func(t *testing.T) {
		p := useWispPurgeProxied(t, 6000)
		jsonOutput = true
		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosedProxiedServer(context.Background(), false, true, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosedProxiedServer: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		obj := decodeSingleJSONObject(t, stdout)
		if got := obj["candidates_capped"]; got != true {
			t.Errorf("candidates_capped = %v, want true", got)
		}
		if len(p.deleted) != 5000 {
			t.Errorf("deleted %d wisps, want the 5000 of the window", len(p.deleted))
		}
	})
}

// TestWispPurgeClosedPreviewDoesNotMutate pins that chunking reaches only the
// forced purge (bd-d2zqqc): a no-force preview or a --dry-run on either route
// deletes nothing and keeps the answer it gave before.
func TestWispPurgeClosedPreviewDoesNotMutate(t *testing.T) {
	preview := func(t *testing.T, stdout string) {
		t.Helper()
		obj := decodeSingleJSONObject(t, stdout)
		if obj["candidates"] != float64(450) || obj["dry_run"] != true {
			t.Errorf("preview = %v, want candidates 450 and dry_run true", obj)
		}
	}

	t.Run("direct no force", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		jsonOutput = true
		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), false, false, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosed: %v\nstderr:\n%s", err, stderr)
		}
		preview(t, stdout)
		if len(s.deleter.requests) != 0 {
			t.Errorf("preview sent %d deletion requests", len(s.deleter.requests))
		}
	})

	t.Run("direct dry run", func(t *testing.T) {
		s := useWispPurgeStub(t, 450)
		jsonOutput = true
		stdout, stderr, err := captureWispGC(t, func() error {
			return runWispPurgeClosed(context.Background(), true, true, nil)
		})
		if err != nil {
			t.Fatalf("runWispPurgeClosed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		for i, req := range s.deleter.requests {
			if !req.DryRun {
				t.Errorf("dry run sent request %d without DryRun", i)
			}
		}
		if len(s.deleter.deleted) != 0 {
			t.Errorf("dry run deleted %d wisps", len(s.deleter.deleted))
		}
	})

	for _, tc := range []struct {
		name          string
		dryRun, force bool
	}{
		{"proxied no force", false, false},
		{"proxied dry run", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := useWispPurgeProxied(t, 450)
			jsonOutput = true
			stdout, stderr, err := captureWispGC(t, func() error {
				return runWispPurgeClosedProxiedServer(context.Background(), tc.dryRun, tc.force, nil)
			})
			if err != nil {
				t.Fatalf("runWispPurgeClosedProxiedServer: %v\nstderr:\n%s", err, stderr)
			}
			preview(t, stdout)
			if units := p.deletionUnits(); len(units) != 0 {
				t.Errorf("preview opened %d deletion units of work", len(units))
			}
			if len(p.deleted) != 0 {
				t.Errorf("preview deleted %d wisps", len(p.deleted))
			}
		})
	}
}
