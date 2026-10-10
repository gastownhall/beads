package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// The legacy file-valued form names a database file a Dolt workspace never
	// creates, so it must select the workspace without the file existing.
	legacyDBFile := filepath.Join(targetBeadsDir, "beads.db")
	if _, err := os.Stat(legacyDBFile); !os.IsNotExist(err) {
		t.Fatalf("precondition: %q must not exist, or the legacy rows below never reach the missing-target check (stat: %v)",
			legacyDBFile, err)
	}

	for _, tc := range []struct{ name, envVar, target string }{
		{name: "BEADS_DB", envVar: "BEADS_DB", target: targetBeadsDir},
		{name: "BD_DB", envVar: "BD_DB", target: targetBeadsDir},
		{name: "BEADS_DB_LegacyDBFile", envVar: "BEADS_DB", target: legacyDBFile},
		{name: "BD_DB_LegacyDBFile", envVar: "BD_DB", target: legacyDBFile},
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
			if _, err := os.Stat(legacyDBFile); !os.IsNotExist(err) {
				t.Fatalf("bd list created %q for %s=%q; the target selects the workspace, not a file (stat: %v)",
					legacyDBFile, tc.envVar, tc.target, err)
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

// ladderRung is one workspace selector in the precedence list documented in
// docs/reference/configuration.md, paired with the issue title that proves its
// workspace was the one actually opened.
type ladderRung struct {
	beadsDir string
	issue    string
}

// TestStorePathEnvDBPrecedenceLadder pins the ADJACENT rung boundaries of the
// workspace-selection ladder the docs promote to a contract.
//
// The tests above pin exactly one adjacent pair — an explicit env target vs
// ambient discovery. Every other boundary was unpinned, so the ladder could be
// reordered without reddening anything: the BEADS_DB-over-BD_DB rung is encoded
// solely by the order of the string literal []string{"BEADS_DB", "BD_DB"} in
// cmd/bd/main.go, and nothing caught an ambient variable outranking an explicit
// --db or -C on the store-requiring path.
//
// Each case also re-asserts the invariant the fix exists to protect: `bd where`
// (no-DB path) and `bd list` (store-requiring path) must select the SAME
// workspace, whichever rung wins.
func TestStorePathEnvDBPrecedenceLadder(t *testing.T) {
	bin := buildBDForInitTests(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ambient := initStoreEnvDBWorkspace(t, bin, root, "ambient", "amb", "AMBIENT-ONLY-ISSUE")
	alpha := initStoreEnvDBWorkspace(t, bin, root, "alpha", "alp", "ALPHA-ONLY-ISSUE")
	beta := initStoreEnvDBWorkspace(t, bin, root, "beta", "bet", "BETA-ONLY-ISSUE")
	alphaRung := ladderRung{beadsDir: filepath.Join(alpha, ".beads"), issue: "ALPHA-ONLY-ISSUE"}
	betaRung := ladderRung{beadsDir: filepath.Join(beta, ".beads"), issue: "BETA-ONLY-ISSUE"}

	for _, tc := range []struct {
		name          string
		boundary      string
		args          []string
		env           []string
		winner, loser ladderRung
	}{
		{
			name:     "FlagPathBeatsEnvDB",
			boundary: "--db <path> over BEADS_DB",
			args:     []string{"--db", alphaRung.beadsDir},
			env:      []string{"BEADS_DB=" + betaRung.beadsDir},
			winner:   alphaRung,
			loser:    betaRung,
		},
		{
			name:     "ChangeDirBeatsEnvDB",
			boundary: "-C <dir> over BEADS_DB",
			args:     []string{"-C", alpha},
			env:      []string{"BEADS_DB=" + betaRung.beadsDir},
			winner:   alphaRung,
			loser:    betaRung,
		},
		{
			name:     "BeadsDBBeatsBdDB",
			boundary: "BEADS_DB over BD_DB",
			env:      []string{"BEADS_DB=" + alphaRung.beadsDir, "BD_DB=" + betaRung.beadsDir},
			winner:   alphaRung,
			loser:    betaRung,
		},
		{
			name:     "BeadsDBBeatsBeadsDir",
			boundary: "BEADS_DB over BEADS_DIR",
			env:      []string{"BEADS_DB=" + alphaRung.beadsDir, "BEADS_DIR=" + betaRung.beadsDir},
			winner:   alphaRung,
			loser:    betaRung,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := envForStoreEnvDBTest(home, tc.env...)
			run := func(sub string) []byte {
				t.Helper()
				args := append(append([]string{}, tc.args...), sub)
				cmd := exec.Command(bin, args...)
				cmd.Dir = ambient
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("bd %v (%s): %v\n%s", args, tc.boundary, err, out)
				}
				return out
			}

			// No-DB path.
			whereOut := string(run("where"))
			if !strings.Contains(whereOut, tc.winner.beadsDir) {
				t.Fatalf("bd where did not select %s for %s\n%s", tc.winner.beadsDir, tc.boundary, whereOut)
			}
			if strings.Contains(whereOut, tc.loser.beadsDir) {
				t.Fatalf("bd where selected the lower rung %s for %s\n%s", tc.loser.beadsDir, tc.boundary, whereOut)
			}

			// Store-requiring path must agree.
			listOut := string(run("list"))
			if !strings.Contains(listOut, tc.winner.issue) {
				t.Fatalf("bd list did not read %s for %s\n%s", tc.winner.beadsDir, tc.boundary, listOut)
			}
			if strings.Contains(listOut, tc.loser.issue) {
				t.Fatalf("bd list read the lower rung %s for %s — the store path disagrees with bd where\n%s",
					tc.loser.beadsDir, tc.boundary, listOut)
			}
			if strings.Contains(listOut, "AMBIENT-ONLY-ISSUE") {
				t.Fatalf("bd list fell through to ambient discovery for %s\n%s", tc.boundary, listOut)
			}
		})
	}
}

// TestStorePathRejectsUnresolvableEnvDBTarget pins that a set-but-unresolvable
// BEADS_DB/BD_DB fails loudly on the store-requiring path instead of
// fabricating a database, and pins the no-DB path's documented answer for the
// same input.
//
// resolveCommandBeadsDir's last resort is filepath.Dir(dbPath), which never
// returns empty for a non-empty input, so before this guard a stale or typo'd
// env target bootstrapped a brand-new empty embedded database at the typo's
// PARENT directory, answered "No issues found." and exited 0 — a false all-clear
// for any script asking whether there are open issues, plus an embeddeddolt
// directory and gate lock written outside any real workspace.
//
// The on-disk assertion is the load-bearing half: an error message alone would
// not prove the store was never created.
func TestStorePathRejectsUnresolvableEnvDBTarget(t *testing.T) {
	bin := buildBDForInitTests(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ambient := initStoreEnvDBWorkspace(t, bin, root, "ambient", "amb", "AMBIENT-ONLY-ISSUE")

	entries := func(dir string) string {
		t.Helper()
		found, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		names := []string{}
		for _, entry := range found {
			names = append(names, entry.Name())
		}
		return strings.Join(names, ",")
	}

	for _, envVar := range []string{"BEADS_DB", "BD_DB"} {
		t.Run(envVar, func(t *testing.T) {
			parent := filepath.Join(root, "typo-"+envVar)
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", parent, err)
			}
			missing := filepath.Join(parent, "does-not-exist")
			before := entries(parent)

			list := exec.Command(bin, "list")
			list.Dir = ambient
			list.Env = envForStoreEnvDBTest(home, envVar+"="+missing)
			out, err := list.CombinedOutput()

			if err == nil {
				t.Fatalf("bd list exited 0 with %s=%q naming a nonexistent path; "+
					"an unresolvable explicit target must fail loudly\n%s", envVar, missing, out)
			}
			if strings.Contains(string(out), "No issues found.") {
				t.Fatalf("bd list answered from a fabricated database for %s=%q\n%s", envVar, missing, out)
			}
			if strings.Contains(string(out), "AMBIENT-ONLY-ISSUE") {
				t.Fatalf("bd list silently fell through to the ambient workspace for %s=%q\n%s",
					envVar, missing, out)
			}
			for _, want := range []string{envVar, missing} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("bd list's failure does not name %q, so the operator cannot tell which "+
						"selector is wrong\n%s", want, out)
				}
			}
			if after := entries(parent); after != before {
				t.Fatalf("bd list bootstrapped a database beside %s=%q: %s contained %q before and %q after",
					envVar, missing, parent, before, after)
			}

			// The no-DB path does not check the target, and
			// docs/reference/configuration.md says so: `bd where` exits 0 and
			// prints the missing path's parent directory. Pinned so that making
			// either path validate, or stop validating, has to update the docs
			// too. The ceiling stops the upward walk at root, so a .beads above
			// the test's temp directory cannot answer instead of the parent.
			where := exec.Command(bin, "where")
			where.Dir = ambient
			where.Env = envForStoreEnvDBTest(home, envVar+"="+missing, "BEADS_CEILING_DIRECTORIES="+root)
			whereOut, err := where.CombinedOutput()
			if err != nil {
				t.Fatalf("bd where failed for %s=%q, but the docs say the no-DB path does not check the target: %v\n%s",
					envVar, missing, err, whereOut)
			}
			if !slices.Contains(strings.Split(strings.TrimSpace(string(whereOut)), "\n"), parent) {
				t.Fatalf("bd where did not print %s=%q's parent directory %q as documented\n%s",
					envVar, missing, parent, whereOut)
			}
			if after := entries(parent); after != before {
				t.Fatalf("bd where created files beside %s=%q: %s contained %q before and %q after",
					envVar, missing, parent, before, after)
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
	// Routing bd list here auto-starts the proxy and its backend dolt
	// sql-server under <beadsDir>/dolt; stop them before the suite sweep.
	stopProxiedServerCleanup(t, filepath.Join(proxiedBeadsDir, "dolt"))

	for _, envVar := range []string{"BEADS_DB", "BD_DB"} {
		t.Run(envVar, func(t *testing.T) {
			env := envForStoreEnvDBTest(filepath.Join(root, "home"), envVar+"="+proxiedBeadsDir)

			list := exec.Command(bin, "list")
			list.Dir = ambient
			list.Env = env
			out, err := list.CombinedOutput()

			// A proxied workspace with no issues prints nothing that names it, so
			// the checks below are all negative. Two positive assertions keep them
			// from greening on a total failure: the command must actually succeed,
			// and the same env must resolve the proxied workspace on the no-DB
			// path. Without these, a proxy or backend that failed to start for an
			// unrelated reason leaves `out` empty and every check below passes.
			if err != nil {
				t.Fatalf("%s pointed at a proxied-server workspace, but bd list failed: %v\n%s",
					envVar, err, out)
			}
			where := exec.Command(bin, "where")
			where.Dir = ambient
			where.Env = env
			whereOut, whereErr := where.CombinedOutput()
			if whereErr != nil {
				t.Fatalf("bd where: %v\n%s", whereErr, whereOut)
			}
			if !strings.Contains(string(whereOut), proxiedBeadsDir) {
				t.Fatalf("%s=%q did not resolve to the proxied workspace on the no-DB path, so the "+
					"assertions below prove nothing about which workspace bd list opened\n%s",
					envVar, proxiedBeadsDir, whereOut)
			}

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
