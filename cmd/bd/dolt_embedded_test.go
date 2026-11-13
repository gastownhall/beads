//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// bdDolt runs "bd dolt" with the given args and returns stdout.
func bdDolt(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dolt"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dolt %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdDoltFail runs "bd dolt" expecting failure and returns stderr+stdout.
func bdDoltFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dolt"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dolt %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// TestEmbeddedDoltBlockedAndCommit was split from TestEmbeddedDolt
// (originally ~183s, measured under --config=embedded) into 2 top-level
// tests over disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every
// original subtest is preserved exactly once. This group keeps the
// server-only-command-blocked, status/show, and commit subtests; the
// remote/push/pull subtests moved to TestEmbeddedDoltRemoteAndPush below.
// Every subtest in both groups only ever reads or mutates its own fresh
// `dir` (or its own ppDir/pfDir/cleanDir), so neither group depends on state
// the other group's subtests produce.
func TestEmbeddedDoltBlockedAndCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")

	// ===== Server-only commands must fail in embedded mode =====

	// status and show are intentionally NOT in this list: they report
	// embedded-mode state rather than erroring out. See the
	// embedded_status/embedded_show subtests below.
	serverOnlyCmds := []struct {
		name string
		args []string
	}{
		{"start", []string{"start"}},
		{"stop", []string{"stop"}},
		{"test", []string{"test"}},
		{"set", []string{"set", "host", "127.0.0.1"}},
		{"killall", []string{"killall"}},
		{"clean-databases", []string{"clean-databases"}},
	}

	for _, tc := range serverOnlyCmds {
		t.Run("blocked_"+tc.name, func(t *testing.T) {
			out := bdDoltFail(t, bd, dir, tc.args...)
			if !strings.Contains(out, "not supported in embedded mode") {
				t.Errorf("expected 'not supported in embedded mode' in output for dolt %s: %s", tc.name, out)
			}
		})
	}

	// ===== Embedded-mode inspection commands succeed with embedded-mode output =====

	t.Run("embedded_status", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "status")
		if !strings.Contains(out, "embedded") {
			t.Errorf("expected embedded-mode status output to mention 'embedded': %s", out)
		}
	})

	t.Run("embedded_status_json", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "status", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("status --json returned invalid JSON: %v\n%s", err, out)
		}
		if result["mode"] != "embedded" {
			t.Errorf("mode = %v, want embedded", result["mode"])
		}
		if result["server_running"] != false {
			t.Errorf("server_running = %v, want false", result["server_running"])
		}
		if _, ok := result["running"]; ok {
			t.Errorf("running should be omitted for embedded mode; use server_running instead: %v", result["running"])
		}
	})

	t.Run("embedded_show", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "show")
		if !strings.Contains(out, "Mode:") || !strings.Contains(out, "embedded") {
			t.Errorf("expected embedded-mode show output to contain 'Mode:' and 'embedded': %s", out)
		}
	})

	// ===== Working commands =====

	// Create an issue so there are pending changes to commit
	bdCreate(t, bd, dir, "Dolt commit test issue", "--type", "task")

	t.Run("commit", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "commit")
		// Should succeed — either commits or reports nothing to commit
		_ = out
	})

	t.Run("commit_with_message", func(t *testing.T) {
		bdCreate(t, bd, dir, "Another issue for commit", "--type", "task")
		out := bdDolt(t, bd, dir, "commit", "-m", "test commit message")
		_ = out
	})

	t.Run("commit_clean_reports_nothing", func(t *testing.T) {
		// bd init leaves the working set clean (bootstrap's own commit
		// tolerates nothing-to-commit, GH#3886). A second, truly no-op
		// "bd dolt commit" here must print "Nothing to commit.", not
		// "Committed." — this is the case EmbeddedDoltStore.Commit's new
		// nothing-to-commit tolerance made possible to reach with a nil
		// error.
		cleanDir, _, _ := bdInit(t, bd, "--prefix", "tdcln")
		out := bdDolt(t, bd, cleanDir, "commit")
		if !strings.Contains(out, "Nothing to commit.") {
			t.Errorf("expected 'Nothing to commit.' on a clean working set, got: %s", out)
		}
	})
}

// TestEmbeddedDoltRemoteAndPush is the second half of the TestEmbeddedDolt
// split described on TestEmbeddedDoltBlockedAndCommit above: the
// remote-list/add/remove and push/pull subtests, none of which depend on
// state produced by the first group's subtests.
func TestEmbeddedDoltRemoteAndPush(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")

	// ===== Remote management =====

	t.Run("remote_list_empty", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "remote", "list")
		_ = out // Should succeed even with no remotes
	})

	t.Run("remote_add_and_list", func(t *testing.T) {
		remoteDir := t.TempDir()
		bdDolt(t, bd, dir, "remote", "add", "test-remote", "file://"+remoteDir)
		out := bdDolt(t, bd, dir, "remote", "list")
		if !strings.Contains(out, "test-remote") {
			t.Errorf("expected 'test-remote' in remote list: %s", out)
		}
	})

	t.Run("remote_remove", func(t *testing.T) {
		bdDolt(t, bd, dir, "remote", "remove", "test-remote")
		out := bdDolt(t, bd, dir, "remote", "list")
		if strings.Contains(out, "test-remote") {
			t.Errorf("expected 'test-remote' to be removed: %s", out)
		}
	})

	// ===== Push/Pull with file remote =====

	t.Run("push_to_file_remote", func(t *testing.T) {
		ppDir, _, _ := bdInit(t, bd, "--prefix", "pp")

		remoteDir := t.TempDir()
		bdDolt(t, bd, ppDir, "remote", "add", "origin", "file://"+remoteDir)

		// Create an issue and commit
		bdCreate(t, bd, ppDir, "Push roundtrip issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "roundtrip commit")

		// Push to file remote
		bdDolt(t, bd, ppDir, "push")
	})

	t.Run("push_force", func(t *testing.T) {
		pfDir, _, _ := bdInit(t, bd, "--prefix", "pf")
		remoteDir := t.TempDir()
		bdDolt(t, bd, pfDir, "remote", "add", "origin", "file://"+remoteDir)

		bdCreate(t, bd, pfDir, "Force push issue", "--type", "task")
		bdDolt(t, bd, pfDir, "commit", "-m", "force push commit")
		bdDolt(t, bd, pfDir, "push", "--force")
	})

	t.Run("push_no_remote", func(t *testing.T) {
		// Push without a remote should fail gracefully, not panic
		cmd := exec.Command(bd, "dolt", "push")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Log("push without remote succeeded unexpectedly")
		}
		_ = out
	})

	t.Run("pull_no_remote", func(t *testing.T) {
		// bd-e2q8zs: a rig with no remote skips the pull with the no-remote
		// guidance and exits 0, as server mode does. Embedded mode's fetch
		// fails with the bare "no remote" error, which the shared no-remote
		// gate (isConfirmedNoRemote) accepts since the server-mode pull moved
		// onto the same fetch; the pull used to exit 1 here. A fresh rig, so
		// no remote the subtests above added or removed is in play; bdDolt
		// fails the subtest on a non-zero exit.
		npDir, _, _ := bdInit(t, bd, "--prefix", "np")
		out := bdDolt(t, bd, npDir, "pull")
		if !strings.Contains(out, "No remote is configured — skipping.") {
			t.Errorf("bd dolt pull with no remote: want the no-remote guidance on stdout, got:\n%s", out)
		}
	})

	// ===== Push/Pull between two repos via shared file remote =====

	t.Run("push_then_push_more", func(t *testing.T) {
		// Push twice to verify incremental pushes work
		ppDir, _, _ := bdInit(t, bd, "--prefix", "pm")
		remoteDir := t.TempDir()
		bdDolt(t, bd, ppDir, "remote", "add", "origin", "file://"+remoteDir)

		// First push
		bdCreate(t, bd, ppDir, "First push issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "first commit")
		bdDolt(t, bd, ppDir, "push")

		// Second push
		bdCreate(t, bd, ppDir, "Second push issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "second commit")
		bdDolt(t, bd, ppDir, "push")
	})
}

// bdSkipIdentityCheck runs bd with BEADS_SKIP_IDENTITY_CHECK=1: the second
// clone of the renumber fixture below pulls the first clone's metadata, and
// the CLI write path would otherwise refuse on the workspace-identity check
// (the field condition `bd doctor --fix`/`bd bootstrap` reconciles; the
// override keeps the test on its subject).
func bdSkipIdentityCheck(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BEADS_SKIP_IDENTITY_CHECK=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// decodeOneChildRenumberJSON decodes a `bd dolt pull --renumber-children
// --json` stdout, which must be exactly one JSON object.
func decodeOneChildRenumberJSON(t *testing.T, out string) storage.ChildRenumberReport {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var report storage.ChildRenumberReport
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("pull --renumber-children --json stdout is not the result JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("pull --renumber-children --json wrote more than one JSON value:\n%s", out)
	}
	return report
}

// TestEmbeddedDoltPullRenumbersMentions (GH#4796, bd-28sb25 AC-5): the
// repair reports, and never rewrites, the local prose that names a renamed
// id. A whole-id mention is reported per field and per comment with the id
// it maps to; a longer id that merely starts with the renamed one (pn-x.10
// for a renamed pn-x.1) is not a mention. The text is left byte-for-byte as
// it was, for the operator to review by hand, and --json stdout is one
// object on the repair and on the pull after it.
func TestEmbeddedDoltPullRenumbersMentions(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dirA, _, _ := bdInit(t, bd, "--prefix", "pn")
	remoteDir := t.TempDir()
	bdDolt(t, bd, dirA, "remote", "add", "origin", "file://"+remoteDir)

	parent := bdCreate(t, bd, dirA, "Parent epic for mention scan", "--type", "epic")
	bdDolt(t, bd, dirA, "commit", "-m", "seed parent")
	bdDolt(t, bd, dirA, "push")

	// The second clone pulls the parent, then both clones mint a child under
	// it while "offline": same id, different beads.
	dirB, _, _ := bdInit(t, bd, "--prefix", "pn")
	bdDolt(t, bd, dirB, "remote", "add", "origin", "file://"+remoteDir)
	bdDolt(t, bd, dirB, "pull")

	localChild := bdCreate(t, bd, dirA, "Local child one", "--parent", parent.ID)
	// Local prose names the child id three ways: the child's own
	// description (which also carries the non-mention look-alike
	// "<id>0"), a comment on the child, and the parent's notes.
	bdSkipIdentityCheck(t, bd, dirA, "update", localChild.ID, "--description",
		"Tracked under "+localChild.ID+", unlike "+localChild.ID+"0.")
	bdSkipIdentityCheck(t, bd, dirA, "comment", localChild.ID, "Follow-up on "+localChild.ID)
	bdSkipIdentityCheck(t, bd, dirA, "update", parent.ID, "--append-notes", "See "+localChild.ID)
	bdDolt(t, bd, dirA, "commit", "-m", "local child with mentions")

	remoteChild := parseIssueJSON(t, []byte(bdSkipIdentityCheck(t, bd, dirB,
		"create", "Remote child one", "--parent", parent.ID, "--json")))
	if remoteChild.ID != localChild.ID {
		t.Fatalf("fixture: colliding children got distinct ids %s and %s", localChild.ID, remoteChild.ID)
	}
	bdSkipIdentityCheck(t, bd, dirB, "dolt", "commit", "-m", "remote child")
	bdSkipIdentityCheck(t, bd, dirB, "dolt", "push")

	result := decodeOneChildRenumberJSON(t, bdDolt(t, bd, dirA, "pull", "--renumber-children", "--json"))
	if len(result.Renames) != 1 || result.Renames[0].From != localChild.ID {
		t.Fatalf("renames = %+v, want the single rename of %s", result.Renames, localChild.ID)
	}
	newID := result.Renames[0].To
	if newID != parent.ID+".2" {
		t.Errorf("renamed id = %s, want %s.2 (above both clones' high-water marks)", newID, parent.ID)
	}
	if result.RenameCommit == "" || !result.Merged || result.Head == "" {
		t.Errorf("result = rename commit %q, merged %v, head %q; want a committed, merged repair",
			result.RenameCommit, result.Merged, result.Head)
	}

	// One report row per whole-id mention: the parent's notes, and the
	// renamed child's comment and description. The look-alike id is not a
	// mention, so the description is reported exactly once. Rows sort by
	// issue then field: the parent before its child, "comment" before
	// "description".
	if len(result.Mentions) != 3 {
		t.Fatalf("mentions = %+v, want 3 (parent notes, child comment, child description)", result.Mentions)
	}
	if m := result.Mentions[0]; m.IssueID != parent.ID || m.Field != "notes" ||
		m.CommentID != "" || m.ID != localChild.ID || m.RenamedTo != newID {
		t.Errorf("mention 0 = %+v, want the parent's notes naming %s -> %s", m, localChild.ID, newID)
	}
	if m := result.Mentions[1]; m.IssueID != newID || m.Field != "comment" ||
		m.CommentID == "" || m.ID != localChild.ID || m.RenamedTo != newID {
		// The comment id is derived from the re-keyed comment's content
		// (timestamped), so it is checked for presence, not value.
		t.Errorf("mention 1 = %+v, want the renamed child's comment naming %s -> %s", m, localChild.ID, newID)
	}
	if m := result.Mentions[2]; m.IssueID != newID || m.Field != "description" ||
		m.CommentID != "" || m.ID != localChild.ID || m.RenamedTo != newID {
		t.Errorf("mention 2 = %+v, want the renamed child's description naming %s -> %s", m, localChild.ID, newID)
	}

	// The text is unchanged: every mention still names the OLD id exactly
	// where it did, for the operator to review.
	shownChild := bdSkipIdentityCheck(t, bd, dirA, "show", newID, "--json")
	if !strings.Contains(shownChild, "Tracked under "+localChild.ID+",") {
		t.Errorf("renamed child's description was rewritten:\n%s", shownChild)
	}
	if !strings.Contains(shownChild, localChild.ID+"0.") {
		t.Errorf("renamed child's description lost the look-alike id:\n%s", shownChild)
	}
	shownParent := bdSkipIdentityCheck(t, bd, dirA, "show", parent.ID, "--json")
	if !strings.Contains(shownParent, "See "+localChild.ID) {
		t.Errorf("parent notes were rewritten:\n%s", shownParent)
	}

	// Pulling again finds nothing to rename and still writes one object.
	again := decodeOneChildRenumberJSON(t, bdDolt(t, bd, dirA, "pull", "--renumber-children", "--json"))
	if len(again.Renames) != 0 || again.RenameCommit != "" || !again.Merged || again.Head != result.Head {
		t.Errorf("second pull = %+v, want no renames, merged, head still %s", again, result.Head)
	}
}

// TestEmbeddedDoltConcurrent exercises dolt operations concurrently.
func TestEmbeddedDoltConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dx")

	// Pre-create issues so commits have something to work with
	for i := 0; i < 4; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("dolt-concurrent-%d", i), "--type", "task")
	}

	const numWorkers = 8

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			var args []string
			expectFail := false
			switch worker % 3 {
			case 0:
				args = []string{"dolt", "commit"}
			case 1:
				// Status is an embedded-mode inspection command.
				args = []string{"dolt", "status"}
			case 2:
				// Server-only command should fail fast.
				args = []string{"dolt", "start"}
				expectFail = true
			}

			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()

			if expectFail {
				if err == nil {
					r.err = fmt.Errorf("expected dolt %s to fail in embedded mode", args[1])
				} else if !strings.Contains(string(out), "not supported in embedded mode") {
					r.err = fmt.Errorf("unexpected error from dolt %s: %s", args[1], out)
				}
			} else {
				if err != nil {
					r.err = fmt.Errorf("dolt %s (worker %d): %v\n%s", strings.Join(args[1:], " "), worker, err, out)
				}
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// TestAdminEmbeddedBlocked verifies that bd admin subcommands are blocked in
// embedded mode. The guard was previously on adminCmd.PersistentPreRunE which
// fired before cmdCtx.ServerMode was populated, causing a false "embedded mode"
// error even in server-mode projects. The guard is now inside each subcommand's
// Run func, after store init. This test ensures embedded mode still correctly
// rejects admin commands.
func TestEmbeddedAdminBlocked(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ta")

	blockedCmds := []struct {
		name string
		args []string
	}{
		{"compact --dolt", []string{"admin", "compact", "--dolt"}},
		{"cleanup --dry-run", []string{"admin", "cleanup", "--dry-run"}},
		{"reset", []string{"admin", "reset"}},
	}

	for _, tc := range blockedCmds {
		tc := tc
		t.Run("blocked_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(bd, tc.args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("bd %s should fail in embedded mode but succeeded: %s", tc.name, out)
				return
			}
			if !strings.Contains(string(out), "not yet supported in embedded mode") {
				t.Errorf("bd %s: expected 'not yet supported in embedded mode', got: %s", tc.name, out)
			}
		})
	}
}

// TestAdminEmbeddedCompactReadOnlyAllowed verifies read-only compact modes are
// not blocked by the embedded-mode admin guard.
func TestEmbeddedAdminCompactReadOnlyAllowed(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ta")

	readOnlyCmds := []struct {
		name string
		args []string
	}{
		{"compact --stats", []string{"admin", "compact", "--stats"}},
		{"compact --dolt --dry-run", []string{"admin", "compact", "--dolt", "--dry-run"}},
	}

	for _, tc := range readOnlyCmds {
		tc := tc
		t.Run("allowed_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(bd, tc.args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("bd %s should be allowed in embedded mode: %v\n%s", tc.name, err, out)
				return
			}
			if strings.Contains(string(out), "not yet supported in embedded mode") {
				t.Errorf("bd %s hit embedded-mode guard unexpectedly: %s", tc.name, out)
			}
		})
	}
}
