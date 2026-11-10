//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerSetState(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sst")
	issue := bdProxiedCreate(t, bd, p.dir, "Set-state target")

	setStateJSON := func(t *testing.T, spec string, extra ...string) map[string]interface{} {
		t.Helper()
		args := append([]string{"set-state", issue.ID, spec, "--json"}, extra...)
		out, err := bdProxiedRun(t, bd, p.dir, args...)
		if err != nil {
			t.Fatalf("set-state %s: %v\n%s", spec, err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got
	}

	t.Run("set_new_dimension", func(t *testing.T) {
		got := setStateJSON(t, "patrol=active", "--reason", "starting patrol")
		if got["changed"] != true {
			t.Errorf("expected changed=true, got %v", got["changed"])
		}
		if got["old_value"] != nil {
			t.Errorf("expected old_value nil for a new dimension, got %v", got["old_value"])
		}
		if got["new_value"] != "active" {
			t.Errorf("new_value = %v, want active", got["new_value"])
		}
		if _, ok := got["event_id"].(string); !ok || got["event_id"] == "" {
			t.Errorf("expected an event_id, got %v", got["event_id"])
		}
		// Label cache must reflect the new state.
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		if !containsStr(labels, "patrol:active") {
			t.Errorf("expected label patrol:active, got %v", labels)
		}
	})

	t.Run("change_swaps_label", func(t *testing.T) {
		got := setStateJSON(t, "patrol=muted")
		if got["changed"] != true || got["old_value"] != "active" || got["new_value"] != "muted" {
			t.Errorf("unexpected change result: %v", got)
		}
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		if containsStr(labels, "patrol:active") {
			t.Errorf("old label patrol:active should have been removed, got %v", labels)
		}
		if !containsStr(labels, "patrol:muted") {
			t.Errorf("expected label patrol:muted, got %v", labels)
		}
	})

	t.Run("no_change_is_noop", func(t *testing.T) {
		got := setStateJSON(t, "patrol=muted")
		if got["changed"] != false {
			t.Errorf("expected changed=false for identical value, got %v", got)
		}
	})

	t.Run("event_bead_created_as_child", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "state", "list", issue.ID, "--json")
		if err != nil {
			t.Fatalf("state list: %v\n%s", err, out)
		}
		var res struct {
			States map[string]string `json:"states"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if res.States["patrol"] != "muted" {
			t.Errorf("state list shows patrol=%q, want muted", res.States["patrol"])
		}
		// The set-state calls created event children under the issue. Since
		// the batch migration (GH#5637) an event's parentage is a
		// parent-child EDGE rather than a dotted child id, so count
		// event-type children through the edge and assert the edge itself
		// at the table.
		children := bdProxiedListJSON(t, bd, p, "--parent", issue.ID, "--status", "all", "--type", "event")
		if len(children) == 0 {
			t.Errorf("expected event-type children under %s, got none", issue.ID)
		}
		db := openProxiedDB(t, p)
		if n := countRows(t, db,
			"SELECT COUNT(*) FROM dependencies d JOIN issues i ON d.issue_id = i.id WHERE i.issue_type = 'event' AND d.depends_on_issue_id = ? AND d.type = 'parent-child'", issue.ID); n != len(children) {
			t.Errorf("parent-child edges from event children = %d, want %d (the listed children)", n, len(children))
		}
	})

	t.Run("failed_patch_leaves_no_event", func(t *testing.T) {
		f := bdProxiedCreate(t, bd, p.dir, "Failed patch target")

		// Control: one successful set-state records exactly one event child
		// and exactly one parent-child edge to it.
		if out, err := bdProxiedRun(t, bd, p.dir, "set-state", f.ID, "patrol=active"); err != nil {
			t.Fatalf("set-state patrol=active: %v\n%s", err, out)
		}
		eventChildren := func() int {
			return len(bdProxiedListJSON(t, bd, p, "--parent", f.ID, "--status", "all", "--type", "event"))
		}
		if got := eventChildren(); got != 1 {
			t.Fatalf("after one successful set-state: %d event children, want 1", got)
		}
		db := openProxiedDB(t, p)
		parentEdges := func() int {
			return countRows(t, db,
				"SELECT COUNT(*) FROM dependencies d JOIN issues i ON d.issue_id = i.id WHERE i.issue_type = 'event' AND d.depends_on_issue_id = ? AND d.type = 'parent-child'", f.ID)
		}
		if got := parentEdges(); got != 1 {
			t.Fatalf("after one successful set-state: %d parent-child edges from event children, want 1", got)
		}
		labelsBefore := getProxiedLabels(t, db, f.ID)

		// The label exceeds labels.label VARCHAR(255) while the event title
		// stays under 500 chars, so the batch's update item is refused after
		// its create item would have landed — which is exactly the refusal
		// that must roll the event back with it.
		longValue := strings.Repeat("v", 300)
		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "set-state", f.ID, "patrol="+longValue)
		if err == nil {
			t.Fatalf("set-state with a 302-char label succeeded; the test needs the label patch to fail")
		}
		if !strings.Contains(stderr, "setting patrol:") || !strings.Contains(stderr, "exceeds maximum length") {
			t.Errorf("failure text does not name the refused label patch:\n%s", stderr)
		}
		if got := eventChildren(); got != 1 {
			t.Errorf("after a failed set-state: %d event children, want 1 (the failed transition left an orphaned committed event)", got)
		}
		if got := parentEdges(); got != 1 {
			t.Errorf("after a failed set-state: %d parent-child edges from event children, want 1", got)
		}
		if labelsAfter := getProxiedLabels(t, db, f.ID); !equalStrSets(labelsBefore, labelsAfter) {
			t.Errorf("labels changed on a failed set-state: before %v, after %v", labelsBefore, labelsAfter)
		}
	})

	t.Run("success_event_shape", func(t *testing.T) {
		s := bdProxiedCreate(t, bd, p.dir, "Success shape subject")
		if out, err := bdProxiedRun(t, bd, p.dir, "set-state", s.ID, "patrol=active"); err != nil {
			t.Fatalf("set-state patrol=active: %v\n%s", err, out)
		}

		// The transition under test, with the reason the event must carry.
		out, err := bdProxiedRun(t, bd, p.dir, "set-state", s.ID, "patrol=muted", "--reason", "Investigating stuck worker", "--json")
		if err != nil {
			t.Fatalf("set-state --json: %v\n%s", err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got["changed"] != true || got["issue_id"] != s.ID || got["old_value"] != "active" || got["new_value"] != "muted" {
			t.Errorf("unexpected JSON envelope: %v", got)
		}
		eventID, ok := got["event_id"].(string)
		if !ok || eventID == "" {
			t.Fatalf("JSON output names no event id: %v", got["event_id"])
		}
		// The batch shape mints an ORDINARY id; the old dotted child id is
		// gone with the CreateRequest.ParentID spelling.
		if strings.Contains(eventID, ".") {
			t.Errorf("event id %q is a dotted child id; the batch shape mints ordinary ids", eventID)
		}

		db := openProxiedDB(t, p)
		count := func(query string, args ...interface{}) int {
			return countRows(t, db, query, args...)
		}
		scalar := func(query string, args ...interface{}) string {
			var v string
			if err := db.QueryRowContext(t.Context(), query, args...).Scan(&v); err != nil {
				t.Fatalf("query %q: %v", query, err)
			}
			return v
		}

		if got := scalar("SELECT status FROM issues WHERE id = ?", eventID); got != "closed" {
			t.Errorf("event status = %q, want closed", got)
		}
		if got := count("SELECT priority FROM issues WHERE id = ?", eventID); got != 4 {
			t.Errorf("event priority = %d, want 4", got)
		}
		if got := scalar("SELECT title FROM issues WHERE id = ?", eventID); got != "State change: patrol → muted" {
			t.Errorf("event title = %q, want the state-change title", got)
		}
		desc := scalar("SELECT description FROM issues WHERE id = ?", eventID)
		if !strings.Contains(desc, "Changed patrol from active to muted") || !strings.Contains(desc, "Investigating stuck worker") {
			t.Errorf("event description does not record transition and reason: %q", desc)
		}
		if n := count("SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'parent-child'", eventID, s.ID); n != 1 {
			t.Errorf("parent-child edges %s -> %s = %d, want 1", eventID, s.ID, n)
		}
		if n := count("SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", s.ID, "patrol:active"); n != 0 {
			t.Errorf("old label patrol:active still present (%d rows)", n)
		}
		if n := count("SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", s.ID, "patrol:muted"); n != 1 {
			t.Errorf("new label patrol:muted rows = %d, want 1", n)
		}

		// The human output retains its shape too: the printed Event line
		// names a row that exists.
		human, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "set-state", s.ID, "phase=planning")
		if err != nil {
			t.Fatalf("set-state human output: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(human, "Set phase = planning on "+s.ID) {
			t.Errorf("human output lost its success line:\n%s", human)
		}
		idx := strings.Index(human, "Event: ")
		if idx < 0 {
			t.Fatalf("human output lost its Event line:\n%s", human)
		}
		printedID := strings.TrimSpace(strings.TrimPrefix(human[idx:], "Event: "))
		if n := count("SELECT COUNT(*) FROM issues WHERE id = ? AND issue_type = 'event'", printedID); n != 1 {
			t.Errorf("human Event line names %q, which is not a stored event row", printedID)
		}
	})

	t.Run("invalid_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "set-state", issue.ID, "patrolactive")
		if err == nil {
			t.Fatalf("expected error for missing '=', got success: %s", out)
		}
	})

	t.Run("multiple_dimensions", func(t *testing.T) {
		m := bdProxiedCreate(t, bd, p.dir, "Multi-dimension state")

		for _, spec := range []string{"phase=planning", "env=staging"} {
			out, err := bdProxiedRun(t, bd, p.dir, "set-state", m.ID, spec)
			if err != nil {
				t.Fatalf("set-state %s: %v\n%s", spec, err, out)
			}
		}

		out, err := bdProxiedRun(t, bd, p.dir, "state", "list", m.ID, "--json")
		if err != nil {
			t.Fatalf("state list: %v\n%s", err, out)
		}
		var res struct {
			States map[string]string `json:"states"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if res.States["phase"] != "planning" {
			t.Errorf("phase = %q, want planning (states=%v)", res.States["phase"], res.States)
		}
		if res.States["env"] != "staging" {
			t.Errorf("env = %q, want staging (states=%v)", res.States["env"], res.States)
		}

		// Each distinct dimension is an independent label; both must be present.
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, m.ID)
		if !containsStr(labels, "phase:planning") || !containsStr(labels, "env:staging") {
			t.Errorf("expected both phase:planning and env:staging labels, got %v", labels)
		}
	})

	t.Run("set_state_on_wisp", func(t *testing.T) {
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp set-state", "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "set-state", wisp.ID, "patrol=muted", "--json")
		if err != nil {
			t.Fatalf("set-state on wisp: %v\n%s", err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got["changed"] != true || got["new_value"] != "muted" {
			t.Errorf("unexpected wisp set-state result: %v", got)
		}

		// Wisp state lives in wisp_labels; verify via the state reader.
		val, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "state", wisp.ID, "patrol")
		if err != nil {
			t.Fatalf("state on wisp: %v\nstderr:\n%s", err, stderr)
		}
		if strings.TrimSpace(val) != "muted" {
			t.Errorf("wisp state value = %q, want muted", strings.TrimSpace(val))
		}

		// ASSERT AT THE TABLE, not only through the reader. The reader answers
		// from whichever plane the write chose, so a write and a read that get
		// the same boolean backwards together still agree with each other and
		// the check above passes. This is the assertion that does not: a wisp's
		// state label belongs in wisp_labels and the durable table stays empty.
		db := openProxiedDB(t, p)
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = ?", wisp.ID, "patrol:muted"); n != 1 {
			t.Errorf("wisp_labels rows for patrol:muted = %d, want 1", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("durable labels rows for a wisp = %d, want 0", n)
		}
	})

	t.Run("state_change_on_wisp_swaps_in_place", func(t *testing.T) {
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp set-state swap", "--ephemeral")

		for _, spec := range []string{"phase=planning", "phase=running"} {
			if out, err := bdProxiedRun(t, bd, p.dir, "set-state", wisp.ID, spec, "--json"); err != nil {
				t.Fatalf("set-state %s on wisp: %v\n%s", spec, err, out)
			}
		}

		// The swap is one edit: the old label must be gone and the new one
		// present, both in the ephemeral plane, with nothing left durable.
		db := openProxiedDB(t, p)
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label LIKE 'phase:%'", wisp.ID); n != 1 {
			t.Errorf("wisp_labels phase rows = %d, want exactly 1 after a swap", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = ?", wisp.ID, "phase:running"); n != 1 {
			t.Errorf("wisp_labels rows for phase:running = %d, want 1", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("durable labels rows for a wisp = %d, want 0", n)
		}
	})

	// AC-6 (GH#5637): the event INHERITS the subject's retention mode, so a
	// wisp subject's whole transition — event, parent edge, label — stays in
	// the wisp plane and no durable row or edge records it. The reader cannot
	// be the witness here: it answers from whichever plane the write chose,
	// so the raw tables are.
	t.Run("wisp_event_retains_mode", func(t *testing.T) {
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp mode event", "--ephemeral")

		out, err := bdProxiedRun(t, bd, p.dir, "set-state", wisp.ID, "patrol=muted", "--json")
		if err != nil {
			t.Fatalf("set-state on wisp: %v\n%s", err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		eventID, ok := got["event_id"].(string)
		if !ok || eventID == "" {
			t.Fatalf("set-state on a wisp named no event id: %v", got["event_id"])
		}

		db := openProxiedDB(t, p)
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisps WHERE id = ? AND ephemeral = 1", eventID); n != 1 {
			t.Errorf("the event is an ephemeral wisp row = %d, want 1: the event must retain the subject's mode", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM issues WHERE id = ?", eventID); n != 0 {
			t.Errorf("the event landed %d durable issue row(s), want none", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?", eventID, wisp.ID); n != 1 {
			t.Errorf("wisp plane parent edges %s -> %s = %d, want 1", eventID, wisp.ID, n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?", eventID, wisp.ID); n != 0 {
			t.Errorf("the event left %d durable edge row(s), want none", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = ?", wisp.ID, "patrol:muted"); n != 1 {
			t.Errorf("wisp_labels rows for patrol:muted = %d, want 1", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("durable labels rows for a wisp = %d, want 0", n)
		}
	})
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// equalStrSets reports whether two label slices hold the same set, ignoring
// order — the label reader's ordering is not part of the contract under test.
func equalStrSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}
	return true
}
