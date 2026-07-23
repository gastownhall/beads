package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestProtocol_OpenParentDoesNotBlockChildren pins protocol clause G2.2:
// parent-child is structure, not blocking. A merely-open, unblocked parent epic
// does NOT hold its children out of the ready front — only a *blocked* parent
// propagates. This is load-bearing for every epic-driven worker fleet: if open
// epics blocked their children, the ready front would be empty for all work
// that lives under an epic.
func TestProtocol_OpenParentDoesNotBlockChildren(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	epic := w.create("--title", "Open epic", "--type", "epic")
	child := w.create("--title", "Child of open epic", "--type", "task", "--parent", epic)
	grandchild := w.create("--title", "Grandchild of open epic", "--type", "task", "--parent", child)

	ready := parseReadyIDs(t, w)
	if !ready[child] {
		t.Errorf("G2.2: child %s of open epic %s is not in the ready front", child, epic)
	}
	if !ready[grandchild] {
		t.Errorf("G2.2: grandchild %s under open epic %s is not in the ready front", grandchild, epic)
	}
	if out := w.run("blocked", "--json"); findByID(parseJSONOutput(t, out), child) != nil {
		t.Errorf("G2.2: child %s of an open epic is reported blocked", child)
	}

	// Contrast (G2.1): a parent that is itself BLOCKED does propagate — the
	// child leaves the ready front until the epic's blocker closes. (The
	// blocker must itself be an epic: cross-type blocking is rejected, GH#1495.)
	blocker := w.create("--title", "Blocks the epic", "--type", "epic")
	w.run("dep", "add", epic, blocker)

	ready = parseReadyIDs(t, w)
	if ready[child] {
		t.Errorf("G2.1: child %s stayed ready while its parent epic %s is blocked by %s", child, epic, blocker)
	}

	// Closing the epic's blocker restores the children to the ready front (R6).
	w.run("close", blocker, "--reason", "done")
	ready = parseReadyIDs(t, w)
	if !ready[child] {
		t.Errorf("G2.2/R6: child %s did not return to the ready front after the epic's blocker closed", child)
	}
}

// TestProtocol_ReadyParentFilterIsTransitive pins protocol clause R3:
// `bd ready --parent <id>` restricts to TRANSITIVE descendants, not just direct
// children (GH#3396 — a one-hop subquery silently dropped grandchildren while
// the help text promised recursion). A fleet draining an epic by
// `bd ready --parent <epic>` would never see work nested one level deeper.
func TestProtocol_ReadyParentFilterIsTransitive(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	epic := w.create("--title", "Program epic", "--type", "epic")
	child := w.create("--title", "Work package", "--type", "task", "--parent", epic)
	grandchild := w.create("--title", "Unit under the work package", "--type", "task", "--parent", child)
	greatGrandchild := w.create("--title", "Sub-unit", "--type", "task", "--parent", grandchild)

	// An unrelated tree, plus an explicit (non-dotted) parent-child edge, so the
	// filter is exercised against both id-derived and edge-derived descent.
	other := w.create("--title", "Unrelated epic", "--type", "epic")
	otherChild := w.create("--title", "Unrelated work", "--type", "task", "--parent", other)
	adopted := w.create("--title", "Adopted child (edge, not dotted id)", "--type", "task")
	w.run("dep", "add", adopted, epic, "--type", "parent-child")

	out := w.run("ready", "--parent", epic, "--json")
	var got []string
	for _, item := range parseJSONOutput(t, out) {
		if id, ok := item["id"].(string); ok {
			got = append(got, id)
		}
	}

	// Every transitive descendant, by dotted id and by parent-child edge; the
	// epic itself and the unrelated tree are excluded.
	requireStringSetEqual(t, got,
		[]string{child, grandchild, greatGrandchild, adopted},
		"R3: `bd ready --parent "+epic+"` transitive descendants")

	for _, id := range got {
		if id == other || id == otherChild {
			t.Errorf("R3: --parent %s leaked an unrelated issue %s", epic, id)
		}
	}
}

// TestProtocol_ReadyDefaultOrdering pins protocol clause R2: the default
// listing order is priority ASC, then created_at ASC (oldest first, FIFO),
// then id ASC. Agents that take "the top item" of `bd ready` depend on this:
// a P0 filed today must outrank a P2 filed a year ago, and the tiebreak must
// be deterministic so two workers reading the same front see the same head.
func TestProtocol_ReadyDefaultOrdering(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// created_at has second granularity, so space the issues whose creation
	// order the test asserts. Same-second pairs are handled by the id tiebreak,
	// which the two explicit-id issues below exercise.
	oldP2 := w.create("--title", "old p2", "-p", "2")
	time.Sleep(1100 * time.Millisecond)
	oldP1 := w.create("--title", "old p1", "-p", "1")
	time.Sleep(1100 * time.Millisecond)
	newP1 := w.create("--title", "new p1", "-p", "1")
	p0 := w.create("--title", "urgent", "-p", "0")

	// Two same-priority issues that share created_at exactly, so ONLY the id
	// ASC tiebreak can order them. created_at has second granularity and a bd
	// invocation takes longer than that, so the pair is imported (J6) rather
	// than created — a CLI-created pair would straddle a second boundary and
	// silently stop exercising the tiebreak.
	prefix, _, _ := strings.Cut(oldP2, "-")
	tieA, tieB := prefix+"-tieaaaa", prefix+"-tiebbbb"
	w.importIssues(
		tiedIssueJSONL(tieB, "tie b", 3, "2020-01-01T00:00:00Z"),
		tiedIssueJSONL(tieA, "tie a", 3, "2020-01-01T00:00:00Z"),
	)

	items := readyItems(t, w)
	if len(items) != 6 {
		t.Fatalf("R2: expected 6 ready issues, got %d: %v", len(items), items)
	}

	// The observed sequence MUST equal the sequence the R2 comparator produces
	// from the issues' own priority/created_at/id values.
	want := make([]readyItem, len(items))
	copy(want, items)
	sort.SliceStable(want, func(i, j int) bool { return r2Less(want[i], want[j]) })
	if gotIDs, wantIDs := ids(items), ids(want); !equalStrings(gotIDs, wantIDs) {
		t.Errorf("R2: ready order = %v, want %v (priority ASC, created_at ASC, id ASC)\n  issues: %v", gotIDs, wantIDs, items)
	}

	// Spelled-out expectations, so the comparator check above cannot pass
	// vacuously on a wrong-but-self-consistent order.
	order := ids(items)
	if order[0] != p0 {
		t.Errorf("R2: head of the ready front = %s, want the P0 issue %s (priority ASC dominates)", order[0], p0)
	}
	if indexOf(order, oldP1) > indexOf(order, newP1) {
		t.Errorf("R2: within priority 1, newer %s outranked older %s (created_at must be ASC)", newP1, oldP1)
	}
	if indexOf(order, oldP1) > indexOf(order, oldP2) {
		t.Errorf("R2: P2 %s outranked P1 %s (priority ASC dominates created_at)", oldP2, oldP1)
	}
	a, b := itemByID(items, tieA), itemByID(items, tieB)
	if !a.createdAt.Equal(b.createdAt) {
		t.Fatalf("R2 setup: imported tie pair should share created_at, got %s vs %s", a.createdAt, b.createdAt)
	}
	if indexOf(order, tieA) > indexOf(order, tieB) {
		t.Errorf("R2: %s and %s share priority and created_at but were ordered %v — the tiebreak must be id ASC",
			tieA, tieB, order)
	}

	// Determinism: the same front read twice yields the same order.
	if second := ids(readyItems(t, w)); !equalStrings(order, second) {
		t.Errorf("R2: ready order is not stable across reads: %v then %v", order, second)
	}
}

// tiedIssueJSONL renders one JSONL issue line with an explicit created_at, so a
// test can construct issues whose timestamps tie exactly.
func tiedIssueJSONL(id, title string, priority int, createdAt string) string {
	line, err := json.Marshal(map[string]any{
		"id":         id,
		"title":      title,
		"status":     "open",
		"priority":   priority,
		"issue_type": "task",
		"created_at": createdAt,
		"updated_at": createdAt,
		"created_by": "protocol-test",
	})
	if err != nil {
		panic(err)
	}
	return string(line)
}

// importIssues writes the given JSONL lines to a file in the workspace and
// imports them.
func (w *workspace) importIssues(lines ...string) {
	w.t.Helper()
	path := filepath.Join(w.dir, "import.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
	w.run("import", path)
}

// readyItem is the (priority, created_at, id) triple the R2 order is defined on.
type readyItem struct {
	id        string
	priority  int
	createdAt time.Time
}

func readyItems(t *testing.T, w *workspace) []readyItem {
	t.Helper()
	out := w.run("ready", "--json")
	var items []readyItem
	for _, obj := range parseJSONOutput(t, out) {
		id, _ := obj["id"].(string)
		p, ok := obj["priority"].(float64)
		if !ok {
			t.Fatalf("R2: ready item %s has no numeric priority: %v", id, obj)
		}
		raw, _ := obj["created_at"].(string)
		items = append(items, readyItem{id: id, priority: int(p), createdAt: parseTimestamp(t, raw)})
	}
	return items
}

func parseTimestamp(t *testing.T, raw string) time.Time {
	t.Helper()
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC()
		}
	}
	t.Fatalf("R2: cannot parse created_at %q", raw)
	return time.Time{}
}

// r2Less is the normative R2 comparator: priority ASC, created_at ASC (FIFO), id ASC.
func r2Less(a, b readyItem) bool {
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if !a.createdAt.Equal(b.createdAt) {
		return a.createdAt.Before(b.createdAt)
	}
	return a.id < b.id
}

func ids(items []readyItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out
}

func itemByID(items []readyItem, id string) readyItem {
	for _, it := range items {
		if it.id == id {
			return it
		}
	}
	return readyItem{}
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

func equalStrings(a, b []string) bool {
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
