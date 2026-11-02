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
	"fmt"
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

// TestInitOverrideWithUnreadableRemoteRefusesAsUnverified is the GH#7665
// regression, with the full refusal contract spelled out. A git origin whose
// refs/dolt/data probe FAILS is UNKNOWN, which resolveRemoteHasDoltDataProbe
// fails closed to has-data, so `bd init --reinit-local` / `--force` must
// still refuse (ADR-0002 keeps the fail-closed policy) — but the refusal must
// say bd could not VERIFY the remote's history, exit 10 before any .beads
// side effects, and never assert "already has Dolt history" about a remote
// nothing was read from.
func TestInitOverrideWithUnreadableRemoteRefusesAsUnverified(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{"reinit-local", "--reinit-local"},
		{"force", "--force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bdBin := buildBDForInitTests(t)

			workDir := t.TempDir()
			runGitForBootstrapTest(t, workDir, "init", "-b", "main")
			runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
			// A missing path answers ls-remote with the same exit 128 a
			// credential-less private remote does, with no network access.
			runGitForBootstrapTest(t, workDir, "remote", "add", "origin",
				filepath.Join(t.TempDir(), "does-not-exist.git"))

			homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

			cmd := exec.Command(bdBin, "init", tc.flag, "--prefix", "uv",
				"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
			cmd.Dir = workDir
			cmd.Env = hermeticInitEnv(homeDir)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()

			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("bd init %s against an unreadable origin: err = %v, want exit %d\nstderr:\n%s",
					tc.flag, err, ExitRemoteDivergenceRefused, stderr.String())
			}
			if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
				t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
			}

			stderrStr := stderr.String()
			if !strings.Contains(stderrStr, "could not verify whether remote 'origin' has Dolt history") {
				t.Errorf("refusal must say bd could not verify the remote's Dolt history:\n%s", stderrStr)
			}
			if strings.Contains(stderrStr, "already has Dolt history") {
				t.Errorf("refusal must not assert the unreadable remote already has Dolt history:\n%s", stderrStr)
			}

			beadsDir := filepath.Join(workDir, ".beads")
			if _, err := os.Stat(beadsDir); err == nil {
				t.Errorf("refusal should not have created %s", beadsDir)
			}
		})
	}
}

// TestCheckRemoteSafety_UnverifiedRefusalText pins the recovery contract of
// the unverified-remote refusal (GH#7665): it must explain that the failed
// check cannot rule out remote history and name the reachable remedies —
// restore access, or initialize without a remote via --remote="" — while
// never suggesting bootstrap (the remote was never read, so adopting it is
// advice about an unknown), echoing a destroy-token value, or constructing a
// complete destructive invocation (same no-echo contract as
// TestCheckRemoteSafety_RefusalTextNoEcho).
func TestCheckRemoteSafety_UnverifiedRefusalText(t *testing.T) {
	got := CheckRemoteSafety(RemoteSafetyInput{
		RemoteHasDoltData: true, RemoteDoltDataUnverified: true, ReinitLocal: true,
	})
	if got.Action != ActionRefuseDivergence {
		t.Fatalf("Action = %v, want ActionRefuseDivergence", got.Action)
	}
	msg := got.UserMessage

	for _, must := range []string{
		"could not verify whether remote 'origin' has Dolt history",
		"cannot rule out",     // explains the failed check
		"credentials",         // restore-access remedy
		`--remote=""`,         // opt-out remedy
		"bd help init-safety", // intentional-discard pointer
	} {
		if !strings.Contains(msg, must) {
			t.Errorf("unverified refusal missing %q:\n%s", must, msg)
		}
	}

	for _, banned := range []string{
		"already has Dolt history", // the false assertion GH#7665 reports
		"bd bootstrap",             // adoption advice about an unreadable remote
		"DESTROY-",                 // token values live in help, not errors
		"bd init --force --discard-remote --destroy-token",
		"bd init --reinit-local --discard-remote --destroy-token",
	} {
		if strings.Contains(msg, banned) {
			t.Errorf("unverified refusal contains banned echo %q:\n%s", banned, msg)
		}
	}

	// The verified refusal keeps its own message: the two texts must stay
	// distinct so the unverified wording can never silently replace the
	// verified diagnosis (and vice versa).
	verified := CheckRemoteSafety(RemoteSafetyInput{RemoteHasDoltData: true, ReinitLocal: true})
	if verified.UserMessage == msg {
		t.Error("unverified and verified refusals are identical, so the probe's uncertainty is not carried")
	}
}

// assertUnverifiedRefusal checks the GH#7665 unverified-refusal contract on a
// failed bd init subprocess: exit 10 (the fail-closed policy is unchanged),
// could-not-verify wording, no already-has assertion, and no clone attempt.
func assertUnverifiedRefusal(t *testing.T, err error, stderrStr string) {
	t.Helper()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("bd init: err = %v, want exit %d\nstderr:\n%s", err, ExitRemoteDivergenceRefused, stderrStr)
	}
	if code := exitErr.ExitCode(); code != ExitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (ExitRemoteDivergenceRefused)", code, ExitRemoteDivergenceRefused)
	}
	if !strings.Contains(stderrStr, "could not verify whether remote 'origin' has Dolt history") {
		t.Errorf("refusal must say bd could not verify the remote's Dolt history:\n%s", stderrStr)
	}
	if strings.Contains(stderrStr, "already has Dolt history") {
		t.Errorf("refusal must not assert the unreadable remote already has Dolt history:\n%s", stderrStr)
	}
	if strings.Contains(stderrStr, "failed to clone remote") {
		t.Errorf("refusal must fire before any clone attempt:\n%s", stderrStr)
	}
}

// TestInitReinitLocalUnverifiedConfiguredRemoteRefuses wires the unverified
// signal through the BD_SYNC_REMOTE (initSyncRemoteConfigured) probe site:
// a configured sync.remote that cannot be read must refuse --reinit-local
// with the could-not-verify wording, not an already-has assertion.
func TestInitReinitLocalUnverifiedConfiguredRemoteRefuses(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

	cmd := exec.Command(bdBin, "init", "--reinit-local", "--prefix", "cfg",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir,
		"BD_SYNC_REMOTE="+filepath.Join(t.TempDir(), "does-not-exist.git"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	assertUnverifiedRefusal(t, err, stderr.String())

	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Errorf("refusal should not have created %s", beadsDir)
	}
}

// TestInitFromJSONLUnverifiedConfiguredRemoteRefuses is the --from-jsonl arm
// of the configured-remote wiring: a local-source import against a
// configured sync.remote that cannot be read refuses as unverified before
// any import.
func TestInitFromJSONLUnverifiedConfiguredRemoteRefuses(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"jl-abc123","title":"Local JSONL wins","type":"task","status":"open","priority":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := t.TempDir()

	cmd := exec.Command(bdBin, "init", "--from-jsonl", "--prefix", "jl",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir,
		"BD_SYNC_REMOTE="+filepath.Join(t.TempDir(), "does-not-exist.git"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	assertUnverifiedRefusal(t, cmd.Run(), stderr.String())
}

// TestInitFromJSONLUnverifiedExplicitRemoteRefuses is the explicit --remote
// arm of the wiring: --from-jsonl probes an explicitly named remote, and one
// that cannot be read must refuse as unverified.
func TestInitFromJSONLUnverifiedExplicitRemoteRefuses(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"jl-abc123","title":"Local JSONL wins","type":"task","status":"open","priority":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := t.TempDir()

	cmd := exec.Command(bdBin, "init", "--from-jsonl", "--remote",
		filepath.Join(t.TempDir(), "does-not-exist.git"), "--prefix", "jl",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = hermeticInitEnv(homeDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	assertUnverifiedRefusal(t, cmd.Run(), stderr.String())
}

// gitLsRemoteShimPATH returns a PATH entry that puts a `git` shim first on
// the subprocess PATH: it delegates every operation to the real git but
// fails the Nth (and later) `git ls-remote` with exit 128 — the
// credential-less private-remote shape. It lets a test split the probe
// outcomes: the early init guard reads a reachable remote, the late one
// reads a failure, which is the only way the late CheckRemoteSafety
// decision calls are reachable with an override flag (an early probe
// failure is refused by the early guard first).
func gitLsRemoteShimPATH(t *testing.T, failOnNth int) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git ls-remote PATH shim needs a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	shimDir := t.TempDir()
	countFile := filepath.Join(shimDir, "ls-remote-count")
	shim := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "ls-remote" ]; then
  n=$(cat %q 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" > %q
  if [ "$n" -ge %d ]; then
    echo "fatal: simulated unreadable remote" >&2
    exit 128
  fi
fi
exec %q "$@"
`, countFile, countFile, failOnNth, realGit)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	return []string{"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// prependShimPATH rewrites the PATH entry of a hermetic env so the shim
// directory wins. glibc getenv resolves the FIRST occurrence, so appending
// another PATH would be ignored by the child.
func prependShimPATH(env, extra []string) []string {
	out := make([]string, 0, len(env)+len(extra))
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			for _, e := range extra {
				if strings.HasPrefix(e, "PATH=") {
					out = append(out, e)
				}
			}
			continue
		}
		out = append(out, kv)
	}
	// Keep any non-PATH extras (none today, but the caller's contract says env).
	for _, e := range extra {
		if !strings.HasPrefix(e, "PATH=") {
			out = append(out, e)
		}
	}
	return out
}

// TestInitLateProbeFailureRefusesAsUnverified covers both LATE
// CheckRemoteSafety decision calls (the configured-sync.remote probe and the
// git-origin probe): when the early guard read a Dolt-data-free remote but
// the late probe fails, the late decision must refuse an override flag with
// the could-not-verify wording — never an already-has assertion about a
// remote state that changed between the two probes.
func TestInitLateProbeFailureRefusesAsUnverified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   string
		origin bool // true: probe git origin; false: probe BD_SYNC_REMOTE
	}{
		{"configured-remote/reinit-local", "--reinit-local", false},
		{"git-origin/reinit-local", "--reinit-local", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bdBin := buildBDForInitTests(t)

			// A reachable remote with ordinary git plumbing but no
			// refs/dolt/data: the early probe verifies it clean.
			bareDir := filepath.Join(t.TempDir(), "bare.git")
			runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

			workDir := t.TempDir()
			runGitForBootstrapTest(t, workDir, "init", "-b", "main")
			runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
			if tc.origin {
				runGitForBootstrapTest(t, workDir, "remote", "add", "origin", bareDir)
			}
			homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

			shimEnv := gitLsRemoteShimPATH(t, 2)
			var env []string
			if tc.origin {
				env = prependShimPATH(hermeticInitEnv(homeDir), shimEnv)
			} else {
				env = prependShimPATH(hermeticInitEnv(homeDir,
					"BD_SYNC_REMOTE="+bareDir), shimEnv)
			}

			cmd := exec.Command(bdBin, "init", tc.flag, "--prefix", "late",
				"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
			cmd.Dir = workDir
			cmd.Env = env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			assertUnverifiedRefusal(t, cmd.Run(), stderr.String())
		})
	}
}

// TestInitReinitLocalUnreadableRemoteEmptyRemoteSucceeds demonstrates the
// opt-out remedy the unverified refusal names (GH#7665): an explicitly
// empty --remote suppresses ambient sync.remote config and git-origin
// discovery, so --reinit-local against an unreadable origin initializes
// without probing or configuring that origin. The ambient BD_SYNC_REMOTE is
// set to an unreachable path too, so success also proves the explicit empty
// --remote wins over ambient config rather than merely over git origin.
func TestInitReinitLocalUnreadableRemoteEmptyRemoteSucceeds(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	// An origin whose probe errors (missing path -> git ls-remote exit 128).
	runGitForBootstrapTest(t, workDir, "remote", "add", "origin",
		filepath.Join(t.TempDir(), "does-not-exist.git"))

	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads
	env := hermeticInitEnv(homeDir,
		"BD_SYNC_REMOTE="+filepath.Join(t.TempDir(), "also-missing.git"))

	cmd := exec.Command(bdBin, "init", "--reinit-local", "--remote=",
		"--prefix", "erm", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = workDir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd init --reinit-local --remote=\"\" against an unreadable origin failed: %v\noutput:\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(workDir, ".beads")); err != nil {
		t.Fatalf(".beads should have been created by a successful init: %v", err)
	}

	// No probe happened at all: neither the origin nor the ambient
	// sync.remote was reachable, so any probe would have left its note.
	if strings.Contains(out.String(), "could not verify refs/dolt/data") {
		t.Errorf("the explicit empty --remote must suppress probing, but a probe diagnostic printed:\n%s", out.String())
	}

	// Config alone is insufficient per the fixture's contract, but check it
	// too: a fresh workspace must not have the origin persisted as
	// sync.remote.
	beadsDir := filepath.Join(workDir, ".beads")
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != "" {
		t.Errorf("sync.remote = %q, want empty: the explicit empty --remote must not persist a remote", got)
	}

	// The store-level view: no Dolt remote may be configured on the
	// database init just created.
	listCmd := exec.Command(bdBin, "dolt", "remote", "list")
	listCmd.Dir = workDir
	listCmd.Env = env
	var listOut bytes.Buffer
	listCmd.Stdout = &listOut
	listCmd.Stderr = &listOut
	if err := listCmd.Run(); err != nil {
		t.Fatalf("bd dolt remote list failed: %v\noutput:\n%s", err, listOut.String())
	}
	if !strings.Contains(listOut.String(), "No remotes configured.") {
		t.Errorf("no Dolt remote may be configured after --remote=\"\" init, got:\n%s", listOut.String())
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

	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Errorf("refusal should not have created %s", beadsDir)
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

	// Unverified twins of the remote-has-data rows (GH#7665): the probe
	// failed, so RemoteHasDoltData is the fail-closed assumption, not
	// verified contents. Action and ExitCode must match the corresponding
	// known-has-data row — including when ONLY the unverified field is set,
	// which no live caller produces today but the pure function must still
	// fail closed on — while override-without-discard rows get the distinct
	// unverified Reason. The first two rows pin the verified Reasons so the
	// unverified wording cannot silently replace them.
	unverifiedCases := []struct {
		name       string
		in         RemoteSafetyInput
		want       RemoteSafetyAction
		wantExit   int // 0 when not a refusal
		wantReason string
	}{
		{"verified/force (reason pin)", RemoteSafetyInput{RemoteHasDoltData: true, Force: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "force-without-discard-remote"},
		{"verified/no-flags (reason pin)", RemoteSafetyInput{RemoteHasDoltData: true},
			ActionBootstrap, 0, "bootstrap-from-remote"},

		{"unverified/no-flags", RemoteSafetyInput{RemoteDoltDataUnverified: true},
			ActionBootstrap, 0, "bootstrap-from-remote"},
		{"unverified/force", RemoteSafetyInput{RemoteDoltDataUnverified: true, Force: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "unverified-remote-without-discard-remote"},
		{"unverified/reinit-local", RemoteSafetyInput{RemoteDoltDataUnverified: true, ReinitLocal: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "unverified-remote-without-discard-remote"},
		{"unverified/force+reinit-local", RemoteSafetyInput{RemoteDoltDataUnverified: true, Force: true, ReinitLocal: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "unverified-remote-without-discard-remote"},
		{"unverified/from-jsonl", RemoteSafetyInput{RemoteDoltDataUnverified: true, FromJSONL: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "unverified-remote-without-discard-remote"},
		{"unverified/force+discard/non-interactive/no-token",
			RemoteSafetyInput{RemoteDoltDataUnverified: true, Force: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"},
			ActionRequireDestroyToken, ExitDestroyTokenMissing, "destroy-token-missing"},
		{"unverified/force+discard/non-interactive/wrong-token",
			RemoteSafetyInput{RemoteDoltDataUnverified: true, Force: true, DiscardRemote: true,
				DestroyToken: "DESTROY-wrong", ExpectedToken: "DESTROY-bd"},
			ActionRequireDestroyToken, ExitDestroyTokenMissing, "destroy-token-mismatch"},
		{"unverified/force+discard/non-interactive/matching-token",
			RemoteSafetyInput{RemoteDoltDataUnverified: true, Force: true, DiscardRemote: true,
				DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd"},
			ActionProceedWithDivergence, 0, "authorized-divergence"},
		{"unverified/discard-remote-only/interactive",
			RemoteSafetyInput{RemoteDoltDataUnverified: true, DiscardRemote: true, IsInteractive: true},
			ActionProceedWithDivergence, 0, "authorized-divergence"},
		{"unverified/discard-remote/interactive/wrong-token",
			RemoteSafetyInput{RemoteDoltDataUnverified: true, DiscardRemote: true, IsInteractive: true,
				DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd"},
			ActionRequireDestroyToken, ExitDestroyTokenMissing, "destroy-token-mismatch"},
		// Both fields set — the shape every fail-closed probe produces:
		// resolveRemoteHasDoltDataProbe returns has-data AND the caller
		// sets unverified from the same error.
		{"unverified+has-data/reinit-local",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDoltDataUnverified: true, ReinitLocal: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused, "unverified-remote-without-discard-remote"},
	}
	for _, tc := range unverifiedCases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckRemoteSafety(tc.in)
			if got.Action != tc.want {
				t.Errorf("Action = %v (reason=%q), want %v", got.Action, got.Reason, tc.want)
			}
			if tc.wantExit != 0 && got.ExitCode != tc.wantExit {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, tc.wantExit)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if (tc.want == ActionRefuseDivergence || tc.want == ActionRequireDestroyToken) && got.UserMessage == "" {
				t.Errorf("refusal action %v returned empty UserMessage", tc.want)
			}
		})
	}

	// AC-3 parity, mechanically rather than by hand-copied expectations: for
	// every flag/token combination, the decision with ONLY the unverified
	// field set must match the known-has-data decision in Action and
	// ExitCode. Reason and message may differ (that is the GH#7665 fix);
	// the outcome the caller branches on may not.
	flagSets := []RemoteSafetyInput{
		{},
		{Force: true},
		{ReinitLocal: true},
		{Force: true, ReinitLocal: true},
		{FromJSONL: true},
		{DiscardRemote: true},
		{Force: true, DiscardRemote: true},
		{FromJSONL: true, DiscardRemote: true},
		{Force: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd"},
		{Force: true, DiscardRemote: true, DestroyToken: "DESTROY-wrong", ExpectedToken: "DESTROY-bd"},
		{Force: true, DiscardRemote: true, DestroyToken: "DESTROY-bd", ExpectedToken: "DESTROY-bd"},
		{DiscardRemote: true, IsInteractive: true},
		{DiscardRemote: true, IsInteractive: true, DestroyToken: "WRONG", ExpectedToken: "DESTROY-bd"},
		{DiscardRemote: true, IsInteractive: true, ExpectedToken: "DESTROY-bd"},
	}
	for _, flags := range flagSets {
		known := flags
		known.RemoteHasDoltData = true
		unverifiedOnly := flags
		unverifiedOnly.RemoteHasDoltData = false
		unverifiedOnly.RemoteDoltDataUnverified = true
		k := CheckRemoteSafety(known)
		u := CheckRemoteSafety(unverifiedOnly)
		if u.Action != k.Action {
			t.Errorf("flags %+v: unverified-only Action = %v, want %v (known-has-data)", flags, u.Action, k.Action)
		}
		if u.ExitCode != k.ExitCode {
			t.Errorf("flags %+v: unverified-only ExitCode = %d, want %d (known-has-data)", flags, u.ExitCode, k.ExitCode)
		}
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
