// Readonly gates for the Dolt publish and destroy commands (GH#7876):
// `bd dolt push`, `bd dolt clean-databases` and `bd federation sync` must
// refuse under strict --readonly with the standard CheckReadonly refusal, the
// way the ~120 other write commands do, instead of running their bodies.
//
// This file MUST NOT carry a cgo build tag: it drives a bd binary built with
// the gms_pure_go tag via subprocess (the hermetic harness declared in
// migration_freeze_gate_test.go), because CheckReadonly exits the process.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doltReadonlyGateWorkspace is an initialized embedded workspace with a
// file:// remote named origin, plus a snapshot of everything a refused
// publish could have changed.
type doltReadonlyGateWorkspace struct {
	bd, dir   string
	remoteDir string
}

func newDoltReadonlyGateWorkspace(t *testing.T) *doltReadonlyGateWorkspace {
	t.Helper()
	bd, dir := setupMigrationFreezeWorkspace(t)
	w := &doltReadonlyGateWorkspace{bd: bd, dir: dir, remoteDir: filepath.Join(dir, "remote")}
	if err := os.MkdirAll(w.remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, code := w.run(t, nil, "create", "seed issue", "-p", "2"); code != 0 {
		t.Fatalf("bd create failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := w.run(t, nil, "dolt", "remote", "add", "origin", "file://"+w.remoteDir); code != 0 {
		t.Fatalf("bd dolt remote add failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	return w
}

func (w *doltReadonlyGateWorkspace) run(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runBDMigrationFreezeWithEnv(t, w.bd, w.dir, env, args...)
}

// readonlyGateSpellings are the two ways to turn strict read-only on.
var readonlyGateSpellings = []struct {
	name  string
	flags []string
	env   []string
}{
	{name: "flag", flags: []string{"--readonly"}},
	{name: "env", env: []string{"BD_READONLY=true"}},
}

// assertStandardReadonlyRefusal checks the exact CheckReadonly refusal shape.
func assertStandardReadonlyRefusal(t *testing.T, op, stdout, stderr string, code int) {
	t.Helper()
	want := "Error: operation '" + op + "' is not allowed in read-only mode\n"
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr does not end with the standard refusal\ngot:  %q\nwant: %q", stderr, want)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when the command is refused, got:\n%s", stdout)
	}
}

// TestDoltPushReadonly pins AC-2: `bd dolt push` refuses under strict
// read-only before its body runs, so neither a real push nor the no-push or
// local-only success returns can slip past the gate. The remote directory is
// compared before and after, so a row that published is caught even if it
// also printed the refusal.
func TestDoltPushReadonly(t *testing.T) {
	w := newDoltReadonlyGateWorkspace(t)
	before := snapshotReadonlyTree(t, w.remoteDir)
	beadsBefore := snapshotReadonlyTree(t, filepath.Join(w.dir, ".beads"))

	rows := []struct {
		name string
		args []string
		env  []string
	}{
		{name: "default", args: []string{"dolt", "push"}},
		{name: "force", args: []string{"dolt", "push", "--force"}},
		{name: "named", args: []string{"dolt", "push", "origin"}},
		{name: "named-flag", args: []string{"dolt", "push", "--remote", "origin"}},
		// no-push returns success before any push; readonly must still refuse.
		{name: "no-push", args: []string{"dolt", "push"}, env: []string{"BD_NO_PUSH=true"}},
		{name: "local-only", args: []string{"dolt", "push"}, env: []string{"BD_DOLT_LOCAL_ONLY=true"}},
	}
	for _, spelling := range readonlyGateSpellings {
		t.Run(spelling.name, func(t *testing.T) {
			for _, row := range rows {
				t.Run(row.name, func(t *testing.T) {
					args := append(append([]string{}, row.args...), spelling.flags...)
					env := append(append([]string{}, spelling.env...), row.env...)
					stdout, stderr, code := w.run(t, env, args...)
					assertStandardReadonlyRefusal(t, "dolt push", stdout, stderr, code)
					if diff := readonlyTreeDiff(before, snapshotReadonlyTree(t, w.remoteDir)); len(diff) > 0 {
						t.Errorf("a refused push changed the remote:\n  %s", strings.Join(diff, "\n  "))
					}
					if diff := readonlyTreeDiff(beadsBefore, snapshotReadonlyTree(t, filepath.Join(w.dir, ".beads"))); len(diff) > 0 {
						t.Errorf("a refused push changed .beads:\n  %s", strings.Join(diff, "\n  "))
					}
				})
			}
		})
	}
}
