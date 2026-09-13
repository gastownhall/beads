//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

// bareRepoWithDoltDataOn creates a bare repository whose only Dolt data ref
// is ref, and a clone directory whose origin points at it.
func bareRepoWithDoltDataOn(t *testing.T, ref string) (bareDir, cloneDir string) {
	t.Helper()
	bareDir = filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)
	seed := t.TempDir()
	runGitForBootstrapTest(t, seed, "init", "-b", "main")
	runGitForBootstrapTest(t, seed, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, seed, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, seed, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, seed, "push", bareDir, "HEAD:"+ref)

	cloneDir = t.TempDir()
	runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", bareDir)
	return bareDir, cloneDir
}

// Auto-detect from the git origin follows the ref: data on a custom ref is a
// sync plan carrying that ref when the ref is given, and is not seen at all
// on the default ref. Both ref shapes.
func TestDetectBootstrapActionWithRef_OriginProbeFollowsRef(t *testing.T) {
	for _, ref := range []string{"refs/heads/beads-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_DATA_DIR", "")
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
			t.Setenv("BEADS_DOLT_SERVER_HOST", "")
			t.Setenv("BEADS_DOLT_SERVER_PORT", "")
			_, cloneDir := bareRepoWithDoltDataOn(t, ref)
			beadsDir := filepath.Join(cloneDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			chdirForTest(t, cloneDir)
			cfg := configfile.DefaultConfig()

			plan := detectBootstrapActionWithRef(beadsDir, cfg, ref)
			if plan.Action != "sync" {
				t.Fatalf("with ref: action = %q reason=%q, want sync", plan.Action, plan.Reason)
			}
			if plan.SyncRemoteRef != ref {
				t.Errorf("with ref: SyncRemoteRef = %q, want %q", plan.SyncRemoteRef, ref)
			}
			if !strings.Contains(plan.Reason, ref) {
				t.Errorf("with ref: reason should name the ref: %q", plan.Reason)
			}

			plan = detectBootstrapActionWithRef(beadsDir, cfg, "")
			if plan.Action != "init" {
				t.Errorf("default ref: action = %q reason=%q, want init (no data on refs/dolt/data)", plan.Action, plan.Reason)
			}
			if plan.SyncRemoteRef != "" {
				t.Errorf("default ref: SyncRemoteRef = %q, want empty", plan.SyncRemoteRef)
			}
		})
	}
}

// writeBootstrapConfig writes .beads/config.yaml and points the config
// subsystem at it, the way the existing sync.remote plan tests do.
func writeBootstrapConfig(t *testing.T, body string) (tmpDir, beadsDir string) {
	t.Helper()
	snapshotBootstrapEnv(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	tmpDir = t.TempDir()
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize failed: %v", err)
	}
	chdirForTest(t, tmpDir)
	return tmpDir, beadsDir
}

// The code-repository guard stands without a ref and yields to a git+ URL
// with one: a repository that holds Dolt data on its own ref is meant to
// look like a code repository.
func TestDetectBootstrapAction_CodeRepoGuardYieldsToGitDataRef(t *testing.T) {
	const syncRemote = "git+file:///srv/ledgers.git"

	t.Run("without a ref the guard rejects", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: "+syncRemote+"\n")
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "none" || !strings.Contains(plan.Reason, "git code-repository URL") {
			t.Fatalf("action = %q reason = %q, want none with the guard's reason", plan.Action, plan.Reason)
		}
	})

	t.Run("sync.remote-ref admits the URL", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: "+syncRemote+"\nsync.remote-ref: refs/dolt/units/team-12542\n")
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "sync" {
			t.Fatalf("action = %q reason = %q, want sync", plan.Action, plan.Reason)
		}
		if plan.SyncRemote != syncRemote || plan.SyncRemoteRef != "refs/dolt/units/team-12542" {
			t.Errorf("plan = %+v, want the configured remote and ref", plan)
		}
	})

	t.Run("an explicit --ref admits the URL", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: "+syncRemote+"\n")
		plan := detectBootstrapActionWithRef(beadsDir, configfile.DefaultConfig(), "refs/heads/beads-data")
		if plan.Action != "sync" || plan.SyncRemoteRef != "refs/heads/beads-data" {
			t.Fatalf("plan = %+v, want sync on refs/heads/beads-data", plan)
		}
	})

	t.Run("an explicit default --ref admits the URL too", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: "+syncRemote+"\nsync.remote-ref: refs/heads/issue-data\n")
		plan := detectBootstrapActionForRef(beadsDir, configfile.DefaultConfig(), "", true)
		if plan.Action != "sync" || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want sync on the default ref: the stated intent, not the ref value, lifts the guard", plan)
		}
	})

	t.Run("a malformed committed key is ignored with a warning", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: http://myserver:7007/mydb\nsync.remote-ref: issue-data\n")
		if got := resolveSyncRemoteRefFromDir(beadsDir); got != "" {
			t.Fatalf("resolveSyncRemoteRefFromDir = %q, want the malformed value ignored", got)
		}
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "sync" || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want sync with no ref", plan)
		}
	})

	t.Run("a ref does not admit a URL dolt does not treat as git-backed", func(t *testing.T) {
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: https://gitlab.com/org/repo\nsync.remote-ref: refs/heads/beads-data\n")
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "none" {
			t.Fatalf("action = %q reason = %q, want none: a forge URL without .git is not a git-backed Dolt remote", plan.Action, plan.Reason)
		}
	})
}

// A ref beside a sync.remote that is not git-backed (file, dolthub, a
// remotesapi http endpoint) cannot apply: a configured key is dropped from the
// plan with a warning, an explicit --ref is refused with the URL named, and
// the explicit default is untouched by either rule.
func TestDetectBootstrapAction_RefOnNonGitSyncRemote(t *testing.T) {
	for _, remote := range []string{"file:///srv/doltremote", "dolthub://org/repo", "http://myserver:7007/mydb"} {
		t.Run(remote, func(t *testing.T) {
			_, beadsDir := writeBootstrapConfig(t, "sync.remote: "+remote+"\nsync.remote-ref: refs/dolt/units/team-12542\n")
			plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
			if plan.Action != "sync" || plan.SyncRemote != remote || plan.SyncRemoteRef != "" {
				t.Fatalf("configured key: plan = %+v, want sync from %s with the ref dropped", plan, remote)
			}

			plan = detectBootstrapActionForRef(beadsDir, configfile.DefaultConfig(), "refs/heads/issue-data", true)
			if plan.Action != "none" || !plan.RefRejected || !strings.Contains(plan.Reason, remote) || !strings.Contains(plan.Reason, "not a git-backed") {
				t.Fatalf("explicit --ref: plan = %+v, want a rejected none plan naming the URL", plan)
			}

			plan = detectBootstrapActionForRef(beadsDir, configfile.DefaultConfig(), "", true)
			if plan.Action != "sync" || plan.SyncRemoteRef != "" {
				t.Fatalf("explicit default: plan = %+v, want sync on the default ref", plan)
			}
		})
	}
}

// After a clone, the ref is persisted beside sync.remote; an explicit
// default (--ref refs/dolt/data) clears a configured key, and a bootstrap
// without --ref leaves the key alone.
func TestPersistBootstrapRef(t *testing.T) {
	newDir := func(t *testing.T, body string) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		beadsDir := newDir(t, "sync.remote: git+file:///srv/ledgers\n")
		if err := persistBootstrapRef(beadsDir, ref, false); err != nil {
			t.Fatalf("persistBootstrapRef(%s): %v", ref, err)
		}
		if got := resolveSyncRemoteRefFromDir(beadsDir); got != ref {
			t.Fatalf("after persist, key = %q, want %q", got, ref)
		}
		if err := persistBootstrapRef(beadsDir, "", false); err != nil {
			t.Fatal(err)
		}
		if got := resolveSyncRemoteRefFromDir(beadsDir); got != ref {
			t.Fatalf("a bootstrap without --ref must leave the key, got %q", got)
		}
		if err := persistBootstrapRef(beadsDir, "", true); err != nil {
			t.Fatal(err)
		}
		if got := resolveSyncRemoteRefFromDir(beadsDir); got != "" {
			t.Fatalf("an explicit default must clear the key, got %q", got)
		}
	}
	beadsDir := newDir(t, "sync.remote: git+file:///srv/ledgers\n")
	if err := persistBootstrapRef(beadsDir, "", true); err != nil {
		t.Fatalf("clearing an absent key must be a no-op: %v", err)
	}

	// A key already spelled flat (the way a committed file may carry it) is
	// read and cleared the same way as a nested one.
	flat := newDir(t, "sync.remote: git+file:///srv/ledgers\nsync.remote-ref: refs/dolt/units/team-12542\n")
	if got := resolveSyncRemoteRefFromDir(flat); got != "refs/dolt/units/team-12542" {
		t.Fatalf("flat key read = %q", got)
	}
	if err := persistBootstrapRef(flat, "", true); err != nil {
		t.Fatal(err)
	}
	if got := resolveSyncRemoteRefFromDir(flat); got != "" {
		t.Fatalf("flat key should be cleared, got %q", got)
	}
}

// An explicit refs/dolt/data in config is the default: no ref on the plan.
func TestDetectBootstrapAction_ExplicitDefaultRefIsNoRef(t *testing.T) {
	_, beadsDir := writeBootstrapConfig(t, "sync.remote: http://myserver:7007/mydb\nsync.remote-ref: refs/dolt/data\n")
	plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
	if plan.Action != "sync" || plan.SyncRemoteRef != "" {
		t.Fatalf("plan = %+v, want sync with an empty SyncRemoteRef", plan)
	}
}
