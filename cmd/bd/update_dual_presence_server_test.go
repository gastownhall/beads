//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestCLI_UpdateDualPresence pins GH#5991's CLI surface. On a server-mode
// workspace whose ID exists in both the issues and the wisps table, `bd update`
// must exit nonzero, name the ID and both tables, point at the existing doctor
// repair and say what that repair removes, and change nothing: not either
// copy, not their auxiliary rows, not the Dolt commit count. Before the fix the
// same command printed "✓ Updated issue", wrote the wisp copy the issues-first
// reader never shows, and minted no commit — two days of silent loss for the
// reporter.
//
// The dual-present fixture is seeded with raw SQL (bd sql), the way the
// reporter's database actually got there: a demote moved the row to wisps, and
// a stale JSONL re-import later recreated the durable row beside it. The
// create/import paths now refuse to mint such a twin (GH#4455), so an
// import-based reproduction no longer works by design.
func TestCLI_UpdateDualPresence(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir := t.TempDir()
	env := os.Environ()
	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	bd := func(args ...string) (string, error) {
		return runBDExecAllowErrorWithEnv(t, tmpDir, env, args...)
	}
	mustBD := func(args ...string) string {
		t.Helper()
		out, err := bd(args...)
		if err != nil {
			t.Fatalf("bd %v failed: %v\n%s", args, err, out)
		}
		return out
	}
	sqlValue := func(query string) string {
		t.Helper()
		out := mustBD("sql", "--csv", query)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) < 2 {
			t.Fatalf("bd sql %q printed no row:\n%s", query, out)
		}
		return strings.TrimSpace(lines[len(lines)-1])
	}

	mustBD("init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "udp", "--quiet")

	createOut := mustBD("create", "dual presence canary", "--type", "task", "--json")
	jsonStart := strings.Index(createOut, "{")
	if jsonStart < 0 {
		t.Fatalf("bd create --json printed no object:\n%s", createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut[jsonStart:]), &created); err != nil {
		t.Fatalf("parse bd create --json output: %v\n%s", err, createOut)
	}
	id := created.ID

	// Demote the row into the wisps table, then give the wisp copy notes of
	// its own so the two copies are told apart by content, not just location.
	mustBD("update", id, "--no-history")
	mustBD("update", id, "--notes", "wisp notes")

	// Recreate the durable row beside the wisp (the stale-import shape), and
	// make the copies disagree: the issues row carries different notes than
	// the wisp row.
	mustBD("sql", fmt.Sprintf(
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type "+
			"FROM wisps WHERE id = '%s'", id))
	mustBD("sql", fmt.Sprintf("UPDATE issues SET notes = 'durable notes' WHERE id = '%s'", id))

	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id)); got != "1" {
		t.Fatalf("fixture: issues copy count = %q, want 1", got)
	}
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM wisps WHERE id = '%s'", id)); got != "1" {
		t.Fatalf("fixture: wisps copy count = %q, want 1", got)
	}

	// Snapshot every piece of state the refusal must leave untouched.
	durableNotes := sqlValue(fmt.Sprintf("SELECT notes FROM issues WHERE id = '%s'", id))
	wispNotes := sqlValue(fmt.Sprintf("SELECT notes FROM wisps WHERE id = '%s'", id))
	if durableNotes != "durable notes" || wispNotes != "wisp notes" {
		t.Fatalf("fixture: durable notes = %q, wisp notes = %q", durableNotes, wispNotes)
	}
	durableAux := sqlValue(fmt.Sprintf(
		"SELECT (SELECT COUNT(*) FROM labels WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM events WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM comments WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM dependencies WHERE issue_id = '%[1]s')", id))
	wispAux := sqlValue(fmt.Sprintf(
		"SELECT (SELECT COUNT(*) FROM wisp_labels WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_events WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_comments WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = '%[1]s')", id))
	commitCount := sqlValue("SELECT COUNT(*) FROM dolt_log")

	// The update under test. --force bypasses only the notes-overwrite fence:
	// at base that is what stood between this command and the silent wisp
	// write; the dual-presence refusal fires before any fence, force or not.
	stdout, stderr, err := runBDExecSeparated(t, testBD, tmpDir, env,
		"update", id, "--notes", "probe", "--force")
	if err == nil {
		t.Fatalf("bd update %s on a dual-present ID exited 0 — the silent write the reporter lost two days to.\nstdout:\n%s\nstderr:\n%s", id, stdout, stderr)
	}
	combined := stdout + "\n" + stderr
	for _, want := range []string{
		id,
		"issues",
		"wisps",
		"bd doctor --check=validate --fix",
		"server-mode workspace",
		"deletes the issues copy",
		"keeps the wisp copy",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("bd update refusal does not mention %q:\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}

	// Both copies, their auxiliary rows and the Dolt commit count are
	// untouched, and the issues-first reader still shows the durable copy.
	if got := sqlValue(fmt.Sprintf("SELECT notes FROM issues WHERE id = '%s'", id)); got != durableNotes {
		t.Errorf("durable copy notes = %q, want unchanged %q", got, durableNotes)
	}
	if got := sqlValue(fmt.Sprintf("SELECT notes FROM wisps WHERE id = '%s'", id)); got != wispNotes {
		t.Errorf("wisp copy notes = %q, want unchanged %q", got, wispNotes)
	}
	if got := sqlValue(fmt.Sprintf(
		"SELECT (SELECT COUNT(*) FROM labels WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM events WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM comments WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM dependencies WHERE issue_id = '%[1]s')", id)); got != durableAux {
		t.Errorf("durable auxiliary rows = %s, want unchanged %s", got, durableAux)
	}
	if got := sqlValue(fmt.Sprintf(
		"SELECT (SELECT COUNT(*) FROM wisp_labels WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_events WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_comments WHERE issue_id = '%[1]s') + (SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = '%[1]s')", id)); got != wispAux {
		t.Errorf("wisp auxiliary rows = %s, want unchanged %s", got, wispAux)
	}
	if got := sqlValue("SELECT COUNT(*) FROM dolt_log"); got != commitCount {
		t.Errorf("dolt_log commit count = %s, want unchanged %s", got, commitCount)
	}
	showOut := mustBD("show", id, "--json")
	if !strings.Contains(showOut, "durable notes") {
		t.Errorf("bd show %s no longer reads the durable copy after the refusal:\n%s", id, showOut)
	}
}

// TestCLI_UpdateDualPresenceRepairWorkflow walks the recovery the refusal
// message points at (GH#5991): the existing doctor validation repair on the
// corrupted workspace keeps the wisp copy's data and deletes the durable
// twin, the next update succeeds and is visible, and the persistence
// conversion after it leaves exactly one durable row carrying the updated
// notes. Before the fix the first step is the one that fails: the update
// silently succeeded, so nothing steered the user at the repair.
func TestCLI_UpdateDualPresenceRepairWorkflow(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir := t.TempDir()
	env := os.Environ()
	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	bd := func(args ...string) (string, error) {
		return runBDExecAllowErrorWithEnv(t, tmpDir, env, args...)
	}
	mustBD := func(args ...string) string {
		t.Helper()
		out, err := bd(args...)
		if err != nil {
			t.Fatalf("bd %v failed: %v\n%s", args, err, out)
		}
		return out
	}
	sqlValue := func(query string) string {
		t.Helper()
		out := mustBD("sql", "--csv", query)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) < 2 {
			t.Fatalf("bd sql %q printed no row:\n%s", query, out)
		}
		return strings.TrimSpace(lines[len(lines)-1])
	}

	mustBD("init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "udpw", "--quiet")

	createOut := mustBD("create", "dual presence repair canary", "--type", "task", "--json")
	jsonStart := strings.Index(createOut, "{")
	if jsonStart < 0 {
		t.Fatalf("bd create --json printed no object:\n%s", createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut[jsonStart:]), &created); err != nil {
		t.Fatalf("parse bd create --json output: %v\n%s", err, createOut)
	}
	id := created.ID

	// The corrupted state, seeded the way the reporter's database got there:
	// a demote into wisps, wisp notes of their own, then the stale import's
	// durable twin with different notes.
	mustBD("update", id, "--no-history")
	mustBD("update", id, "--notes", "wisp notes", "--force")
	mustBD("sql", fmt.Sprintf(
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type "+
			"FROM wisps WHERE id = '%s'", id))
	mustBD("sql", fmt.Sprintf("UPDATE issues SET notes = 'durable notes' WHERE id = '%s'", id))
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id)); got != "1" {
		t.Fatalf("fixture: issues copy count = %q, want 1", got)
	}
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM wisps WHERE id = '%s'", id)); got != "1" {
		t.Fatalf("fixture: wisps copy count = %q, want 1", got)
	}

	// 1. The update refuses and names the repair — the step that fails
	// before the fix, where the update silently wrote the wisp copy.
	stdout, stderr, err := runBDExecSeparated(t, testBD, tmpDir, env,
		"update", id, "--notes", "probe", "--force")
	if err == nil {
		t.Fatalf("bd update %s on a dual-present ID exited 0 instead of pointing at the repair.\nstdout:\n%s\nstderr:\n%s", id, stdout, stderr)
	}
	for _, want := range []string{id, "bd doctor --check=validate --fix"} {
		if !strings.Contains(stdout+"\n"+stderr, want) {
			t.Errorf("bd update refusal does not mention %q:\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}

	// 2. The doctor validation repair: keeps the wisp copy's data, deletes
	// the durable twin.
	fixOut, err := bd("doctor", "--check=validate", "--fix", "--yes")
	if err != nil {
		t.Fatalf("bd doctor --check=validate --fix --yes failed: %v\n%s", err, fixOut)
	}
	if !strings.Contains(fixOut, "cross-table duplicate") {
		t.Errorf("the doctor repair output does not name the cross-table duplicate it fixed:\n%s", fixOut)
	}
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id)); got != "0" {
		t.Errorf("after the repair the durable copy of %s still exists; want it deleted (kept the wisp copy)", id)
	}
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM wisps WHERE id = '%s'", id)); got != "1" {
		t.Errorf("after the repair the wisp copy of %s is gone; want the repair to keep it", id)
	}
	if got := sqlValue(fmt.Sprintf("SELECT notes FROM wisps WHERE id = '%s'", id)); got != "wisp notes" {
		t.Errorf("after the repair the wisp copy of %s carries notes %q; want the wisp data kept", id, got)
	}

	// 3. The update the refusal blocked now succeeds — and is visible.
	mustBD("update", id, "--notes", "repaired notes", "--force")
	showOut := mustBD("show", id, "--json")
	if !strings.Contains(showOut, "repaired notes") {
		t.Errorf("bd show %s does not display the post-repair update:\n%s", id, showOut)
	}

	// 4. Persistence conversion: one durable row with the updated notes, no
	// wisp row.
	mustBD("update", id, "--persistent")
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id)); got != "1" {
		t.Errorf("after --persistent: issues copy count = %q, want 1", got)
	}
	if got := sqlValue(fmt.Sprintf("SELECT COUNT(*) FROM wisps WHERE id = '%s'", id)); got != "0" {
		t.Errorf("after --persistent: wisps copy count = %q, want 0", got)
	}
	if got := sqlValue(fmt.Sprintf("SELECT notes FROM issues WHERE id = '%s'", id)); got != "repaired notes" {
		t.Errorf("after --persistent: durable notes = %q, want %q", got, "repaired notes")
	}
}
