//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedSetStateFailedPatchLeavesNoEvent: when the label patch of
// `bd set-state` fails, the state-change event must not be committed either
// (GH#5637). A value whose dimension:value label overflows labels.label
// VARCHAR(255) while the event title stays under 500 chars makes the patch
// fail after the event create succeeded.
func TestEmbeddedSetStateFailedPatchLeavesNoEvent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sa")
	issue := bdCreate(t, bd, dir, "State atomicity issue", "--type", "task")

	events := func() int {
		return len(bdListJSON(t, bd, dir, "--parent", issue.ID, "--all", "--type", "event"))
	}

	// Control: a successful set-state records exactly one event child.
	bdSetState(t, bd, dir, issue.ID, "phase=planning")
	if got := events(); got != 1 {
		t.Fatalf("after one successful set-state: %d event children, want 1", got)
	}

	longValue := strings.Repeat("v", 300)
	cmd := exec.Command(bd, "set-state", issue.ID, "phase="+longValue)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("set-state with a 302-char label succeeded; the repro needs the label patch to fail\nstdout:\n%s", stdout.String())
	}
	t.Logf("set-state failed as intended: %v\nstderr:\n%s", err, stderr.String())

	if got := bdState(t, bd, dir, issue.ID, "phase"); strings.TrimSpace(got) != "planning" {
		t.Fatalf("phase after failed set-state = %q, want planning", strings.TrimSpace(got))
	}
	if got := events(); got != 1 {
		t.Errorf("after a failed set-state: %d event children, want 1 (the failed transition left an orphaned committed event)", got)
	}
}

// TestEmbeddedSetStateSuccessShape pins the success contract of the atomic
// set-state (GH#5637): one closed priority-4 event recording the transition
// and its reason, one parent-child edge to the subject, the label swapped by
// one write, and the JSON output naming the minted event id.
func TestEmbeddedSetStateSuccessShape(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ss")
	issue := bdCreate(t, bd, dir, "Success shape subject", "--type", "task")
	bdSetState(t, bd, dir, issue.ID, "patrol=active")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	// The embedded database admits ONE writer. An OpenSQL handle holds that
	// slot, so a bd subprocess run while it is open blocks on the lock —
	// every CLI invocation below must find the handle closed. Each block of
	// table assertions opens its own short-lived handle and closes it before
	// the next CLI call.
	withDB := func(fn func(count func(query string, args ...interface{}) int, scalar func(query string, args ...interface{}) string)) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		}()
		count := func(query string, args ...interface{}) int {
			var n int
			if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
				t.Fatalf("query %q: %v", query, err)
			}
			return n
		}
		scalar := func(query string, args ...interface{}) string {
			var s string
			if err := db.QueryRowContext(t.Context(), query, args...).Scan(&s); err != nil {
				t.Fatalf("query %q: %v", query, err)
			}
			return s
		}
		fn(count, scalar)
	}

	var eventsBefore int
	withDB(func(count func(string, ...interface{}) int, _ func(string, ...interface{}) string) {
		eventsBefore = count("SELECT COUNT(*) FROM issues WHERE issue_type = 'event'")
	})

	// The transition under test, with the reason the event must carry.
	cmd := exec.Command(bd, "set-state", issue.ID, "patrol=muted", "--reason", "Investigating stuck worker", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("set-state --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &got); err != nil {
		t.Fatalf("set-state --json output is not the retained JSON shape: %v\n%s", err, stdout.String())
	}
	if got["changed"] != true || got["issue_id"] != issue.ID || got["dimension"] != "patrol" {
		t.Errorf("unexpected JSON envelope: %v", got)
	}
	if got["old_value"] != "active" || got["new_value"] != "muted" {
		t.Errorf("old/new values = %v -> %v, want active -> muted", got["old_value"], got["new_value"])
	}
	eventID, ok := got["event_id"].(string)
	if !ok || eventID == "" {
		t.Fatalf("JSON output names no event id: %v", got["event_id"])
	}

	var status, title string
	var priority int
	withDB(func(count func(string, ...interface{}) int, scalar func(string, ...interface{}) string) {
		// EXACTLY ONE NEW EVENT, and it is the row the output named.
		if delta := count("SELECT COUNT(*) FROM issues WHERE issue_type = 'event'") - eventsBefore; delta != 1 {
			t.Errorf("set-state created %d event rows, want exactly 1", delta)
		}
		status = scalar("SELECT status FROM issues WHERE id = ?", eventID)
		priority = count("SELECT priority FROM issues WHERE id = ?", eventID)
		title = scalar("SELECT title FROM issues WHERE id = ?", eventID)
	})
	if status != "closed" {
		t.Errorf("event status = %q, want closed", status)
	}
	if priority != 4 {
		t.Errorf("event priority = %d, want 4", priority)
	}
	if title != "State change: patrol → muted" {
		t.Errorf("event title = %q, want the state-change title", title)
	}
	withDB(func(count func(string, ...interface{}) int, scalar func(string, ...interface{}) string) {
		desc := scalar("SELECT description FROM issues WHERE id = ?", eventID)
		if !strings.Contains(desc, "Changed patrol from active to muted") {
			t.Errorf("event description does not record the transition: %q", desc)
		}
		if !strings.Contains(desc, types.ReasonMarker+"Investigating stuck worker") {
			t.Errorf("event description does not carry the reason after %q: %q", types.ReasonMarker, desc)
		}
		// THE PARENT EDGE TARGETS THE SUBJECT, exactly once.
		if n := count("SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'parent-child'", eventID, issue.ID); n != 1 {
			t.Errorf("parent-child edges %s -> %s = %d, want 1", eventID, issue.ID, n)
		}
		// THE LABEL SWAPPED: old value gone, new value present.
		if n := count("SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", issue.ID, "patrol:active"); n != 0 {
			t.Errorf("old label patrol:active still present (%d rows)", n)
		}
		if n := count("SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", issue.ID, "patrol:muted"); n != 1 {
			t.Errorf("new label patrol:muted rows = %d, want 1", n)
		}
	})

	// AN ORDINARY MINTED ID. The parent edge replaced the old
	// CreateRequest.ParentID spelling, so the event no longer consumes one
	// of the subject's child numbers and its id is not dotted.
	if strings.Contains(eventID, ".") {
		t.Errorf("event id %q is a dotted child id; the batch shape mints ordinary ids", eventID)
	}

	// THE HUMAN OUTPUT RETAINS ITS SHAPE too: the printed Event line names a
	// row that exists, and the success line names the dimension.
	human := bdSetState(t, bd, dir, issue.ID, "phase=planning")
	if !strings.Contains(human, "Set phase = planning on "+issue.ID) {
		t.Errorf("human output lost its success line:\n%s", human)
	}
	eventLineIdx := strings.Index(human, "Event: ")
	if eventLineIdx < 0 {
		t.Fatalf("human output lost its Event line:\n%s", human)
	}
	printedID := strings.TrimSpace(strings.TrimPrefix(human[eventLineIdx:], "Event: "))
	withDB(func(count func(string, ...interface{}) int, _ func(string, ...interface{}) string) {
		if n := count("SELECT COUNT(*) FROM issues WHERE id = ? AND issue_type = 'event'", printedID); n != 1 {
			t.Errorf("human Event line names %q, which is not a stored event row", printedID)
		}
	})
}
