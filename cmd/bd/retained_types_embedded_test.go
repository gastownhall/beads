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
)

// TestEmbeddedRetainedTypes drives the workspace's types.retained guard end to
// end through the CLI on the direct route: with a type named in the setting,
// a plain `bd prune`, `bd purge` and `bd gc` keep that type's closed beads and
// say how many they kept (text and --json); `bd prune --retain-ids` then
// deletes and retires them; and `bd delete`, which names its ids one by one,
// still deletes one.
func TestEmbeddedRetainedTypes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rk")

	run := func(args ...string) (stdout, stderr string, exitCode int) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, errOut, err := runCommandBuffers(t, cmd)
		var exit *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exit):
			exitCode = exit.ExitCode()
		default:
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
		}
		return out.String(), errOut.String(), exitCode
	}
	runJSON := func(args ...string) map[string]any {
		t.Helper()
		out, stderr, code := run(append(args, "--json")...)
		if code != 0 {
			t.Fatalf("bd %s --json exited %d:\n%s\n%s", strings.Join(args, " "), code, out, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &payload); err != nil {
			t.Fatalf("bd %s --json output is not JSON: %v\n%s", strings.Join(args, " "), err, out)
		}
		return payload
	}
	exists := func(id string) bool {
		t.Helper()
		_, _, code := run("show", id, "--json")
		return code == 0
	}
	closedChore := func(id string, extra ...string) string {
		t.Helper()
		issue := bdCreate(t, bd, dir, append([]string{"keyed " + id, "--id", id, "--type", "chore"}, extra...)...)
		bdClose(t, bd, dir, issue.ID)
		return issue.ID
	}

	// A malformed value fails closed at the write: stored, it would read as
	// names no row carries and protect nothing.
	for _, bad := range []string{"[chore]", "chore task", "null"} {
		if out, stderr, code := run("config", "set", "types.retained", bad); code == 0 {
			t.Errorf("bd config set types.retained %q succeeded:\n%s\n%s\nwant the malformed value refused", bad, out, stderr)
		}
	}
	// `bd config set-many` writes the database plane by its own route (one
	// commit for the batch), so it must refuse the same values: a malformed
	// pair fails the whole batch before anything is written.
	for _, bad := range []string{"types.retained=[chore]", "types.retained=null"} {
		if out, stderr, code := run("config", "set-many", bad, "rk.untouched=1"); code == 0 {
			t.Errorf("bd config set-many %q succeeded:\n%s\n%s\nwant the malformed value refused", bad, out, stderr)
		}
	}
	if out, _, code := run("config", "get", "rk.untouched"); code != 0 || !strings.Contains(out, "rk.untouched (not set)") {
		t.Errorf("bd config get rk.untouched = %q (exit %d) after a refused set-many; want the batch to write nothing", out, code)
	}
	if out, stderr, code := run("config", "set", "types.retained", "chore"); code != 0 {
		t.Fatalf("bd config set types.retained exited %d:\n%s\n%s", code, out, stderr)
	}

	t.Run("prune keeps a retained type and says so", func(t *testing.T) {
		id := closedChore("rk-prune1")
		// A preview that would delete nothing answers in the empty shape.
		preview := runJSON("prune", "--pattern", id, "--dry-run")
		if preview["pruned_count"] != float64(0) || preview["retention_required"] != float64(1) {
			t.Errorf("bd prune --dry-run --json = %v, want pruned_count 0 and retention_required 1", preview)
		}
		pruned := runJSON("prune", "--pattern", id, "--force")
		if pruned["pruned_count"] != float64(0) || pruned["retention_required"] != float64(1) {
			t.Errorf("bd prune --force --json = %v, want pruned_count 0 and retention_required 1", pruned)
		}
		out, stderr, code := run("prune", "--pattern", id, "--force")
		if code != 0 || !strings.Contains(out, "1 closed bead(s) of a retained type") || !strings.Contains(out, "--retain-ids") {
			t.Errorf("bd prune --force exited %d:\n%s\n%s\nwant the retained-type line naming --retain-ids", code, out, stderr)
		}
		if !exists(id) {
			t.Fatalf("plain bd prune deleted %s, a bead of a retained type", id)
		}
		retired := runJSON("prune", "--pattern", id, "--force", "--retain-ids")
		if retired["pruned_count"] != float64(1) || retired["retain_ids"] != true {
			t.Errorf("bd prune --retain-ids --json = %v, want pruned_count 1 and retain_ids true", retired)
		}
		if exists(id) {
			t.Errorf("bd prune --retain-ids left %s", id)
		}
	})

	t.Run("purge keeps a retained ephemeral type", func(t *testing.T) {
		id := closedChore("rk-purge1", "--ephemeral")
		purged := runJSON("purge", "--pattern", id, "--force")
		if purged["purged_count"] != float64(0) || purged["retention_required"] != float64(1) {
			t.Errorf("bd purge --force --json = %v, want purged_count 0 and retention_required 1", purged)
		}
		if !exists(id) {
			t.Fatalf("plain bd purge deleted %s, a bead of a retained type", id)
		}
	})

	// The decay counts every closed retained bead in the workspace: this
	// subtest's own and the ephemeral one the purge subtest kept.
	t.Run("gc decay keeps a retained type", func(t *testing.T) {
		kept := closedChore("rk-gc1")
		other := bdCreate(t, bd, dir, "decays", "--id", "rk-gc2", "--type", "task").ID
		bdClose(t, bd, dir, other)
		// --older-than 0 selects closed_at before now cut to the second, and a
		// close stamp is stored rounded half-up to the second, so a bead closed
		// in the last 1.5 s can still read as not closed before the cutoff.
		time.Sleep(2 * time.Second)
		out, stderr, code := run("gc", "--older-than", "0", "--force", "--skip-dolt")
		if code != 0 || !strings.Contains(out, "Kept 2 closed issue(s) of a retained type") {
			t.Errorf("bd gc exited %d:\n%s\n%s\nwant the kept-retained line", code, out, stderr)
		}
		if !exists(kept) {
			t.Fatalf("bd gc decayed %s, a bead of a retained type", kept)
		}
		if exists(other) {
			t.Errorf("bd gc kept %s, which is not of a retained type", other)
		}
		report := runJSON("gc", "--older-than", "0", "--force", "--skip-dolt")
		if report["retention_required"] != float64(2) {
			t.Errorf("bd gc --json = %v, want retention_required 2", report)
		}
	})

	// The setting is read from the database only, so a key in config.yaml
	// protects nothing; every bulk delete that honors the setting says so.
	t.Run("a config.yaml key draws the ignored warning", func(t *testing.T) {
		cfgPath := filepath.Join(dir, ".beads", "config.yaml")
		orig, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("reading %s: %v", cfgPath, err)
		}
		t.Cleanup(func() { _ = os.WriteFile(cfgPath, orig, 0o600) })
		withKey := append(append([]byte{}, orig...), []byte("\ntypes.retained: op\n")...)
		if err := os.WriteFile(cfgPath, withKey, 0o600); err != nil {
			t.Fatalf("writing %s: %v", cfgPath, err)
		}
		for _, args := range [][]string{
			{"prune", "--pattern", "rk-none-*", "--dry-run"},
			{"purge", "--pattern", "rk-none-*", "--dry-run"},
		} {
			out, stderr, code := run(args...)
			if code != 0 || !strings.Contains(stderr, "types.retained in config.yaml (op) is ignored") {
				t.Errorf("bd %s exited %d:\n%s\n%s\nwant the ignored-key warning on stderr", strings.Join(args, " "), code, out, stderr)
			}
		}
	})

	t.Run("delete still deletes a retained type", func(t *testing.T) {
		id := closedChore("rk-delete1")
		if out, stderr, code := run("delete", id, "--force"); code != 0 {
			t.Fatalf("bd delete %s exited %d:\n%s\n%s", id, code, out, stderr)
		}
		if exists(id) {
			t.Errorf("bd delete %s left the row: delete names its ids and must not consult types.retained", id)
		}
	})
}
