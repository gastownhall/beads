//go:build cgo

package main

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(stderr.String(), "setting patrol:") || !strings.Contains(stderr.String(), "exceeds maximum length") {
		t.Errorf("stderr does not report the over-length label patch as `setting patrol:`:\n%s", stderr.String())
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
	// An ordinary minted id: the parent edge, not a dotted child id, makes the
	// event the subject's child.
	if strings.Contains(eventID, ".") {
		t.Errorf("event id %q is a dotted child id; the batch shape mints ordinary ids", eventID)
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
		!strings.Contains(event.Description, types.ReasonMarker+"Investigating stuck worker") {
		t.Errorf("event description = %q, want the transition and the reason after %q", event.Description, types.ReasonMarker)
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

// stateHookFiring is one hook run a plantStateHookRecorder script saved: the
// event and the issue id the runner passed as $2 and $1, and the issue payload
// it wrote to the script's stdin.
type stateHookFiring struct {
	event, id string
	payload   stateHookPayload
}

// stateHookPayload is the part of a hook's issue payload the set-state hook
// contract reads.
type stateHookPayload struct {
	ID           string   `json:"id"`
	Labels       []string `json:"labels"`
	Dependencies []struct {
		IssueID     string `json:"issue_id"`
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	} `json:"dependencies"`
}

// plantStateHookRecorder installs on_create and on_update scripts that save
// each firing's payload to a file of its own, named for the event, the issue
// and the script's pid, and returns the directory they save into. A file per
// firing keeps hook processes that run at once from interleaving their writes.
func plantStateHookRecorder(t *testing.T, beadsDir string) string {
	t.Helper()
	logDir := t.TempDir()
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/bin/sh\ncat > " + shellQuote(logDir) + "/\"$2 $1 $$.json\"\n"
	for _, name := range []string{"on_create", "on_update"} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
			t.Fatalf("plant %s: %v", name, err)
		}
	}
	return logDir
}

// readStateHookFirings waits for want saved firings and decodes them. The
// command has returned by then, and a command waits for the hooks it fired
// before it exits, so a count past want is a hook that fired too often.
func readStateHookFirings(t *testing.T, logDir string, want int) []stateHookFiring {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatalf("read the hook recorder's directory: %v", err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) > want {
			t.Fatalf("hooks fired %d times, want %d: %q", len(names), want, names)
		}
		if len(names) == want {
			if firings, ok := decodeStateHookFirings(logDir, names); ok {
				return firings
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hooks fired %d times, want %d: %q", len(names), want, names)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// decodeStateHookFirings reads every saved firing, reporting false while a
// script is still writing one.
func decodeStateHookFirings(logDir string, names []string) ([]stateHookFiring, bool) {
	firings := make([]stateHookFiring, 0, len(names))
	for _, name := range names {
		fields := strings.Fields(strings.TrimSuffix(name, ".json"))
		if len(fields) != 3 {
			return nil, false
		}
		data, err := os.ReadFile(filepath.Join(logDir, name)) // #nosec G304 -- this test's own temp file
		if err != nil {
			return nil, false
		}
		firing := stateHookFiring{event: fields[0], id: fields[1]}
		if err := json.Unmarshal(data, &firing.payload); err != nil {
			return nil, false
		}
		firings = append(firings, firing)
	}
	return firings, true
}

// assertStateChangeHooks checks one set-state's firings against the hook
// contract both routes share (GH#5637): on_create for the event, on_update for
// the event carrying its parent-child edge to the subject, which is the
// payload a script tells the changed bead by, and on_update for the subject
// carrying its new label. Nothing else fires.
func assertStateChangeHooks(t *testing.T, firings []stateHookFiring, eventID, subject, label string) {
	t.Helper()
	got := map[string]int{}
	for _, firing := range firings {
		key := firing.event + " " + firing.id
		got[key]++
		if firing.payload.ID != firing.id {
			t.Errorf("%s: the payload names %q", key, firing.payload.ID)
		}
		switch key {
		case "update " + eventID:
			if !stateHookPayloadHasParent(firing.payload, eventID, subject) {
				t.Errorf("%s: payload dependencies = %+v, want the parent-child edge %s -> %s", key, firing.payload.Dependencies, eventID, subject)
			}
		case "update " + subject:
			if !slices.Contains(firing.payload.Labels, label) {
				t.Errorf("%s: payload labels = %v, want %s among them", key, firing.payload.Labels, label)
			}
		}
	}
	if want := map[string]int{"create " + eventID: 1, "update " + eventID: 1, "update " + subject: 1}; !maps.Equal(got, want) {
		t.Errorf("hooks fired %v, want %v", got, want)
	}
}

func stateHookPayloadHasParent(payload stateHookPayload, eventID, subject string) bool {
	for _, dep := range payload.Dependencies {
		if dep.IssueID == eventID && dep.DependsOnID == subject && dep.Type == string(types.DepParentChild) {
			return true
		}
	}
	return false
}

// TestEmbeddedSetStateHooksSeeTheEventItsEdgeAndTheSubject pins what a
// hook-enabled workspace sees for a set-state on the direct route (GH#5637):
// on_create for the event, on_update for the event carrying its parent-child
// edge to the subject, and on_update for the subject, for a durable subject
// and a wisp alike, and nothing for a refused set-state. The proxied route pins
// the same contract (TestProxiedServerSetState's
// hooks_see_the_event_its_edge_and_the_subject), so a script sees one shape on
// both.
func TestEmbeddedSetStateHooksSeeTheEventItsEdgeAndTheSubject(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("hook script form is POSIX shell")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sh")
	durable := bdCreate(t, bd, dir, "Hooked durable subject", "--type", "task")
	wisp := bdCreate(t, bd, dir, "Hooked wisp subject", "--type", "task", "--ephemeral")

	for _, c := range []struct{ name, subject string }{{"durable", durable.ID}, {"wisp", wisp.ID}} {
		t.Run(c.name, func(t *testing.T) {
			// Planted after the setup, so every firing it saves is this
			// set-state's.
			logDir := plantStateHookRecorder(t, beadsDir)
			res := stateSetStateJSON(t, bd, dir, c.subject, "patrol=muted")
			eventID, _ := res["event_id"].(string)
			if eventID == "" {
				t.Fatalf("set-state --json returned no event_id: %v", res)
			}
			assertStateChangeHooks(t, readStateHookFirings(t, logDir, 3), eventID, c.subject, "patrol:muted")
		})
	}

	t.Run("refused", func(t *testing.T) {
		logDir := plantStateHookRecorder(t, beadsDir)
		cmd := exec.Command(bd, "set-state", durable.ID, "patrol="+strings.Repeat("v", 260))
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if stdout, _, err := runCommandBuffers(t, cmd); err == nil {
			t.Fatalf("set-state with a 267-character label succeeded, want it refused\nstdout:\n%s", stdout.String())
		}
		readStateHookFirings(t, logDir, 0)
	})
}
