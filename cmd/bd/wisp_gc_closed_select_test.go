package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// purgeSelectReader is the molReader selectClosedWispsForPurge reads: issues,
// edges keyed by target, custom statuses and per-call failures. The embedded
// nil interface makes any other call panic loudly. A status read answers at
// most statusReadCap rows, as a store with a result cap would, so a selector
// that names every source in one read loses some of them.
type purgeSelectReader struct {
	molReader
	issues      map[string]*types.Issue
	edges       []*types.Dependency
	custom      []types.CustomStatus
	infra       map[types.IssueType]bool
	unsupported bool // the batch edge read refuses with *storage.ErrUnsupported

	failCandidates, failEdges, failFallback, failCustom, failStatus bool

	statusReadSizes []int
}

const purgeSelectStatusReadCap = 1000

var errPurgeSelectRead = errors.New("injected read failure")

func (r *purgeSelectReader) SearchIssues(_ context.Context, _ string, f types.IssueFilter) ([]*types.Issue, error) {
	if f.IDs != nil {
		// The live-status read: the named sources in a not-done status.
		r.statusReadSizes = append(r.statusReadSizes, len(f.IDs))
		if r.failStatus {
			return nil, errPurgeSelectRead
		}
		var out []*types.Issue
		for _, id := range f.IDs {
			if issue := r.issues[id]; issue != nil && slices.Contains(f.Statuses, issue.Status) {
				out = append(out, issue)
			}
		}
		if len(out) > purgeSelectStatusReadCap {
			out = out[:purgeSelectStatusReadCap]
		}
		return out, nil
	}
	// The candidate read: closed wisps not of an excluded type.
	if r.failCandidates {
		return nil, errPurgeSelectRead
	}
	if f.Status == nil || *f.Status != types.StatusClosed || f.Ephemeral == nil || !*f.Ephemeral {
		return nil, fmt.Errorf("unexpected candidate filter %+v", f)
	}
	var out []*types.Issue
	for _, issue := range r.issues {
		if issue.Status == types.StatusClosed && issue.Ephemeral && !slices.Contains(f.ExcludeTypes, issue.IssueType) {
			out = append(out, issue)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *purgeSelectReader) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return r.infra[t]
}

func (r *purgeSelectReader) GetDependentRecordsForIssues(_ context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	if r.unsupported {
		return nil, fmt.Errorf("wrapped: %w", &storage.ErrUnsupported{Op: "GetDependentRecordsForIssues", Backend: "test"})
	}
	if r.failEdges {
		return nil, errPurgeSelectRead
	}
	out := make(map[string][]*types.Dependency)
	for _, target := range targetIDs {
		for _, dep := range r.edges {
			if dep.DependsOnID == target {
				out[target] = append(out[target], dep)
			}
		}
	}
	return out, nil
}

func (r *purgeSelectReader) GetDependentsWithMetadata(_ context.Context, target string) ([]*types.IssueWithDependencyMetadata, error) {
	if r.failFallback {
		return nil, errPurgeSelectRead
	}
	var out []*types.IssueWithDependencyMetadata
	for _, dep := range r.edges {
		if dep.DependsOnID == target {
			out = append(out, &types.IssueWithDependencyMetadata{Issue: *r.issues[dep.IssueID], DependencyType: dep.Type})
		}
	}
	return out, nil
}

func (r *purgeSelectReader) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	if r.failCustom {
		return nil, errPurgeSelectRead
	}
	return r.custom, nil
}

// purgeSelectFixture builds a reader from "id:status[:type]" rows (every row
// a wisp unless its id starts with "d-") and "source>target:type" edges.
func purgeSelectFixture(rows []string, edges []string) *purgeSelectReader {
	r := &purgeSelectReader{issues: make(map[string]*types.Issue), infra: map[types.IssueType]bool{"agent": true}}
	for _, row := range rows {
		parts := strings.SplitN(row, ":", 3)
		id, status, typ := parts[0], parts[1], "task"
		if len(parts) == 3 {
			typ = parts[2]
		}
		r.issues[id] = &types.Issue{
			ID:        id,
			Status:    types.Status(status),
			IssueType: types.IssueType(typ),
			Ephemeral: !strings.HasPrefix(id, "d-"),
			Pinned:    typ == "pinned",
		}
	}
	for _, edge := range edges {
		ends := strings.SplitN(edge, ">", 2)
		target := strings.SplitN(ends[1], ":", 2)
		r.edges = append(r.edges, &types.Dependency{IssueID: ends[0], DependsOnID: target[0], Type: types.DependencyType(target[1])})
	}
	return r
}

// TestSelectClosedWispsForPurge pins GH#5152's closed-mode selector: a closed
// wisp a live row depends on through parent-child or tracks is held back, and
// so is every closed ancestor of a held-back wisp, while blocks edges and
// unrelated links protect nothing (GH#5735). "Live" is the not-done status
// vocabulary, custom statuses included, on either plane. A read the selector
// needs and cannot make fails it rather than holding nothing back.
func TestSelectClosedWispsForPurge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rows    []string
		edges   []string
		custom  []types.CustomStatus
		exclude []types.IssueType
		setup   func(*purgeSelectReader)

		want                  []string
		pinned, infra, held   int
		wantErr               bool
		wantStatusReadSkipped bool
	}{
		{
			name:  "open child holds back its closed root",
			rows:  []string{"r:closed", "c:open", "u:closed"},
			edges: []string{"c>r:parent-child"},
			want:  []string{"u"}, held: 1,
		},
		{
			name:  "live tracker holds back the tracked wisp",
			rows:  []string{"t:closed", "s:in_progress"},
			edges: []string{"s>t:tracks"},
			want:  nil, held: 1,
		},
		{
			name:  "blocks-only live dependent protects nothing",
			rows:  []string{"step:closed", "next:open"},
			edges: []string{"next>step:blocks"},
			want:  []string{"step"}, wantStatusReadSkipped: true,
		},
		{
			name:  "related and discovered-from links protect nothing",
			rows:  []string{"a:closed", "b:open", "c:open"},
			edges: []string{"b>a:related", "c>a:discovered-from"},
			want:  []string{"a"}, wantStatusReadSkipped: true,
		},
		{
			name:  "closed child protects nothing: the molecule is reclaimed whole",
			rows:  []string{"r:closed", "c:closed"},
			edges: []string{"c>r:parent-child"},
			want:  []string{"c", "r"},
		},
		{
			name:  "held-back root holds back its closed ancestors",
			rows:  []string{"g:closed", "p:closed", "r:closed", "c:open"},
			edges: []string{"r>p:parent-child", "p>g:parent-child", "c>r:parent-child"},
			want:  nil, held: 3,
		},
		{
			name:  "cycle of closed wisps terminates",
			rows:  []string{"a:closed", "b:closed", "c:open"},
			edges: []string{"a>b:parent-child", "b>a:parent-child", "c>a:tracks"},
			want:  nil, held: 2,
		},
		{
			name:   "custom non-done status is live, custom done status is not",
			rows:   []string{"r1:closed", "c1:wip", "r2:closed", "c2:shipped"},
			edges:  []string{"c1>r1:parent-child", "c2>r2:parent-child"},
			custom: []types.CustomStatus{{Name: "wip", Category: types.CategoryActive}, {Name: "shipped", Category: types.CategoryDone}},
			want:   []string{"r2"}, held: 1,
		},
		{
			name:  "durable live source protects a wisp",
			rows:  []string{"t:closed", "d-epic:open"},
			edges: []string{"d-epic>t:tracks"},
			want:  nil, held: 1,
		},
		{
			name:    "pinned, infra and excluded candidates are filtered before the edge read",
			rows:    []string{"p:closed:pinned", "a:closed:agent", "b:closed:bug", "u:closed"},
			exclude: []types.IssueType{"bug"},
			want:    []string{"u"}, pinned: 1, infra: 1, wantStatusReadSkipped: true,
		},
		{
			name:  "every candidate held back",
			rows:  []string{"r:closed", "c:open", "t:closed", "s:blocked"},
			edges: []string{"c>r:parent-child", "s>t:tracks"},
			want:  nil, held: 2,
		},
		{
			name:  "no candidates reads no edges",
			rows:  []string{"c:open"},
			setup: func(r *purgeSelectReader) { r.failEdges = true },
			want:  nil, wantStatusReadSkipped: true,
		},
		{
			name:  "unsupported batch edge read falls back to per-target reads",
			rows:  []string{"r:closed", "c:open", "u:closed", "x:closed", "n:open"},
			edges: []string{"c>r:parent-child", "n>x:blocks"},
			setup: func(r *purgeSelectReader) { r.unsupported = true },
			want:  []string{"u", "x"}, held: 1,
		},
		{name: "candidate read failure", rows: []string{"u:closed"}, setup: func(r *purgeSelectReader) { r.failCandidates = true }, wantErr: true},
		{name: "edge read failure", rows: []string{"u:closed"}, setup: func(r *purgeSelectReader) { r.failEdges = true }, wantErr: true},
		{
			name:    "fallback edge read failure",
			rows:    []string{"u:closed"},
			setup:   func(r *purgeSelectReader) { r.unsupported = true; r.failFallback = true },
			wantErr: true,
		},
		{
			name:    "custom status read failure",
			rows:    []string{"r:closed", "c:open"},
			edges:   []string{"c>r:parent-child"},
			setup:   func(r *purgeSelectReader) { r.failCustom = true },
			wantErr: true,
		},
		{
			name:    "source status read failure",
			rows:    []string{"r:closed", "c:open"},
			edges:   []string{"c>r:parent-child"},
			setup:   func(r *purgeSelectReader) { r.failStatus = true },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := purgeSelectFixture(tt.rows, tt.edges)
			r.custom = tt.custom
			if tt.setup != nil {
				tt.setup(r)
			}
			sel, err := selectClosedWispsForPurge(context.Background(), r, tt.exclude)
			if tt.wantErr {
				if !errors.Is(err, errPurgeSelectRead) {
					t.Fatalf("selectClosedWispsForPurge error = %v, want the injected read failure", err)
				}
				if len(sel.Candidates) != 0 {
					t.Errorf("failed selection returned candidates %v", sel.ids())
				}
				return
			}
			if err != nil {
				t.Fatalf("selectClosedWispsForPurge: %v", err)
			}
			if got := sel.ids(); !slices.Equal(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Errorf("candidates = %v, want %v", got, tt.want)
			}
			if sel.PinnedSkipped != tt.pinned || sel.InfraSkipped != tt.infra || sel.LiveDependentSkipped != tt.held {
				t.Errorf("skips = pinned %d, infra %d, live %d; want %d, %d, %d",
					sel.PinnedSkipped, sel.InfraSkipped, sel.LiveDependentSkipped, tt.pinned, tt.infra, tt.held)
			}
			if tt.wantStatusReadSkipped && len(r.statusReadSizes) != 0 {
				t.Errorf("status read ran %v with no protecting sources; an empty IDs filter must not become a scan", r.statusReadSizes)
			}
		})
	}

	// More live sources than one status read answers: every one of them must
	// still protect its root, so the selector batches the source ids.
	t.Run("many live sources are all read", func(t *testing.T) {
		t.Parallel()
		const n = 1200
		var rows, edges []string
		for i := 0; i < n; i++ {
			rows = append(rows, fmt.Sprintf("r%04d:closed", i), fmt.Sprintf("c%04d:open", i))
			edges = append(edges, fmt.Sprintf("c%04d>r%04d:parent-child", i, i))
		}
		rows = append(rows, "u:closed")
		r := purgeSelectFixture(rows, edges)
		sel, err := selectClosedWispsForPurge(context.Background(), r, nil)
		if err != nil {
			t.Fatalf("selectClosedWispsForPurge: %v", err)
		}
		if got := sel.ids(); !slices.Equal(got, []string{"u"}) || sel.LiveDependentSkipped != n {
			t.Errorf("candidates = %d ids, live_dependent_skipped %d; want only u, %d held back", len(got), sel.LiveDependentSkipped, n)
		}
		for _, size := range r.statusReadSizes {
			if size > purgeSelectStatusReadCap {
				t.Errorf("one status read named %d sources; reads must be batched below the store's result cap", size)
			}
		}
	})
}

// TestWispPurgeClosedReadFailureDeletesNothing is AC-4 at the caller
// boundary: whichever read the closed-mode selection needs fails, `gc
// --closed --force` returns an error and never reaches the deletion.
func TestWispPurgeClosedReadFailureDeletesNothing(t *testing.T) {
	var deleted [][]string
	var reports []map[string]interface{}
	orig := wispPurgeClosedDelete
	wispPurgeClosedDelete = func(ids []string, _, _, _, _ bool, _ *int64, report map[string]interface{}) error {
		deleted = append(deleted, ids)
		reports = append(reports, report)
		return nil
	}
	t.Cleanup(func() { wispPurgeClosedDelete = orig })

	fixture := func() *purgeSelectReader {
		return purgeSelectFixture(
			[]string{"r:closed", "c:open", "u:closed"},
			[]string{"c>r:parent-child"},
		)
	}
	failures := map[string]func(*purgeSelectReader){
		"candidates":    func(r *purgeSelectReader) { r.failCandidates = true },
		"edges":         func(r *purgeSelectReader) { r.failEdges = true },
		"fallback":      func(r *purgeSelectReader) { r.unsupported = true; r.failFallback = true },
		"custom status": func(r *purgeSelectReader) { r.failCustom = true },
		"source status": func(r *purgeSelectReader) { r.failStatus = true },
	}
	for name, fail := range failures {
		r := fixture()
		fail(r)
		if err := runWispPurgeClosed(context.Background(), r, false, true, nil); err == nil {
			t.Errorf("%s read failure: gc --closed --force returned nil", name)
		}
	}
	if len(deleted) != 0 {
		t.Fatalf("read failures reached deletion with %v", deleted)
	}

	// The same reader without a failure deletes the unrelated wisp only and
	// reports the held-back root.
	if err := runWispPurgeClosed(context.Background(), fixture(), false, true, nil); err != nil {
		t.Fatalf("gc --closed --force: %v", err)
	}
	if len(deleted) != 1 || !slices.Equal(deleted[0], []string{"u"}) {
		t.Fatalf("deleted %v, want [[u]]", deleted)
	}
	if got := reports[0]["live_dependent_skipped"]; got != 1 {
		t.Errorf("report live_dependent_skipped = %v, want 1", got)
	}
}
