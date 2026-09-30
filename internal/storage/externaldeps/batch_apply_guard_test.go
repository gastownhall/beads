package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// recordingApplier is a backend BatchApplier that records what reached it.
type recordingApplier struct {
	requests []publicops.ApplyBatchRequest
}

func (a *recordingApplier) ApplyBatch(_ context.Context, req publicops.ApplyBatchRequest) (publicops.ApplyBatchResult, error) {
	a.requests = append(a.requests, req)
	return publicops.ApplyBatchResult{Items: make([]publicops.ItemResult, len(req.Items))}, nil
}

type applierStore struct {
	*fakeStore
	applier *recordingApplier
}

func (s *applierStore) BatchApplier() (publicops.BatchApplier, error) { return s.applier, nil }

func closeItem(id string) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{ID: id}}}
}

func closingUpdate(id string) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{
		Target: publicops.Ref{ID: id},
		Patch:  publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}},
	}}
}

func applyReq(items ...publicops.ApplyItem) publicops.ApplyBatchRequest {
	return publicops.ApplyBatchRequest{Actor: "w", Items: items}
}

// blockedItemError asserts err is the external close refusal naming item index.
func blockedItemError(t *testing.T, op string, err error, index int) {
	t.Helper()
	var itemErr *publicops.ItemError
	if !errors.Is(err, storage.ErrCloseBlocked) || !errors.As(err, &itemErr) || itemErr.Index != index {
		t.Errorf("%s: err = %v, want the external close refusal at item %d", op, err, index)
	}
}

// TestStoreArmApplyBatchGuardsClosingItems pins the store arm's apply-batch
// guard: an unforced close item or closing update item an unsatisfied
// `external:` blocker holds refuses the WHOLE request before the inner applier
// is reached; a satisfied blocker, a force, or an unrelated item passes
// unchanged; an already-closed target is forwarded pinned to its closed state
// (ExpectedStatus / ExpectedVersion) on a copy of the request; an external
// edge an earlier dep_add item adds is seen. At most one read per call, of the
// closing targets' own edges, and never the workspace's.
func TestStoreArmApplyBatchGuardsClosingItems(t *testing.T) {
	blocked, provided, free, done := issue("be-blocked"), issue("be-provided"), issue("be-free"), issue("be-done")
	done.Status, done.RowVersion = types.StatusClosed, 7
	raw := &fakeStore{
		ready: []*types.Issue{blocked, provided, free, done},
		deps: map[string][]*types.Dependency{
			blocked.ID:  {externalDep(blocked.ID, "external:remote:missing", types.DepBlocks)},
			provided.ID: {externalDep(provided.ID, "external:remote:payments", types.DepBlocks)},
			done.ID:     {externalDep(done.ID, "external:remote:missing", types.DepBlocks)},
		},
	}
	foreign := &fakeStore{labels: map[string][]*types.Issue{"provides:payments": {{ID: "fx-1", Status: types.StatusClosed}}}}
	opens := 0
	store := New(&applierStore{fakeStore: raw},
		func(project ProjectName) (string, bool) { return "/projects/remote", project == "remote" },
		func(context.Context, string) (storage.DoltStorage, error) { opens++; return foreign, nil })
	applierRaw := store.inner.(*applierStore)
	applierRaw.applier = &recordingApplier{}
	applier, err := store.BatchApplier()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	run := func(op string, req publicops.ApplyBatchRequest) error {
		t.Helper()
		raw.edgeReads, raw.ownReads = 0, 0
		_, err := applier.ApplyBatch(ctx, req)
		if raw.edgeReads != 0 {
			t.Errorf("%s: %d workspace edge reads, want 0", op, raw.edgeReads)
		}
		if raw.ownReads > 1 {
			t.Errorf("%s: %d own-edge reads, want at most 1", op, raw.ownReads)
		}
		return err
	}
	sent := func() int { return len(applierRaw.applier.requests) }

	for _, tc := range []struct {
		name  string
		req   publicops.ApplyBatchRequest
		index int
	}{
		{"close item", applyReq(closeItem(free.ID), closeItem(blocked.ID)), 1},
		{"closing update item", applyReq(closingUpdate(blocked.ID)), 0},
		{"edge added earlier in the request", applyReq(
			publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
				Source: publicops.Ref{ID: free.ID}, Target: publicops.Ref{ID: "external:remote:missing"}, Type: types.DepBlocks,
			}},
			closeItem(free.ID)), 1},
		{"row the request creates", applyReq(
			publicops.ApplyItem{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: "k", Issue: &types.Issue{Title: "n"}}},
			publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
				Source: publicops.Ref{Key: "k"}, Target: publicops.Ref{ID: "external:remote:missing"}, Type: types.DepBlocks,
			}},
			publicops.ApplyItem{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{Key: "k"}}}), 2},
	} {
		before := sent()
		blockedItemError(t, tc.name, run(tc.name, tc.req), tc.index)
		if sent() != before {
			t.Errorf("%s: a refused request reached the inner applier", tc.name)
		}
	}

	// A caller guard that admits a row that is not closed is not a re-close.
	notClosed := closingUpdate(done.ID)
	open := types.StatusOpen
	notClosed.Update.ExpectedStatus = &open
	blockedItemError(t, "re-close guarded on open", run("re-close guarded on open", applyReq(notClosed)), 0)

	// Allowed: satisfied, forced, and the edge added AFTER the close.
	forcedClose := closeItem(blocked.ID)
	forcedClose.Close.Force = true
	forcedUpdate := closingUpdate(blocked.ID)
	forcedUpdate.Update.ForceClosePolicy = true
	for _, tc := range []struct {
		name string
		req  publicops.ApplyBatchRequest
	}{
		{"satisfied", applyReq(closeItem(provided.ID), closingUpdate(provided.ID))},
		{"forced", applyReq(forcedClose, forcedUpdate)},
		{"edge added after the close", applyReq(closeItem(free.ID), publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
			Source: publicops.Ref{ID: free.ID}, Target: publicops.Ref{ID: "external:remote:missing"}, Type: types.DepBlocks,
		}})},
	} {
		before := sent()
		if err := run(tc.name, tc.req); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if sent() != before+1 {
			t.Errorf("%s: not forwarded", tc.name)
		}
		if got := applierRaw.applier.requests[sent()-1]; !slices.Equal(applyItemPtrs(got), applyItemPtrs(tc.req)) {
			t.Errorf("%s: forwarded request was rewritten", tc.name)
		}
	}

	// Already closed: forwarded pinned, the caller's request untouched.
	req := applyReq(closeItem(done.ID), closingUpdate(done.ID))
	if err := run("re-close", req); err != nil {
		t.Fatalf("re-close of an already-closed flagged issue: %v", err)
	}
	got := applierRaw.applier.requests[sent()-1]
	if v := got.Items[0].Close.ExpectedVersion; v == nil || *v != done.RowVersion {
		t.Errorf("close item forwarded with ExpectedVersion %v, want the closed row's %d", v, done.RowVersion)
	}
	if s := got.Items[1].Update.ExpectedStatus; s == nil || *s != types.StatusClosed {
		t.Errorf("update item forwarded with ExpectedStatus %v, want closed", s)
	}
	if req.Items[0].Close.ExpectedVersion != nil || req.Items[1].Update.ExpectedStatus != nil {
		t.Error("the caller's request was written through")
	}
	// A second close of the same row after the first is not pinnable.
	blockedItemError(t, "re-close touched earlier", run("re-close touched earlier", applyReq(closingUpdate(done.ID), closeItem(done.ID))), 1)

	// An update item whose row an earlier item rewrote is pinned as-modified
	// even though the read saw it open: the forced close before it decides.
	forcedFirst := closingUpdate(blocked.ID)
	forcedFirst.Update.ForceClosePolicy = true
	if err := run("re-close after a forced close", applyReq(forcedFirst, closingUpdate(blocked.ID))); err != nil {
		t.Fatalf("re-close after a forced close: %v", err)
	}
	if s := applierRaw.applier.requests[sent()-1].Items[1].Update.ExpectedStatus; s == nil || *s != types.StatusClosed {
		t.Errorf("update after a forced close forwarded with ExpectedStatus %v, want closed", s)
	}

	if opens == 0 {
		t.Fatal("no foreign project was opened; the case proves nothing")
	}
}

// statusApplyingIssues writes an update's status onto the stored row once
// apply is set, so a later item of the same batch sees it.
type statusApplyingIssues struct {
	*fakeIssueUseCase
	apply bool
}

func (u *statusApplyingIssues) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if u.apply && isClosedUpdate(spec.Fields) {
		if row, _ := u.GetIssue(ctx, id); row != nil {
			row.Status = types.StatusClosed
		}
	}
	return u.fakeIssueUseCase.ApplyUpdate(ctx, id, spec, actor)
}

func applyItemPtrs(req publicops.ApplyBatchRequest) []any {
	out := make([]any, 0, len(req.Items))
	for _, item := range req.Items {
		switch {
		case item.Close != nil:
			out = append(out, item.Close)
		case item.Update != nil:
			out = append(out, item.Update)
		case item.DepAdd != nil:
			out = append(out, item.DepAdd)
		default:
			out = append(out, item.Create)
		}
	}
	return out
}

// TestUOWArmApplyBatchGuardsClosingItemsOutsideTheWriteTransaction pins the
// unit-of-work arm: the same refusals, the re-close exemption decided inside
// the batch's own transaction, one read of the closing targets' own edges per
// call and none of the workspace's (the batch runs over the undecorated
// provider, so no use-case override re-reads them per item), and no
// foreign-store open while a unit of work is open.
func TestUOWArmApplyBatchGuardsClosingItemsOutsideTheWriteTransaction(t *testing.T) {
	blocked, provided, free, done := issue("be-blocked"), issue("be-provided"), issue("be-free"), issue("be-done")
	done.Status = types.StatusClosed
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked, provided, free, done}}
	deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID:  {externalDep(blocked.ID, "external:remote:missing", types.DepBlocks)},
		provided.ID: {externalDep(provided.ID, "external:remote:payments", types.DepBlocks)},
		done.ID:     {externalDep(done.ID, "external:remote:missing", types.DepBlocks)},
	}}}
	statuses := &statusApplyingIssues{fakeIssueUseCase: issues}
	counting := &countingUOWProvider{openCountingProvider: &openCountingProvider{uw: &fakeUOW{issues: statuses, deps: deps}}}
	foreign := &fakeStore{labels: map[string][]*types.Issue{"provides:payments": {{ID: "fx-1", Status: types.StatusClosed}}}}
	opens, opensInsideAUOW := 0, 0
	provider := WrapUOWProvider(counting,
		func(project ProjectName) (string, bool) { return "/projects/remote", project == "remote" },
		func(context.Context, string) (storage.DoltStorage, error) {
			opens++
			if counting.open != 0 {
				opensInsideAUOW++
			}
			return foreign, nil
		})
	applier, err := provider.(uow.BatchApplierSource).BatchApplier()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	run := func(op string, req publicops.ApplyBatchRequest, wantReads, wantUOWs int) error {
		t.Helper()
		issues.closed, counting.opened = nil, 0
		// The batch's own unit of work is the LAST one opened; an own-edge read
		// in an earlier one is the policy's pre-read (reads inside the batch are
		// the applier's hydration, not the policy's).
		var readIn []int
		deps.onOwnRead = func([]string) { readIn = append(readIn, counting.opened) }
		defer func() { deps.onOwnRead = nil }()
		_, err := applier.ApplyBatch(ctx, req)
		// One read of the closing targets' own edges per call, in its own
		// read-only unit of work before the batch; none when nothing closes
		// unforced or the only closing target is one the request creates. The
		// workspace's external edges are never read.
		assertReads(t, op, deps, 0)
		preReads := 0
		for _, in := range readIn {
			if in < counting.opened {
				preReads++
			}
		}
		if preReads != wantReads {
			t.Errorf("%s: policy own-edge reads = %d, want %d", op, preReads, wantReads)
		}
		if counting.opened != wantUOWs {
			t.Errorf("%s: units of work opened = %d, want %d", op, counting.opened, wantUOWs)
		}
		return err
	}

	blockedItemError(t, "close item", run("close item", applyReq(closeItem(free.ID), closeItem(blocked.ID)), 1, 2), 1)
	if slices.Contains(issues.closed, blocked.ID) {
		t.Errorf("the externally blocked %s reached the close use case", blocked.ID)
	}
	blockedItemError(t, "closing update item", run("closing update item", applyReq(closingUpdate(blocked.ID)), 1, 2), 0)
	if slices.Contains(issues.closed, blocked.ID) {
		t.Errorf("the externally blocked %s reached ApplyUpdate", blocked.ID)
	}
	created := applyReq(
		publicops.ApplyItem{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: "k", Issue: &types.Issue{Title: "n"}}},
		publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
			Source: publicops.Ref{Key: "k"}, Target: publicops.Ref{ID: "external:remote:missing"}, Type: types.DepBlocks,
		}},
		publicops.ApplyItem{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{Key: "k"}}})
	blockedItemError(t, "row the request creates", run("row the request creates", created, 0, 0), 2)

	forcedClose := closeItem(blocked.ID)
	forcedClose.Close.Force = true
	forcedUpdate := closingUpdate(blocked.ID)
	forcedUpdate.Update.ForceClosePolicy = true
	for _, tc := range []struct {
		name        string
		req         publicops.ApplyBatchRequest
		reads, uows int
		want        []string
	}{
		{"satisfied", applyReq(closeItem(provided.ID), closingUpdate(provided.ID)), 1, 2, []string{provided.ID, provided.ID}},
		{"forced", applyReq(forcedClose, forcedUpdate), 0, 1, []string{blocked.ID, blocked.ID}},
		{"already closed", applyReq(closeItem(done.ID), closingUpdate(done.ID)), 1, 2, []string{done.ID, done.ID}},
	} {
		if err := run(tc.name, tc.req, tc.reads, tc.uows); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !slices.Equal(issues.closed, tc.want) {
			t.Errorf("%s: mutations that reached the use case = %v, want %v", tc.name, issues.closed, tc.want)
		}
	}

	// As-modified: a forced close earlier in the request makes the flagged
	// close after it a re-close, decided inside the transaction. Without the
	// force check on the closing update the first item would be refused.
	statuses.apply = true
	forcedFirst := closingUpdate(blocked.ID)
	forcedFirst.Update.ForceClosePolicy = true
	if err := run("re-close after a forced close", applyReq(forcedFirst, closeItem(blocked.ID)), 1, 2); err != nil {
		t.Errorf("re-close after a forced close: %v", err)
	}
	if want := []string{blocked.ID, blocked.ID}; !slices.Equal(issues.closed, want) {
		t.Errorf("re-close after a forced close: mutations = %v, want %v", issues.closed, want)
	}

	if opens == 0 {
		t.Fatal("no foreign project was opened; the case proves nothing")
	}
	if opensInsideAUOW != 0 {
		t.Errorf("%d of %d foreign-store opens happened with a unit of work open; resolution must precede the write transaction", opensInsideAUOW, opens)
	}
	if counting.open != 0 {
		t.Errorf("%d units of work left open", counting.open)
	}
}
