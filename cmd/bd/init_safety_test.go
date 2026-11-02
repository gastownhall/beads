package main

// Init-safety guard matrix + end-to-end subprocess tests.
//
// The table-driven tests here enforce the invariant from
// engdocs/adr/0002-init-safety-invariants.md. Adding a new flag that can
// interact with remote history is a signal to extend this matrix — if the
// table doesn't exhaustively cover (dataSource × flagSet) → outcome, the
// ADR's structural lock has a gap.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// hermeticInitEnv sets sync.remote via BD_SYNC_REMOTE (viper AutomaticEnv,
// config.go:165-169), stripped of inherited BD_*/BEADS_* vars and
// HOME-isolated so ambient ~/.beads or ~/.config/bd config can't leak in.
//
// BD_DISABLE_METRICS and BD_DISABLE_EVENT_FLUSH are re-added after the strip:
// they are not ambient user config, they are fixed harness guards this test
// package owns (test_repo_beads_guard_test.go:139-140), and dropping them
// re-arms the detached-flusher race that test_repo_beads_guard_test.go:118-135
// documents — the subprocess's homeDir is a t.TempDir() that gets RemoveAll'd
// while the orphaned `bd send-metrics` child still holds files open under it.
func hermeticInitEnv(homeDir string, extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BD_") || strings.HasPrefix(e, "BEADS_") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+homeDir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"BD_DISABLE_METRICS=1", "BD_DISABLE_EVENT_FLUSH=1")
	return append(env, extra...)
}

// TestInitReinitLocalConfiguredRemoteWithoutDoltDataSucceeds is the
// caller-level regression test for GH#4861: sync.remote (via BD_SYNC_REMOTE,
// not --remote, to hit initSyncRemoteConfigured) has no refs/dolt/data, so
// `bd init --reinit-local` must succeed rather than refuse. Supersedes
// TestCheckRemoteSafety_ConfiguredRemoteWithoutDoltData, which called the
// unmodified CheckRemoteSafety directly and tested nothing about this fix
// (both its cases are already in TestCheckRemoteSafety_GuardMatrix).
func TestInitReinitLocalConfiguredRemoteWithoutDoltDataSucceeds(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)
	// bareDir has ordinary git plumbing but no refs/dolt/data (GH#4861 shape).

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")

	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

	cmd := exec.Command(bdBin, "init", "--reinit-local", "--prefix", "cfg", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir, "BD_SYNC_REMOTE="+bareDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd init --reinit-local with a Dolt-data-free sync.remote failed: %v\nstderr:\n%s", err, stderr.String())
	}

	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); err != nil {
		t.Fatalf(".beads should have been created by a successful init: %v", err)
	}
}

// TestInitFreshWithUnreachableGitOriginSucceeds pins the plain-init arm of
// the tri-state probe: a git origin the probe cannot reach at all (exit 128 —
// the credential-less private-remote shape every CI container has) resolves
// to UNKNOWN, which fails closed for the refusal gate but must NOT convert a
// plain `bd init` into a bootstrap clone against that same unreachable
// origin. There is no local history to protect here, and the clone can only
// fail the same way the probe did, so init falls back to a fresh local
// database — the behavior this path always had before the probe existed.
// Regression: #5136 broke TestE2E_InitDoltMetadataRoundtrip (Main-lane only,
// so its PR ran green) exactly this way.
func TestInitFreshWithUnreachableGitOriginSucceeds(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	// An origin whose probe errors (missing path -> git ls-remote exit 128),
	// not one that answers "no refs/dolt/data".
	runGitForBootstrapTest(t, workDir, "remote", "add", "origin",
		filepath.Join(t.TempDir(), "does-not-exist.git"))

	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

	cmd := exec.Command(bdBin, "init", "--backend", "dolt", "--prefix", "org", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		lower := strings.ToLower(out.String())
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", out.String())
		}
		t.Fatalf("bd init with an unreachable git origin failed: %v\noutput:\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(workDir, ".beads")); err != nil {
		t.Fatalf(".beads should have been created by a successful init: %v", err)
	}

	if _, err := os.Stat(filepath.Join(homeDir, ".beads", "eventsData")); !os.IsNotExist(err) {
		t.Errorf("metrics queue dir was created under the isolated HOME (err=%v); "+
			"the detached send-metrics child will race t.TempDir cleanup "+
			"(see test_repo_beads_guard_test.go:118)", err)
	}
}

// TestInitReinitLocalConfiguredRemoteWithDoltDataRefuses is the sibling case:
// sync.remote (again via BD_SYNC_REMOTE, not --remote) points at a bare repo
// that DOES have refs/dolt/data pushed to it. `bd init --reinit-local` must
// still refuse with ExitRemoteDivergenceRefused (10) — this is the ADR-0002
// invariant the GH#4861 fix must not weaken while closing the false positive.
func TestInitReinitLocalConfiguredRemoteWithDoltDataRefuses(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	homeDir := t.TempDir() // see collision note in the sibling test above

	cmd := exec.Command(bdBin, "init", "--reinit-local", "--prefix", "cfg", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir, "BD_SYNC_REMOTE="+bareDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("bd init --reinit-local succeeded; expected refusal (sync.remote has Dolt history). stderr:\n%s", stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
	}
	// A probe that READ the remote keeps the verified refusal: GH#7665's
	// unverified wording is only for a probe that failed.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).") {
		t.Errorf("verified refusal text changed:\n%s", stderrStr)
	}
	for _, banned := range []string{unverifiedRemoteRefusalHeadline, "could not verify refs/dolt/data"} {
		if strings.Contains(stderrStr, banned) {
			t.Errorf("a remote that was read must not be reported as unverified (%q):\n%s", banned, stderrStr)
		}
	}

	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Errorf("refusal should not have created %s", beadsDir)
	}
}

// unverifiedRemoteRefusalHeadline is the first line of the GH#7665 refusal.
const unverifiedRemoteRefusalHeadline = "bd init refuses: could not verify whether remote 'origin' has Dolt history (refs/dolt/data)."

// runInitExpectingUnverifiedRefusal runs bd init and asserts the GH#7665
// contract: exit 10 (the fail-closed refusal is kept), the unverified-remote
// refusal, and never the verified text claiming history nobody could read.
// It returns stderr.
func runInitExpectingUnverifiedRefusal(t *testing.T, bdBin, workDir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bdBin, args...)
	cmd.Dir = workDir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stderr.String()
	if err == nil {
		t.Fatalf("bd %v succeeded; expected the unverified-remote refusal. stderr:\n%s", args, out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, out)
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)\nstderr:\n%s", code, ExitRemoteDivergenceRefused, out)
	}
	if !strings.Contains(out, unverifiedRemoteRefusalHeadline) {
		t.Errorf("refusal does not say the remote could not be verified:\n%s", out)
	}
	if strings.Contains(out, "already has Dolt history") {
		t.Errorf("refusal claims the unreadable remote already has Dolt history (GH#7665):\n%s", out)
	}
	if !strings.Contains(out, "could not verify refs/dolt/data on") {
		t.Errorf("the probe warning naming the URL and git error is missing:\n%s", out)
	}
	return out
}

// TestInitOverrideWithUnreadableRemoteRefusesAsUnverified is the GH#7665
// caller-level regression: a remote whose refs/dolt/data probe fails (a
// missing path makes git ls-remote exit 128, the same shape as a private
// repository with no credentials, with no network) must still refuse a
// local-source override with exit 10, but must say the remote could not be
// verified instead of asserting it has Dolt history. Each subtest reaches a
// different early probe site; all refuse before any filesystem side effect.
func TestInitOverrideWithUnreadableRemoteRefusesAsUnverified(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	newWorkDir := func(t *testing.T, origin string) string {
		t.Helper()
		workDir := t.TempDir()
		runGitForBootstrapTest(t, workDir, "init", "-b", "main")
		runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
		if origin != "" {
			runGitForBootstrapTest(t, workDir, "remote", "add", "origin", origin)
		}
		return workDir
	}
	writeJSONL := func(t *testing.T, workDir string) {
		t.Helper()
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"jl-abc123","title":"Local JSONL","type":"task","status":"open","priority":2}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missing := func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "does-not-exist.git")
	}
	common := []string{"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}

	// Git-origin discovery: the reported gc rig add shape.
	for _, flag := range []string{"--reinit-local", "--force"} {
		t.Run("git-origin"+flag, func(t *testing.T) {
			workDir := newWorkDir(t, missing(t))
			args := append([]string{"init", flag, "--prefix", "uv"}, common...)
			runInitExpectingUnverifiedRefusal(t, bdBin, workDir, hermeticInitEnv(t.TempDir()), args...)
			if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
				t.Errorf("refusal should not have created %s", filepath.Join(workDir, ".beads"))
			}
		})
	}

	// Configured sync.remote (BD_SYNC_REMOTE, not --remote).
	t.Run("configured-sync-remote/reinit-local", func(t *testing.T) {
		workDir := newWorkDir(t, "")
		args := append([]string{"init", "--reinit-local", "--prefix", "uv"}, common...)
		runInitExpectingUnverifiedRefusal(t, bdBin, workDir, hermeticInitEnv(t.TempDir(), "BD_SYNC_REMOTE="+missing(t)), args...)
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Errorf("refusal should not have created %s", filepath.Join(workDir, ".beads"))
		}
	})

	// --from-jsonl with each remote source: refuses before any import.
	for _, tc := range []struct {
		name   string
		origin bool
		env    func(t *testing.T) []string
		extra  func(t *testing.T) []string
	}{
		{"from-jsonl/git-origin", true, nil, nil},
		{"from-jsonl/configured-sync-remote", false, func(t *testing.T) []string { return []string{"BD_SYNC_REMOTE=" + missing(t)} }, nil},
		{"from-jsonl/explicit-remote", false, nil, func(t *testing.T) []string { return []string{"--remote", missing(t)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := ""
			if tc.origin {
				origin = missing(t)
			}
			workDir := newWorkDir(t, origin)
			writeJSONL(t, workDir)
			var extraEnv, extraArgs []string
			if tc.env != nil {
				extraEnv = tc.env(t)
			}
			if tc.extra != nil {
				extraArgs = tc.extra(t)
			}
			args := append(append([]string{"init", "--from-jsonl", "--prefix", "jl"}, extraArgs...), common...)
			runInitExpectingUnverifiedRefusal(t, bdBin, workDir, hermeticInitEnv(t.TempDir(), extraEnv...), args...)
			for _, name := range []string{"embeddeddolt", "dolt", "metadata.json"} {
				if _, err := os.Stat(filepath.Join(workDir, ".beads", name)); err == nil {
					t.Errorf("refusal should not have created .beads/%s", name)
				}
			}
		})
	}
}

// TestInitOverrideLateUnreadableRemoteRefusesAsUnverified covers the two
// LATE probe sites (configured sync.remote and git-origin discovery after
// beadsDir is known). They are reached only when the early probe answered,
// so a git shim answers the first `ls-remote` from a real empty bare repo
// (no refs/dolt/data) and fails every later one, the shape of a remote that
// becomes unreadable between the two checks. No network is used.
func TestInitOverrideLateUnreadableRemoteRefusesAsUnverified(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("the git shim is a POSIX shell script")
	}
	bdBin := buildBDForInitTests(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found: %v", err)
	}

	newShim := func(t *testing.T) string {
		t.Helper()
		shimDir := t.TempDir()
		marker := filepath.Join(t.TempDir(), "first-ls-remote-done")
		script := "#!/bin/sh\n" +
			"if [ \"$1\" = ls-remote ]; then\n" +
			"  if [ -e '" + marker + "' ]; then\n" +
			"    echo 'fatal: shim: remote unreadable on the late probe' >&2\n" +
			"    exit 128\n" +
			"  fi\n" +
			"  : > '" + marker + "'\n" +
			"fi\n" +
			"exec '" + realGit + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil { // #nosec G306 -- test shim must be executable
			t.Fatal(err)
		}
		return shimDir
	}
	common := []string{"--reinit-local", "--prefix", "uv", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}

	for _, tc := range []struct {
		name       string
		configured bool
	}{
		{"git-origin", false},
		{"configured-sync-remote", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bareDir := filepath.Join(t.TempDir(), "empty.git")
			runGitForBootstrapTest(t, "", "init", "--bare", bareDir)
			workDir := t.TempDir()
			runGitForBootstrapTest(t, workDir, "init", "-b", "main")
			runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
			extra := []string{"PATH=" + newShim(t) + string(os.PathListSeparator) + os.Getenv("PATH")}
			if tc.configured {
				extra = append(extra, "BD_SYNC_REMOTE="+bareDir)
			} else {
				runGitForBootstrapTest(t, workDir, "remote", "add", "origin", bareDir)
			}
			runInitExpectingUnverifiedRefusal(t, bdBin, workDir, hermeticInitEnv(t.TempDir(), extra...), append([]string{"init"}, common...)...)
		})
	}
}

// TestInitReinitLocalUnreadableRemoteEmptyRemoteSucceeds pins the opt-out the
// GH#7665 refusal offers: adding --remote="" to the refused command
// initializes without a remote, so the unreadable origin (and an ambient
// sync.remote naming an equally unreadable one) is neither probed nor
// configured. This covers a fresh workspace only: --remote="" does not erase
// a sync.remote or Dolt remote that already exists.
func TestInitReinitLocalUnreadableRemoteEmptyRemoteSucceeds(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	runGitForBootstrapTest(t, workDir, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist.git"))
	homeDir := t.TempDir()
	ambientRemote := filepath.Join(t.TempDir(), "also-missing.git")

	cmd := exec.Command(bdBin, "init", "--non-interactive", "--reinit-local", "--remote=", "--prefix", "er", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir, "BD_SYNC_REMOTE="+ambientRemote)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd init --reinit-local --remote=\"\" against an unreadable origin failed: %v\noutput:\n%s", err, out.String())
	}
	for _, banned := range []string{"could not verify refs/dolt/data", "bd init refuses", "Configured Dolt remote"} {
		if strings.Contains(out.String(), banned) {
			t.Errorf("an explicitly empty --remote must not probe or wire a remote, got %q:\n%s", banned, out.String())
		}
	}

	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); err != nil {
		t.Fatalf(".beads should have been created by a successful init: %v", err)
	}
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != "" {
		t.Errorf("sync.remote persisted as %q; an explicitly empty --remote must not persist one", got)
	}

	// Config alone is not proof: ask the store which Dolt remotes it has.
	list := exec.Command(bdBin, "dolt", "remote", "list", "--json")
	list.Dir = workDir
	list.Env = hermeticInitEnv(homeDir)
	var listOut, listErr bytes.Buffer
	list.Stdout = &listOut
	list.Stderr = &listErr
	if err := list.Run(); err != nil {
		t.Fatalf("bd dolt remote list --json: %v\nstdout:\n%s\nstderr:\n%s", err, listOut.String(), listErr.String())
	}
	var remotes []map[string]any
	if err := json.Unmarshal(listOut.Bytes(), &remotes); err != nil {
		t.Fatalf("parse bd dolt remote list --json: %v\n%s", err, listOut.String())
	}
	if len(remotes) != 0 {
		t.Errorf("the store has Dolt remotes %v; an explicitly empty --remote must configure none", remotes)
	}
}

// TestInitOverrideNonGitDoltRemoteOffersWorkingBootstrap pins the GH#7665
// refusal's remedy for a Dolt remote that is not a git repository. git
// ls-remote can never read one, so --reinit-local / --force always get the
// unverified refusal, and restoring access can never make the check pass.
// The refusal must name bd bootstrap, and bd bootstrap must then adopt the
// remote. The remote is a populated file:// remote bd pushed to; the
// workspace was cloned from it with bd init --remote and then lost its local
// database.
func TestInitOverrideNonGitDoltRemoteOffersWorkingBootstrap(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("the file:// remote URL is built from a POSIX path")
	}
	bdBin := buildBDForInitTests(t)
	env := hermeticInitEnv(t.TempDir())
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bdBin, args...)
		cmd.Dir = dir
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("bd %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	newGitDir := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		runGitForBootstrapTest(t, dir, "init", "-b", "main")
		runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")
		return dir
	}
	common := []string{"--non-interactive", "--skip-hooks", "--skip-agents"}

	remoteURL := "file://" + t.TempDir()
	srcDir := newGitDir(t)
	run(t, srcDir, append([]string{"init", "--prefix", "nd", "--quiet"}, common...)...)
	issueID := strings.TrimSpace(run(t, srcDir, "create", "Remote issue", "--type", "task", "--silent"))
	if !strings.HasPrefix(issueID, "nd-") {
		t.Fatalf("bd create --silent printed %q, want an nd- issue ID", issueID)
	}
	run(t, srcDir, "dolt", "remote", "add", "origin", remoteURL)
	run(t, srcDir, "dolt", "push")

	workDir := newGitDir(t)
	run(t, workDir, append([]string{"init", "--remote", remoteURL, "--quiet"}, common...)...)
	beadsDir := filepath.Join(workDir, ".beads")
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != remoteURL {
		t.Fatalf("sync.remote = %q after bd init --remote, want %q", got, remoteURL)
	}
	dbDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.RemoveAll(dbDir); err != nil {
		t.Fatal(err)
	}

	for _, flag := range []string{"--reinit-local", "--force"} {
		out := runInitExpectingUnverifiedRefusal(t, bdBin, workDir, env, append([]string{"init", flag}, common...)...)
		if !strings.Contains(out, "bd bootstrap") {
			t.Errorf("bd init %s refusal for a non-git Dolt remote does not name bd bootstrap:\n%s", flag, out)
		}
		if _, err := os.Stat(dbDir); err == nil {
			t.Fatalf("bd init %s refusal created %s", flag, dbDir)
		}
	}

	run(t, workDir, "bootstrap", "--non-interactive")
	if out := run(t, workDir, "list", "--json"); !strings.Contains(out, `"`+issueID+`"`) {
		t.Errorf("bd bootstrap did not adopt the remote: %s missing from bd list --json:\n%s", issueID, out)
	}
}

func TestCheckRemoteSafety_GuardMatrix(t *testing.T) {
	cases := []struct {
		name     string
		in       RemoteSafetyInput
		want     RemoteSafetyAction
		wantExit int // 0 when not a refusal
	}{
		// No-remote-data column: nothing to refuse, regardless of flags.
		{"fresh/no-flags", RemoteSafetyInput{}, ActionNoRemoteData, 0},
		{"fresh/force", RemoteSafetyInput{Force: true}, ActionNoRemoteData, 0},
		{"fresh/reinit-local", RemoteSafetyInput{ReinitLocal: true}, ActionNoRemoteData, 0},
		{"fresh/from-jsonl", RemoteSafetyInput{FromJSONL: true}, ActionNoRemoteData, 0},
		{"fresh/discard-remote", RemoteSafetyInput{DiscardRemote: true}, ActionNoRemoteData, 0},
		{"fresh/force+discard", RemoteSafetyInput{Force: true, DiscardRemote: true}, ActionNoRemoteData, 0},

		// Remote-has-data column, user did NOT force: bootstrap is the
		// adoption path. This is the existing correct behavior.
		{"remote/no-flags", RemoteSafetyInput{RemoteHasDoltData: true}, ActionBootstrap, 0},

		// Remote-has-data column, user forced WITHOUT discard-remote: the
		// bd-q83 bug. Must refuse with ExitRemoteDivergenceRefused.
		{
			"remote/force",
			RemoteSafetyInput{RemoteHasDoltData: true, Force: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused,
		},
		{
			"remote/reinit-local",
			RemoteSafetyInput{RemoteHasDoltData: true, ReinitLocal: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused,
		},
		{
			"remote/force+reinit-local",
			RemoteSafetyInput{RemoteHasDoltData: true, Force: true, ReinitLocal: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused,
		},
		{
			"remote/from-jsonl",
			RemoteSafetyInput{RemoteHasDoltData: true, FromJSONL: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused,
		},

		// Remote-has-data, user forced WITH discard-remote, non-interactive,
		// no token: requires destroy-token.
		{
			"remote/force+discard/non-interactive/no-token",
			RemoteSafetyInput{RemoteHasDoltData: true, Force: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},
		{
			"remote/from-jsonl+discard/non-interactive/no-token",
			RemoteSafetyInput{RemoteHasDoltData: true, FromJSONL: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},

		// Remote-has-data, user forced WITH discard-remote, non-interactive,
		// WRONG token: requires destroy-token.
		{
			"remote/force+discard/non-interactive/wrong-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, Force: true, DiscardRemote: true,
				DestroyToken: "DESTROY-wrong", ExpectedToken: "DESTROY-bd",
			},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},

		// Remote-has-data, user forced WITH discard-remote, non-interactive,
		// MATCHING token: authorized divergence.
		{
			"remote/force+discard/non-interactive/matching-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, Force: true, DiscardRemote: true,
				DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd",
			},
			ActionProceedWithDivergence, 0,
		},
		{
			"remote/from-jsonl+discard/non-interactive/matching-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, FromJSONL: true, DiscardRemote: true,
				DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd",
			},
			ActionProceedWithDivergence, 0,
		},

		// Remote-has-data, user forced WITH discard-remote, INTERACTIVE:
		// no token needed at decision time (caller will prompt).
		{
			"remote/force+discard/interactive",
			RemoteSafetyInput{
				RemoteHasDoltData: true, Force: true, DiscardRemote: true,
				IsInteractive: true,
			},
			ActionProceedWithDivergence, 0,
		},

		// Remote-has-data, DISCARD without FORCE: also authorized since
		// the user named the intent explicitly. This is a small
		// ergonomics win — a user who knows about --discard-remote
		// doesn't also have to type --reinit-local. The local-safety
		// guard still fires separately (checkExistingBeadsData) if there's
		// local data; that's a separate column not in this matrix.
		{
			"remote/discard-remote-only/interactive",
			RemoteSafetyInput{RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true},
			ActionProceedWithDivergence, 0,
		},

		// INTERACTIVE with a supplied-but-WRONG token (#6480): the token must
		// still be validated. The caller's typed-confirmation prompt only
		// fires when NO token was supplied, so skipping validation here would
		// discard the remote with no confirmation of any kind.
		{
			"remote/discard-remote/interactive/wrong-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd",
			},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},
		// INTERACTIVE with a token but no expected token to compare against:
		// nothing can validate it, so refuse rather than fall to the prompt.
		// DEFENSIVE: unreachable from current callers — all three call sites
		// pass ExpectedToken: FormatDestroyToken(prefix), which is never
		// empty. This pins the fail-closed arm of a pure function; it is not
		// evidence about any live path (only wrong-token above is).
		{
			"remote/discard-remote/interactive/token-without-expected",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "WRONG",
			},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},
		// INTERACTIVE with a MATCHING token: the token is the authorization.
		{
			"remote/discard-remote/interactive/matching-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd",
			},
			ActionProceedWithDivergence, 0,
		},
		// INTERACTIVE with NO token supplied (expected token known): unchanged
		// — the decision defers to the TTY typed-confirmation prompt.
		{
			"remote/discard-remote/interactive/no-token-with-expected",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				ExpectedToken: "DESTROY-bd",
			},
			ActionProceedWithDivergence, 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckRemoteSafety(tc.in)
			if got.Action != tc.want {
				t.Errorf("Action = %v (reason=%q), want %v", got.Action, got.Reason, tc.want)
			}
			if tc.wantExit != 0 && got.ExitCode != tc.wantExit {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, tc.wantExit)
			}
			// Refusal actions must populate UserMessage (per ADR text contract).
			if (tc.want == ActionRefuseDivergence || tc.want == ActionRequireDestroyToken) && got.UserMessage == "" {
				t.Errorf("refusal action %v returned empty UserMessage", tc.want)
			}
		})
	}
}

// TestCheckRemoteSafety_UnverifiedRemoteMatrix pins GH#7665: a failed
// refs/dolt/data probe is UNKNOWN, which fails closed exactly like a remote
// known to have Dolt history. Every remote-has-data row of the guard matrix
// is replayed with the remote unverified — both with RemoteHasDoltData still
// set (what the fail-closed probe resolver passes today) and with ONLY the
// unverified field set, so the uncertainty alone can never open the guard.
// Action and ExitCode must match the verified row. Only the override-without-
// discard refusal changes: it gets its own Reason and message, because the
// verified text asserts history nobody could read.
func TestCheckRemoteSafety_UnverifiedRemoteMatrix(t *testing.T) {
	rows := []struct {
		name string
		in   RemoteSafetyInput
	}{
		{"no-flags", RemoteSafetyInput{}},
		{"force", RemoteSafetyInput{Force: true}},
		{"reinit-local", RemoteSafetyInput{ReinitLocal: true}},
		{"force+reinit-local", RemoteSafetyInput{Force: true, ReinitLocal: true}},
		{"from-jsonl", RemoteSafetyInput{FromJSONL: true}},
		{"force+discard/non-interactive/no-token", RemoteSafetyInput{Force: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"}},
		{"discard-only/non-interactive/no-token", RemoteSafetyInput{DiscardRemote: true, ExpectedToken: "DESTROY-bd"}},
		{"from-jsonl+discard/non-interactive/no-token", RemoteSafetyInput{FromJSONL: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"}},
		{"force+discard/non-interactive/wrong-token", RemoteSafetyInput{Force: true, DiscardRemote: true, DestroyToken: "DESTROY-wrong", ExpectedToken: "DESTROY-bd"}},
		{"force+discard/non-interactive/matching-token", RemoteSafetyInput{Force: true, DiscardRemote: true, DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd"}},
		{"force+discard/interactive", RemoteSafetyInput{Force: true, DiscardRemote: true, IsInteractive: true}},
		{"discard-only/interactive", RemoteSafetyInput{DiscardRemote: true, IsInteractive: true}},
		{"discard-only/interactive/wrong-token", RemoteSafetyInput{DiscardRemote: true, IsInteractive: true, DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd"}},
		{"discard-only/interactive/matching-token", RemoteSafetyInput{DiscardRemote: true, IsInteractive: true, DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd"}},
		{"discard-only/interactive/no-token-with-expected", RemoteSafetyInput{DiscardRemote: true, IsInteractive: true, ExpectedToken: "DESTROY-bd"}},
	}

	for _, row := range rows {
		known := row.in
		known.RemoteHasDoltData = true
		want := CheckRemoteSafety(known)

		for _, variant := range []struct {
			name    string
			hasData bool
		}{
			{"unverified+has-data", true},
			{"unverified-only", false},
		} {
			t.Run(row.name+"/"+variant.name, func(t *testing.T) {
				in := row.in
				in.RemoteHasDoltData = variant.hasData
				in.RemoteDoltDataUnverified = true
				got := CheckRemoteSafety(in)

				if got.Action != want.Action {
					t.Errorf("Action = %v (reason=%q), want %v as for the verified row (reason=%q)", got.Action, got.Reason, want.Action, want.Reason)
				}
				if got.ExitCode != want.ExitCode {
					t.Errorf("ExitCode = %d, want %d as for the verified row", got.ExitCode, want.ExitCode)
				}
				if got.Action == ActionRefuseDivergence {
					if got.Reason != "unverified-remote-without-discard-remote" {
						t.Errorf("Reason = %q, want %q", got.Reason, "unverified-remote-without-discard-remote")
					}
					if got.UserMessage != refusalMessageUnverifiedRemote() {
						t.Errorf("UserMessage =\n%s\n\nwant the unverified-remote refusal:\n%s", got.UserMessage, refusalMessageUnverifiedRemote())
					}
					return
				}
				if got.Reason != want.Reason || got.UserMessage != want.UserMessage {
					t.Errorf("Reason/UserMessage = %q/%q, want the verified row's %q/%q", got.Reason, got.UserMessage, want.Reason, want.UserMessage)
				}
			})
		}
	}

	// The verified refusal keeps its own Reason and text.
	verified := CheckRemoteSafety(RemoteSafetyInput{RemoteHasDoltData: true, ReinitLocal: true})
	if verified.Reason != "force-without-discard-remote" || verified.UserMessage != refusalMessageDivergence() {
		t.Errorf("verified refusal changed: reason=%q message=\n%s", verified.Reason, verified.UserMessage)
	}
	if refusalMessageUnverifiedRemote() == refusalMessageDivergence() {
		t.Error("the unverified and verified refusals are identical, so the uncertainty is not carried")
	}
}

// TestCheckRemoteSafety_TokenRefusalDistinguishesPresence pins which refusal
// text each exit-12 path gets. The distinction is load-bearing, not cosmetic:
// the missing-token message tells the user to "re-run interactively ... and
// confirm at the prompt", and that remedy only works when NO token was
// supplied, because the caller's typed-confirmation prompt is gated on
// destroyToken == "" (init.go:3139). Handing it to a user who supplied a
// wrong token sends them around the identical refusal forever.
func TestCheckRemoteSafety_TokenRefusalDistinguishesPresence(t *testing.T) {
	cases := []struct {
		name         string
		in           RemoteSafetyInput
		wantReason   string
		wantSupplied bool
	}{
		{
			"non-interactive/no-token",
			RemoteSafetyInput{RemoteHasDoltData: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"},
			"destroy-token-missing", false,
		},
		{
			"non-interactive/wrong-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true,
				DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd",
			},
			"destroy-token-mismatch", true,
		},
		{
			"interactive/wrong-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd",
			},
			"destroy-token-mismatch", true,
		},
		{
			// Defensive arm (see the guard matrix): a token was still
			// supplied, so it gets the mismatch text.
			"interactive/token-without-expected",
			RemoteSafetyInput{
				RemoteHasDoltData: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "WRONG",
			},
			"destroy-token-mismatch", true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckRemoteSafety(tc.in)
			if got.Action != ActionRequireDestroyToken {
				t.Fatalf("Action = %v, want ActionRequireDestroyToken", got.Action)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			wantMsg := refusalMessageTokenMissing()
			if tc.wantSupplied {
				wantMsg = refusalMessageTokenMismatch()
			}
			if got.UserMessage != wantMsg {
				t.Errorf("UserMessage =\n%s\n\nwant\n%s", got.UserMessage, wantMsg)
			}
			// The defect this pins: the "re-run interactively and confirm
			// at the prompt" remedy must never reach a caller who supplied
			// a token, since the prompt cannot fire for them.
			if tc.wantSupplied && got.UserMessage == refusalMessageTokenMissing() {
				t.Errorf("token-supplied refusal got the missing-token text, whose remedy is unreachable here:\n%s", got.UserMessage)
			}
		})
	}

	if refusalMessageTokenMissing() == refusalMessageTokenMismatch() {
		t.Error("the two token refusals are identical, so token presence is not actually carried")
	}
	// The mismatch remedy must name the reachable escape: drop the flag to
	// be prompted. Without this the message is merely vaguer, not correct.
	if !strings.Contains(refusalMessageTokenMismatch(), "drop --destroy-token") {
		t.Errorf("mismatch refusal does not offer the reachable remedy (drop the flag, get prompted):\n%s", refusalMessageTokenMismatch())
	}
}

// TestCheckRemoteSafety_TokenRefusalTextNoEcho extends the ADR error-message
// contract to both destroy-token refusals. TestCheckRemoteSafety_RefusalTextNoEcho
// below covers only the divergence message, whose assertions (bd bootstrap,
// --from-jsonl) are specific to it.
func TestCheckRemoteSafety_TokenRefusalTextNoEcho(t *testing.T) {
	msgs := map[string]string{
		"missing":  refusalMessageTokenMissing(),
		"mismatch": refusalMessageTokenMismatch(),
	}
	bannedEchoes := []string{
		"bd init --force --discard-remote --destroy-token",
		"bd init --reinit-local --discard-remote --destroy-token",
		"DESTROY-",
	}
	for name, msg := range msgs {
		for _, banned := range bannedEchoes {
			if strings.Contains(msg, banned) {
				t.Errorf("%s refusal contains destructive echo %q:\n%s", name, banned, msg)
			}
		}
		if !strings.Contains(msg, "bd help init-safety") {
			t.Errorf("%s refusal does not point to 'bd help init-safety':\n%s", name, msg)
		}
	}
}

// TestHandleRemoteSafetyDecision_RefusalSeam pins the caller half of the
// #6480 composition. CheckRemoteSafety's guard matrix proves the decision;
// this proves the decision is honored at the seam — that a refusal aborts
// with its exit code and never reaches the typed-confirmation prompt, and
// that it leaves the cross-checkpoint *confirmed latch alone. Before this,
// handleRemoteSafetyDecision had zero test references repo-wide.
//
// Not covered here: the token-ABSENT prompt arm (init.go:3139). It is gated
// on term.IsTerminal(os.Stdin), which is false under `go test`, so asserting
// "no prompt" for it would pass no matter what the token gate said — a
// vacuous pin. Reaching it needs a PTY or a TTY-detection seam; both exceed
// this change. The arms below discriminate without one.
func TestHandleRemoteSafetyDecision_RefusalSeam(t *testing.T) {
	cases := []struct {
		name          string
		decision      RemoteSafetyDecision
		wantBootstrap bool
		wantExit      int // 0 = expect nil error
	}{
		{
			"require-destroy-token aborts with 12",
			RemoteSafetyDecision{
				Action: ActionRequireDestroyToken, Reason: "destroy-token-mismatch",
				ExitCode: ExitDestroyTokenMissing, UserMessage: "refusal",
			},
			false, ExitDestroyTokenMissing,
		},
		{
			"refuse-divergence aborts with 10",
			RemoteSafetyDecision{
				Action: ActionRefuseDivergence, Reason: "force-without-discard-remote",
				ExitCode: ExitRemoteDivergenceRefused, UserMessage: "refusal",
			},
			false, ExitRemoteDivergenceRefused,
		},
		{
			"bootstrap adopts the remote",
			RemoteSafetyDecision{Action: ActionBootstrap, Reason: "bootstrap-from-remote"},
			true, 0,
		},
		{
			// A matching token IS the authorization: proceed, no abort.
			"authorized divergence proceeds",
			RemoteSafetyDecision{Action: ActionProceedWithDivergence, Reason: "authorized-divergence"},
			false, 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			confirmed := false
			// destroyToken non-empty keeps the prompt arm shut on its own
			// terms rather than relying on the harness lacking a TTY.
			bootstrap, err := handleRemoteSafetyDecision(
				tc.decision, "bd", "git+https://example.invalid/repo.git",
				"DESTROY-bd", nil, true, &confirmed,
			)
			if bootstrap != tc.wantBootstrap {
				t.Errorf("bootstrap = %v, want %v", bootstrap, tc.wantBootstrap)
			}
			if tc.wantExit == 0 {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			} else {
				code, ok := exitCodeFromError(err)
				if !ok {
					t.Fatalf("err = %v, want an *exitError with code %d", err, tc.wantExit)
				}
				if code != tc.wantExit {
					t.Errorf("exit code = %d, want %d", code, tc.wantExit)
				}
			}
			// A refusal must not silently satisfy a later checkpoint's
			// "already confirmed" short-circuit.
			if confirmed {
				t.Error("confirmed latch was set without a typed confirmation")
			}
		})
	}
}

// TestCheckRemoteSafety_RefusalTextNoEcho enforces the error-message
// contract: runtime refusal text must not echo a complete destructive
// invocation. A copy-pasteable command in the error is the failure class
// that produced the 58f5989bf 247-issue incident.
func TestCheckRemoteSafety_RefusalTextNoEcho(t *testing.T) {
	// The refusal text must point to `bd help init-safety` or
	// `bd bootstrap` (safe tools), not construct a full destructive
	// command. Grep for flag combinations that would form a destructive
	// one-liner.
	in := RemoteSafetyInput{
		RemoteHasDoltData: true, Force: true,
	}
	msg := CheckRemoteSafety(in).UserMessage

	bannedEchoes := []string{
		"bd init --force --discard-remote --destroy-token",
		"bd init --reinit-local --discard-remote --destroy-token",
		"DESTROY-",
	}
	for _, banned := range bannedEchoes {
		if strings.Contains(msg, banned) {
			t.Errorf("refusal text contains destructive one-liner %q:\n%s", banned, msg)
		}
	}

	// It MUST name the safe path.
	if !strings.Contains(msg, "bd bootstrap") {
		t.Errorf("refusal text does not name the safe tool 'bd bootstrap':\n%s", msg)
	}
	// And it must point to the help topic for the destructive path.
	if !strings.Contains(msg, "bd help init-safety") {
		t.Errorf("refusal text does not point to 'bd help init-safety':\n%s", msg)
	}
	if !strings.Contains(msg, "--from-jsonl") {
		t.Errorf("refusal text does not name the JSONL local-source flag:\n%s", msg)
	}
}

// TestCheckRemoteSafety_UnverifiedRefusalText pins the recovery text of the
// GH#7665 refusal. It must explain that the check failed, not that history
// exists, and offer a remedy that works for each kind of remote: restoring
// access for a git remote; bd bootstrap for a Dolt remote that is not a git
// repository, which the git check can never read but bootstrap reads through
// Dolt; or init with no remote via --remote="". The bootstrap offer must
// stay scoped to those remotes: for a git remote it needs the very access
// the probe lacked. The ADR no-echo contract applies as to every refusal.
func TestCheckRemoteSafety_UnverifiedRefusalText(t *testing.T) {
	msg := CheckRemoteSafety(RemoteSafetyInput{RemoteDoltDataUnverified: true, ReinitLocal: true}).UserMessage

	for _, must := range []string{
		"could not verify whether remote 'origin' has Dolt history",
		"the check of the remote failed",
		"cannot rule out existing Dolt history",
		"Restore access to the remote",
		"A Dolt remote that is not a git repository",
		"bd bootstrap",
		`--remote=""`,
		"bd help init-safety",
		"--from-jsonl",
	} {
		if !strings.Contains(msg, must) {
			t.Errorf("unverified refusal missing %q:\n%s", must, msg)
		}
	}
	for _, banned := range []string{
		"already has Dolt history",
		"bd init --force --discard-remote --destroy-token",
		"bd init --reinit-local --discard-remote --destroy-token",
		"--destroy-token=",
		"DESTROY-",
	} {
		if strings.Contains(msg, banned) {
			t.Errorf("unverified refusal contains %q:\n%s", banned, msg)
		}
	}
	// Scoped, not just present: the paragraph that offers bootstrap names
	// the remotes it is for.
	for _, para := range strings.Split(msg, "\n\n") {
		if strings.Contains(para, "bd bootstrap") && !strings.Contains(para, "not a git repository") {
			t.Errorf("unverified refusal offers bd bootstrap without scoping it to Dolt remotes that are not git repositories:\n%s", para)
		}
	}
}

func TestShouldWireInitRemote(t *testing.T) {
	tests := []struct {
		name              string
		syncURL           string
		syncFromRemote    bool
		syncURLFromConfig bool
		syncURLFromGit    bool
		want              bool
	}{
		{
			name:    "no url",
			syncURL: "",
			want:    false,
		},
		{
			name:              "unattributed url is not wired",
			syncURL:           "git+https://github.com/org/plain-source.git",
			syncFromRemote:    false,
			syncURLFromConfig: false,
			want:              false,
		},
		{
			// Keep this true. The URL is the supported pre-first-push wiring:
			// the first `bd dolt push` creates refs/dolt/data on that same git
			// remote, and bootstrap resolves the persisted value by probing
			// for that ref (#5743). Do not "fix" a bootstrap failure by
			// un-persisting here — that leaves every already-committed
			// .beads/config.yaml in the wild broken.
			name:           "plain git origin without dolt data",
			syncURL:        "git+https://github.com/org/plain-source.git",
			syncURLFromGit: true,
			want:           true,
		},
		{
			name:              "git origin with refs/dolt/data",
			syncURL:           "git+https://github.com/org/beads-data.git",
			syncFromRemote:    true,
			syncURLFromConfig: false,
			want:              true,
		},
		{
			name:              "explicit sync.remote",
			syncURL:           "http://myserver:7007/mydb",
			syncFromRemote:    false,
			syncURLFromConfig: true,
			want:              true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldWireInitRemote(tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGit)
			if got != tt.want {
				t.Errorf("shouldWireInitRemote(%q, %v, %v, %v) = %v, want %v",
					tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGit, got, tt.want)
			}
		})
	}
}

func TestShouldConfigureInitDoltRemoteHonorsLocalOnly(t *testing.T) {
	tests := []struct {
		name              string
		syncURL           string
		syncFromRemote    bool
		syncURLFromConfig bool
		syncURLFromGit    bool
		localOnly         bool
		wantConfigure     bool
		wantWire          bool
	}{
		{
			name:           "git origin configures by default",
			syncURL:        "git+https://github.com/org/project.git",
			syncURLFromGit: true,
			wantConfigure:  true,
			wantWire:       true,
		},
		{
			name:           "local only suppresses init remote wiring without changing predicate",
			syncURL:        "git+https://github.com/org/project.git",
			syncURLFromGit: true,
			localOnly:      true,
			wantConfigure:  false,
			wantWire:       true,
		},
		{
			name:          "no url remains false",
			localOnly:     true,
			wantConfigure: false,
			wantWire:      false,
		},
		{
			name:              "explicit sync remote configures when local only is false",
			syncURL:           "https://dolt.example.invalid/repo",
			syncURLFromConfig: true,
			wantConfigure:     true,
			wantWire:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotConfigure := shouldConfigureInitDoltRemote(tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGit, tt.localOnly)
			if gotConfigure != tt.wantConfigure {
				t.Errorf("shouldConfigureInitDoltRemote(..., localOnly=%v) = %v, want %v", tt.localOnly, gotConfigure, tt.wantConfigure)
			}
			gotWire := shouldWireInitRemote(tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGit)
			if gotWire != tt.wantWire {
				t.Errorf("shouldWireInitRemote(...) = %v, want %v", gotWire, tt.wantWire)
			}
		})
	}
}

func TestLocalOnlyInitSkipsConfigureButPersistsExplicitRemote(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("dolt.local-only: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	remote := "git+ssh://git@example.com/org/project.git"
	if shouldConfigureInitDoltRemote(remote, false, true, false, true) {
		t.Fatal("local-only init should not configure a Dolt remote")
	}

	if err := persistInitSyncRemote(beadsDir, remote, remote, false, true, false); err != nil {
		t.Fatalf("persistInitSyncRemote failed: %v", err)
	}
	got := config.GetStringFromDir(beadsDir, "sync.remote")
	if got != remote {
		data, _ := os.ReadFile(configPath)
		t.Fatalf("sync.remote was not persisted under local-only config: got %q, want %q\nfile:\n%s",
			got, remote, data)
	}
}

// TestIsDoltLocalOnly covers the config-reading helper that
// TestLocalOnlyInitSkipsConfigureButPersistsExplicitRemote bypasses by
// passing localOnly directly. dolt.local-only maps to BD_DOLT_LOCAL_ONLY
// through the config env-key replacer (".","-" → "_").
func TestIsDoltLocalOnly(t *testing.T) {
	// Cannot be parallel: mutates the global env + config singleton.
	tests := []struct {
		name   string
		envVal string
		setEnv bool
		want   bool
	}{
		{name: "default off when unset", setEnv: false, want: false},
		{name: "explicit true", envVal: "true", setEnv: true, want: true},
		{name: "explicit false", envVal: "false", setEnv: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("BD_DOLT_LOCAL_ONLY", tt.envVal)
			} else {
				os.Unsetenv("BD_DOLT_LOCAL_ONLY")
				t.Cleanup(func() { os.Unsetenv("BD_DOLT_LOCAL_ONLY") })
			}

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			if got := isDoltLocalOnly(); got != tt.want {
				t.Errorf("isDoltLocalOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFormatDestroyToken asserts the token format contract that
// bd help init-safety documents. If this format changes, help text
// and the ADR must update together.
func TestFormatDestroyToken(t *testing.T) {
	got := FormatDestroyToken("bd")
	want := "DESTROY-bd"
	if got != want {
		t.Errorf("FormatDestroyToken(\"bd\") = %q, want %q", got, want)
	}
}

// TestInitForceRefusesWhenRemoteHasDoltData is the end-to-end subprocess
// regression test for bd-q83. It creates a bare repo with a synthetic
// `refs/dolt/data` ref, then runs `bd init --force` in a clone of that
// repo. Expected: non-zero exit, refusal message points to `bd bootstrap`,
// no `.beads/` directory created.
//
// Per the test-engineer position: the fixture uses a synthetic git ref
// (no real Dolt push needed). `gitOriginHasDoltDataRef` only checks that the
// ref exists, not its content.
func TestInitForceRefusesWhenRemoteHasDoltData(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, cloneDir, "config", "core.hooksPath", ".git/hooks")

	cmd := exec.Command(bdBin, "init", "--force", "--prefix", "bd", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = cloneDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	// Expected: non-zero exit.
	if err == nil {
		t.Fatalf("bd init --force succeeded; expected refusal. stderr:\n%s", stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
	}

	// Refusal text must name the safe path and keep the verified already-has
	// diagnosis: the unverified could-not-verify wording (GH#7665) must never
	// replace it for a remote that was actually read.
	stderrStr := stderr.String()
	for _, must := range []string{"bd bootstrap", "bd help init-safety", "remote 'origin'", "already has Dolt history"} {
		if !strings.Contains(stderrStr, must) {
			t.Errorf("refusal missing %q:\n%s", must, stderrStr)
		}
	}
	// Refusal text must NOT echo a destructive one-liner.
	for _, banned := range []string{"--destroy-token=DESTROY-", "--force --discard-remote --destroy-token"} {
		if strings.Contains(stderrStr, banned) {
			t.Errorf("refusal echoes banned one-liner %q:\n%s", banned, stderrStr)
		}
	}

	// No .beads/ should have been created.
	beadsDir := filepath.Join(cloneDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Errorf("refusal should not have created %s", beadsDir)
	}
}

// TestInitFromJSONLRefusesWhenRemoteHasDoltData is the GH#3427 regression
// guard for the safe default: --from-jsonl must not be silently ignored and
// must not start a remote clone when origin advertises refs/dolt/data.
func TestInitFromJSONLRefusesWhenRemoteHasDoltData(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, cloneDir, "config", "core.hooksPath", ".git/hooks")

	beadsDir := filepath.Join(cloneDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"jl-abc123","title":"Local JSONL wins","type":"task","status":"open","priority":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bdBin, "init", "--from-jsonl", "--prefix", "jl", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = cloneDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatalf("bd init --from-jsonl succeeded; expected remote-divergence refusal. stderr:\n%s", stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
	}

	stderrStr := stderr.String()
	for _, must := range []string{"bd bootstrap", "bd help init-safety", "--from-jsonl", "remote 'origin'"} {
		if !strings.Contains(stderrStr, must) {
			t.Errorf("refusal missing %q:\n%s", must, stderrStr)
		}
	}
	if strings.Contains(stderrStr, "failed to clone remote") {
		t.Errorf("--from-jsonl should refuse before attempting remote clone:\n%s", stderrStr)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); err == nil {
		t.Errorf("refusal should not have created an embedded Dolt database")
	}
}

func TestInitFromJSONLExplicitRemoteRefusesWhenRemoteHasDoltData(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

	sourceDir := t.TempDir()
	runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
	runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
	runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

	cloneDir := t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "config", "core.hooksPath", ".git/hooks")

	beadsDir := filepath.Join(cloneDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"jl-abc123","title":"Local JSONL wins","type":"task","status":"open","priority":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bdBin, "init", "--from-jsonl", "--remote", bareDir, "--prefix", "jl", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = cloneDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatalf("bd init --from-jsonl --remote succeeded; expected remote-divergence refusal. stderr:\n%s", stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
	}

	stderrStr := stderr.String()
	for _, must := range []string{"bd bootstrap", "bd help init-safety", "--from-jsonl", "remote 'origin'"} {
		if !strings.Contains(stderrStr, must) {
			t.Errorf("refusal missing %q:\n%s", must, stderrStr)
		}
	}
	if strings.Contains(stderrStr, "failed to clone remote") {
		t.Errorf("--from-jsonl should refuse before attempting remote clone:\n%s", stderrStr)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); err == nil {
		t.Errorf("refusal should not have created an embedded Dolt database")
	}
}

// TestInitForceDiscardRemoteProceedsWithToken is the positive counterpart:
// when the caller explicitly passes --discard-remote with a valid
// destroy-token, init proceeds. This is slower (real Dolt init) so we
// bound its scope to verify "refusal clears" — not full bootstrap
// correctness, which is covered elsewhere.
//
// Skipped if dolt binary isn't available; remote-divergence refusal is
// the primary regression concern.
func TestInitForceDiscardRemoteProceedsWithToken(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt binary not available; skipping end-to-end discard-remote path")
	}
	// The pure-function matrix above covers the decision; this test just
	// confirms the CLI wires the happy path through. Keep minimal to
	// avoid flaky dolt interactions in CI.
	t.Skip("covered by matrix; full end-to-end deferred to a follow-up when the full init path is simplified")
}

// Helpers used above: buildBDForInitTests, runGitForBootstrapTest — both
// defined elsewhere in the test package (init_test.go and bootstrap_test.go).
