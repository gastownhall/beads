//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// syncCloseRaceRun runs one bd command in dir as actor, returning stdout,
// stderr and the exit code (-1 when the process did not exit normally).
func syncCloseRaceRun(t *testing.T, bd, dir, actor string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BEADS_ACTOR="+actor)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
	return "", "", -1
}

// syncCloseRaceMust runs a bd command that must succeed.
func syncCloseRaceMust(t *testing.T, bd, dir, actor string, args ...string) string {
	t.Helper()
	stdout, stderr, code := syncCloseRaceRun(t, bd, dir, actor, args...)
	if code != 0 {
		t.Fatalf("bd %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// syncCloseRaceExec runs SQL statements directly against a workspace's
// embedded database (bd must not be running against it).
func syncCloseRaceExec(t *testing.T, beadsDir string, stmts ...string) {
	t.Helper()
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	for _, stmt := range stmts {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// TestEmbeddedSyncCloseRaceHalts pins GH#7783 shape 1 end to end: clone b
// closes an issue and syncs; clone a, which never pulled, claims the same
// issue later and runs bd sync. The close and the claim contest the close
// group with exactly one side closed, so the sync must halt (exit 2) without
// pushing, name the issue and both statuses, leave the remote's close intact,
// and keep a's claim locally — instead of settling last-write-wins and
// publishing a silent reopen.
func TestEmbeddedSyncCloseRaceHalts(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt sync tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	remoteURL := "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "remote"))

	dirA, beadsDirA, _ := bdInit(t, bd, "--prefix", "cr")
	issue := bdCreate(t, bd, dirA, "close race", "--type", "task")
	syncCloseRaceMust(t, bd, dirA, "host-a", "dolt", "remote", "add", "origin", remoteURL)
	syncCloseRaceMust(t, bd, dirA, "host-a", "dolt", "push")

	dirB := t.TempDir()
	initGitRepoAt(t, dirB)
	syncCloseRaceMust(t, bd, dirB, "host-b", "init", "--quiet", "--prefix", "cr", "--remote", remoteURL, "--skip-hooks", "--skip-agents")

	// b closes and publishes.
	syncCloseRaceMust(t, bd, dirB, "host-b", "close", issue.ID, "--reason", "done on b")
	syncCloseRaceMust(t, bd, dirB, "host-b", "sync")
	remoteHead := vcStatusCommit(t, bd, dirB)

	// a claims without pulling. Its claim is made strictly later than the
	// close: pin a's updated_at an hour past b's instead of sleeping, so the
	// base LWW rule would deterministically let the claim win.
	syncCloseRaceMust(t, bd, dirA, "host-a", "update", issue.ID, "--claim")
	closedOnB := bdShow(t, bd, dirB, issue.ID)
	if closedOnB.ClosedAt == nil {
		t.Fatalf("precondition: b's issue is not closed: %+v", closedOnB)
	}
	later := closedOnB.UpdatedAt.UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	syncCloseRaceExec(t, beadsDirA,
		"UPDATE issues SET updated_at = '"+later+"' WHERE id = '"+issue.ID+"'",
		"CALL DOLT_COMMIT('-Am', 'test: claim strictly after the remote close')",
	)

	stdout, stderr, code := syncCloseRaceRun(t, bd, dirA, "host-a", "sync")
	if code != ExitSyncConflict {
		t.Fatalf("bd sync exit = %d, want %d (halt on a close racing a claim)\nstdout:\n%s\nstderr:\n%s",
			code, ExitSyncConflict, stdout, stderr)
	}
	combined := stdout + stderr
	for _, want := range []string{issue.ID, "closed", "in_progress"} {
		if !strings.Contains(combined, want) {
			t.Errorf("halt output must name %q\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
	// The embedded pull aborted the merge, so nothing is live for
	// 'bd conflicts resolve'; the remedy is a strategy pull, named with the
	// warning that it settles every conflicted row of the pull.
	for _, want := range []string{
		"  bd dolt pull --strategy ours\n",
		"  bd dolt pull --strategy theirs\n",
		"EVERY conflicted row of the pull",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("halt must name the embedded remedy %q\nstderr:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "bd conflicts resolve") {
		t.Errorf("an aborted pull leaves no live conflict, so the halt must not name bd conflicts resolve\nstderr:\n%s", stderr)
	}

	// Nothing was pushed: b's pull is a no-op and the close stands.
	syncCloseRaceMust(t, bd, dirB, "host-b", "dolt", "pull")
	if got := vcStatusCommit(t, bd, dirB); got != remoteHead {
		t.Errorf("remote head moved from %s to %s: the halted sync pushed", remoteHead, got)
	}
	onRemote := bdShow(t, bd, dirB, issue.ID)
	if string(onRemote.Status) != "closed" || onRemote.CloseReason != "done on b" {
		t.Errorf("remote issue = status %q, close_reason %q; want closed, %q", onRemote.Status, onRemote.CloseReason, "done on b")
	}

	// a's claim survives the abort for the operator to resolve.
	local := bdShow(t, bd, dirA, issue.ID)
	if string(local.Status) != "in_progress" || local.Assignee != "host-a" {
		t.Errorf("local issue = status %q, assignee %q; want in_progress, host-a", local.Status, local.Assignee)
	}
}

// vcStatusCommit returns the workspace's current commit hash.
func vcStatusCommit(t *testing.T, bd, dir string) string {
	t.Helper()
	out := bdVC(t, bd, dir, "status", "--json")
	var result struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Commit == "" {
		t.Fatalf("bd vc status --json: %v\n%s", err, out)
	}
	return result.Commit
}
