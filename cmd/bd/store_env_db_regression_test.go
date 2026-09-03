package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// envForStoreEnvDBTest returns a hermetic environment with every BEADS_*/BD_*
// inherited from the developer's shell stripped, so the only workspace
// selector in play is the one the subtest sets.
func envForStoreEnvDBTest(home string, extra ...string) []string {
	filtered := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BEADS_") || strings.HasPrefix(entry, "BD_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"BD_NON_INTERACTIVE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	)
	return append(filtered, extra...)
}

// initStoreEnvDBWorkspace creates a real embedded-Dolt workspace holding one
// distinctively-titled issue, so a later read proves *which* workspace was
// opened rather than merely that some workspace was.
func initStoreEnvDBWorkspace(t *testing.T, bin, root, name, prefix, issue string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	home := filepath.Join(root, "home")
	for _, args := range [][]string{
		{"init", "--non-interactive", "--prefix", prefix},
		{"create", issue, "-p", "1"},
	} {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = envForStoreEnvDBTest(home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd %v in %s: %v\n%s", args, name, err, out)
		}
	}
	return dir
}

// TestStorePathHonorsEnvDBTargetOverAmbientWorkspace pins be-bgc2x.
//
// `bd where` (no-DB path) and `bd list` (store-requiring path) must agree on
// which workspace an explicit BEADS_DB/BD_DB target selects. selectedNoDBBeadsDir
// honors both vars, but the store path never consults them: it binds the
// ambient workspace via prepareSelectedCommandContext (cmd/bd/main.go ~1273),
// whose os.Setenv("BEADS_DIR", ...) side effect then short-circuits
// beads.FindDatabasePath()'s BEADS_DIR branch (internal/beads/beads.go ~547)
// before its BEADS_DB branch is ever reached. The read silently returns the
// wrong workspace's issues at exit 0.
func TestStorePathHonorsEnvDBTargetOverAmbientWorkspace(t *testing.T) {
	bin := buildBDForInitTests(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ambient := initStoreEnvDBWorkspace(t, bin, root, "ambient", "amb", "AMBIENT-ONLY-ISSUE")
	target := initStoreEnvDBWorkspace(t, bin, root, "target", "tgt", "TARGET-ONLY-ISSUE")
	targetBeadsDir := filepath.Join(target, ".beads")

	for _, tc := range []struct{ name, envVar string }{
		{name: "BEADS_DB", envVar: "BEADS_DB"},
		{name: "BD_DB", envVar: "BD_DB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := envForStoreEnvDBTest(filepath.Join(root, "home"), tc.envVar+"="+targetBeadsDir)

			// Control: the no-DB path already honors the explicit target.
			where := exec.Command(bin, "where")
			where.Dir = ambient
			where.Env = env
			whereOut, err := where.CombinedOutput()
			if err != nil {
				t.Fatalf("bd where: %v\n%s", err, whereOut)
			}
			if !strings.Contains(string(whereOut), targetBeadsDir) {
				t.Fatalf("precondition: bd where did not resolve the %s target %q\n%s",
					tc.envVar, targetBeadsDir, whereOut)
			}

			// The store-requiring path must select the same workspace.
			list := exec.Command(bin, "list")
			list.Dir = ambient
			list.Env = env
			listOut, err := list.CombinedOutput()
			if err != nil {
				t.Fatalf("bd list: %v\n%s", err, listOut)
			}
			if strings.Contains(string(listOut), "AMBIENT-ONLY-ISSUE") {
				t.Fatalf("bd list read the AMBIENT workspace despite %s=%q; bd where resolved the target. "+
					"Explicit env target silently ignored on the store-requiring path.\n%s",
					tc.envVar, targetBeadsDir, listOut)
			}
			if !strings.Contains(string(listOut), "TARGET-ONLY-ISSUE") {
				t.Fatalf("bd list did not read the %s target workspace %q\n%s",
					tc.envVar, targetBeadsDir, listOut)
			}
		})
	}
}
