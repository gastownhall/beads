//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_bridge_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/encode"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The off-role text-path bridge, run as a DUAL RUN: the same question asked of
// the reference store directly and of the http client, and the two answers
// compared.
//
// That is the only form in which D4's parent-walk inversion can be believed. The
// encode gate proves the parameters round-trip through the server's own decoder;
// what it cannot prove is that the ROWS come back the same, because the derived
// exclusions the inversion inverts are applied by a query neither side of that
// gate runs. Here both sides run it.
//
// The fixtures are the spec's own pin list: the default rendering, `--all`, the
// `--include-*` variants, and a residual refusal.

// seedWalkTree plants a two-level tree plus the rows the derived defaults are
// supposed to hide: a closed child, a template child and a gate child.
func seedWalkTree(t *testing.T, ctx context.Context, e *servedEnv) {
	t.Helper()
	rows := []*types.Issue{
		{ID: "wlk-root", Title: "root", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "wlk-open", Title: "open child", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "wlk-deep", Title: "grandchild", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "wlk-closed", Title: "closed child", Status: types.StatusClosed, IssueType: types.TypeTask},
		{ID: "wlk-template", Title: "template child", Status: types.StatusOpen, IssueType: types.TypeTask, IsTemplate: true},
		{ID: "wlk-gate", Title: "gate child", Status: types.StatusOpen, IssueType: types.IssueType("gate")},
	}
	for _, row := range rows {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}
	for _, edge := range []*types.Dependency{
		{IssueID: "wlk-open", DependsOnID: "wlk-root", Type: types.DepParentChild},
		{IssueID: "wlk-closed", DependsOnID: "wlk-root", Type: types.DepParentChild},
		{IssueID: "wlk-template", DependsOnID: "wlk-root", Type: types.DepParentChild},
		{IssueID: "wlk-gate", DependsOnID: "wlk-root", Type: types.DepParentChild},
		{IssueID: "wlk-deep", DependsOnID: "wlk-open", Type: types.DepParentChild},
	} {
		if err := e.addDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s: %v", edge.IssueID, err)
		}
	}
}

// walkOverBoth runs one level of the walk against both stores.
//
// It reproduces findAllDescendants' own mutation exactly — a value copy of the
// built filter with ParentID re-pointed and Limit zeroed — because the whole
// question is whether the filter the CLI hands the store crosses intact.
func walkOverBoth(t *testing.T, ctx context.Context, e *servedEnv, in issueops.ListRequest, parent string) (local, remote []string, remoteErr error) {
	t.Helper()
	cfg, err := workapi.LoadStoreListConfig(ctx, e.reference)
	if err != nil {
		t.Fatalf("load the reference vocabulary: %v", err)
	}
	filter, err := workapi.BuildListFilter(in, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	filter.ParentID = &parent
	filter.Limit = 0
	// Every text rendering of `bd list` sets this, so the filter under test is
	// the one the walk really receives.
	filter.SkipCounts = true

	localRows, err := e.reference.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("reference SearchIssues: %v", err)
	}
	remoteRows, remoteErr := e.subject.SearchIssues(ctx, "", filter)
	return issueIDs(localRows), issueIDs(remoteRows), remoteErr
}

func issueIDs(rows []*types.Issue) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	sort.Strings(out)
	return out
}

// TestTheParentWalkAnswersTheSameRowsOverHTTP is the dual run D4's pin list asks
// for.
func TestTheParentWalkAnswersTheSameRowsOverHTTP(t *testing.T) {
	e := newServedEnv(t, "wlk")
	ctx := t.Context()
	seedWalkTree(t, ctx, e)

	for _, tc := range []struct {
		name string
		in   issueops.ListRequest
		want []string
	}{
		{
			// The fixture D4 exists for: a bare `bd list --parent <id>`, whose
			// filter carries ExcludeStatus, Pinned, IsTemplate, the gate and
			// infra exclusions and SkipWisps — none of which the wire publishes,
			// and all of which the server re-derives from their own absence. It
			// fails outright if the inversion is wrong in either direction.
			name: "the default text rendering",
			in:   issueops.ListRequest{},
			want: []string{"wlk-open"},
		},
		{
			name: "--all admits the closed child",
			in:   issueops.ListRequest{AllFlag: true},
			want: []string{"wlk-closed", "wlk-open"},
		},
		{
			name: "--include-templates admits the template child",
			in:   issueops.ListRequest{IncludeTemplates: true},
			want: []string{"wlk-open", "wlk-template"},
		},
		{
			name: "--include-gates admits the gate child",
			in:   issueops.ListRequest{IncludeGates: true},
			want: []string{"wlk-gate", "wlk-open"},
		},
		{
			name: "--status closed selects only the closed child",
			in:   issueops.ListRequest{Status: "closed"},
			want: []string{"wlk-closed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, remote, err := walkOverBoth(t, ctx, e, tc.in, "wlk-root")
			if err != nil {
				t.Fatalf("the http walk refused: %v", err)
			}
			if !equalIDs(local, tc.want) {
				t.Fatalf("the REFERENCE answered %v, the fixture expects %v — the fixture is wrong, not the client", local, tc.want)
			}
			if !equalIDs(remote, local) {
				t.Errorf("http answered %v, the reference answered %v", remote, local)
			}
		})
	}
}

// TestTheParentWalkRefusesAResidualOverHTTP is the residual-refusal case from
// the same pin list, run end to end: the refusal has to happen in the client,
// before a request that would answer a wider question is built.
func TestTheParentWalkRefusesAResidualOverHTTP(t *testing.T) {
	e := newServedEnv(t, "wlr")
	ctx := t.Context()
	seedWalkTree(t, ctx, e)

	_, _, err := walkOverBoth(t, ctx, e, issueops.ListRequest{ExcludeTypes: []string{"chore"}}, "wlk-root")
	if !errors.Is(err, encode.ErrRefused) {
		t.Fatalf("a --exclude-type walk returned %v, want a refusal", err)
	}
	var refusal *encode.RefusedError
	if errors.As(err, &refusal) && refusal.Row.ID != "P-IssueFilter.ExcludeTypes" {
		t.Errorf("the refusal cites %q, want P-IssueFilter.ExcludeTypes", refusal.Row.ID)
	}
}

// TestTheReadyBridgeAnswersTheSameRowsOverHTTP is the other half of the text
// bridge: `bd ready`'s listing, which is still raw at tip.
//
// It feeds the filter workapi.BuildReadyFilter actually builds — Status=StatusOpen
// and all — rather than a hand-built WorkFilter{}. That hand-built shape, whose
// empty status the reference store reads as open+in_progress, is exactly what
// masked the ship blocker: the pre-fix bridge refused the StatusOpen every real
// `bd ready` carries, and a fixture that never sent one could not see it. The
// seeded in_progress row makes the open-only derivation observable.
func TestTheReadyBridgeAnswersTheSameRowsOverHTTP(t *testing.T) {
	e := newServedEnv(t, "rdy")
	ctx := t.Context()

	for _, row := range []*types.Issue{
		{ID: "rdy-1", Title: "ready one", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 1},
		{ID: "rdy-2", Title: "ready two", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Assignee: "ada"},
		{ID: "rdy-3", Title: "in progress", Status: types.StatusInProgress, IssueType: types.TypeTask, Priority: 1},
		{ID: "rdy-4", Title: "closed", Status: types.StatusClosed, IssueType: types.TypeTask},
	} {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}

	for _, tc := range []struct {
		name string
		req  issueops.ReadyRequest
	}{
		{"an unfiltered listing", issueops.ReadyRequest{Sort: "priority"}},
		{"an assignee filter", issueops.ReadyRequest{Assignee: "ada", Sort: "priority"}},
		{"a bounded page", issueops.ReadyRequest{Sort: "priority", Limit: ptrTo(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The one shape the CLI produces: workapi.BuildReadyFilter stamps
			// Status=StatusOpen, which the pre-fix bridge refused on sight.
			filter, err := workapi.BuildReadyFilter(tc.req)
			if err != nil {
				t.Fatalf("BuildReadyFilter: %v", err)
			}
			local, err := e.reference.GetReadyWork(ctx, filter)
			if err != nil {
				t.Fatalf("reference GetReadyWork: %v", err)
			}
			remote, err := e.subject.GetReadyWork(ctx, filter)
			if err != nil {
				t.Fatalf("http GetReadyWork: %v", err)
			}
			if !equalIDs(issueIDs(remote), issueIDs(local)) {
				t.Errorf("http answered %v, the reference answered %v", issueIDs(remote), issueIDs(local))
			}
		})
	}
}

// TestTheReadyBridgeDualRunsBuildReadyFilterOverBothReadPaths is the ship-blocker
// regression pin (ga-b8ddd.13). Every real `bd ready` and `bd ready --json` builds
// its filter through workapi.BuildReadyFilter, which stamps Status=StatusOpen — the
// value the pre-fix bridge refused, so every http invocation failed before it
// dialed. This pushes that exact filter through BOTH read paths, the text
// GetReadyWork and the --json GetReadyWorkWithCounts, and byte-compares the http
// answer against the reference store's own. The in_progress and closed rows make
// the open-only derivation observable: a bridge that sent the wrong status, or a
// server that re-derived open+in_progress, would diverge here rather than pass.
func TestTheReadyBridgeDualRunsBuildReadyFilterOverBothReadPaths(t *testing.T) {
	e := newServedEnv(t, "rdd")
	ctx := t.Context()

	for _, row := range []*types.Issue{
		{ID: "rdd-open-hi", Title: "open high", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 0},
		{ID: "rdd-open-lo", Title: "open low", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 3, Assignee: "ada"},
		{ID: "rdd-inprog", Title: "in progress", Status: types.StatusInProgress, IssueType: types.TypeTask, Priority: 0},
		{ID: "rdd-closed", Title: "closed", Status: types.StatusClosed, IssueType: types.TypeTask},
	} {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}

	for _, tc := range []struct {
		name string
		req  issueops.ReadyRequest
	}{
		{"the default listing", issueops.ReadyRequest{Sort: "priority"}},
		{"an assignee filter", issueops.ReadyRequest{Assignee: "ada", Sort: "priority"}},
		{"a bounded page", issueops.ReadyRequest{Sort: "priority", Limit: ptrTo(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := workapi.BuildReadyFilter(tc.req)
			if err != nil {
				t.Fatalf("BuildReadyFilter: %v", err)
			}
			if filter.Status != types.StatusOpen {
				t.Fatalf("BuildReadyFilter produced Status=%q, want the StatusOpen this regression is about", filter.Status)
			}

			// The text path (`bd ready`).
			localText, err := e.reference.GetReadyWork(ctx, filter)
			if err != nil {
				t.Fatalf("reference GetReadyWork: %v", err)
			}
			remoteText, err := e.subject.GetReadyWork(ctx, filter)
			if err != nil {
				t.Fatalf("http GetReadyWork refused the BuildReadyFilter shape: %v", err)
			}
			assertSameJSON(t, "GetReadyWork", localText, remoteText)

			// The --json path (`bd ready --json`).
			localCounts, err := e.reference.GetReadyWorkWithCounts(ctx, filter)
			if err != nil {
				t.Fatalf("reference GetReadyWorkWithCounts: %v", err)
			}
			remoteCounts, err := e.subject.GetReadyWorkWithCounts(ctx, filter)
			if err != nil {
				t.Fatalf("http GetReadyWorkWithCounts refused the BuildReadyFilter shape: %v", err)
			}
			assertSameJSON(t, "GetReadyWorkWithCounts", localCounts, remoteCounts)

			// The --json path with its in-band total (upstream #6731): the
			// same page, and the size of the whole ready set. "a bounded
			// page" comes back truncated, so it exercises the countReadyWork
			// round trip; the others answer from has_more alone.
			localPage, localTotal, err := e.reference.GetReadyWorkWithCountsAndTotal(ctx, filter)
			if err != nil {
				t.Fatalf("reference GetReadyWorkWithCountsAndTotal: %v", err)
			}
			remotePage, remoteTotal, err := e.subject.GetReadyWorkWithCountsAndTotal(ctx, filter)
			if err != nil {
				t.Fatalf("http GetReadyWorkWithCountsAndTotal: %v", err)
			}
			assertSameJSON(t, "GetReadyWorkWithCountsAndTotal", localPage, remotePage)
			if remoteTotal != localTotal {
				t.Errorf("http total = %d, the reference answered %d", remoteTotal, localTotal)
			}
			unbounded := filter
			unbounded.Limit = 0
			all, err := e.reference.GetReadyWorkWithCounts(ctx, unbounded)
			if err != nil {
				t.Fatalf("reference unbounded GetReadyWorkWithCounts: %v", err)
			}
			if remoteTotal != len(all) {
				t.Errorf("http total = %d, want the unbounded listing's %d", remoteTotal, len(all))
			}

			// Open-only is re-derived server-side from the status the bridge
			// dropped: neither the in_progress nor the closed row may appear.
			for _, got := range issueIDs(remoteText) {
				if got == "rdd-inprog" || got == "rdd-closed" {
					t.Errorf("ready work admitted %q; open-only was not re-derived", got)
				}
			}
		})
	}
}

// TestTheReadyBridgeRefusesAnInexpressibleFilter is L12 at the seam: a filter
// field with no wire parameter refuses rather than being dropped, because a
// dropped filter widens the answer invisibly.
//
// The examples are GENUINELY non-default: an in_progress restriction and a
// multi-status OR set, neither of which workapi.BuildReadyFilter ever produces.
// StatusOpen is deliberately absent — it is the derived default the bridge now
// drops, and asserting a refusal on it (as this test once did) is what cemented
// the ship blocker.
func TestTheReadyBridgeRefusesAnInexpressibleFilter(t *testing.T) {
	e := newServedEnv(t, "rdr")

	for _, tc := range []struct {
		name   string
		filter types.WorkFilter
		row    string
	}{
		{
			"an in_progress restriction the wire cannot state",
			types.WorkFilter{Status: types.StatusInProgress},
			"E-WorkFilter.Status",
		},
		{
			// The derived-default Status passes the inversion; the populated
			// OR-set the wire cannot carry still refuses on the residual sweep.
			"a multi-status OR set",
			types.WorkFilter{Status: types.StatusOpen, Statuses: []types.Status{types.StatusOpen, types.StatusInProgress}},
			"E-WorkFilter.Statuses",
		},
		{
			// An id exclusion set has no ready parameter; dropping it would
			// list (and count) the very rows the caller excluded.
			"an id exclusion set",
			types.WorkFilter{Status: types.StatusOpen, ExcludeIDs: []string{"rdr-1"}},
			"E-WorkFilter.ExcludeIDs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.subject.GetReadyWork(t.Context(), tc.filter)
			if !errors.Is(err, encode.ErrRefused) {
				t.Fatalf("%s returned %v, want a refusal", tc.name, err)
			}
			var refusal *encode.RefusedError
			if errors.As(err, &refusal) && refusal.Row.ID != tc.row {
				t.Errorf("the refusal cites %q, want %q", refusal.Row.ID, tc.row)
			}
			// The in-band-total listing refuses the same filter the same way,
			// before either request: its count encoder is the listing's.
			_, _, err = e.subject.GetReadyWorkWithCountsAndTotal(t.Context(), tc.filter)
			if !errors.Is(err, encode.ErrRefused) {
				t.Fatalf("GetReadyWorkWithCountsAndTotal: %s returned %v, want a refusal", tc.name, err)
			}
			if errors.As(err, &refusal) && refusal.Row.ID != tc.row {
				t.Errorf("GetReadyWorkWithCountsAndTotal: the refusal cites %q, want %q", refusal.Row.ID, tc.row)
			}
		})
	}
}

// TestTheReadyBridgeEnforcesMaxRowsClientSide is D12 on the cursorless
// operation: the wire publishes no max_rows, so the cap is synthesized into the
// sentinel `bd ready` classifies into exit 2.
func TestTheReadyBridgeEnforcesMaxRowsClientSide(t *testing.T) {
	e := newServedEnv(t, "rmx")
	ctx := t.Context()

	for _, id := range []string{"rmx-1", "rmx-2"} {
		if err := e.createIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// The derived-default status the bridge drops; an empty status now refuses,
	// so the cap is exercised on the shape `bd ready` actually sends.
	if _, err := e.subject.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen}); err != nil {
		t.Fatalf("an uncapped listing: %v", err)
	}
	_, err := e.subject.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusOpen, MaxRows: 1, MaxRowsSource: "--max-rows"})
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("the cap did not fire: %v", err)
	}
	if tooMany.Cap != 1 || tooMany.Source != "--max-rows" {
		t.Errorf("the synthesized cap error = %+v, want Cap 1 attributed to --max-rows", tooMany)
	}
}

// TestTheParentExistenceProbeCrossesTheWire is the raw GetIssue the walk runs
// before it descends, and the explicit-id probe `bd create --graph` runs. A
// miss is storage.ErrNotFound on EVERY leg — the local reference store and the
// http store alike — so a caller written against the backend contract never
// needs a second, http-only branch for (nil, nil).
func TestTheParentExistenceProbeCrossesTheWire(t *testing.T) {
	e := newServedEnv(t, "prb")
	ctx := t.Context()

	if err := e.createIssue(ctx, &types.Issue{ID: "prb-1", Title: "present", Status: types.StatusOpen, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := e.subject.GetIssue(ctx, "prb-1")
	if err != nil || got == nil || got.ID != "prb-1" {
		t.Fatalf("GetIssue on a hit = (%v, %v)", got, err)
	}
	legs := []struct {
		name  string
		store interface {
			GetIssue(context.Context, string) (*types.Issue, error)
		}
	}{
		{"local", e.reference},
		{"http", e.subject},
	}
	for _, leg := range legs {
		for _, id := range []string{"prb-nope", ""} {
			got, err := leg.store.GetIssue(ctx, id)
			if !errors.Is(err, issueops.ErrNotFound) || got != nil {
				t.Errorf("%s GetIssue(%q) on a miss = (%v, %v), want (nil, storage.ErrNotFound)", leg.name, id, got, err)
			}
		}
	}
	// The http miss reads exactly as the local one does.
	_, local := e.reference.GetIssue(ctx, "prb-nope")
	_, remote := e.subject.GetIssue(ctx, "prb-nope")
	if local == nil || remote == nil || local.Error() != remote.Error() {
		t.Errorf("miss text: local %q, http %q; want the same", local, remote)
	}
}

// TestBridgeGetIssuePopulatesRowVersionForAGuardedWrite is HIGH 5's second pin:
// the raw bridge GetIssue above returns a hit by value with no RowVersion
// assertion, same as the molecule loader's existence probe
// (internal/molecules/molecules.go) that is this method's other caller — so a
// stitched-at-zero regression here would have shipped silently next to the
// passing test above it.
//
// types.Issue.RowVersion is json:"-"; getIssue's wire body carries the same
// token under Revision instead, and bridge.go's GetIssue must stitch it back
// onto the issue it returns, exactly as Reader.Get does in role_reader.go
// (pinned by TestServedReaderGetPopulatesRowVersionForAGuardedWrite). This
// proves it the same way: by guarding a write with whatever GetIssue just
// answered, over the same wire, and requiring a second write that reuses the
// now-stale token to be refused. A read alone cannot tell a stitched zero from
// a real one that happens to equal it on a fresh table — only a guard that
// the write actually enforces can.
func TestBridgeGetIssuePopulatesRowVersionForAGuardedWrite(t *testing.T) {
	e := newServedEnv(t, "brgv")
	ctx := t.Context()
	lifecycle, err := e.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	issue := &types.Issue{Title: "bridge row version round trip", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := e.createIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := e.subject.GetIssue(ctx, issue.ID)
	if err != nil || got == nil {
		t.Fatalf("GetIssue: (%v, %v)", got, err)
	}
	if got.RowVersion == 0 {
		t.Fatalf("GetIssue answered RowVersion 0; want the row's real token")
	}
	staleVersion := got.RowVersion

	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("first guarded write")},
	}); err != nil {
		t.Fatalf("Update guarded by the token GetIssue answered: %v", err)
	}

	_, err = lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("second guarded write, same stale token")},
	})
	if err == nil {
		t.Fatal("Update reused GetIssue's token after a write moved the row past it; want a version-guard refusal")
	}
}

// TestSearchIssuesExactIDPopulatesRowVersionForAGuardedWrite pins the same
// stitch as TestBridgeGetIssuePopulatesRowVersionForAGuardedWrite, for
// SearchIssues' own exact-id fast path (getIssuesByExactID in resolve.go)
// rather than the raw bridge GetIssue. That path decodes the same
// apigen.ContextResponse-shaped IssueDetails getIssue does, but — unlike
// bridge.go's GetIssue and role_reader.go's Reader.Get — it never parsed
// details.Revision, so SearchIssues(ctx, "", IssueFilter{IDs: ...}) answered
// RowVersion 0 for a row every other read path reports a real token for. This
// proves the stitch the same way the bridge test does: by guarding a write
// with whatever the exact-id search just answered, over the same wire, and
// requiring a second write that reuses the now-stale token to be refused.
func TestSearchIssuesExactIDPopulatesRowVersionForAGuardedWrite(t *testing.T) {
	e := newServedEnv(t, "sixi")
	ctx := t.Context()
	lifecycle, err := e.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	issue := &types.Issue{Title: "exact-id search row version round trip", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := e.createIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := e.subject.SearchIssues(ctx, "", types.IssueFilter{IDs: []string{issue.ID}})
	if err != nil {
		t.Fatalf("SearchIssues(exact id): %v", err)
	}
	if len(rows) != 1 || rows[0] == nil {
		t.Fatalf("SearchIssues(exact id) = %+v, want exactly one hit", rows)
	}
	if rows[0].RowVersion == 0 {
		t.Fatalf("SearchIssues(exact id) answered RowVersion 0; want the row's real token")
	}
	staleVersion := rows[0].RowVersion

	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("first guarded write")},
	}); err != nil {
		t.Fatalf("Update guarded by the token SearchIssues answered: %v", err)
	}

	_, err = lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("second guarded write, same stale token")},
	})
	if err == nil {
		t.Fatal("Update reused SearchIssues' token after a write moved the row past it; want a version-guard refusal")
	}
}

// TestServedWorkspaceConfigWritesRefuse IS GONE, and what it asserted is worth a
// line rather than a silent deletion: while D8 row 11 was PARTIAL, the two write
// verbs refused per METHOD and named themselves, so the accessor could serve the
// reads without the writes silently becoming no-ops. Upstream #5596 published
// both operations and client wave ga-jpywb dials them, so there is no refusal
// left to pin — served_config_test.go runs the whole role contract instead, and
// served_config_write_test.go pins the three things only this leg has.
//
// TestServedRedactedSettingIsAbsentWithReason is ledger row L9, and the reason
// this backend gives ONE answer per key: a withheld value is not the same fact
// as an unset one, so both the raw probe and the role report the withholding
// rather than returning an empty string a caller would read as "nothing stored".
func TestServedRedactedSettingIsAbsentWithReason(t *testing.T) {
	e := newServedEnv(t, "red")
	ctx := t.Context()

	if err := e.setConfig(ctx, "github.api_token", "ghp-supersecret"); err != nil {
		t.Fatalf("seed the credential-bearing key: %v", err)
	}

	if _, err := e.subject.GetConfig(ctx, "github.api_token"); !errors.Is(err, ErrSettingRedacted) {
		t.Errorf("GetConfig on a redacted key = %v, want ErrSettingRedacted", err)
	}
	settings, err := e.subject.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	if _, err := settings.GetSetting(ctx, issueops.GetSettingRequest{Key: "github.api_token"}); !errors.Is(err, ErrSettingRedacted) {
		t.Errorf("GetSetting on a redacted key = %v, want the same answer the raw probe gives", err)
	}

	// The ENUMERATION cannot fail over one key of many, so it lists the key with
	// an empty value: the workspace really does store something there.
	all, err := e.subject.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig: %v", err)
	}
	if value, ok := all["github.api_token"]; !ok || value != "" {
		t.Errorf("GetAllConfig gave the redacted key (%q, present=%v), want present and empty", value, ok)
	}
}

// TestServedStatisticsProbeAnswersTruthfully is D4's off-role stats row: the
// empty-state probe `bd ready` prints "No open issues" from, which must answer
// from the SERVER rather than fail into a false statement.
func TestServedStatisticsProbeAnswersTruthfully(t *testing.T) {
	e := newServedEnv(t, "stp")
	ctx := t.Context()

	before, err := e.subject.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics on an empty workspace: %v", err)
	}
	if err := e.createIssue(ctx, &types.Issue{ID: "stp-1", Title: "open work", Status: types.StatusOpen, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	after, err := e.subject.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if after.OpenIssues != before.OpenIssues+1 {
		t.Errorf("open issues went %d -> %d across one seeded row", before.OpenIssues, after.OpenIssues)
	}
}

// TestServedVocabularyReadsDegradeRatherThanFail is L7: the client-side status
// and type vocabulary comes from the pre-migration config keys, which are its
// only wire-visible source, and the reads must never hard-fail — every one of
// them is wrapped by LoadStoreListConfig in a failure that would kill `bd list`.
func TestServedVocabularyReadsDegradeRatherThanFail(t *testing.T) {
	e := newServedEnv(t, "voc")
	ctx := t.Context()

	// Seeded OUT OF ALPHABETICAL ORDER on purpose: the read answers in NAME
	// order, which is what every leg's projected-table read answers, and not in
	// the order the config string happens to list them.
	if err := e.setConfig(ctx, "types.custom", "spike,chore"); err != nil {
		t.Fatalf("seed types.custom: %v", err)
	}
	if err := e.setConfig(ctx, "status.custom", "shipped:done"); err != nil {
		t.Fatalf("seed status.custom: %v", err)
	}

	custom, err := e.subject.GetCustomTypes(ctx)
	if err != nil {
		t.Fatalf("GetCustomTypes: %v", err)
	}
	if len(custom) != 2 || custom[0] != "chore" || custom[1] != "spike" {
		t.Errorf("GetCustomTypes = %v, want [chore spike] — ordered by name, as the table read every other leg makes is", custom)
	}

	statuses, err := e.subject.GetCustomStatusesDetailed(ctx)
	if err != nil {
		t.Fatalf("GetCustomStatusesDetailed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Name != "shipped" || statuses[0].Category != types.CategoryDone {
		t.Errorf("GetCustomStatusesDetailed = %+v, want shipped:done", statuses)
	}

	// The infra vocabulary has no error channel at all, so an unconfigured
	// workspace must still answer the defaults rather than an empty map — an
	// empty one says "nothing is infrastructure" and admits agent and message
	// rows into every default listing.
	if infra := e.subject.GetInfraTypes(ctx); !infra["agent"] || !infra["message"] {
		t.Errorf("GetInfraTypes = %v, want the defaults", infra)
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
