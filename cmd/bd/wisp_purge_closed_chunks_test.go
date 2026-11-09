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
// a second object (an error envelope printed after the answer) fails.
func decodeSingleJSONObject(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	dec := json.NewDecoder(bytes.NewBufferString(out))
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("decoding stdout as one JSON object: %v\nstdout:\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON value:\n%s", out)
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
	for _, want := range []string{"50", "context deadline exceeded", "bd mol wisp gc --closed --force"} {
		if !bytes.Contains([]byte(stderr), []byte(want)) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

// TestWispPurgeClosedJSONReportsPartialProgress pins the --json half: a purge
// stopped by a failed request prints exactly one JSON object on stdout, with
// the success keys counting only the committed chunks plus remaining_count,
// error and hint, and exits nonzero without a second error envelope.
func TestWispPurgeClosedJSONReportsPartialProgress(t *testing.T) {
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
	if got, _ := obj["hint"].(string); !bytes.Contains([]byte(got), []byte("bd mol wisp gc --closed --force")) {
		t.Errorf("hint = %q, want the rerun command", got)
	}
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
