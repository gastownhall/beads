//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// stateAtomicityCount runs one COUNT query against an embedded workspace's
// database, between bd invocations.
func stateAtomicityCount(t *testing.T, beadsDir, query string, args ...any) int {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// stateAtomicityRows is what a set-state writes, counted at the tables: event
// rows on both planes, parent-child edges into the subject on both planes, the
// subject's label rows on both planes, and its patrol value as bd state reads
// it.
type stateAtomicityRows struct {
	events, edges, labels int
	patrol                string
}

func readStateAtomicityRows(t *testing.T, bd, dir, beadsDir, subject string) stateAtomicityRows {
	t.Helper()
	return stateAtomicityRows{
		events: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM issues WHERE issue_type = 'event') +
			(SELECT COUNT(*) FROM wisps WHERE issue_type = 'event')`),
		edges: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM dependencies WHERE type = 'parent-child'
				AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?) +
			(SELECT COUNT(*) FROM wisp_dependencies WHERE type = 'parent-child'
				AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?)`, subject, subject),
		labels: stateAtomicityCount(t, beadsDir, `SELECT
			(SELECT COUNT(*) FROM labels WHERE issue_id = ?) +
			(SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?)`, subject, subject),
		patrol: strings.TrimSpace(bdState(t, bd, dir, subject, "patrol")),
	}
}

// TestEmbeddedSetStateRefusedLabelPatchWritesNothing is GH#5637 on the
// embedded route: when set-state's label patch is refused, the state-change
// event and its parent edge must not be committed either. A value whose
// dimension:value label overflows the 255-character label limit while the
// event title stays under 500 makes the patch the one write that refuses.
func TestEmbeddedSetStateRefusedLabelPatchWritesNothing(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sa")
	subject := bdCreate(t, bd, dir, "State atomicity subject", "--type", "task")
	bdSetState(t, bd, dir, subject.ID, "patrol=active")

	before := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)
	if want := (stateAtomicityRows{events: 1, edges: 1, labels: 1, patrol: "active"}); before != want {
		t.Fatalf("baseline = %+v, want %+v", before, want)
	}

	cmd := exec.Command(bd, "set-state", subject.ID, "patrol="+strings.Repeat("v", 260))
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("set-state with a 267-character label succeeded, want the label patch refused\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "setting patrol:") {
		t.Errorf("stderr does not report the refused label patch as `setting patrol:`:\n%s", stderr.String())
	}

	after := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)
	if after.events != before.events {
		t.Errorf("event rows %d -> %d across a refused set-state, want no new event", before.events, after.events)
	}
	if after.edges != before.edges {
		t.Errorf("parent-child edges into %s %d -> %d across a refused set-state, want no new edge", subject.ID, before.edges, after.edges)
	}
	if after.labels != before.labels || after.patrol != before.patrol {
		t.Errorf("labels %d (patrol %q) -> %d (patrol %q) across a refused set-state, want them unchanged",
			before.labels, before.patrol, after.labels, after.patrol)
	}
}

// stateSetStateJSON runs `bd set-state ... --json` and decodes its object.
func stateSetStateJSON(t *testing.T, bd, dir string, args ...string) map[string]any {
	t.Helper()
	out := bdSetState(t, bd, dir, append(args, "--json")...)
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("bd set-state %s --json printed no object:\n%s", strings.Join(args, " "), out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &m); err != nil {
		t.Fatalf("bd set-state %s --json: %v\n%s", strings.Join(args, " "), err, out)
	}
	return m
}

// stateEventEdges counts the parent-child edges from one event to its subject
// across both dependency tables.
func stateEventEdges(t *testing.T, beadsDir, event, subject string) int {
	t.Helper()
	return stateAtomicityCount(t, beadsDir, `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = 'parent-child'
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND type = 'parent-child'
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id) = ?)`, event, subject, event, subject)
}

// TestEmbeddedSetStateRecordsOneEventWithItsEdgeAndLabelSwap pins what a
// successful set-state on a durable subject writes and prints: exactly one
// closed priority-4 event naming the transition and the reason, one
// parent-child edge from it to the subject, the old dimension label replaced
// by the new one, and the unchanged JSON and human output shapes carrying the
// event id that was written.
func TestEmbeddedSetStateRecordsOneEventWithItsEdgeAndLabelSwap(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sr")
	subject := bdCreate(t, bd, dir, "State record subject", "--type", "task")
	bdSetState(t, bd, dir, subject.ID, "patrol=active")
	before := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)

	res := stateSetStateJSON(t, bd, dir, subject.ID, "patrol=muted", "--reason", "Investigating stuck worker")
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"changed", "dimension", "event_id", "issue_id", "new_value", "old_value", "schema_version"}; !slices.Equal(keys, want) {
		t.Errorf("set-state --json keys = %v, want %v", keys, want)
	}
	if res["changed"] != true || res["dimension"] != "patrol" || res["issue_id"] != subject.ID ||
		res["old_value"] != "active" || res["new_value"] != "muted" {
		t.Errorf("set-state --json = %v, want changed patrol active -> muted on %s", res, subject.ID)
	}
	eventID, _ := res["event_id"].(string)
	if eventID == "" {
		t.Fatalf("set-state --json returned no event_id: %v", res)
	}

	after := readStateAtomicityRows(t, bd, dir, beadsDir, subject.ID)
	if want := (stateAtomicityRows{events: before.events + 1, edges: before.edges + 1, labels: before.labels, patrol: "muted"}); after != want {
		t.Errorf("rows after set-state = %+v, want %+v (one event, one edge, the label swapped in place)", after, want)
	}
	if got := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label LIKE 'patrol:%'", subject.ID); got != 1 {
		t.Errorf("%s carries %d patrol labels, want 1", subject.ID, got)
	}
	if got := stateEventEdges(t, beadsDir, eventID, subject.ID); got != 1 {
		t.Errorf("parent-child edges %s -> %s = %d, want 1", eventID, subject.ID, got)
	}
	event := bdShow(t, bd, dir, eventID)
	if event.IssueType != types.TypeEvent || event.Status != types.StatusClosed || event.Priority != 4 {
		t.Errorf("event %s = type %q status %q priority %d, want a closed priority-4 event", eventID, event.IssueType, event.Status, event.Priority)
	}
	if event.Title != "State change: patrol → muted" {
		t.Errorf("event title = %q, want the transition", event.Title)
	}
	if !strings.Contains(event.Description, "Changed patrol from active to muted") ||
		!strings.Contains(event.Description, "Investigating stuck worker") {
		t.Errorf("event description = %q, want the transition and the reason", event.Description)
	}

	human := bdSetState(t, bd, dir, subject.ID, "patrol=paused")
	if !strings.Contains(human, "Set patrol = paused on "+subject.ID) || !strings.Contains(human, "Previous: muted") {
		t.Errorf("human output lost its shape:\n%s", human)
	}
	_, printed, ok := strings.Cut(human, "Event: ")
	printed = strings.TrimSpace(printed)
	if !ok || printed == "" {
		t.Fatalf("human output names no event:\n%s", human)
	}
	if got := stateEventEdges(t, beadsDir, printed, subject.ID); got != 1 {
		t.Errorf("printed event %q has %d parent-child edges to %s, want 1 (the id written)", printed, got, subject.ID)
	}
}
