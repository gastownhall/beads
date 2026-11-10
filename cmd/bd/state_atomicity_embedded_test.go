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

// TestEmbeddedSetStateRecordsOneHistoryCommit pins AC-3 (GH#5637): one
// successful set-state on a settled durable database with auto-commit records
// ONE history commit, labelled `bd: set-state <id>`, carrying the event, its
// parent edge and the label swap together — rather than the two independent
// commits the old shape made, each with its own message.
func TestEmbeddedSetStateRecordsOneHistoryCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sh")
	issue := bdCreate(t, bd, dir, "History subject", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	// Read the version log in-process between commands, closing the handle
	// before the next bd subprocess runs (one writer at a time).
	logCount := func() (int, string) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		var msg string
		if err := db.QueryRowContext(t.Context(), "SELECT message FROM dolt_log ORDER BY commit_order DESC LIMIT 1").Scan(&msg); err != nil {
			t.Fatalf("read dolt_log head: %v", err)
		}
		return n, msg
	}

	// Baseline on a settled database: init and create have already
	// committed, so history recording is working before the run under test.
	before, _ := logCount()
	bdSetState(t, bd, dir, issue.ID, "patrol=muted", "--reason", "one commit")
	after, head := logCount()

	if delta := after - before; delta != 1 {
		t.Fatalf("set-state recorded %d history commits, want exactly 1", delta)
	}
	if want := "bd: set-state " + issue.ID; head != want {
		t.Fatalf("head commit message = %q, want %q", head, want)
	}

	// The ONE commit carries all three writes: nothing else was committed
	// after the baseline, so the event, its parent edge and the swapped
	// label below can only be inside it.
	if got := bdState(t, bd, dir, issue.ID, "patrol"); strings.TrimSpace(got) != "muted" {
		t.Errorf("patrol after set-state = %q, want muted", strings.TrimSpace(got))
	}
	if children := len(bdListJSON(t, bd, dir, "--parent", issue.ID, "--all", "--type", "event")); children != 1 {
		t.Errorf("event children = %d, want 1", children)
	}
}

// TestEmbeddedSetStateKeepsTheWispPlane pins AC-6 (GH#5637): a subject whose
// retention mode stays stable across the command — ephemeral, or no-history —
// gets an event that RETAINS that mode, an edge and labels that persist in the
// wisp plane, and no durable issue/edge/label row or history entry beside
// them. The assertions read the raw wisp tables rather than the reader, because
// the reader answers from whichever plane the write chose and a write and a
// read that agree on the wrong plane still agree with each other.
func TestEmbeddedSetStateKeepsTheWispPlane(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	for _, tc := range []struct {
		name       string
		flag       string
		modeColumn string
	}{
		{"ephemeral", "--ephemeral", "ephemeral"},
		{"no_history", "--no-history", "no_history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bd := buildEmbeddedBD(t)
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sw")
			subject := bdCreate(t, bd, dir, "Wisp state subject", "--type", "task", tc.flag)
			bdSetState(t, bd, dir, subject.ID, "patrol=active")

			cfg, _ := configfile.Load(beadsDir)
			database := ""
			if cfg != nil {
				database = cfg.GetDoltDatabase()
			}
			// withDB: the one-writer discipline — every CLI call below must
			// find the OpenSQL handle closed (see TestEmbeddedSetStateSuccessShape).
			withDB := func(fn func(count func(query string, args ...interface{}) int)) {
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
				fn(count)
			}
			logCount := func() int {
				var n int
				withDB(func(count func(string, ...interface{}) int) {
					n = count("SELECT COUNT(*) FROM dolt_log")
				})
				return n
			}

			before := logCount()
			cmd := exec.Command(bd, "set-state", subject.ID, "patrol=muted", "--reason", "wisp mode", "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("set-state on a %s subject: %v\nstdout:\n%s\nstderr:\n%s", tc.name, err, stdout.String(), stderr.String())
			}
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &got); err != nil {
				t.Fatalf("set-state --json output is not JSON: %v\n%s", err, stdout.String())
			}
			eventID, ok := got["event_id"].(string)
			if !ok || eventID == "" {
				t.Fatalf("JSON output names no event id: %v", got["event_id"])
			}

			withDB(func(count func(string, ...interface{}) int) {
				// THE EVENT RETAINS THE SUBJECT'S MODE: it is a wisp row with
				// the mode column set, and no durable issue row exists.
				if n := count("SELECT COUNT(*) FROM wisps WHERE id = ? AND "+tc.modeColumn+" = 1", eventID); n != 1 {
					t.Errorf("event %s as a %s wisp row = %d, want 1", eventID, tc.name, n)
				}
				if n := count("SELECT COUNT(*) FROM issues WHERE id = ?", eventID); n != 0 {
					t.Errorf("the event landed %d durable issue row(s), want none", n)
				}
				// THE EDGE PERSISTS IN THE WISP PLANE, and only there.
				if n := count("SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?", eventID, subject.ID); n != 1 {
					t.Errorf("wisp plane parent edges %s -> %s = %d, want 1", eventID, subject.ID, n)
				}
				if n := count("SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?", eventID, subject.ID); n != 0 {
					t.Errorf("the event left %d durable edge row(s), want none", n)
				}
				// THE LABEL SWAP PERSISTS IN THE WISP PLANE, and the durable
				// label table stays empty for the subject.
				if n := count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = 'patrol:active'", subject.ID); n != 0 {
					t.Errorf("old label patrol:active survived the swap (%d wisp rows)", n)
				}
				if n := count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = 'patrol:muted'", subject.ID); n != 1 {
					t.Errorf("wisp_labels rows for patrol:muted = %d, want 1", n)
				}
				if n := count("SELECT COUNT(*) FROM labels WHERE issue_id = ?", subject.ID); n != 0 {
					t.Errorf("durable label rows for a wisp subject = %d, want 0", n)
				}
			})
			if delta := logCount() - before; delta != 0 {
				t.Errorf("a wisp-plane set-state recorded %d durable history entries, want none", delta)
			}
		})
	}
}
