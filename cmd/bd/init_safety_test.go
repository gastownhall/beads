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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

		// --- git-origin adoption consent (#6099) --------------------------------
		// A remote DERIVED from git origin (no --remote, no sync.remote) is
		// only adopted with consent; --no-adopt skips the derivation
		// entirely; explicit/configured remotes and the existing override/
		// token rows above keep their outcomes unchanged.
		{
			// The reported incident: derived origin has confirmed history,
			// plain init, no consent flag. Must demand consent (exit 1,
			// the generic failure code) instead of silently bootstrapping.
			"derived/no-flags",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true},
			ActionRequireAdoptionConsent, 1,
		},
		{
			// --yes is consent given ahead of time: adopt.
			"derived/yes",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, AssumeYes: true},
			ActionBootstrap, 0,
		},
		{
			// An UNKNOWN probe never demands consent: the plain-init
			// warn-and-fresh fallback for an unreachable origin keeps its
			// existing behavior (fail-closed RemoteHasDoltData=true still
			// feeds the override refusals).
			"derived/probe-unknown",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, RemoteProbeUnknown: true},
			ActionBootstrap, 0,
		},
		{
			// A derived origin with no Dolt history is untouched: nothing to
			// adopt, nothing to consent to.
			"derived/no-data",
			RemoteSafetyInput{RemoteDerivedFromGitOrigin: true},
			ActionNoRemoteData, 0,
		},
		{
			// Consent never bypasses the divergence guard: --yes with a
			// local-source override flag keeps the bd-q83 refusal.
			"derived/yes+force",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, AssumeYes: true, Force: true},
			ActionRefuseDivergence, ExitRemoteDivergenceRefused,
		},
		{
			// ...nor the destroy-token requirement.
			"derived/yes+force+discard/no-token",
			RemoteSafetyInput{
				RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, AssumeYes: true,
				Force: true, DiscardRemote: true, ExpectedToken: "DESTROY-bd",
			},
			ActionRequireDestroyToken, ExitDestroyTokenMissing,
		},
		{
			// --no-adopt skips the derived remote and beats --yes: the safer
			// of two contradictory instructions wins, and it takes effect
			// even before the no-data branch (no probe ran at all).
			"derived/no-adopt",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, NoAdopt: true},
			ActionSkipRemote, 0,
		},
		{
			"derived/no-adopt+yes",
			RemoteSafetyInput{RemoteHasDoltData: true, RemoteDerivedFromGitOrigin: true, NoAdopt: true, AssumeYes: true},
			ActionSkipRemote, 0,
		},
		{
			"derived/no-adopt/no-data",
			RemoteSafetyInput{RemoteDerivedFromGitOrigin: true, NoAdopt: true},
			ActionSkipRemote, 0,
		},
		{
			// The opt-out never touches a remote the caller named explicitly
			// or configured: those keep their existing bootstrap semantics.
			"explicit/no-adopt",
			RemoteSafetyInput{RemoteHasDoltData: true, NoAdopt: true},
			ActionBootstrap, 0,
		},
		{
			"explicit/yes+no-adopt",
			RemoteSafetyInput{RemoteHasDoltData: true, AssumeYes: true, NoAdopt: true},
			ActionBootstrap, 0,
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
				"DESTROY-bd", nil, true, &confirmed, nil, false,
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

	// Refusal text must name the safe path.
	stderrStr := stderr.String()
	for _, must := range []string{"bd bootstrap", "bd help init-safety", "remote 'origin'"} {
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

// TestInitGitOriginAdoptionRefusesWithoutConsent is the CI-lane end-to-end
// regression for GH#6099 item 1: a fork-less clone whose origin carries
// synthetic refs/dolt/data, a non-interactive plain `bd init` with no --yes,
// must refuse with exit 1 naming the origin and its consent/opt-out
// remedies, creating no workspace. The embedded twin
// (TestEmbeddedInitGitOriginAdoptionRequiresConsent) proves the same refusal
// against a real upstream issue history.
func TestInitGitOriginAdoptionRefusesWithoutConsent(t *testing.T) {
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
	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads

	cmd := exec.Command(bdBin, "init", "--prefix", "mine", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = cloneDir
	cmd.Env = hermeticInitEnv(homeDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err == nil {
		t.Fatalf("bd init succeeded; expected the adoption-consent refusal. stderr:\n%s", stderr.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("non-exec-exit error: %v\nstderr:\n%s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Errorf("exit code = %d, want 1 (adoption consent refusal, the generic failure code)", code)
	}

	stderrStr := stderr.String()
	for _, must := range []string{
		bareDir,         // the refusal names the origin it would adopt
		"bd init --yes", // and the consent remedy
		"--no-adopt",    // and the opt-out remedy
		"BD_NO_REMOTE_ADOPT",
	} {
		if !strings.Contains(stderrStr, must) {
			t.Errorf("adoption refusal missing %q:\n%s", must, stderrStr)
		}
	}
	if strings.Contains(stderrStr, "Bootstrapped from remote") {
		t.Errorf("init output claims a bootstrap happened:\n%s", stderrStr)
	}

	// The early-refusal path must create no workspace.
	beadsDir := filepath.Join(cloneDir, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		t.Errorf("adoption refusal should not have created %s", beadsDir)
	}
}

// TestInitGitOriginAdoptionPromptDeclines pins the interactive arm of the
// #6099 consent gate at the command level, using the pipe-driven pattern of
// TestProtocol_ExitCode130_PromptCanceled (BD_NON_INTERACTIVE=0 keeps the
// prompt alive without a TTY): a decline or a no-answer at the adoption
// prompt exits 1 with the refusal naming the origin, creating no workspace,
// and Ctrl-C at the prompt keeps the standard cancellation exit 130.
func TestInitGitOriginAdoptionPromptDeclines(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	// fixture: a clone whose git origin carries synthetic refs/dolt/data,
	// the same shape as TestInitGitOriginAdoptionRefusesWithoutConsent.
	newClone := func(t *testing.T) (dir string, remoteURL string) {
		t.Helper()
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

		dir = t.TempDir()
		runGitForBootstrapTest(t, dir, "init", "-b", "main")
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", bareDir)
		runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")
		// The derived sync URL bd reports is the git origin URL verbatim
		// (no file:// scheme added), so that raw path is what the prompt
		// and the refusal name.
		return dir, bareDir
	}

	// startAdoptionPrompt launches bd init interactive-eligible against the
	// clone, wires combined stdout+stderr to a reader the test watches, and
	// returns the process handle, the stdin writer, and helpers for output
	// and waiting. The adoption prompt is the FIRST prompt on this path
	// (--prefix is given and the role prompt needs a TTY), so seeing it
	// proves the gate fired before anything else touched the workspace.
	startAdoptionPrompt := func(t *testing.T, cloneDir string) (cmd *exec.Cmd, stdinW *os.File, waitCh <-chan error, output func() string, promptSeen <-chan struct{}) {
		t.Helper()
		stdinR, stdinW, err := os.Pipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		cmd = exec.Command(bdBin, "init", "--prefix", "mine", "--quiet", "--skip-hooks", "--skip-agents")
		cmd.Dir = cloneDir
		cmd.Stdin = stdinR
		cmd.Stdout = outW
		cmd.Stderr = outW
		// BD_NON_INTERACTIVE=0 forces the interactive path despite the pipe
		// (isNonInteractiveInit treats an explicit 0 as interactive); CI=
		// strips any ambient CI signal the same way.
		cmd.Env = hermeticInitEnv(t.TempDir(), "BD_NON_INTERACTIVE=0", "CI=")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start bd init: %v", err)
		}
		_ = stdinR.Close()
		_ = outW.Close()

		var (
			mu   sync.Mutex
			buf  bytes.Buffer
			seen = make(chan struct{})
			once sync.Once
			wait = make(chan error, 1)
		)
		go func() { wait <- cmd.Wait() }()
		go func() {
			b := make([]byte, 1024)
			for {
				n, readErr := outR.Read(b)
				if n > 0 {
					mu.Lock()
					buf.Write(b[:n])
					if strings.Contains(buf.String(), "Adopt this origin's issue history?") {
						once.Do(func() { close(seen) })
					}
					mu.Unlock()
				}
				if readErr != nil {
					return
				}
			}
		}()
		return cmd, stdinW, wait, func() string {
			mu.Lock()
			defer mu.Unlock()
			return buf.String()
		}, seen
	}

	t.Run("decline at the prompt exits 1 with no workspace", func(t *testing.T) {
		cloneDir, remoteURL := newClone(t)
		cmd, stdinW, waitCh, output, promptSeen := startAdoptionPrompt(t, cloneDir)

		select {
		case <-promptSeen:
		case err := <-waitCh:
			t.Fatalf("bd init exited before the adoption prompt: %v\n%s", err, output())
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("timed out waiting for the adoption prompt\n%s", output())
		}

		if _, err := stdinW.Write([]byte("n\n")); err != nil {
			t.Fatalf("write decline: %v", err)
		}
		_ = stdinW.Close()

		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("bd did not exit after the decline\n%s", output())
		}

		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("declined init: expected a non-zero exit, got %v\n%s", waitErr, output())
		}
		if code := exitErr.ExitCode(); code != 1 {
			t.Errorf("exit code = %d, want 1 (adoption declined)\n%s", code, output())
		}
		out := output()
		for _, want := range []string{remoteURL, "bd init --yes", "--no-adopt"} {
			if !strings.Contains(out, want) {
				t.Errorf("decline refusal missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Bootstrapped from remote") {
			t.Errorf("declined init still bootstrapped:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(cloneDir, ".beads")); err == nil {
			t.Errorf("declined adoption must not create a workspace (.beads exists)\n%s", out)
		}
	})

	t.Run("no answer (EOF) exits 1 with no workspace", func(t *testing.T) {
		cloneDir, remoteURL := newClone(t)
		cmd, stdinW, waitCh, output, promptSeen := startAdoptionPrompt(t, cloneDir)

		select {
		case <-promptSeen:
		case err := <-waitCh:
			t.Fatalf("bd init exited before the adoption prompt: %v\n%s", err, output())
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("timed out waiting for the adoption prompt\n%s", output())
		}

		// No answer: close stdin, so the prompt's read gets EOF. An EOF is a
		// read error on this path, and a read error is not consent.
		_ = stdinW.Close()

		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("bd did not exit after EOF\n%s", output())
		}

		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("EOF'd init: expected a non-zero exit, got %v\n%s", waitErr, output())
		}
		if code := exitErr.ExitCode(); code != 1 {
			t.Errorf("exit code = %d, want 1 (no answer is not consent)\n%s", code, output())
		}
		if !strings.Contains(output(), remoteURL) {
			t.Errorf("EOF refusal should still name the origin:\n%s", output())
		}
		if _, err := os.Stat(filepath.Join(cloneDir, ".beads")); err == nil {
			t.Errorf("no-answer adoption must not create a workspace (.beads exists)\n%s", output())
		}
	})

	t.Run("cancel at the prompt exits 130", func(t *testing.T) {
		cloneDir, _ := newClone(t)
		cmd, stdinW, waitCh, output, promptSeen := startAdoptionPrompt(t, cloneDir)
		defer func() { _ = stdinW.Close() }()

		select {
		case <-promptSeen:
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatalf("send SIGINT: %v", err)
			}
		case err := <-waitCh:
			t.Fatalf("bd init exited before the adoption prompt: %v\n%s", err, output())
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("timed out waiting for the adoption prompt\n%s", output())
		}

		var waitErr error
		select {
		case waitErr = <-waitCh:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
			t.Fatalf("bd did not exit after SIGINT\n%s", output())
		}

		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("canceled init: expected a non-zero exit, got %v\n%s", waitErr, output())
		}
		if code := exitErr.ExitCode(); code != 130 {
			t.Errorf("exit code = %d, want 130 (the standard prompt-cancel exit)\n%s", code, output())
		}
		if _, err := os.Stat(filepath.Join(cloneDir, ".beads")); err == nil {
			t.Errorf("canceled adoption must not create a workspace (.beads exists)\n%s", output())
		}
	})
}

// TestInitGitOriginNoAdoptOptsOut pins the opt-out half of #6099:
// --no-adopt / BD_NO_REMOTE_ADOPT=1 means bd init never derives a remote from
// git origin — no origin probe, no clone, a fresh local database, no derived
// wiring — and it beats --yes when both are given (the safer instruction
// wins). The explicit --remote and configured sync.remote routes are
// unaffected (resolveInitConfiguredSyncRemote runs before the opt-out leg).
func TestInitGitOriginNoAdoptOptsOut(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	// fixture builds a clone whose git origin is bareDir. When withDoltData
	// is set it also carries synthetic refs/dolt/data, the shape that without
	// the opt-out would bootstrap-adopt; when unreachable is set bareDir is
	// replaced by a path that does not exist, the probe-error shape.
	newClone := func(t *testing.T, withDoltData, unreachable bool) (dir string, originURL string) {
		t.Helper()
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		if unreachable {
			bareDir = filepath.Join(t.TempDir(), "does-not-exist.git")
		}
		if !unreachable {
			runGitForBootstrapTest(t, "", "init", "--bare", bareDir)
			sourceDir := t.TempDir()
			runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
			runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
			runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
			runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
			runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
			runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
			if withDoltData {
				runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")
			}
		}
		dir = t.TempDir()
		runGitForBootstrapTest(t, dir, "init", "-b", "main")
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", bareDir)
		runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")
		return dir, bareDir
	}

	// runOptOut runs a plain non-interactive init in the clone and asserts
	// the opted-out shape: success, a fresh local workspace, no bootstrap, no
	// derived wiring persisted.
	runOptOut := func(t *testing.T, cloneDir, originURL string, envExtra []string, args ...string) {
		t.Helper()
		cmd := exec.Command(bdBin, append([]string{"init", "--prefix", "mine", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}, args...)...)
		cmd.Dir = cloneDir
		cmd.Env = hermeticInitEnv(t.TempDir(), envExtra...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("opted-out bd init failed: %v\n%s", err, out.String())
		}

		if _, err := os.Stat(filepath.Join(cloneDir, ".beads")); err != nil {
			t.Fatalf("opted-out init should create a fresh local workspace: %v\n%s", err, out.String())
		}
		if strings.Contains(out.String(), "Bootstrapped from remote") {
			t.Errorf("opted-out init still bootstrapped from the origin:\n%s", out.String())
		}
		// No derived wiring: sync.remote must not mention the origin.
		configYAML, err := os.ReadFile(filepath.Join(cloneDir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml after opted-out init: %v", err)
		}
		if strings.Contains(string(configYAML), originURL) {
			t.Errorf("opted-out init must not persist the origin as sync.remote; config.yaml:\n%s", configYAML)
		}
	}

	t.Run("no-adopt with a data-bearing origin", func(t *testing.T) {
		cloneDir, originURL := newClone(t, true, false)
		runOptOut(t, cloneDir, originURL, nil, "--no-adopt")
	})

	t.Run("BD_NO_REMOTE_ADOPT=1 env", func(t *testing.T) {
		cloneDir, originURL := newClone(t, true, false)
		runOptOut(t, cloneDir, originURL, []string{"BD_NO_REMOTE_ADOPT=1"})
	})

	t.Run("no-adopt beats --yes", func(t *testing.T) {
		// The safer of two contradictory instructions wins: --yes is consent
		// to adopting, but --no-adopt says never derive in the first place.
		cloneDir, originURL := newClone(t, true, false)
		runOptOut(t, cloneDir, originURL, nil, "--no-adopt", "--yes")
	})

	t.Run("no-adopt skips the origin probe entirely", func(t *testing.T) {
		// An unreachable origin never gets probed at all, so its
		// fail-closed UNKNOWN warning must not appear either.
		cloneDir, _ := newClone(t, false, true)
		cmd := exec.Command(bdBin, "init", "--prefix", "mine", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents", "--no-adopt")
		cmd.Dir = cloneDir
		cmd.Env = hermeticInitEnv(t.TempDir())
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("opted-out bd init with an unreachable origin failed: %v\n%s", err, out.String())
		}
		if strings.Contains(out.String(), "Could not verify") {
			t.Errorf("opted-out init still probed the origin:\n%s", out.String())
		}
		if strings.Contains(out.String(), "Bootstrapped from remote") {
			t.Errorf("opted-out init with an unreachable origin still bootstrapped:\n%s", out.String())
		}
	})
}
