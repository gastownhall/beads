package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runBDInDir runs the prebuilt bd binary in dir and returns combined output.
// Workspace-pointing env vars are scrubbed so the child always operates on
// dir, not on a workspace leaked by an earlier in-process test (executing
// rootCmd rebinds BEADS_DIR process-wide and not every test restores it).
func runBDInDir(t *testing.T, bin, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	scrub := map[string]bool{
		"BEADS_DIR": true, "BEADS_DB": true, "BD_DB": true,
		"BEADS_DOLT_SERVER_MODE": true, "BEADS_DOLT_SHARED_SERVER": true,
	}
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && scrub[name] {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// resetInitFlags restores initCmd flag state so one test's flags do not leak
// into the next Execute call (cobra flag values persist per process).
func resetInitFlags(t *testing.T, names ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range names {
			f := initCmd.Flags().Lookup(name)
			if f == nil {
				t.Fatalf("unknown init flag %q", name)
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	})
}

// runInitCommand executes "bd init" with the given extra args in a fresh temp
// dir and returns the workspace's .beads dir and the Execute error.
func runInitCommand(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()

	origDBPath := dbPath
	origStore := store
	t.Cleanup(func() {
		if store != nil && store != origStore {
			_ = store.Close()
		}
		store = origStore
		dbPath = origDBPath
	})
	dbPath = ""
	store = nil

	// Neutralize Dolt-mode environment leakage: a leftover shared-server or
	// server-mode env var must not influence flatfile init (TASKS-9tsg).
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DIR", "")
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DOLT_SHARED_SERVER")
	os.Unsetenv("BEADS_DOLT_SERVER_MODE")

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	rootCmd.SetArgs(append([]string{"init"}, extraArgs...))
	err := rootCmd.Execute()
	return filepath.Join(tmpDir, ".beads"), err
}

// TASKS-9tsg: flatfile init must dispatch before the embedded-Dolt lock and
// shared-server startup — a flat-file workspace must never contain the
// Dolt-only .beads/embeddeddolt/ directory (its presence makes clones be
// misclassified as Dolt workspaces).
func TestInitFlatfileCreatesNoEmbeddedDoltArtifacts(t *testing.T) {
	resetInitFlags(t, "backend", "prefix", "quiet", "skip-hooks", "skip-agents")

	beadsDir, err := runInitCommand(t,
		"--backend=flatfile", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("bd init --backend=flatfile failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, "issues")); err != nil {
		t.Errorf("expected flatfile issues/ dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err != nil {
		t.Errorf("expected metadata.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(err) {
		t.Errorf("flatfile init created Dolt artifact .beads/embeddeddolt (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Errorf("flatfile init created Dolt artifact .beads/dolt (stat err=%v)", err)
	}
}

// TASKS-vs5y: flatfile prefixes become filename components
// (issues/<prefix>-<n>.json); a prefix with a path separator or space must be
// rejected up front — before any side effects — instead of producing a
// workspace where every 'bd create' fails with ENOENT. Auto-detect-shaped
// prefixes (underscores, uppercase) stay accepted for parity with the Dolt
// path's database-name rule.
func TestRunInitFlatfileValidatesPrefix(t *testing.T) {
	for _, tc := range []struct {
		prefix  string
		wantErr bool
	}{
		{"a/b", true},
		{`a\b`, true},
		{"a b", true},
		{"-lead", true},
		{"my_Project", false},
		{"tasks-2", false},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			origStore := store
			t.Cleanup(func() {
				if store != nil && store != origStore {
					_ = store.Close()
				}
				store = origStore
			})

			tmpDir := t.TempDir()
			t.Chdir(tmpDir)
			beadsDir := filepath.Join(tmpDir, ".beads")

			err := runInitFlatfile(t.Context(), beadsDir, tc.prefix, true, true, true, false)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("runInitFlatfile(prefix=%q) succeeded; want validation error", tc.prefix)
				}
				// Validation must fire before side effects: no half-created workspace.
				if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
					t.Errorf("invalid prefix %q left a partially created %s (stat err=%v)", tc.prefix, beadsDir, statErr)
				}
			} else if err != nil {
				t.Fatalf("runInitFlatfile(prefix=%q) failed: %v", tc.prefix, err)
			}
		})
	}
}

// TASKS-vqtt: Dolt-only init flags must be rejected on --backend=flatfile,
// not silently ignored (the user asked for server mode / a named database
// and would silently get neither).
//
// NOTE: this test must run AFTER any in-process test that inits with an
// explicit non-Dolt --backend (tests run in source order within a file):
// setting a flag via Execute adds it to pflag's `actual` set permanently —
// there is no API to remove it — so the allowlist's Flags().Visit sees the
// Dolt-only flags from this test in every later in-process
// `init --backend=<non-dolt>` call, even though their values are reset.
func TestInitFlatfileRejectsDoltOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
		args []string
	}{
		{"server", "--server", []string{"--server"}},
		{"database", "--database", []string{"--database", "mydb"}},
		{"shared-server", "--shared-server", []string{"--shared-server"}},
		{"remote", "--remote", []string{"--remote", "https://example.com/x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flagNames := []string{"backend", "quiet"}
			for _, a := range tc.args {
				if len(a) > 2 && a[:2] == "--" {
					flagNames = append(flagNames, a[2:])
				}
			}
			resetInitFlags(t, flagNames...)

			_, err := runInitCommand(t, append([]string{"--backend=flatfile", "--quiet"}, tc.args...)...)
			if err == nil {
				t.Fatalf("bd init --backend=flatfile %s succeeded; want rejection", tc.flag)
			}
			if !strings.Contains(err.Error(), tc.flag) {
				t.Errorf("error %q does not name the rejected flag %s", err, tc.flag)
			}
		})
	}
}

// TASKS-abg5 (unit): metadata backend=flatfile must trip the existing-data
// guard with the same benign "already initialized" marker the SQL arms use,
// so --init-if-missing stays idempotent and plain re-init refuses instead of
// re-stamping metadata.json.
func TestCheckExistingBeadsDataAtFlatfile(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"flatfile","project_id":"test-project"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkExistingBeadsDataAt(beadsDir, "tst")
	if err == nil {
		t.Fatal("checkExistingBeadsDataAt on a flatfile workspace returned nil; want already-initialized refusal")
	}
	if !errors.Is(err, errWorkspaceAlreadyInitialized) {
		t.Errorf("guard error %v does not match errWorkspaceAlreadyInitialized; --init-if-missing idempotency depends on it", err)
	}
}

// TASKS-abg5 (e2e): a live flat-file workspace must survive re-init attempts.
// (1) plain `bd init` re-run must refuse (it used to mint a new project_id and
// rewrite the prefix), (2) `bd init --backend=dolt` must refuse (it used to
// overwrite metadata.json and orphan every issue file), (3) --init-if-missing
// must skip idempotently with exit 0, and (4) --reinit-local remains the
// explicit escape hatch.
func TestInitGuardsLiveFlatfileWorkspace(t *testing.T) {
	bdBin := buildBDForInitTests(t)
	dir := t.TempDir()

	if out, err := runBDInDir(t, bdBin, dir, "init", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("first init failed: %v\n%s", err, out)
	}
	metaPath := filepath.Join(dir, ".beads", "metadata.json")
	metaBefore, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	// (1) Plain re-run refuses and changes nothing.
	out, err := runBDInDir(t, bdBin, dir, "init", "--prefix", "other", "--quiet", "--skip-hooks", "--skip-agents")
	if err == nil {
		t.Errorf("plain re-init of a flatfile workspace succeeded; want refusal\n%s", out)
	}
	if !strings.Contains(out, "already initialized") {
		t.Errorf("re-init refusal does not say 'already initialized':\n%s", out)
	}

	// (2) --backend=dolt refuses and changes nothing.
	out, err = runBDInDir(t, bdBin, dir, "init", "--backend=dolt", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err == nil {
		t.Errorf("bd init --backend=dolt over a flatfile workspace succeeded; want refusal\n%s", out)
	}
	if metaAfter, _ := os.ReadFile(metaPath); string(metaAfter) != string(metaBefore) {
		t.Errorf("metadata.json changed despite refusals:\nbefore: %s\nafter:  %s", metaBefore, metaAfter)
	}

	// (3) --init-if-missing skips idempotently.
	out, err = runBDInDir(t, bdBin, dir, "init", "--init-if-missing", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Errorf("init --init-if-missing on initialized flatfile workspace failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Skipping init") {
		t.Errorf("--init-if-missing did not report the idempotent skip:\n%s", out)
	}
	if metaAfter, _ := os.ReadFile(metaPath); string(metaAfter) != string(metaBefore) {
		t.Errorf("--init-if-missing rewrote metadata.json:\nbefore: %s\nafter:  %s", metaBefore, metaAfter)
	}

	// (4) --reinit-local is the explicit escape hatch (empty workspace: no
	// confirmation needed) and re-stamps a fresh identity.
	out, err = runBDInDir(t, bdBin, dir, "init", "--reinit-local", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("init --reinit-local failed: %v\n%s", err, out)
	}
	metaAfter, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metaAfter), `"backend":"flatfile"`) {
		t.Errorf("reinit did not keep flatfile backend: %s", metaAfter)
	}
	if string(metaAfter) == string(metaBefore) {
		t.Errorf("--reinit-local did not re-initialize (metadata.json unchanged)")
	}
}

// TASKS-abg5: the guard routes destructive flat-file re-init through
// --reinit-local + the destroy-token confirmation; --destroy-token must
// therefore be accepted by the flatfile flag allowlist, and a non-empty
// workspace must refuse non-interactive reinit until the token matches.
func TestInitFlatfileReinitDestroyToken(t *testing.T) {
	bdBin := buildBDForInitTests(t)
	dir := t.TempDir()

	if out, err := runBDInDir(t, bdBin, dir, "init", "--backend=flatfile", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	if out, err := runBDInDir(t, bdBin, dir, "create", "Guard fixture issue"); err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	// Without the token, non-interactive reinit of a non-empty workspace refuses.
	out, err := runBDInDir(t, bdBin, dir, "init", "--backend=flatfile", "--reinit-local", "--non-interactive", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err == nil {
		t.Errorf("non-interactive reinit without --destroy-token succeeded; want refusal\n%s", out)
	}

	// With the matching token, reinit proceeds.
	out, err = runBDInDir(t, bdBin, dir, "init", "--backend=flatfile", "--reinit-local", "--non-interactive", "--destroy-token", "DESTROY-tst", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("reinit with matching --destroy-token failed: %v\n%s", err, out)
	}
}

// TASKS-luzu: explicit Dolt server intent with the backend left to default
// must refuse — `bd init --server` (or --database, --shared-server, …)
// used to validate the flag, then silently produce a flat-file workspace
// with the server intent discarded and a stray .beads/dolt/ marker behind.
func TestInitDefaultBackendRejectsDoltServerFlags(t *testing.T) {
	cases := []struct {
		flag string
		args []string
	}{
		{"server", []string{"--server"}},
		{"shared-server", []string{"--shared-server"}},
		{"database", []string{"--database", "beadsdb"}},
		{"server-port", []string{"--server-port", "13307"}},
		// The onboarding wizards are Dolt flows that run after store
		// creation; the flat-file dispatch used to skip them silently
		// (exit 0, no wizard) — the same validate-then-discard shape.
		{"contributor", []string{"--contributor"}},
		{"team", []string{"--team"}},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			resetInitFlags(t, "backend", "prefix", "quiet", "skip-hooks", "skip-agents", tc.flag)
			beadsDir, err := runInitCommand(t, append([]string{"--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents"}, tc.args...)...)
			if err == nil {
				t.Fatalf("bd init %s with defaulted backend succeeded; want refusal", strings.Join(tc.args, " "))
			}
			if !strings.Contains(err.Error(), "--backend=dolt") {
				t.Errorf("refusal does not point at --backend=dolt: %v", err)
			}
			if !strings.Contains(err.Error(), "--"+tc.flag) {
				t.Errorf("refusal does not name the offending flag --%s: %v", tc.flag, err)
			}
			if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
				t.Errorf(".beads was created despite the refusal (stat err: %v)", statErr)
			}
		})
	}
}

// runGitIn runs a git command in dir, failing the test on error.
func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TASKS-nfbg: `bd init` in a repo whose origin has refs/dolt/data used to
// clone the team's Dolt database, print "Bootstrapped from remote", then
// stamp metadata backend=flatfile — orphaning the just-cloned history while
// bd list showed an empty store. With the backend defaulted, init must
// refuse with guidance instead; with explicit --backend=flatfile it must
// proceed fresh WITHOUT cloning.
func TestInitDefaultBackendRefusesDoltOriginData(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	work := t.TempDir()
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitIn(t, work, "init", "-q")
	runGitIn(t, work, "init", "-q", "--bare", origin)
	runGitIn(t, work, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(work, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, work, "add", "seed.txt")
	runGitIn(t, work, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "seed")
	runGitIn(t, work, "push", "-q", "origin", "HEAD:refs/dolt/data")

	// Defaulted backend: refuse, never clone-then-stamp.
	out, err := runBDInDir(t, bdBin, work, "init", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err == nil {
		t.Fatalf("plain bd init with Dolt data on origin succeeded; want refusal\n%s", out)
	}
	if !strings.Contains(out, "--backend=dolt") || !strings.Contains(out, "--backend=flatfile") {
		t.Errorf("refusal does not offer both --backend=dolt and --backend=flatfile:\n%s", out)
	}
	if strings.Contains(out, "Bootstrapped from remote") {
		t.Errorf("init bootstrapped from the Dolt remote despite the flatfile default:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(work, ".beads", "metadata.json")); !os.IsNotExist(statErr) {
		t.Errorf("refusal still stamped metadata.json (stat err: %v)", statErr)
	}

	// Explicit --backend=flatfile: proceed fresh, no Dolt clone artifacts.
	out, err = runBDInDir(t, bdBin, work, "init", "--backend=flatfile", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents")
	if err != nil {
		t.Fatalf("bd init --backend=flatfile with Dolt data on origin failed: %v\n%s", err, out)
	}
	meta, err := os.ReadFile(filepath.Join(work, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"backend":"flatfile"`) {
		t.Errorf("metadata.json backend is not flatfile: %s", meta)
	}
	for _, doltDir := range []string{"embeddeddolt", "dolt"} {
		if _, statErr := os.Stat(filepath.Join(work, ".beads", doltDir)); !os.IsNotExist(statErr) {
			t.Errorf("explicit flatfile init left Dolt artifact .beads/%s (stat err: %v)", doltDir, statErr)
		}
	}
}

// GH#2950 parity: the Dolt init arm stamps beads.role after store creation;
// the flat-file arm must too, or every routed read command (bd list, bd
// ready) warns "beads.role not configured" on each invocation — and worse,
// harnesses that parse combined output get their JSON polluted.
func TestInitFlatfileStampsBeadsRole(t *testing.T) {
	bdBin := buildBDForInitTests(t)
	dir := t.TempDir()
	runGitIn(t, dir, "init", "-b", "main")

	if out, err := runBDInDir(t, bdBin, dir, "init", "--prefix", "tst", "--quiet", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	roleCmd := exec.Command("git", "config", "--get", "beads.role")
	roleCmd.Dir = dir
	role, err := roleCmd.Output()
	if err != nil {
		t.Fatalf("beads.role not stamped by flatfile init: %v", err)
	}
	if got := strings.TrimSpace(string(role)); got != "maintainer" {
		t.Errorf("beads.role = %q, want default %q", got, "maintainer")
	}

	// bd list output must be warning-free on both streams.
	out, err := runBDInDir(t, bdBin, dir, "list", "--json", "--all")
	if err != nil {
		t.Fatalf("bd list: %v\n%s", err, out)
	}
	if strings.Contains(out, "beads.role not configured") {
		t.Errorf("bd list still warns about beads.role:\n%s", out)
	}
}

// GH#2023 on the flat-file arm: `bd init --from-jsonl` must import the
// committed .beads/issues.jsonl into the fresh store. The Claude-Code-for-Web
// flow (npm package) wipes everything but issues.jsonl between sessions and
// re-inits; the flatfile dispatch used to return before the shared JSONL
// import block, silently ignoring the flag.
func TestInitFlatfileFromJSONL(t *testing.T) {
	bdBin := buildBDForInitTests(t)
	dir := t.TempDir()
	runGitIn(t, dir, "init", "-b", "main")

	if out, err := runBDInDir(t, bdBin, dir, "init", "--prefix", "web", "--quiet", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("session-1 init: %v\n%s", err, out)
	}
	if out, err := runBDInDir(t, bdBin, dir, "create", "carried issue", "--id", "web-1", "-t", "task"); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	jsonl := filepath.Join(dir, ".beads", "issues.jsonl")
	if out, err := runBDInDir(t, bdBin, dir, "export", "-o", jsonl); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	// Wipe everything but the committed JSONL, like a fresh clone.
	entries, err := os.ReadDir(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "issues.jsonl" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, ".beads", e.Name())); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runBDInDir(t, bdBin, dir, "init", "--quiet", "--from-jsonl", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("session-2 init --from-jsonl: %v\n%s", err, out)
	}
	out, err := runBDInDir(t, bdBin, dir, "list", "--json", "--all")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "web-1") {
		t.Errorf("issue web-1 not re-imported from JSONL:\n%s", out)
	}
}
