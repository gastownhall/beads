package conformance

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the behavior contract for the portable, non-version-control methods
// that SQLite implements through shared issueops helpers: molecule rollups, repo-mtime
// cache, event/dependency streams, dependent
// counts, wisp cascade discovery, comment/audit writes, id rekey, source-repo purge, and
// batch create. Each case is validated against the embedded-Dolt reference (the oracle)
// and, once a method is wired into a backend, must match it there too.
//
// Ordering that the implementations leave unspecified (map iteration, same-second event
// ties, tie-broken "current step") is asserted as a set, never positionally.

// RunPortableMethods runs the portable-method behavior contract. The Dolt reference runs
// it via RunAll; SQLite runs it for the methods supplied by the shared layer.
func RunPortableMethods(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("MoleculeProgress", func(t *testing.T) { testGetMoleculeProgress(t, factory) })
	t.Run("MoleculeLastActivity", func(t *testing.T) { testGetMoleculeLastActivity(t, factory) })
	t.Run("RepoMtime", func(t *testing.T) { testRepoMtime(t, factory) })
	t.Run("AllEventsSince", func(t *testing.T) { testGetAllEventsSince(t, factory) })
	t.Run("AllDependencyRecords", func(t *testing.T) { testGetAllDependencyRecords(t, factory) })
	t.Run("IterAllDependencyRecords", func(t *testing.T) { testIterAllDependencyRecords(t, factory) })
	t.Run("IterAllEventsSince", func(t *testing.T) { testIterAllEventsSince(t, factory) })
	t.Run("CountDependentsByStatus", func(t *testing.T) { testCountDependentsByStatus(t, factory) })
	t.Run("FindWispDependentsRecursive", func(t *testing.T) { testFindWispDependentsRecursive(t, factory) })
	t.Run("AddComment", func(t *testing.T) { testAddComment(t, factory) })
	t.Run("ImportIssueComment", func(t *testing.T) { testImportIssueComment(t, factory) })
	t.Run("PromoteFromEphemeral", func(t *testing.T) { testPromoteFromEphemeral(t, factory) })
	t.Run("UpdateIssueID", func(t *testing.T) { testUpdateIssueID(t, factory) })
	t.Run("DeleteIssuesBySourceRepo", func(t *testing.T) { testDeleteIssuesBySourceRepo(t, factory) })
	t.Run("CreateIssuesWithFullOptions", func(t *testing.T) { testCreateIssuesWithFullOptions(t, factory) })
	t.Run("ReconcileHierarchicalChildIDs", func(t *testing.T) { testReconcileHierarchicalChildIDs(t, factory) })
	t.Run("Slots", func(t *testing.T) { testSlots(t, factory) })
}

// --- helpers (file-private) ---

func depTargets(deps []*types.Dependency) []string {
	out := make([]string, len(deps))
	for i, d := range deps {
		out[i] = d.DependsOnID
	}
	sort.Strings(out)
	return out
}

func boolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eventTypes(events []*types.Event, issueID string) []string {
	var out []string
	for _, e := range events {
		if e.IssueID == issueID {
			out = append(out, string(e.EventType))
		}
	}
	sort.Strings(out)
	return out
}

// parentChild wires child as a parent-child dependent of molecule.
func parentChild(t *testing.T, s storage.DoltStorage, child, molecule string) {
	t.Helper()
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: child, DependsOnID: molecule, Type: types.DepParentChild}, "actor"))
}

// --- Molecule ---

func testGetMoleculeProgress(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Missing molecule: a zeroed struct (with the id echoed), never ErrNotFound.
	got, err := s.GetMoleculeProgress(c, "test-nope")
	must(t, err)
	if got == nil || got.MoleculeID != "test-nope" || got.Total != 0 || got.MoleculeTitle != "" {
		t.Fatalf("missing molecule = %+v, want zeroed with id echoed", got)
	}

	// Molecule with a mix of child statuses.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-m", Title: "Mol", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c1", Title: "c1", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c2", Title: "c2", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c3", Title: "c3", Status: types.StatusClosed}), "a"))
	parentChild(t, s, "test-c1", "test-m")
	parentChild(t, s, "test-c2", "test-m")
	parentChild(t, s, "test-c3", "test-m")

	got, err = s.GetMoleculeProgress(c, "test-m")
	must(t, err)
	if got.MoleculeTitle != "Mol" || got.Total != 3 || got.Completed != 1 || got.InProgress != 1 {
		t.Fatalf("progress = %+v, want title=Mol total=3 completed=1 inprogress=1", got)
	}
	if got.CurrentStepID != "test-c2" {
		t.Errorf("CurrentStepID = %q, want test-c2 (the single in_progress child)", got.CurrentStepID)
	}
	// FirstClosed/LastClosed are never populated by this implementation.
	if got.FirstClosed != nil || got.LastClosed != nil {
		t.Errorf("FirstClosed/LastClosed = %v/%v, want nil/nil", got.FirstClosed, got.LastClosed)
	}

	// Wisp molecule routes to the wisp tables and rolls up identically.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wm", Title: "WMol", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wc1", Title: "wc1", Status: types.StatusClosed, Ephemeral: true}), "a"))
	parentChild(t, s, "test-wc1", "test-wm")
	wgot, err := s.GetMoleculeProgress(c, "test-wm")
	must(t, err)
	if wgot.Total != 1 || wgot.Completed != 1 {
		t.Fatalf("wisp progress = %+v, want total=1 completed=1", wgot)
	}
}

func testGetMoleculeLastActivity(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Missing molecule: an error (NOT the ErrNotFound sentinel).
	if _, err := s.GetMoleculeLastActivity(c, "test-nope"); err == nil {
		t.Fatal("missing molecule: want error, got nil")
	}

	// Childless molecule reports its own updated_at as molecule_updated.
	updated := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-m", Title: "Mol", CreatedAt: updated, UpdatedAt: updated}), "a"))
	la, err := s.GetMoleculeLastActivity(c, "test-m")
	must(t, err)
	if la.Source != "molecule_updated" || !la.LastActivity.Equal(updated) || la.SourceStepID != "" {
		t.Fatalf("childless = %+v, want molecule_updated at %v, no step", la, updated)
	}

	// Children, none closed: step_updated at the max child updated_at.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-a", Title: "a", Status: types.StatusOpen, CreatedAt: old, UpdatedAt: old}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-b", Title: "b", Status: types.StatusOpen, CreatedAt: newer, UpdatedAt: newer}), "a"))
	parentChild(t, s, "test-a", "test-m")
	parentChild(t, s, "test-b", "test-m")
	la, err = s.GetMoleculeLastActivity(c, "test-m")
	must(t, err)
	if la.Source != "step_updated" || !la.LastActivity.Equal(newer) || la.SourceStepID != "test-b" {
		t.Fatalf("step activity = %+v, want step_updated at %v from test-b", la, newer)
	}
}

// --- Repo mtime cache ---

func testRepoMtime(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	// Absolute paths only — ClearRepoMtime abs-normalizes its key, Get/Set do not.
	repo := t.TempDir()
	jsonl := repo + "/issues.jsonl"

	// Missing key reads as 0 with no error.
	if v, err := s.GetRepoMtime(c, repo); err != nil || v != 0 {
		t.Fatalf("missing = (%d,%v), want (0,nil)", v, err)
	}

	// Round-trip, including a large int64 with no truncation.
	const big = int64(4611686018427387904) // 1<<62
	must(t, s.SetRepoMtime(c, repo, jsonl, big))
	if v, err := s.GetRepoMtime(c, repo); err != nil || v != big {
		t.Fatalf("after set = (%d,%v), want (%d,nil)", v, err, big)
	}

	// Upsert: last write wins for the same path.
	must(t, s.SetRepoMtime(c, repo, jsonl, 222))
	if v, _ := s.GetRepoMtime(c, repo); v != 222 {
		t.Errorf("after overwrite = %d, want 222", v)
	}

	// Distinct paths are independent.
	repo2 := t.TempDir()
	must(t, s.SetRepoMtime(c, repo2, repo2+"/j", 7))
	if v, _ := s.GetRepoMtime(c, repo2); v != 7 {
		t.Errorf("second path = %d, want 7", v)
	}

	// Clear removes the entry; re-clear is a no-op.
	must(t, s.ClearRepoMtime(c, repo))
	if v, err := s.GetRepoMtime(c, repo); err != nil || v != 0 {
		t.Fatalf("after clear = (%d,%v), want (0,nil)", v, err)
	}
	must(t, s.ClearRepoMtime(c, repo))
}

// --- Event stream ---

func testGetAllEventsSince(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Empty store: nil slice, nil error.
	if evs, err := s.GetAllEventsSince(c, time.Time{}); err != nil || len(evs) != 0 {
		t.Fatalf("empty = (%d,%v), want (0,nil)", len(evs), err)
	}

	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "one"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "wisp", Ephemeral: true}), "a")) // routes to wisp_events

	evs, err := s.GetAllEventsSince(c, past)
	must(t, err)
	// Both the durable and the wisp 'created' events are unioned in.
	if !contains(eventTypes(evs, "test-1"), "created") {
		t.Errorf("durable created event missing; types=%v", eventTypes(evs, "test-1"))
	}
	if !contains(eventTypes(evs, "test-w"), "created") {
		t.Errorf("wisp created event missing (UNION ALL wisp_events); types=%v", eventTypes(evs, "test-w"))
	}
	// Non-decreasing created_at.
	for i := 1; i < len(evs); i++ {
		if evs[i].CreatedAt.Before(evs[i-1].CreatedAt) {
			t.Errorf("events not ascending at %d: %v < %v", i, evs[i].CreatedAt, evs[i-1].CreatedAt)
		}
	}

	// A since in the future excludes everything.
	if future, err := s.GetAllEventsSince(c, time.Now().UTC().Add(time.Hour)); err != nil || len(future) != 0 {
		t.Fatalf("future = (%d,%v), want (0,nil)", len(future), err)
	}
}

func testIterAllEventsSince(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "one"}), "a"))

	it, err := s.IterAllEventsSince(c, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	must(t, err)
	evs, err := storage.Collect(c, it)
	must(t, err)
	if !contains(eventTypes(evs, "test-1"), "created") {
		t.Errorf("stream missing created event for test-1; types=%v", eventTypes(evs, "test-1"))
	}

	// Future since: empty stream, clean lifecycle.
	it2, err := s.IterAllEventsSince(c, time.Now().UTC().Add(time.Hour))
	must(t, err)
	empty, err := storage.Collect(c, it2)
	must(t, err)
	if len(empty) != 0 {
		t.Errorf("future stream = %d events, want 0", len(empty))
	}
}

// --- Dependency records ---

func testGetAllDependencyRecords(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Empty store: a non-nil, empty map.
	m, err := s.GetAllDependencyRecords(c)
	must(t, err)
	if m == nil || len(m) != 0 {
		t.Fatalf("empty = %v, want non-nil empty map", m)
	}

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-a", Title: "a"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-b", Title: "b"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "w", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-epic", Title: "e", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-child", Title: "ch"}), "a"))
	// Durable blocks edge, a wisp-source edge, and a parent-child edge (no type filter).
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks}, "actor"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-w", DependsOnID: "test-a", Type: types.DepBlocks}, "actor"))
	parentChild(t, s, "test-child", "test-epic")

	m, err = s.GetAllDependencyRecords(c)
	must(t, err)
	if got := depTargets(m["test-b"]); !slices.Equal(got, []string{"test-a"}) {
		t.Errorf("test-b targets = %v, want [test-a]", got)
	}
	if got := depTargets(m["test-w"]); !slices.Equal(got, []string{"test-a"}) {
		t.Errorf("wisp-source edge missing: test-w targets = %v, want [test-a]", got)
	}
	if got := depTargets(m["test-child"]); !slices.Equal(got, []string{"test-epic"}) {
		t.Errorf("parent-child edge missing: test-child targets = %v, want [test-epic]", got)
	}
	// Metadata defaults to the JSON-empty literal, not "".
	if len(m["test-b"]) > 0 && m["test-b"][0].Metadata != "{}" {
		t.Errorf("default metadata = %q, want {}", m["test-b"][0].Metadata)
	}

	// A removed edge drops out.
	must(t, s.RemoveDependency(c, "test-b", "test-a", "actor"))
	m, err = s.GetAllDependencyRecords(c)
	must(t, err)
	if len(m["test-b"]) != 0 {
		t.Errorf("after remove, test-b still has %v", depTargets(m["test-b"]))
	}
}

func testIterAllDependencyRecords(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Empty store: immediate exhaustion.
	it, err := s.IterAllDependencyRecords(c)
	must(t, err)
	if empty, err := storage.Collect(c, it); err != nil || len(empty) != 0 {
		t.Fatalf("empty stream = (%d,%v), want (0,nil)", len(empty), err)
	}

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-a", Title: "a"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-b", Title: "b"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "w", Ephemeral: true}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks}, "actor"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-w", DependsOnID: "test-a", Type: types.DepBlocks}, "actor"))

	// The stream is the flattened multiset of GetAllDependencyRecords (order unspecified).
	it, err = s.IterAllDependencyRecords(c)
	must(t, err)
	streamed, err := storage.Collect(c, it)
	must(t, err)
	got := make([]string, 0, len(streamed))
	for _, d := range streamed {
		got = append(got, d.IssueID+"->"+d.DependsOnID)
	}
	sort.Strings(got)
	if want := []string{"test-b->test-a", "test-w->test-a"}; !slices.Equal(got, want) {
		t.Errorf("streamed edges = %v, want %v (as a set)", got, want)
	}
}

// --- Dependent counts ---

func testCountDependentsByStatus(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// No such target: 0, nil.
	if n, err := s.CountDependentsByStatus(c, "nope", types.StatusOpen); err != nil || n != 0 {
		t.Fatalf("missing target = (%d,%v), want (0,nil)", n, err)
	}

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-t", Title: "target"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-d", Title: "dep", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wd", Title: "wisp dep", Status: types.StatusOpen, Ephemeral: true}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-d", DependsOnID: "test-t", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-wd", DependsOnID: "test-t", Type: types.DepBlocks}, "a"))

	// Durable + wisp dependents are both counted (be-xbl83 regression guard).
	if n, err := s.CountDependentsByStatus(c, "test-t", types.StatusOpen); err != nil || n != 2 {
		t.Fatalf("open dependents = (%d,%v), want (2,nil) — wisp dependent must be included", n, err)
	}
	// Status discriminates.
	if n, _ := s.CountDependentsByStatus(c, "test-t", types.StatusClosed); n != 0 {
		t.Errorf("closed dependents = %d, want 0", n)
	}

	// No type filter: a parent-child dependent still counts.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-e", Title: "epic", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-ch", Title: "child", Status: types.StatusOpen}), "a"))
	parentChild(t, s, "test-ch", "test-e")
	if n, err := s.CountDependentsByStatus(c, "test-e", types.StatusOpen); err != nil || n != 1 {
		t.Fatalf("parent-child dependent count = (%d,%v), want (1,nil) — no type filter", n, err)
	}
}

func testFindWispDependentsRecursive(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// nil input -> nil map (distinct from the empty-non-nil no-dependents case).
	if m, err := s.FindWispDependentsRecursive(c, nil); err != nil || m != nil {
		t.Fatalf("nil input = (%v,%v), want (nil,nil)", m, err)
	}

	// Seed with no wisp dependents -> non-nil empty map.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-x", Title: "x"}), "a"))
	if m, err := s.FindWispDependentsRecursive(c, []string{"test-x"}); err != nil || m == nil || len(m) != 0 {
		t.Fatalf("no wisp deps = (%v,%v), want non-nil empty map", m, err)
	}

	// Transitive wisp chain; only wisp_dependencies is walked (durable dependent excluded).
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-p", Title: "p", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c1", Title: "c1", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-gc", Title: "gc", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-dd", Title: "durable dep"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-c1", DependsOnID: "test-p", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-gc", DependsOnID: "test-c1", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-dd", DependsOnID: "test-p", Type: types.DepBlocks}, "a")) // durable, must be skipped

	m, err := s.FindWispDependentsRecursive(c, []string{"test-p"})
	must(t, err)
	if got := boolKeys(m); !slices.Equal(got, []string{"test-c1", "test-gc"}) {
		t.Fatalf("wisp dependents = %v, want [test-c1 test-gc] (durable dependent excluded, seed excluded)", got)
	}
}

// --- Comment / audit writes ---

func testAddComment(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-ac", Title: "T"}), "a"))

	// AddComment records an audit EVENT, not a structured comment.
	must(t, s.AddComment(c, "test-ac", "alice", "hello"))
	evs, err := s.GetEvents(c, "test-ac", 0)
	must(t, err)
	if !contains(eventTypes(evs, "test-ac"), string(types.EventCommented)) {
		t.Errorf("no commented event; types=%v", eventTypes(evs, "test-ac"))
	}
	if n, _ := s.CountIssueComments(c, "test-ac"); n != 0 {
		t.Errorf("CountIssueComments = %d, want 0 — AddComment writes events, not comments", n)
	}

	// Not idempotent: a second identical call adds a second commented event.
	must(t, s.AddComment(c, "test-ac", "alice", "hello"))
	evs, _ = s.GetEvents(c, "test-ac", 0)
	commented := 0
	for _, e := range evs {
		if e.EventType == types.EventCommented {
			commented++
		}
	}
	if commented != 2 {
		t.Errorf("commented events = %d, want 2", commented)
	}
}

func testImportIssueComment(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-ic", Title: "T"}), "a"))

	// Structured comment preserving a caller-supplied (whole-second UTC) timestamp.
	ts := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := s.ImportIssueComment(c, "test-ic", "alice", "imported", ts)
	must(t, err)
	if got == nil || got.Author != "alice" || got.Text != "imported" || !got.CreatedAt.Equal(ts) || got.ID == "" {
		t.Fatalf("import returned %+v, want author=alice text=imported at %v with an id", got, ts)
	}
	comments, err := s.GetIssueComments(c, "test-ic")
	must(t, err)
	if len(comments) != 1 || comments[0].Text != "imported" || !comments[0].CreatedAt.Equal(ts) {
		t.Fatalf("GetIssueComments = %+v, want the imported comment at %v", comments, ts)
	}
	if n, _ := s.CountIssueComments(c, "test-ic"); n != 1 {
		t.Errorf("CountIssueComments = %d, want 1", n)
	}

	// Non-existent issue is rejected before insert.
	if _, err := s.ImportIssueComment(c, "test-missing", "a", "x", ts); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("import to missing issue err = %v, want a 'not found' error", err)
	}

	// Two imports at distinct timestamps read back in created_at ASC order.
	t1 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-ic2", Title: "T2"}), "a"))
	if _, err := s.ImportIssueComment(c, "test-ic2", "a", "second", t2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportIssueComment(c, "test-ic2", "a", "first", t1); err != nil {
		t.Fatal(err)
	}
	ordered, _ := s.GetIssueComments(c, "test-ic2")
	if len(ordered) != 2 || ordered[0].Text != "first" || ordered[1].Text != "second" {
		t.Errorf("comment order = %v, want [first second] by created_at ASC", commentTexts(ordered))
	}
}

func testPromoteFromEphemeral(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Promote a non-wisp / missing id is rejected.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-dur", Title: "durable"}), "a"))
	if err := s.PromoteFromEphemeral(c, "test-dur", "a"); err == nil {
		t.Error("promote of durable issue: want error, got nil")
	}
	if err := s.PromoteFromEphemeral(c, "test-ghost", "a"); err == nil {
		t.Error("promote of missing id: want error, got nil")
	}

	// Happy path: wisp with a label and an inbound dependent.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "Wisp", Priority: 1, Ephemeral: true, Labels: []string{"x"}}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-dep", Title: "dep", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-dep", DependsOnID: "test-w", Type: types.DepBlocks}, "a"))

	must(t, s.PromoteFromEphemeral(c, "test-w", "a"))
	got, err := s.GetIssue(c, "test-w")
	must(t, err)
	if got.Ephemeral {
		t.Error("promoted issue still Ephemeral")
	}
	if labels, _ := s.GetLabels(c, "test-w"); !contains(labels, "x") {
		t.Errorf("labels after promote = %v, want to include x", labels)
	}
	// The inbound edge was retargeted and still points at the (now durable) issue.
	deps, _ := s.GetAllDependencyRecords(c)
	if got := depTargets(deps["test-dep"]); !slices.Equal(got, []string{"test-w"}) {
		t.Errorf("inbound dep after promote = %v, want [test-w]", got)
	}
	// Second promote fails (no longer a wisp).
	if err := s.PromoteFromEphemeral(c, "test-w", "a"); err == nil {
		t.Error("double promote: want error, got nil")
	}
}

// --- Id rekey / source-repo purge / batch create ---

func testUpdateIssueID(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "One"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "Two"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-2", DependsOnID: "test-1", Type: types.DepBlocks}, "a"))

	// Rename test-1 -> test-9; dependents follow.
	must(t, s.UpdateIssueID(c, "test-1", "test-9", &types.Issue{Title: "One"}, "a"))
	if _, err := s.GetIssue(c, "test-9"); err != nil {
		t.Fatalf("renamed issue not retrievable: %v", err)
	}
	if _, err := s.GetIssue(c, "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("old id lookup err = %v, want ErrNotFound", err)
	}
	deps, _ := s.GetAllDependencyRecords(c)
	if got := depTargets(deps["test-2"]); !slices.Equal(got, []string{"test-9"}) {
		t.Errorf("dependency target after rename = %v, want [test-9]", got)
	}

	// Renaming a non-existent id errors (plain error; not necessarily the sentinel).
	if err := s.UpdateIssueID(c, "test-missing", "test-x", &types.Issue{}, "a"); err == nil {
		t.Error("rename of missing id: want error, got nil")
	}
}

func testDeleteIssuesBySourceRepo(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "A", SourceRepo: "repoX"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "B", SourceRepo: "repoX"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-3", Title: "C", SourceRepo: "repoY"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "W", SourceRepo: "repoX", Ephemeral: true}), "a"))
	must(t, s.AddLabel(c, "test-1", "bug", "a"))

	// Deletes only the matching durable issues; wisps are never touched.
	n, err := s.DeleteIssuesBySourceRepo(c, "repoX")
	must(t, err)
	if n != 2 {
		t.Errorf("deleted = %d, want 2 (durable repoX only)", n)
	}
	if _, err := s.GetIssue(c, "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("test-1 still present: %v", err)
	}
	if _, err := s.GetIssue(c, "test-3"); err != nil {
		t.Errorf("test-3 (repoY) wrongly affected: %v", err)
	}
	if _, err := s.GetIssue(c, "test-w"); err != nil {
		t.Errorf("wisp wrongly deleted by source_repo: %v", err)
	}
	// Child rows of the deleted issue are cascade-cleaned.
	if labels, _ := s.GetLabels(c, "test-1"); len(labels) != 0 {
		t.Errorf("labels of deleted issue = %v, want none", labels)
	}
	// No match -> (0, nil).
	if n, err := s.DeleteIssuesBySourceRepo(c, "repoMISSING"); err != nil || n != 0 {
		t.Errorf("no-match delete = (%d,%v), want (0,nil)", n, err)
	}
}

func testCreateIssuesWithFullOptions(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Empty slice is a no-op.
	must(t, s.CreateIssuesWithFullOptions(c, nil, "a", storage.BatchCreateOptions{}))

	// Durable batch: both created with a 'created' event each.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-a", Title: "A"}),
		withDefaults(&types.Issue{ID: "test-b", Title: "B"}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))
	if _, err := s.GetIssue(c, "test-a"); err != nil {
		t.Fatalf("batch issue test-a missing: %v", err)
	}

	// Same-batch dependency edge is created; a missing-target edge is silently skipped.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-x", Title: "X"}),
		withDefaults(&types.Issue{ID: "test-y", Title: "Y", Dependencies: []*types.Dependency{{IssueID: "test-y", DependsOnID: "test-x", Type: types.DepBlocks}}}),
		withDefaults(&types.Issue{ID: "test-z", Title: "Z", Dependencies: []*types.Dependency{{IssueID: "test-z", DependsOnID: "test-absent", Type: types.DepBlocks}}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))
	recs, _ := s.GetAllDependencyRecords(c)
	if got := depTargets(recs["test-y"]); !slices.Equal(got, []string{"test-x"}) {
		t.Errorf("in-batch edge test-y = %v, want [test-x]", got)
	}
	if len(recs["test-z"]) != 0 {
		t.Errorf("edge to missing target should be skipped, got %v", depTargets(recs["test-z"]))
	}

	// ConflictSkip leaves an existing row untouched.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-keep", Title: "Original"}), "a"))
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{withDefaults(&types.Issue{ID: "test-keep", Title: "Replacement"})},
		"a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true, ConflictSkip: true}))
	if got, _ := s.GetIssue(c, "test-keep"); got.Title != "Original" {
		t.Errorf("ConflictSkip overwrote title to %q, want Original", got.Title)
	}
}

// Batch-creating dotted hierarchical child IDs runs ReconcileChildCounters, which
// emits `... ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, ?)` in the
// child-counter upsert (issueops/create.go). That GREATEST path is reachable only
// from batch create — the single-issue path never reconciles counters — so it was
// dead in the rest of the suite, which is how a missing SQLite GREATEST translation
// (child creation aborted with "no such function: GREATEST") shipped green. This
// scenario mints x-1, x-1.1, x-1.2 through the batch path and asserts every backend
// both creates the children and reconciles the counter to the max direct child.
func testReconcileHierarchicalChildIDs(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "x-1", Title: "parent", IssueType: types.TypeEpic}), "a"))

	// Two dotted children in one batch: this is the create that runs the GREATEST
	// upsert. On a backend that cannot translate GREATEST the whole batch aborts.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "x-1.1", Title: "child one"}),
		withDefaults(&types.Issue{ID: "x-1.2", Title: "child two"}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))

	for _, id := range []string{"x-1.1", "x-1.2"} {
		if _, err := s.GetIssue(c, id); err != nil {
			t.Fatalf("hierarchical child %s not created via the GREATEST upsert path: %v", id, err)
		}
	}

	// A grandchild (x-1.2.1) reconciles x-1.2's counter — the same GREATEST upsert
	// one level deeper — and, being a grandchild, must not advance the x-1 counter.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "x-1.2.1", Title: "grandchild"}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))

	// GetNextChildID reserves-and-advances, so call it once per parent. x-1 was
	// reconciled to its max direct child (2, grandchild excluded) → next is x-1.3;
	// x-1.2 was reconciled to 1 by the grandchild → next is x-1.2.2.
	if next, err := s.GetNextChildID(c, "x-1"); err != nil || next != "x-1.3" {
		t.Errorf("GetNextChildID(x-1) = (%q,%v), want (x-1.3,nil) — reconciled to 2, grandchild excluded", next, err)
	}
	if next, err := s.GetNextChildID(c, "x-1.2"); err != nil || next != "x-1.2.2" {
		t.Errorf("GetNextChildID(x-1.2) = (%q,%v), want (x-1.2.2,nil) — grandchild counter reconciled", next, err)
	}
}

// --- Metadata slots (gt per-issue KV over the issue metadata JSON) ---

func testSlots(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-sl", Title: "T"}), "a"))

	// Get before set is an error (no such slot).
	if _, err := s.SlotGet(c, "test-sl", "k"); err == nil {
		t.Error("SlotGet on an unset key: want error, got nil")
	}

	// Set then get round-trips; a second key is independent.
	must(t, s.SlotSet(c, "test-sl", "k", "v1", "a"))
	must(t, s.SlotSet(c, "test-sl", "k2", "other", "a"))
	if v, err := s.SlotGet(c, "test-sl", "k"); err != nil || v != "v1" {
		t.Fatalf("SlotGet after set = (%q,%v), want (v1,nil)", v, err)
	}

	// Overwrite: last write wins.
	must(t, s.SlotSet(c, "test-sl", "k", "v2", "a"))
	if v, _ := s.SlotGet(c, "test-sl", "k"); v != "v2" {
		t.Errorf("SlotGet after overwrite = %q, want v2", v)
	}

	// Clear removes only the named key; a missing/repeat clear is a silent no-op.
	must(t, s.SlotClear(c, "test-sl", "k", "a"))
	if _, err := s.SlotGet(c, "test-sl", "k"); err == nil {
		t.Error("SlotGet after clear: want error, got nil")
	}
	if v, _ := s.SlotGet(c, "test-sl", "k2"); v != "other" {
		t.Errorf("k2 wrongly affected by clearing k: %q", v)
	}
	must(t, s.SlotClear(c, "test-sl", "k", "a"))     // idempotent
	must(t, s.SlotClear(c, "test-sl", "never", "a")) // never-set key

	// Operating on a non-existent issue is an error (GetIssue fails).
	if err := s.SlotSet(c, "test-missing", "k", "v", "a"); err == nil {
		t.Error("SlotSet on a missing issue: want error, got nil")
	}
}

func commentTexts(cs []*types.Comment) []string {
	out := make([]string, len(cs))
	for i, cm := range cs {
		out[i] = cm.Text
	}
	return out
}

func contains(xs []string, x string) bool {
	return slices.Contains(xs, x)
}
