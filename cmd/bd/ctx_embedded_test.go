//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// TestEmbeddedReadyCtx drives the declared-context predicate end to end on
// the embedded store: `requires:` labels on the issue side, --ctx on the
// reader side, across bd ready, bd ready --claim, bd list and bd blocked.
func TestEmbeddedReadyCtx(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cx")

	gpu := bdCreate(t, bd, dir, "needs a gpu", "--type", "task", "--labels", "requires:gpu")
	studio := bdCreate(t, bd, dir, "needs the studio and a gpu", "--type", "task", "--labels", "requires:host=studio,requires:gpu")
	plain := bdCreate(t, bd, dir, "needs nothing", "--type", "task")
	anyHost := bdCreate(t, bd, dir, "needs some host", "--type", "task", "--labels", "requires:host")

	ids := func(t *testing.T, args ...string) []string {
		t.Helper()
		cmd := exec.Command(bd, append(args, "--json")...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		if i := strings.IndexAny(s, "[{"); i > 0 {
			s = s[i:]
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(s), &rows); err != nil {
			var wrapped struct {
				Issues []struct {
					ID string `json:"id"`
				} `json:"issues"`
			}
			if err2 := json.Unmarshal([]byte(s), &wrapped); err2 != nil {
				t.Fatalf("bd %s: cannot decode %q: %v / %v", strings.Join(args, " "), s, err, err2)
			}
			rows = wrapped.Issues
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		sort.Strings(out)
		return out
	}
	want := func(t *testing.T, got []string, issues ...string) {
		t.Helper()
		sort.Strings(issues)
		if strings.Join(got, ",") != strings.Join(issues, ",") {
			t.Fatalf("got %v, want %v", got, issues)
		}
	}

	t.Run("undeclared context shows everything", func(t *testing.T) {
		want(t, ids(t, "ready"), gpu.ID, studio.ID, plain.ID, anyHost.ID)
	})
	t.Run("declared-empty context hides every requiring issue", func(t *testing.T) {
		want(t, ids(t, "ready", "--ctx", ""), plain.ID)
	})
	t.Run("bare token", func(t *testing.T) {
		want(t, ids(t, "ready", "--ctx", "gpu"), gpu.ID, plain.ID)
	})
	t.Run("keyed token satisfies its bare key too, and all requirements must hold", func(t *testing.T) {
		want(t, ids(t, "ready", "--ctx", "gpu,host=studio"), gpu.ID, studio.ID, plain.ID, anyHost.ID)
		want(t, ids(t, "ready", "--ctx", "host=studio"), plain.ID, anyHost.ID)
		want(t, ids(t, "ready", "--ctx", "host=mini", "--ctx", "gpu"), gpu.ID, plain.ID, anyHost.ID)
	})
	t.Run("ctx-file merges with --ctx", func(t *testing.T) {
		f := dir + "/ctx"
		if err := os.WriteFile(f, []byte("# box\nhost=studio\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		want(t, ids(t, "ready", "--ctx", "gpu", "--ctx-file", f), gpu.ID, studio.ID, plain.ID, anyHost.ID)
	})
	t.Run("list and list --ready carry it", func(t *testing.T) {
		want(t, ids(t, "list", "--ctx", "gpu"), gpu.ID, plain.ID)
		want(t, ids(t, "list", "--ready", "--ctx", "gpu"), gpu.ID, plain.ID)
	})
	t.Run("blocked carries it", func(t *testing.T) {
		blocked := bdCreate(t, bd, dir, "blocked and needs a gpu", "--type", "task", "--labels", "requires:gpu")
		cmd := exec.Command(bd, "dep", "add", blocked.ID, plain.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dep add: %v\n%s", err, out)
		}
		want(t, ids(t, "blocked", "--ctx", "gpu"), blocked.ID)
		want(t, ids(t, "blocked", "--ctx", ""))
		want(t, ids(t, "blocked"), blocked.ID)
	})
	t.Run("explain refuses a declared context", func(t *testing.T) {
		cmd := exec.Command(bd, "ready", "--explain", "--ctx", "gpu")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil || !strings.Contains(stderr.String(), "--explain") {
			t.Fatalf("want a refusal naming --explain, got err=%v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
	})
	// Last: --claim mutates. A reader declaring nothing but `host=mini` can
	// run only the plain issue and the any-host one, so whichever wins, it
	// must not be a gpu issue.
	t.Run("claim never takes work the context cannot run", func(t *testing.T) {
		got := ids(t, "ready", "--claim", "--ctx", "host=mini")
		if len(got) != 1 || got[0] == gpu.ID || got[0] == studio.ID {
			t.Fatalf("claimed %v; must be one of %s / %s", got, plain.ID, anyHost.ID)
		}
	})
}
