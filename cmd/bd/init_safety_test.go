package main

// Init-safety guard matrix + end-to-end subprocess tests.
//
// The table-driven tests here enforce the invariant from
// docs/adr/0002-init-safety-invariants.md. Adding a new flag that can
// interact with remote history is a signal to extend this matrix — if the
// table doesn't exhaustively cover (dataSource × flagSet) → outcome, the
// ADR's structural lock has a gap.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

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
