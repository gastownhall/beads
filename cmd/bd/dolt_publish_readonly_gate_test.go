// The readonly gate for the Dolt publish/cleanup commands (GH#7876 items 2
// and 3) and `bd federation sync` (item 5's readonly twin): `bd dolt push`,
// `bd dolt clean-databases` and `bd federation sync` publish or destroy data,
// so they must refuse under strict --readonly exactly like the ~120 write
// commands that call CheckReadonly at the top of their RunE. All three run
// through the hermetic subprocess harness because CheckReadonly exits the
// process; the sqlite workspace behind it has no Dolt remote at all, so a run
// that is NOT refused fails on its own before touching any transport — the
// exact refusal text is the proof the gate fired first.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorkspaceYAMLConfig appends lines to the workspace's .beads/config.yaml,
// the file config.GetBool reads for flag-backed keys such as readonly.
func writeWorkspaceYAMLConfig(t *testing.T, dir, text string) {
	t.Helper()
	path := filepath.Join(dir, ".beads", "config.yaml")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(current, []byte(text)...), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// assertReadonlyRefusal checks the shared shape of a publish-command
// refusal: exit 1, empty stdout, and exactly the standard CheckReadonly
// message on stderr.
func assertReadonlyRefusal(t *testing.T, stdout, stderr string, exitCode int, operation string) {
	t.Helper()
	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	want := "Error: operation '" + operation + "' is not allowed in read-only mode\n"
	if stderr != want {
		t.Errorf("stderr is not exactly the standard refusal\ngot:  %q\nwant: %q", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when the command is refused, got:\n%s", stdout)
	}
}

// TestDoltPushBlockedInReadonlyMode is the readonly gate for `bd dolt push`
// (GH#7876 item 3). The gate is the first statement of the RunE, so it fires
// even when no-push would otherwise return success.
func TestDoltPushBlockedInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	for _, tt := range []struct {
		name string
		// configKeys are written with `bd config set` before the refused run,
		// for rows whose early returns the gate must precede.
		configKeys map[string]string
		// readonlyInYAML appends `readonly: true` to .beads/config.yaml
		// instead: readonly is a workspace-YAML flag, not a database setting,
		// and `bd config set` would store it where GetBool never reads it.
		readonlyInYAML bool
		args           []string
		readonlyBy     string // "flag" or "config"
	}{
		{name: "default", args: []string{"dolt", "push"}, readonlyBy: "flag"},
		{name: "force", args: []string{"dolt", "push", "--force"}, readonlyBy: "flag"},
		{name: "named_remote", args: []string{"dolt", "push", "origin"}, readonlyBy: "flag"},
		// no-push returns success before any store work; readonly must win.
		{name: "no_push_would_skip", configKeys: map[string]string{"no-push": "true"}, args: []string{"dolt", "push"}, readonlyBy: "flag"},
		// The config spelling of readonly: `readonly: true` in
		// .beads/config.yaml, no --readonly on the command line.
		{name: "readonly_from_config", readonlyInYAML: true, args: []string{"dolt", "push"}, readonlyBy: "config"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.configKeys {
				stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "config", "set", key, value)
				if code != 0 {
					t.Fatalf("bd config set %s failed (exit %d):\nstdout:\n%s\nstderr:\n%s", key, code, stdout, stderr)
				}
			}
			if tt.readonlyInYAML {
				writeWorkspaceYAMLConfig(t, dir, "readonly: true\n")
			}
			args := append([]string{}, tt.args...)
			if tt.readonlyBy == "flag" {
				args = append(args, "--readonly")
			}
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)
			assertReadonlyRefusal(t, stdout, stderr, code, "dolt push")
		})
	}
}

// TestDoltPushReadonlyGateLetsControlRun pins that the gate keys on readonly
// alone: without it the same command is not refused for a readonly reason
// (the sqlite workspace has no Dolt remote, so it fails differently).
func TestDoltPushReadonlyGateLetsControlRun(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "dolt", "push")
	if strings.Contains(stderr, "not allowed in read-only mode") {
		t.Fatalf("bd dolt push without --readonly was refused by the readonly gate:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if code == 0 && !strings.Contains(stderr, "skipping push") {
		t.Logf("control run: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
