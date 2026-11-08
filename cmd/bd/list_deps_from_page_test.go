//go:build cgo

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// pageEdgeSpyStore is a real store that counts the two ways a `bd list` text
// rendering can read the listed issues' edges after its page read: the
// EdgeReader role and the raw GetDependencyRecordsForIssues batch. afterList,
// when set, runs once after the page read returns, standing in for another
// process writing between the page and any later read.
type pageEdgeSpyStore struct {
	storage.DoltStorage
	edgeReaders    int
	depRecordReads int
	afterList      func(ctx context.Context)
}

func (s *pageEdgeSpyStore) EdgeReader() (issueops.EdgeReader, error) {
	s.edgeReaders++
	return s.DoltStorage.EdgeReader()
}

func (s *pageEdgeSpyStore) GetDependencyRecordsForIssues(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	s.depRecordReads++
	return s.DoltStorage.GetDependencyRecordsForIssues(ctx, ids)
}

func (s *pageEdgeSpyStore) IssueReader() (issueops.Reader, error) {
	inner, err := s.DoltStorage.IssueReader()
	if err != nil {
		return nil, err
	}
	return afterListReader{Reader: inner, spy: s}, nil
}

func (s *pageEdgeSpyStore) reset() {
	s.edgeReaders, s.depRecordReads, s.afterList = 0, 0, nil
}

// afterListReader runs the spy's afterList hook once its page read returns.
type afterListReader struct {
	issueops.Reader
	spy *pageEdgeSpyStore
}

func (r afterListReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	page, err := r.Reader.List(ctx, req)
	if hook := r.spy.afterList; hook != nil && err == nil {
		r.spy.afterList = nil
		hook(ctx)
	}
	return page, err
}

// usePageEdgeSpy opens a Dolt server store and installs a pageEdgeSpyStore over
// it as the command store, outside agent mode.
func usePageEdgeSpy(t *testing.T, prefix string) (*dolt.DoltStore, *pageEdgeSpyStore) {
	t.Helper()
	inner, _ := useOutputReadSpy(t, prefix)
	spy := &pageEdgeSpyStore{DoltStorage: inner}
	store = spy
	t.Setenv("BD_AGENT_MODE", "")
	t.Setenv("CLAUDE_CODE", "")
	return inner, spy
}

func addPageEdge(t *testing.T, s *dolt.DoltStore, from, to string, typ types.DependencyType) {
	t.Helper()
	if err := s.AddDependency(t.Context(), &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}, "tester"); err != nil {
		t.Fatalf("AddDependency(%s -[%s]-> %s): %v", from, typ, to, err)
	}
}

// seedPageEdgeGraph creates an epic with a child, a second child that the
// first blocks, and an open gate holding the second child back.
func seedPageEdgeGraph(t *testing.T, s *dolt.DoltStore, prefix string) {
	t.Helper()
	createOutputReadIssue(t, s, &types.Issue{ID: prefix + "-epic", Title: "Epic", Priority: 0, IssueType: types.TypeEpic})
	createOutputReadIssue(t, s, &types.Issue{ID: prefix + "-a", Title: "Alpha", Priority: 1})
	createOutputReadIssue(t, s, &types.Issue{ID: prefix + "-b", Title: "Bravo", Priority: 2})
	createOutputReadIssue(t, s, &types.Issue{ID: prefix + "-gate", Title: "Open gate", Priority: 2, IssueType: types.TypeGate})
	addPageEdge(t, s, prefix+"-a", prefix+"-epic", types.DepParentChild)
	addPageEdge(t, s, prefix+"-b", prefix+"-epic", types.DepParentChild)
	addPageEdge(t, s, prefix+"-b", prefix+"-a", types.DepBlocks)
	addPageEdge(t, s, prefix+"-b", prefix+"-gate", types.DepBlocks)
}

// TestListPrettyRendersDependenciesFromThePage pins that the pretty `bd list`
// tree draws its nesting, its --deps rows and its gate glyph from the edges
// the page read returned, with no edge read of its own, while --parent and
// --format, whose rows the page does not hydrate the same way, still read the
// edges once.
func TestListPrettyRendersDependenciesFromThePage(t *testing.T) {
	inner, spy := usePageEdgeSpy(t, "lp")
	seedPageEdgeGraph(t, inner, "lp")

	for _, tc := range []struct {
		name        string
		args        []string
		edgeReaders int
		want        []string
	}{
		{"tree", nil, 0, []string{"── ○ lp-a", ui.StatusIconGated + " lp-b"}},
		{"deps all", []string{"--deps=all"}, 0, []string{"── ○ lp-a", "[depends-on]", "lp-a Alpha"}},
		{"parent tree", []string{"--parent", "lp-epic"}, 1, []string{"── ○ lp-a", ui.StatusIconGated + " lp-b"}},
		{"format digraph", []string{"--format", "digraph"}, 1, []string{"lp-b lp-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy.reset()
			out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, tc.args...), nil) })
			if spy.edgeReaders != tc.edgeReaders {
				t.Errorf("bd list %v took the edge reader %d times, want %d", tc.args, spy.edgeReaders, tc.edgeReaders)
			}
			if spy.depRecordReads != 0 {
				t.Errorf("bd list %v read dependency records %d times, want 0", tc.args, spy.depRecordReads)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("bd list %v output lacks %q:\n%s", tc.args, w, out)
				}
			}
		})
	}
}

// TestListCompactAndAgentGateDecorationUsesThePage pins that the flat compact
// and agent renderings decide "gated by" from the edges the page read returned,
// with no dependency-record read for the open rows that can be gated.
func TestListCompactAndAgentGateDecorationUsesThePage(t *testing.T) {
	inner, spy := usePageEdgeSpy(t, "lg")
	seedPageEdgeGraph(t, inner, "lg")

	for _, tc := range []struct {
		name  string
		agent bool
	}{
		{"compact", false},
		{"agent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.agent {
				t.Setenv("BD_AGENT_MODE", "1")
			}
			spy.reset()
			out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, "--flat", "--no-pager"), nil) })
			if spy.depRecordReads != 0 {
				t.Errorf("bd list --flat (%s) read dependency records %d times, want 0", tc.name, spy.depRecordReads)
			}
			if spy.edgeReaders != 0 {
				t.Errorf("bd list --flat (%s) took the edge reader %d times, want 0", tc.name, spy.edgeReaders)
			}
			if !strings.Contains(out, "gated by: lg-gate") {
				t.Errorf("bd list --flat (%s) does not name the gate holding lg-b:\n%s", tc.name, out)
			}
		})
	}
}

// TestListPageDependenciesFollowTheDurableRowOfAnOverlappingID pins the plane
// of the rendered edges for an id present in both issues and wisps: the
// default listing shows the durable row, and its edges are the durable row's,
// not the shadow wisp's.
func TestListPageDependenciesFollowTheDurableRowOfAnOverlappingID(t *testing.T) {
	inner, spy := usePageEdgeSpy(t, "lo")
	createOutputReadIssue(t, inner, &types.Issue{ID: "lo-ovl", Title: "Durable body", Priority: 2})
	addPageEdge(t, inner, "lo-ovl", "external:durable:cap", types.DepBlocks)
	db := inner.UnderlyingDB()
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) "+
			"VALUES (?, 'Shadow wisp', '', '', '', '', 'open', 2, 'task', 1)", "lo-ovl"); err != nil {
		t.Fatalf("seed shadow wisp: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_external, type, created_at, created_by) "+
			"VALUES (UUID(), ?, 'external:wisp:cap', 'blocks', NOW(), 'tester')", "lo-ovl"); err != nil {
		t.Fatalf("seed shadow wisp edge: %v", err)
	}

	spy.reset()
	out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, "--deps=all"), nil) })
	if !strings.Contains(out, "Durable body") {
		t.Fatalf("bd list printed another body than the durable row's:\n%s", out)
	}
	if !strings.Contains(out, "external:durable:cap") {
		t.Errorf("bd list --deps=all lacks the durable row's edge:\n%s", out)
	}
	if strings.Contains(out, "external:wisp:cap") {
		t.Errorf("bd list --deps=all drew the shadow wisp's edge under the durable body:\n%s", out)
	}
}

// TestListPageDependenciesRenderAsOfThePageSnapshot pins the snapshot the
// pretty tree draws: an issue deleted by another process after the page read
// is still drawn with the edges the page read returned, so it nests under its
// parent as listed. The retained edge read of --format, a later transaction,
// still drops the edges of an issue it finds missing.
func TestListPageDependenciesRenderAsOfThePageSnapshot(t *testing.T) {
	inner, spy := usePageEdgeSpy(t, "ld")
	createOutputReadIssue(t, inner, &types.Issue{ID: "ld-epic", Title: "Epic", Priority: 0, IssueType: types.TypeEpic})
	createOutputReadIssue(t, inner, &types.Issue{ID: "ld-child", Title: "Child", Priority: 1})
	createOutputReadIssue(t, inner, &types.Issue{ID: "ld-target", Title: "Target", Priority: 1})
	createOutputReadIssue(t, inner, &types.Issue{ID: "ld-src", Title: "Source", Priority: 1})
	addPageEdge(t, inner, "ld-child", "ld-epic", types.DepParentChild)
	addPageEdge(t, inner, "ld-src", "ld-target", types.DepBlocks)
	deleteAfterList := func(id string) func(ctx context.Context) {
		return func(ctx context.Context) {
			if err := inner.DeleteIssue(ctx, id); err != nil {
				t.Errorf("concurrent delete of %s: %v", id, err)
			}
		}
	}

	t.Run("pretty tree", func(t *testing.T) {
		spy.reset()
		spy.afterList = deleteAfterList("ld-child")
		out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t), nil) })
		if spy.afterList != nil {
			t.Fatal("the page read never ran the concurrent delete")
		}
		if !strings.Contains(out, "── ○ ld-child") {
			t.Errorf("bd list does not nest ld-child under its parent as of the page read:\n%s", out)
		}
	})

	t.Run("format digraph keeps missing-anchor suppression", func(t *testing.T) {
		spy.reset()
		control := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, "--format", "digraph"), nil) })
		if !strings.Contains(control, "ld-src ld-target") {
			t.Fatalf("bd list --format digraph lacks the ld-src edge before any delete:\n%s", control)
		}

		spy.reset()
		spy.afterList = deleteAfterList("ld-src")
		out := captureStdout(t, func() error { return runListCore(newListFlagsCommand(t, "--format", "digraph"), nil) })
		if spy.afterList != nil {
			t.Fatal("the page read never ran the concurrent delete")
		}
		if spy.edgeReaders != 1 {
			t.Errorf("bd list --format digraph took the edge reader %d times, want 1", spy.edgeReaders)
		}
		if strings.Contains(out, "ld-src ld-target") {
			t.Errorf("bd list --format digraph printed an edge of ld-src, deleted before its edge read:\n%s", out)
		}
	})
}
