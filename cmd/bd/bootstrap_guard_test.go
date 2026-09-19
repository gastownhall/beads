//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestIsGitCodeRepoURL verifies the URL classifier that guards sync.remote
// against accidental git code-repository URLs.
func TestIsGitCodeRepoURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		// .git suffix — always a code-repo signal regardless of host
		{"git+ssh://github.com/org/repo.git", true},
		{"https://github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"https://gitlab.com/org/repo.git", true},
		{"git+https://bitbucket.org/org/repo.git", true},
		{"https://codeberg.org/org/repo.git", true},
		// .git on unknown host: still blocked (Dolt DBs never use .git suffix)
		{"git+ssh://my-dolt.example.com/org/repo.git", true},

		// Well-known forges without .git suffix
		{"https://github.com/org/repo", true},
		{"git+ssh://github.com/org/db", true},
		{"https://gitlab.com/org/group/repo", true},
		{"https://bitbucket.org/org/repo", true},
		{"https://codeberg.org/org/repo", true},

		// github.com / gitlab.com subdomains
		{"https://raw.github.com/org/repo", true},
		{"https://api.github.com/repos/org/repo", true},
		{"https://example.gitlab.com/org/repo", true},

		// Valid Dolt-native schemes — never blocked
		{"dolthub://myorg/mydb", false},
		{"s3://bucket/path", false},
		{"gs://bucket/path", false},
		{"az://container/path", false},
		{"file:///tmp/doltdb", false},

		// git+ssh to a self-hosted Dolt remote — NOT a code repo
		{"git+ssh://my-self-hosted-dolt.example.com/org/db", false},
		{"https://doltremoteapi.dolthub.com/org/db", false},
		{"https://doltremoteapi.example.com/mydb", false},

		// SCP-style to non-forge — allowed
		{"git@my-dolt.example.com:org/db", false},

		// Edge: empty string
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := isGitCodeRepoURL(tt.url)
			if got != tt.want {
				t.Errorf("isGitCodeRepoURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// setupSyncRemoteConfig writes sync.remote to .beads/config.yaml and
// initializes the config subsystem so resolveSyncRemote() can read it.
// Returns a cleanup function that resets config state.
func setupSyncRemoteConfig(t *testing.T, beadsDir, remote string) func() {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(beadsDir, "config.yaml"),
		[]byte("sync.remote: "+remote+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	config.ResetForTesting()
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return func() { config.ResetForTesting() }
}

// stubProbeGitRemoteDoltData replaces the refs/dolt/data probe for the
// duration of a test. Every test that configures a forge sync.remote must
// install a stub: the real probe shells out to `git ls-remote` against
// whatever host the fixture URL names.
//
// A configured sync.remote also suppresses the git-origin auto-detect leg
// (gitOriginHasDoltDataRef, which is not behind this seam), so these tests
// cannot reach the network through that path either — including the
// fall-through cases, whose whole point is to keep going after the probe.
func stubProbeGitRemoteDoltData(t *testing.T, fn func(string) (bool, error)) {
	t.Helper()
	orig := probeGitRemoteDoltData
	probeGitRemoteDoltData = fn
	t.Cleanup(func() { probeGitRemoteDoltData = orig })
}

// forgeSyncRemoteForms enumerates the shapes a git-forge sync.remote arrives
// in, with the git+ URL normalizeRemoteURL produces for each. Every one of
// them is a value bd itself writes (bd init from a git origin, bd dolt remote
// add, a hand-edited config.yaml, or finalizeSyncedBootstrap).
var forgeSyncRemoteForms = []struct {
	name      string
	remote    string
	wantClone string
}{
	{"raw https with .git", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
	{"raw https without .git", "https://github.com/org/repo", "git+https://github.com/org/repo"},
	{"scp style", "git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
	{"ssh scheme", "ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
	{"already git+https", "git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
	{"git+ssh without user", "git+ssh://github.com/org/repo.git", "git+ssh://github.com/org/repo.git"},
	{"gitlab", "https://gitlab.com/group/repo.git", "git+https://gitlab.com/group/repo.git"},
}

// newForgeSyncRemoteWorkspace builds a workspace whose config.yaml carries
// remote as sync.remote and chdirs into it, returning the .beads path.
func newForgeSyncRemoteWorkspace(t *testing.T, remote string) string {
	t.Helper()
	snapshotBootstrapEnv(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cleanup := setupSyncRemoteConfig(t, beadsDir, remote)
	t.Cleanup(cleanup)

	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// TestDetectBootstrapAction_GitForgeSyncRemote_ProbeRouting covers the #5743 /
// #5663 fix: a git-forge sync.remote is a supported Dolt remote, so bootstrap
// probes refs/dolt/data and routes on the answer instead of rejecting the URL
// outright (which printed a success tick and exited 0 with no database).
func TestDetectBootstrapAction_GitForgeSyncRemote_ProbeRouting(t *testing.T) {
	t.Run("data present clones the git+ form", func(t *testing.T) {
		for _, tc := range forgeSyncRemoteForms {
			t.Run(tc.name, func(t *testing.T) {
				beadsDir := newForgeSyncRemoteWorkspace(t, tc.remote)

				var probed []string
				stubProbeGitRemoteDoltData(t, func(url string) (bool, error) {
					probed = append(probed, url)
					return true, nil
				})

				plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

				if plan.Action != "sync" {
					t.Fatalf("action=%q, want %q (remote carries refs/dolt/data)", plan.Action, "sync")
				}
				if plan.SyncRemote != tc.wantClone {
					t.Errorf("SyncRemote=%q, want %q", plan.SyncRemote, tc.wantClone)
				}
				// The durable #4421 pin: once isGitCodeRepoURL recognizes a
				// URL as a forge, no raw http(s) form of it may reach
				// DOLT_CLONE, because Dolt routes those through the
				// remotesapi client and retries forever at ~1000% CPU. The
				// git+ prefix is what selects the git remote factory.
				//
				// Scope note: this pins the routing, not the classifier. A
				// forge the classifier does not recognize (self-hosted Gitea
				// at https://git.example.com/org/repo, no .git suffix) still
				// takes the trusted-as-is path unprobed, exactly as it did
				// before this change — a pre-existing gap tracked separately.
				if !strings.HasPrefix(plan.SyncRemote, "git+") {
					t.Errorf("SyncRemote=%q lacks the git+ prefix; a recognized forge URL must never reach DOLT_CLONE raw (#4421)", plan.SyncRemote)
				}
				if plan.Blocked {
					t.Error("Blocked=true on a successful sync plan")
				}
				if len(probed) != 1 || probed[0] != tc.wantClone {
					t.Errorf("probe calls = %v, want exactly [%q]", probed, tc.wantClone)
				}
			})
		}
	})

	t.Run("no data falls through to init and still wires the remote", func(t *testing.T) {
		beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, nil })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if plan.Action != "init" {
			t.Fatalf("action=%q, want %q (remote wired before the first bd dolt push)", plan.Action, "init")
		}
		if plan.Blocked {
			t.Error("Blocked=true; a clean 'no data yet' probe is not a failure")
		}
		// Carried so executeInitAction can register it as Dolt "origin".
		// Without it the first `bd dolt push` has no remote and either
		// refuses or adopts the git origin — a different repository under
		// the dedicated-data-repo pattern.
		if plan.SyncRemote != "git+https://github.com/org/repo.git" {
			t.Errorf("SyncRemote=%q, want the routed URL so the fresh database gets wired to it", plan.SyncRemote)
		}
	})

	t.Run("no data falls through to backup restore", func(t *testing.T) {
		beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
		backupDir := filepath.Join(beadsDir, "backup")
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte(`{"id":"bd-1"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, nil })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		// Proves the fall-through runs the whole remaining detector chain,
		// not just the fresh-init tail.
		if plan.Action != "restore" {
			t.Fatalf("action=%q, want %q (backup must still be found after the probe falls through)", plan.Action, "restore")
		}
		if plan.BackupDir != backupDir {
			t.Errorf("BackupDir=%q, want %q", plan.BackupDir, backupDir)
		}
	})

	// The error text is the shape the real probe now produces: git's own
	// stderr, threaded out of exec.ExitError.Stderr by gitLsRemoteProbeError.
	probeErr := errors.New("git ls-remote: exit status 128: fatal: Could not read from remote repository.")

	t.Run("probe error blocks and never clones", func(t *testing.T) {
		const remote = "git+ssh://github.com/org/repo.git"
		beadsDir := newForgeSyncRemoteWorkspace(t, remote)
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, probeErr })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if plan.Action != "none" {
			t.Fatalf("action=%q, want %q (UNKNOWN must fail closed)", plan.Action, "none")
		}
		if !plan.Blocked {
			t.Error("Blocked=false; an unverifiable remote must exit non-zero, not print a success tick")
		}
		if plan.SyncRemote != "" {
			t.Errorf("SyncRemote=%q, want empty (an unverified URL must not be cloned from or pushed to)", plan.SyncRemote)
		}
		if plan.BlockedRemote != remote {
			t.Errorf("BlockedRemote=%q, want %q (needed for the ls-remote hint)", plan.BlockedRemote, remote)
		}
		if !strings.Contains(plan.Reason, remote) {
			t.Errorf("Reason=%q does not name the offending URL %q", plan.Reason, remote)
		}
		if !strings.Contains(plan.Reason, "Could not read from remote repository") {
			t.Errorf("Reason=%q does not carry git's own diagnosis", plan.Reason)
		}
	})

	t.Run("probe error still restores from a local backup", func(t *testing.T) {
		// Offline laptop or credential-less CI: verifying the remote failed,
		// but restoring a local backup touches no remote. The same machine
		// with network and an empty remote would restore from this identical
		// file, so a probe failure must not be the thing that denies it.
		beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
		backupDir := filepath.Join(beadsDir, "backup")
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte(`{"id":"bd-1"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, probeErr })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if plan.Action != "restore" {
			t.Fatalf("action=%q reason=%q, want %q", plan.Action, plan.Reason, "restore")
		}
		if plan.Blocked {
			t.Error("Blocked=true on a plan that recovers locally")
		}
		if plan.SyncRemote != "" {
			t.Errorf("SyncRemote=%q, want empty: the remote is unverified, so nothing may be wired to it", plan.SyncRemote)
		}
	})

	t.Run("probe error still imports git-tracked jsonl", func(t *testing.T) {
		beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
		jsonl := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonl, []byte(`{"id":"bd-1"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, probeErr })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if plan.Action != "jsonl-import" {
			t.Fatalf("action=%q reason=%q, want %q", plan.Action, plan.Reason, "jsonl-import")
		}
		if plan.Blocked {
			t.Error("Blocked=true on a plan that recovers locally")
		}
	})

	t.Run("credentials are stripped from the reason and the blocked URL", func(t *testing.T) {
		const token = "ghp_SUPERSECRETTOKENVALUE"
		beadsDir := newForgeSyncRemoteWorkspace(t, "https://x-access-token:"+token+"@github.com/org/repo.git")
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return false, probeErr })

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if !plan.Blocked {
			t.Fatalf("action=%q, want a blocked plan", plan.Action)
		}
		// Reason and blocked_remote both land in CI logs and agent
		// transcripts; the clone funnel already scrubs userinfo before
		// reporting and these diagnostics must not be the hole in that.
		if strings.Contains(plan.Reason, token) {
			t.Errorf("Reason leaks the token: %q", plan.Reason)
		}
		if strings.Contains(plan.BlockedRemote, token) {
			t.Errorf("BlockedRemote leaks the token: %q", plan.BlockedRemote)
		}
		if !strings.Contains(plan.BlockedRemote, "github.com/org/repo.git") {
			t.Errorf("BlockedRemote=%q lost the part the user needs", plan.BlockedRemote)
		}
	})

	t.Run("routing is cwd independent in a monorepo subdir", func(t *testing.T) {
		remote := "https://github.com/org/repo.git"
		beadsDir := newForgeSyncRemoteWorkspace(t, remote)

		subDir := filepath.Join(filepath.Dir(beadsDir), "sub", "dir")
		if err := os.MkdirAll(subDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(subDir); err != nil {
			t.Fatal(err)
		}

		var probed []string
		stubProbeGitRemoteDoltData(t, func(url string) (bool, error) {
			probed = append(probed, url)
			return true, nil
		})

		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

		if plan.Action != "sync" {
			t.Fatalf("action=%q, want %q from a subdirectory", plan.Action, "sync")
		}
		if len(probed) != 1 || probed[0] != "git+https://github.com/org/repo.git" {
			t.Errorf("probe calls = %v, want exactly [%q]", probed, "git+https://github.com/org/repo.git")
		}
	})
}

// TestBootstrapPlanOutcome pins the exit status of every plan shape.
// Action=="none" used to be an unconditional success, which is how a rejected
// sync.remote exited 0 with no database (#5743). Blocked is the single
// discriminator; --dry-run is always a successful preview.
func TestBootstrapPlanOutcome(t *testing.T) {
	tests := []struct {
		name     string
		plan     BootstrapPlan
		dryRun   bool
		wantExit int // 0 means "nil error"
	}{
		{
			name:     "existing database is a genuine no-op",
			plan:     BootstrapPlan{Action: "none", HasExisting: true, Reason: "Database already exists at /tmp/x"},
			wantExit: 0,
		},
		{
			name:     "blocked plan fails",
			plan:     BootstrapPlan{Action: "none", Blocked: true, Reason: "could not verify refs/dolt/data"},
			wantExit: 1,
		},
		{
			name:     "sync plan proceeds",
			plan:     BootstrapPlan{Action: "sync", SyncRemote: "git+https://github.com/org/repo.git"},
			wantExit: 0,
		},
		{
			name:     "init plan proceeds",
			plan:     BootstrapPlan{Action: "init"},
			wantExit: 0,
		},
		{
			// bd doctor documents `bd bootstrap --dry-run` as the safe
			// inspection step, so a preview must never abort a set -e script.
			name:     "dry run previews a blocked plan without failing",
			plan:     BootstrapPlan{Action: "none", Blocked: true, Reason: "could not verify refs/dolt/data"},
			dryRun:   true,
			wantExit: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := bootstrapPlanOutcome(tt.plan, tt.dryRun)
			if tt.wantExit == 0 {
				if err != nil {
					t.Fatalf("bootstrapPlanOutcome() = %v, want nil", err)
				}
				return
			}
			code, ok := exitCodeFromError(err)
			if !ok {
				t.Fatalf("bootstrapPlanOutcome() = %v, want an *exitError", err)
			}
			if code != tt.wantExit {
				t.Errorf("exit code = %d, want %d", code, tt.wantExit)
			}
		})
	}
}

// TestDetectBootstrapAction_BlockedIsDerivedNotInherited covers the state-leak
// class the seeded plan used to create: existingBootstrapDBPlan's "none"
// verdict was copied into the working plan, so every later branch inherited
// its HasExisting/Blocked/Reason. A probe error on top of a seeded
// HasExisting=true produced action=none + blocked=true + has_existing=true,
// which printed the success tick and exited 0 — the #5743 bug, reintroduced.
func TestDetectBootstrapAction_BlockedIsDerivedNotInherited(t *testing.T) {
	// newSeededServerWorkspace builds the synthesized-beadsDir shape: the
	// .beads directory does not exist, so existingBootstrapDBPlan's verdict is
	// held as a fallback rather than returned outright.
	newSeededServerWorkspace := func(t *testing.T, remote string, check bootstrapServerDBCheck) (string, *configfile.Config) {
		t.Helper()
		snapshotBootstrapEnv(t)

		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "config-beads")
		if err := os.MkdirAll(configDir, 0o750); err != nil {
			t.Fatal(err)
		}
		cleanup := setupSyncRemoteConfig(t, configDir, remote)
		t.Cleanup(cleanup)

		doltDataDir := filepath.Join(tmpDir, "dolt-data")
		if err := os.MkdirAll(filepath.Join(doltDataDir, "mydb"), 0o750); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

		oldWd, _ := os.Getwd()
		t.Cleanup(func() { _ = os.Chdir(oldWd) })
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		origCheck := checkBootstrapServerDB
		checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck { return check }
		t.Cleanup(func() { checkBootstrapServerDB = origCheck })

		origDelay := bootstrapRetryDelay
		bootstrapRetryDelay = func(time.Duration) {}
		t.Cleanup(func() { bootstrapRetryDelay = origDelay })

		cfg := configfile.DefaultConfig()
		cfg.DoltMode = configfile.DoltModeServer
		cfg.DoltDatabase = "mydb"
		cfg.DoltDataDir = doltDataDir
		// Never created: this is the "fresh clone, .beads synthesized" path.
		return filepath.Join(tmpDir, ".beads"), cfg
	}

	t.Run("existing server database wins over a failed probe", func(t *testing.T) {
		beadsDir, cfg := newSeededServerWorkspace(t, "https://github.com/org/repo.git",
			bootstrapServerDBCheck{Exists: true, Reachable: true})
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) {
			return false, errors.New("git ls-remote: exit status 128")
		})

		plan := detectBootstrapAction(beadsDir, cfg)

		if !plan.HasExisting {
			t.Fatalf("HasExisting=false; the server database exists (action=%q reason=%q)", plan.Action, plan.Reason)
		}
		if plan.Blocked {
			t.Error("Blocked=true alongside HasExisting=true — self-contradictory, and it would print the tick AND claim a block")
		}
		if err := bootstrapPlanOutcome(plan, false); err != nil {
			t.Errorf("outcome = %v, want nil: a database exists", err)
		}
	})

	t.Run("failed server verification does not taint a later sync plan", func(t *testing.T) {
		beadsDir, cfg := newSeededServerWorkspace(t, "https://github.com/org/repo.git",
			bootstrapServerDBCheck{Reachable: true, Err: errors.New("dial tcp: connection refused")})
		stubProbeGitRemoteDoltData(t, func(string) (bool, error) { return true, nil })

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "sync" {
			t.Fatalf("action=%q reason=%q, want %q", plan.Action, plan.Reason, "sync")
		}
		if plan.Blocked {
			t.Error(`Blocked=true on a sync plan: "nothing may clone from a blocked plan" is the field's own contract`)
		}
		if plan.BlockedRemote != "" {
			t.Errorf("BlockedRemote=%q on a sync plan", plan.BlockedRemote)
		}
	})

	t.Run("unverifiable server database with no other source blocks", func(t *testing.T) {
		beadsDir, cfg := newSeededServerWorkspace(t, "",
			bootstrapServerDBCheck{Reachable: true, Err: errors.New("dial tcp: connection refused")})

		plan := detectBootstrapAction(beadsDir, cfg)

		if plan.Action != "none" || !plan.Blocked {
			t.Fatalf("action=%q blocked=%v, want none+blocked", plan.Action, plan.Blocked)
		}
		if err := bootstrapPlanOutcome(plan, false); err == nil {
			t.Error("outcome = nil; an unverifiable database must not report success")
		}
	})
}

// TestPrintBootstrapPlan_NoneWithoutDB_NoSuccessTick pins the other half of
// the #5743 false success: the "✓ Database already exists" line must be
// reserved for plans that actually found a database.
func TestPrintBootstrapPlan_NoneWithoutDB_NoSuccessTick(t *testing.T) {
	blocked := BootstrapPlan{
		Action:        "none",
		Blocked:       true,
		BeadsDir:      "/workspace/.beads",
		BlockedRemote: "git+ssh://github.com/org/repo.git",
		Reason:        `could not verify refs/dolt/data on sync.remote "git+ssh://github.com/org/repo.git": git ls-remote: exit status 128: fatal: Could not read from remote repository.`,
	}

	t.Run("blocked plan prints nothing to stdout", func(t *testing.T) {
		out := captureStdout(t, func() error { printBootstrapPlan(blocked); return nil })
		if strings.TrimSpace(out) != "" {
			t.Fatalf("blocked plan wrote to stdout (the success tick lives there):\n%s", out)
		}
	})

	t.Run("blocked plan reports the reason and actionable hints on stderr", func(t *testing.T) {
		out := captureStderr(t, func() { printBootstrapPlan(blocked) })

		if strings.Contains(out, "Database already exists") {
			t.Fatalf("blocked plan printed a success tick:\n%s", out)
		}
		if !strings.Contains(out, blocked.Reason) {
			t.Errorf("output does not carry the reason:\n%s", out)
		}
		// The hint must be runnable: git ls-remote does not understand the
		// git+ prefix, so it is stripped exactly as the probe strips it.
		if !strings.Contains(out, "git ls-remote ssh://github.com/org/repo.git refs/dolt/data") {
			t.Errorf("output lacks a runnable ls-remote hint:\n%s", out)
		}
		// Deleting sync.remote is NOT offered as a remedy: the git-origin
		// probe swallows its error, so on a credential failure that retry
		// would "succeed" by creating a database diverged from the team's.
		if strings.Contains(out, "remove 'sync.remote'") {
			t.Errorf("hint still suggests deleting sync.remote, which can silently diverge:\n%s", out)
		}
	})

	t.Run("existing database keeps the success tick", func(t *testing.T) {
		plan := BootstrapPlan{
			Action:      "none",
			HasExisting: true,
			BeadsDir:    "/workspace/.beads",
			Reason:      "Database already exists at /workspace/.beads/embeddeddolt",
		}

		out := captureStdout(t, func() error { printBootstrapPlan(plan); return nil })

		if !strings.Contains(out, "✓ Database already exists: /workspace/.beads") {
			t.Fatalf("existing database lost its success line:\n%s", out)
		}
		if !strings.Contains(out, "Nothing to do") {
			t.Errorf("existing database lost its 'Nothing to do' line:\n%s", out)
		}
	})

	t.Run("sync plan does not print credentials", func(t *testing.T) {
		const token = "ghp_SUPERSECRETTOKENVALUE"
		plan := BootstrapPlan{
			Action:     "sync",
			BeadsDir:   "/workspace/.beads",
			Database:   "beads",
			SyncRemote: "git+https://x-access-token:" + token + "@github.com/org/repo.git",
		}

		out := captureStdout(t, func() error { printBootstrapPlan(plan); return nil })

		if strings.Contains(out, token) {
			t.Fatalf("plan output leaks the token:\n%s", out)
		}
		if !strings.Contains(out, "github.com/org/repo.git") {
			t.Errorf("plan output lost the remote entirely:\n%s", out)
		}
	})
}

// TestBootstrapPlanJSON_RedactsCredentials closes the JSON half of the same
// credential leak: `bd bootstrap --json` serializes the plan, and BootstrapPlan.
// MarshalJSON must redact SyncRemote the way printBootstrapPlan redacts the human
// path. The existing "sync plan does not print credentials" subtest covers only
// printBootstrapPlan, so without this the JSON gap is invisible. It also pins the
// "redact on a copy" contract — the plan executeBootstrapPlan clones from must
// keep its real credentials.
func TestBootstrapPlanJSON_RedactsCredentials(t *testing.T) {
	const token = "ghp_SUPERSECRETTOKENVALUE"
	plan := BootstrapPlan{
		Action:     "sync",
		BeadsDir:   "/workspace/.beads",
		Database:   "beads",
		SyncRemote: "git+https://x-access-token:" + token + "@github.com/org/repo.git",
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan) failed: %v", err)
	}
	out := string(encoded)

	if strings.Contains(out, token) {
		t.Fatalf("bootstrap plan JSON leaks the token:\n%s", out)
	}
	if !strings.Contains(out, "github.com/org/repo.git") {
		t.Errorf("bootstrap plan JSON dropped the remote host/path entirely:\n%s", out)
	}
	// Redaction must not mutate the plan executeBootstrapPlan consumes for the
	// clone, or the clone would lose the credentials it needs.
	if !strings.Contains(plan.SyncRemote, token) {
		t.Errorf("MarshalJSON mutated the source plan's SyncRemote: %q", plan.SyncRemote)
	}
}

// TestDetectBootstrapAction_ValidDoltSyncRemoteUnchanged verifies that a valid
// Dolt remote URL is still accepted as-is (no regression from Layer 1 guard).
func TestDetectBootstrapAction_ValidDoltSyncRemoteUnchanged(t *testing.T) {
	for _, remote := range []string{
		"https://doltremoteapi.dolthub.com/org/db",
		"dolthub://myorg/mydb",
		"git+ssh://my-self-hosted-dolt.example.com/org/db",
	} {
		t.Run(remote, func(t *testing.T) {
			beadsDir := newForgeSyncRemoteWorkspace(t, remote)

			// Non-forge remotes take the trusted-as-is path and must never
			// pay for an ls-remote — a Dolt remotesapi endpoint has no
			// refs/dolt/data to probe in the first place.
			stubProbeGitRemoteDoltData(t, func(url string) (bool, error) {
				t.Fatalf("probed non-forge remote %q; the trusted-as-is path must not probe", url)
				return false, nil
			})

			cfg := configfile.DefaultConfig()
			plan := detectBootstrapAction(beadsDir, cfg)

			if plan.Action != "sync" {
				t.Errorf("remote=%q: action=%q, want %q (valid Dolt remote must not be blocked)", remote, plan.Action, "sync")
			}
			if plan.SyncRemote != remote {
				t.Errorf("remote=%q: SyncRemote=%q, want same URL", remote, plan.SyncRemote)
			}
		})
	}
}

// TestDetectBootstrapAction_ServerTransientRestart_DBFoundAfterRetry verifies
// that when the server is reachable but the DB appears absent on the first probe
// (e.g. during a managed Dolt restart), the retry loop waits and eventually finds
// the DB — returning action=none instead of falling through to sync/init
// .
func TestDetectBootstrapAction_ServerTransientRestart_DBFoundAfterRetry(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(tmpDir, "dolt-data")
	if err := os.MkdirAll(filepath.Join(doltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = doltDataDir

	// Simulate a restart: DB absent for first 2 probes, then found.
	callCount := 0
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(_ bootstrapServerProbeConfig) bootstrapServerDBCheck {
		callCount++
		if callCount <= 2 {
			return bootstrapServerDBCheck{Exists: false, Reachable: true}
		}
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Errorf("action=%q, want %q — DB found after retry must suppress clone", plan.Action, "none")
	}
	if !plan.HasExisting {
		t.Error("HasExisting=false, want true — DB was found on the retry probe")
	}
	if callCount < 3 {
		t.Errorf("checkBootstrapServerDB called %d times, want >=3 (retry must fire)", callCount)
	}
}

// TestDetectBootstrapAction_ServerGenuinelyAbsent_FallsThrough verifies that
// when the server is reachable but the DB is genuinely absent after all retries,
// detection falls through to init (no change from pre-retry behavior).
func TestDetectBootstrapAction_ServerGenuinelyAbsent_FallsThrough(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(tmpDir, "dolt-data")
	if err := os.MkdirAll(filepath.Join(doltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = doltDataDir

	// DB is always absent — simulates a genuinely missing database.
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(_ bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action == "none" {
		t.Errorf("action=none, want non-none — genuinely absent DB must not block recovery")
	}
	// No backup or JSONL → should fall through to init.
	if plan.Action != "init" {
		t.Errorf("action=%q, want %q — no backup/jsonl available, fresh init expected", plan.Action, "init")
	}
}
