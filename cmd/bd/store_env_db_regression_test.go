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

// TestStorePathHonorsEnvDBTargetOverAmbientWorkspace pins be-git2o (GH#6255).
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

	for _, tc := range []struct{ name, envVar, target string }{
		{name: "BEADS_DB", envVar: "BEADS_DB", target: targetBeadsDir},
		{name: "BD_DB", envVar: "BD_DB", target: targetBeadsDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := envForStoreEnvDBTest(filepath.Join(root, "home"), tc.envVar+"="+tc.target)

			// Control: the no-DB path already honors the explicit target.
			where := exec.Command(bin, "where")
			where.Dir = ambient
			where.Env = env
			whereOut, err := where.CombinedOutput()
			if err != nil {
				t.Fatalf("bd where: %v\n%s", err, whereOut)
			}
			if !strings.Contains(string(whereOut), targetBeadsDir) {
				t.Fatalf("precondition: bd where did not resolve %s=%q to %q\n%s",
					tc.envVar, tc.target, targetBeadsDir, whereOut)
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
					tc.envVar, tc.target, listOut)
			}
			if !strings.Contains(string(listOut), "TARGET-ONLY-ISSUE") {
				t.Fatalf("bd list did not read the %s target workspace %q\n%s",
					tc.envVar, tc.target, listOut)
			}
		})
	}
}

// TestStorePathAndNoDBPathAgreeOnWorkspaceRootEnvTarget pins the SHAPE of the
// invariant above for a target form neither path honors.
//
// A directory-valued BEADS_DB naming the workspace ROOT (no trailing /.beads)
// is resolved by internal/beads' FindDatabasePath via findDatabaseInBeadsDir
// (GH#2548), but NOT by cmd/bd: resolveCommandBeadsDir walks upward from
// filepath.Dir(target), so for "<ws>" it probes "<ws>/../.beads" and never
// "<ws>/.beads". Measured on origin/main and on this branch, neither `bd where`
// nor `bd list` selects the target for that form.
//
// This test does not assert that the form works — it does not, on either path,
// and making only the store path honor it would reintroduce exactly the
// where/list divergence the fix above removes. What it pins is that the two
// paths keep AGREEING: whatever the workspace-root form resolves to, both
// commands must resolve it the same way, so a later change to one path cannot
// silently split them again.
func TestStorePathAndNoDBPathAgreeOnWorkspaceRootEnvTarget(t *testing.T) {
	bin := buildBDForInitTests(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ambient := initStoreEnvDBWorkspace(t, bin, root, "ambient", "amb", "AMBIENT-ONLY-ISSUE")
	target := initStoreEnvDBWorkspace(t, bin, root, "target", "tgt", "TARGET-ONLY-ISSUE")
	targetBeadsDir := filepath.Join(target, ".beads")

	for _, envVar := range []string{"BEADS_DB", "BD_DB"} {
		t.Run(envVar, func(t *testing.T) {
			env := envForStoreEnvDBTest(filepath.Join(root, "home"), envVar+"="+target)

			where := exec.Command(bin, "where")
			where.Dir = ambient
			where.Env = env
			whereOut, err := where.CombinedOutput()
			if err != nil {
				t.Fatalf("bd where: %v\n%s", err, whereOut)
			}
			whereSelectedTarget := strings.Contains(string(whereOut), targetBeadsDir)

			list := exec.Command(bin, "list")
			list.Dir = ambient
			list.Env = env
			listOut, err := list.CombinedOutput()
			if err != nil {
				t.Fatalf("bd list: %v\n%s", err, listOut)
			}
			listSelectedTarget := strings.Contains(string(listOut), "TARGET-ONLY-ISSUE")

			if whereSelectedTarget != listSelectedTarget {
				t.Fatalf("where/list disagree on a workspace-root %s=%q: "+
					"where selected target=%v, list selected target=%v. "+
					"The two paths must resolve an explicit env target identically.\n"+
					"--- where ---\n%s\n--- list ---\n%s",
					envVar, target, whereSelectedTarget, listSelectedTarget, whereOut, listOut)
			}

			// Whatever it resolves to, it must never be the ambient workspace's
			// data presented as if the explicit target had been honored.
			if !listSelectedTarget && strings.Contains(string(listOut), "AMBIENT-ONLY-ISSUE") {
				t.Fatalf("bd list silently read the AMBIENT workspace for %s=%q\n%s",
					envVar, target, listOut)
			}
		})
	}
}

// TestStorePathEnvDBTargetRoutesProxiedServerWorkspace covers the behaviour
// change the fix above introduces for workspaces that have no local database
// file.
//
// Resolving BEADS_DB/BD_DB before the ambient-discovery block means every later
// `if dbPath == ""` guard in the same PreRun is skipped for an env-selected
// workspace — including the branch that routes proxied-server, registered-remote
// and unsupported-backend workspaces by setting dbPath to the .beads dir. That
// branch existed precisely because such a workspace may have no local Dolt
// database to discover, so skipping it could plausibly turn an env-selected
// proxied workspace into "no database found".
//
// It does not: the .beads directory IS the resolved dbPath on this path, and
// config loading downstream reaches the proxied-server config the same way.
// `--db` already skipped the same guards, so the env path now matches it.
//
// Measured against origin/main, the same invocation reads the AMBIENT
// workspace's issues instead — the bug the fix above removes.
func TestStorePathEnvDBTargetRoutesProxiedServerWorkspace(t *testing.T) {
	bin := buildBDForInitTests(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ambient := initStoreEnvDBWorkspace(t, bin, root, "ambient", "amb", "AMBIENT-ONLY-ISSUE")

	// A proxied-server workspace, declared by metadata alone: no local Dolt
	// database directory is created, which is the whole point.
	proxiedBeadsDir := filepath.Join(root, "proxied", ".beads")
	if err := os.MkdirAll(proxiedBeadsDir, 0o700); err != nil {
		t.Fatalf("mkdir proxied: %v", err)
	}
	metadata := `{"database":"dolt","backend":"dolt","dolt_mode":"proxied-server",` +
		`"dolt_server_host":"127.0.0.1","dolt_database":"proxiedtest"}`
	if err := os.WriteFile(filepath.Join(proxiedBeadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write proxied metadata: %v", err)
	}

	for _, envVar := range []string{"BEADS_DB", "BD_DB"} {
		t.Run(envVar, func(t *testing.T) {
			list := exec.Command(bin, "list")
			list.Dir = ambient
			list.Env = envForStoreEnvDBTest(filepath.Join(root, "home"), envVar+"="+proxiedBeadsDir)
			out, _ := list.CombinedOutput()

			if strings.Contains(string(out), "AMBIENT-ONLY-ISSUE") {
				t.Fatalf("%s pointed at a proxied-server workspace, but bd list read the AMBIENT "+
					"workspace instead — the env target was not routed.\n%s", envVar, out)
			}
			// The proxied workspace has no local database on purpose; routing it
			// as "no database found" would be the regression this pins against.
			for _, bad := range []string{"no database found", "No database found", "not a beads workspace"} {
				if strings.Contains(string(out), bad) {
					t.Fatalf("%s pointed at a proxied-server workspace, but bd list reported %q — "+
						"the proxied routing branch was skipped without a replacement.\n%s", envVar, bad, out)
				}
			}
		})
	}
}
