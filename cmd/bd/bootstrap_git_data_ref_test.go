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
			// A branch is reported as found, a ref under refs/dolt/ as Dolt data.
			if branch := strings.HasPrefix(ref, "refs/heads/"); branch != strings.Contains(plan.Reason, "carries "+ref) || branch == strings.Contains(plan.Reason, "Dolt data") {
				t.Errorf("with ref %s: reason = %q; the probe proves only that a branch exists", ref, plan.Reason)
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

// recordProbe installs a probe stub that answers hasData (or err) and
// records the ref it was asked for.
func recordProbe(t *testing.T, hasData bool, err error) *[]string {
	t.Helper()
	var probedRefs []string
	stubProbeGitRemoteDoltDataAt(t, func(_, ref string) (bool, error) {
		probedRefs = append(probedRefs, ref)
		return hasData, err
	})
	return &probedRefs
}

// The remote persisted beside the key is credential-free: config.yaml is
// committed, and a token in BD_SYNC_REMOTE stays in the environment. A
// sync.remote the file already carries is left alone.
func TestPersistBootstrapRemoteAndRef_StripsCredentials(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	plan := BootstrapPlan{BeadsDir: beadsDir, SyncRemote: "https://x-access-token:s3cret@github.com/org/repo.git", SyncRemoteRef: "refs/heads/beads-data"}
	if err := persistBootstrapRemoteAndRef(plan); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("config.yaml carries the token:\n%s", raw)
	}
	if got, _ := config.WorkspaceYamlValue(beadsDir, "sync.remote"); got != "https://github.com/org/repo.git" {
		t.Errorf("sync.remote = %q, want the credential-free URL", got)
	}
	if got := resolveSyncRemoteRefFromDir(beadsDir); got != "refs/heads/beads-data" {
		t.Errorf("%s = %q, want the ref", syncRemoteRefKey, got)
	}
	plan.SyncRemote = "git+file:///srv/other.git"
	if err := persistBootstrapRemoteAndRef(plan); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.WorkspaceYamlValue(beadsDir, "sync.remote"); got != "https://github.com/org/repo.git" {
		t.Errorf("sync.remote rewritten to %q, want the existing value kept", got)
	}
}

// The --ref note on a plan that wires no remote: silent on a sync plan, on a
// plan with a remote, on a Blocked plan, and without --ref; "no effect" for
// a non-default ref or when no key is configured; and the key clear
// announced for --ref refs/dolt/data over a configured key, as a prediction
// in a dry run.
func TestExplicitRefNote(t *testing.T) {
	_, beadsDir := seedBootstrapConfig(t, "", "refs/heads/issue-data")
	initPlan := BootstrapPlan{Action: "init"}
	if note := explicitRefNote(initPlan, "", true, beadsDir, false); !strings.Contains(note, "clears "+syncRemoteRefKey) || !strings.Contains(note, "refs/heads/issue-data") {
		t.Errorf("default --ref over a configured key: note = %q, want the clear announced", note)
	}
	if note := explicitRefNote(initPlan, "", true, beadsDir, true); !strings.Contains(note, "would clear") {
		t.Errorf("dry run: note = %q, want a prediction", note)
	}
	if note := explicitRefNote(initPlan, "refs/heads/x", true, beadsDir, false); !strings.Contains(note, "has no effect") {
		t.Errorf("non-default --ref: note = %q, want no effect", note)
	}
	if note := explicitRefNote(initPlan, "", true, t.TempDir(), false); !strings.Contains(note, "has no effect") {
		t.Errorf("default --ref with no key: note = %q, want no effect", note)
	}
	for _, p := range []BootstrapPlan{{Action: "sync", SyncRemote: "git+file:///x.git"}, {Action: "init", SyncRemote: "git+file:///x.git"}, {Action: "none", Blocked: true}} {
		if note := explicitRefNote(p, "", true, beadsDir, false); note != "" {
			t.Errorf("plan %+v: note = %q, want none", p, note)
		}
	}
	if note := explicitRefNote(initPlan, "", false, beadsDir, false); note != "" {
		t.Errorf("without --ref: note = %q, want none", note)
	}
}

// The text plan says what the run will do to a remote: the blocked plan's
// ls-remote hint names the ref it could not verify, a sync plan on a branch
// says the data is known at clone time, and a local plan that wires the new
// database to a remote names the remote and the ref before the user confirms.
func TestPrintBootstrapPlan_Ref(t *testing.T) {
	t.Run("blocked plan's hint carries the ref", func(t *testing.T) {
		out := captureStderr(t, func() {
			printBootstrapPlan(BootstrapPlan{Action: "none", Blocked: true, BeadsDir: "/w/.beads", BlockedRemote: "git+ssh://github.com/org/repo.git", SyncRemoteRef: "refs/dolt/units/team-12542", Reason: "could not verify refs/dolt/units/team-12542"})
		})
		if !strings.Contains(out, "git ls-remote ssh://github.com/org/repo.git refs/dolt/units/team-12542") {
			t.Errorf("hint should probe the unverified ref:\n%s", out)
		}
	})

	t.Run("sync plan on a branch says the data is known at clone time", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printBootstrapPlan(BootstrapPlan{Action: "sync", SyncRemote: "git+file:///srv/ledgers.git", SyncRemoteRef: "refs/heads/beads-data", Database: "beads"})
			return nil
		})
		if !strings.Contains(out, "Data ref: refs/heads/beads-data") || !strings.Contains(out, "is a branch or tag") {
			t.Errorf("branch ref should carry the note:\n%s", out)
		}
		out = captureStdout(t, func() error {
			printBootstrapPlan(BootstrapPlan{Action: "sync", SyncRemote: "git+file:///srv/ledgers.git", SyncRemoteRef: "refs/dolt/units/team-12542", Database: "beads"})
			return nil
		})
		if !strings.Contains(out, "Data ref: refs/dolt/units/team-12542") || strings.Contains(out, "branch or tag") {
			t.Errorf("a ref under refs/dolt/ gets no note:\n%s", out)
		}
	})

	t.Run("local plan names the remote it wires and the ref", func(t *testing.T) {
		for _, action := range []string{"init", "restore", "jsonl-import"} {
			out := captureStdout(t, func() error {
				printBootstrapPlan(BootstrapPlan{Action: action, SyncRemote: "git+file:///srv/ledgers.git", SyncRemoteRef: "refs/dolt/units/team-12542", Database: "beads", BackupDir: "/w/.beads/backup", JSONLFile: "/w/.beads/issues.jsonl"})
				return nil
			})
			if !strings.Contains(out, "Remote: git+file:///srv/ledgers.git") || !strings.Contains(out, "Data ref: refs/dolt/units/team-12542") {
				t.Errorf("%s plan should name the remote and the ref it wires:\n%s", action, out)
			}
		}
		out := captureStdout(t, func() error {
			printBootstrapPlan(BootstrapPlan{Action: "init", Database: "beads"})
			return nil
		})
		if strings.Contains(out, "Remote:") || strings.Contains(out, "Data ref:") {
			t.Errorf("a plan with no remote names none:\n%s", out)
		}
	})
}

// A sync.remote that looks like a code repository is probed for Dolt data
// before anything is cloned (#5743), and the probe follows the ref: the
// configured sync.remote-ref, an explicit --ref, or refs/dolt/data. Data on
// the ref is a clone from it; no data yet carries the remote and the ref so
// the fresh database is wired to origin on that ref; a probe failure names
// the ref it could not verify.
func TestDetectBootstrapAction_CodeRepoURLProbesTheRef(t *testing.T) {
	const syncRemote = "git+file:///srv/ledgers.git"

	t.Run("without a ref the probe asks for refs/dolt/data", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "")
		probed := recordProbe(t, false, nil)
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "init" || plan.SyncRemote != syncRemote || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want init wired to the remote on the default ref", plan)
		}
		if len(*probed) != 1 || (*probed)[0] != "" {
			t.Fatalf("probed refs = %q, want exactly one probe of the default ref", *probed)
		}
	})

	for _, ref := range []string{"refs/heads/beads-data", "refs/dolt/units/team-12542"} {
		t.Run("sync.remote-ref "+ref, func(t *testing.T) {
			_, beadsDir := seedBootstrapConfig(t, syncRemote, ref)
			probed := recordProbe(t, true, nil)
			plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
			if plan.Action != "sync" || plan.SyncRemote != syncRemote || plan.SyncRemoteRef != ref {
				t.Fatalf("plan = %+v, want sync from the configured remote on %s", plan, ref)
			}
			if !strings.Contains(plan.Reason, ref) {
				t.Errorf("reason should name the ref: %q", plan.Reason)
			}
			if len(*probed) != 1 || (*probed)[0] != ref {
				t.Fatalf("probed refs = %q, want exactly [%q]", *probed, ref)
			}
		})
	}

	t.Run("an explicit --ref is probed", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "")
		probed := recordProbe(t, true, nil)
		plan := detectBootstrapActionForRef(beadsDir, configfile.DefaultConfig(), "refs/heads/beads-data", true)
		if plan.Action != "sync" || plan.SyncRemoteRef != "refs/heads/beads-data" {
			t.Fatalf("plan = %+v, want sync on refs/heads/beads-data", plan)
		}
		if len(*probed) != 1 || (*probed)[0] != "refs/heads/beads-data" {
			t.Fatalf("probed refs = %q", *probed)
		}
	})

	t.Run("an explicit default --ref overrides the key", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "refs/heads/issue-data")
		probed := recordProbe(t, true, nil)
		plan := detectBootstrapActionForRef(beadsDir, configfile.DefaultConfig(), "", true)
		if plan.Action != "sync" || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want sync on the default ref", plan)
		}
		if len(*probed) != 1 || (*probed)[0] != "" {
			t.Fatalf("probed refs = %q, want the default ref, not the key", *probed)
		}
	})

	t.Run("no data on the ref falls through and carries the ref", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "refs/dolt/units/team-12542")
		recordProbe(t, false, nil)
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "init" || plan.Blocked {
			t.Fatalf("plan = %+v, want init: no data yet is not a failure", plan)
		}
		if plan.SyncRemote != syncRemote || plan.SyncRemoteRef != "refs/dolt/units/team-12542" {
			t.Fatalf("plan = %+v, want the remote and the ref carried so the fresh database is wired to origin on it", plan)
		}
	})

	t.Run("a probe failure names the ref", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "refs/dolt/units/team-12542")
		recordProbe(t, false, errors.New("git ls-remote: exit status 128"))
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "none" || !plan.Blocked || plan.SyncRemote != "" {
			t.Fatalf("plan = %+v, want a blocked plan with nothing to clone from", plan)
		}
		if !strings.Contains(plan.Reason, "could not verify refs/dolt/units/team-12542") {
			t.Errorf("reason should name the ref it could not verify: %q", plan.Reason)
		}
		if plan.SyncRemoteRef != "refs/dolt/units/team-12542" {
			t.Errorf("SyncRemoteRef = %q, want the unverified ref carried for the Reproduce line", plan.SyncRemoteRef)
		}
	})

	t.Run("a probe failure that restores locally carries no ref", func(t *testing.T) {
		// The restore wires no remote, so a ref on it would be a promise
		// nothing keeps: the plan's JSON and the --ref note must agree.
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "refs/dolt/units/team-12542")
		backupDir := filepath.Join(beadsDir, "backup")
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte(`{"id":"bd-1"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		recordProbe(t, false, errors.New("git ls-remote: exit status 128"))
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "restore" || plan.Blocked {
			t.Fatalf("plan = %+v, want restore: a local backup rescues a probe failure", plan)
		}
		if plan.SyncRemote != "" || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want neither remote nor ref on a plan that wires nothing", plan)
		}
	})

	t.Run("a branch ref found by the probe is reported as found, not as Dolt data", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, syncRemote, "refs/heads/beads-data")
		recordProbe(t, true, nil)
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "sync" || plan.SyncRemoteRef != "refs/heads/beads-data" {
			t.Fatalf("plan = %+v, want sync on the branch", plan)
		}
		if !strings.Contains(plan.Reason, "carries refs/heads/beads-data") || strings.Contains(plan.Reason, "has Dolt data") {
			t.Errorf("reason = %q: the probe proves only that the branch exists", plan.Reason)
		}
	})

	t.Run("a forge URL is routed to git+ and carries the ref", func(t *testing.T) {
		_, beadsDir := seedBootstrapConfig(t, "https://gitlab.com/org/repo", "refs/heads/beads-data")
		probed := recordProbe(t, true, nil)
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "sync" || plan.SyncRemote != "git+https://gitlab.com/org/repo" || plan.SyncRemoteRef != "refs/heads/beads-data" {
			t.Fatalf("plan = %+v, want sync from the git+ form on the ref: the routed URL is git-backed whatever its suffix", plan)
		}
		if len(*probed) != 1 || (*probed)[0] != "refs/heads/beads-data" {
			t.Fatalf("probed refs = %q", *probed)
		}
	})

	t.Run("a malformed committed key is ignored with a warning", func(t *testing.T) {
		// Hand-written on purpose: bd's writer refuses this value, so only a
		// hand-edited committed file can carry it.
		_, beadsDir := writeBootstrapConfig(t, "sync.remote: http://myserver:7007/mydb\nsync.remote-ref: issue-data\n")
		if got := resolveSyncRemoteRefFromDir(beadsDir); got != "" {
			t.Fatalf("resolveSyncRemoteRefFromDir = %q, want the malformed value ignored", got)
		}
		plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
		if plan.Action != "sync" || plan.SyncRemoteRef != "" {
			t.Fatalf("plan = %+v, want sync with no ref", plan)
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
			_, beadsDir := seedBootstrapConfig(t, remote, "refs/dolt/units/team-12542")
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
	// newDir seeds sync.remote through bd's writer; the flat-key case below
	// writes its file by hand because the flat spelling is what it tests.
	newDir := func(t *testing.T) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		seedSyncRemote(t, beadsDir, "git+file:///srv/ledgers")
		return beadsDir
	}
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		beadsDir := newDir(t)
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
	beadsDir := newDir(t)
	if err := persistBootstrapRef(beadsDir, "", true); err != nil {
		t.Fatalf("clearing an absent key must be a no-op: %v", err)
	}

	// A key already spelled flat (the way a committed file may carry it) is
	// read and cleared the same way as a nested one.
	flat := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(flat, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flat, "config.yaml"), []byte("sync.remote: git+file:///srv/ledgers\nsync.remote-ref: refs/dolt/units/team-12542\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	_, beadsDir := seedBootstrapConfig(t, "http://myserver:7007/mydb", "refs/dolt/data")
	plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())
	if plan.Action != "sync" || plan.SyncRemoteRef != "" {
		t.Fatalf("plan = %+v, want sync with an empty SyncRemoteRef", plan)
	}
}

// seedBootstrapConfig is writeBootstrapConfig with sync.remote and
// sync.remote-ref, each written when not empty, through bd's own writer and
// read back, the way seedSyncRemote does it: the fixture then cannot hand bd
// a spelling bd itself would never have written. The hand-written shapes
// that remain in this file are the point of their tests (a flat committed
// key, a malformed value the writer would refuse, a user-global file in
// both spellings).
func seedBootstrapConfig(t *testing.T, remote, ref string) (tmpDir, beadsDir string) {
	t.Helper()
	tmpDir, beadsDir = writeBootstrapConfig(t, "")
	if remote != "" {
		seedSyncRemote(t, beadsDir, remote)
	}
	if ref != "" {
		if err := config.SetYamlConfigInDir(beadsDir, syncRemoteRefKey, ref); err != nil {
			t.Fatalf("record %s: %v", syncRemoteRefKey, err)
		}
		if got := config.GetStringFromDir(beadsDir, syncRemoteRefKey); got != ref {
			t.Fatalf("%s does not read back after being written: got %q, want %q", syncRemoteRefKey, got, ref)
		}
	}
	// writeBootstrapConfig loaded the empty file; load the seeded one.
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return tmpDir, beadsDir
}

// A refuse plan (#6503: server mode, workspace already initialized, database
// missing on the server) is Blocked with no remote to reproduce against, so
// it carries no ref either: nothing reads one there, and --json shows
// neither sync_remote nor sync_remote_ref.
func TestDetectBootstrapAction_RefusePlanCarriesNoRef(t *testing.T) {
	beadsDir := newForgeSyncRemoteWorkspace(t, "https://github.com/org/repo.git")
	stubProbeGitRemoteDoltDataAt(t, func(string, string) (bool, error) { return false, nil })

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	cfg.ProjectID = "proj-already-initialized-456"
	t.Setenv("BEADS_DOLT_DATA_DIR", cfg.DoltDataDir)

	orig := checkBootstrapServerDB
	checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Reachable: true, Exists: false}
	}
	t.Cleanup(func() { checkBootstrapServerDB = orig })
	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	t.Cleanup(func() { bootstrapRetryDelay = origDelay })

	plan := detectBootstrapActionWithRef(beadsDir, cfg, "refs/dolt/units/team-12542")
	if plan.Action != "refuse" || !plan.Blocked {
		t.Fatalf("plan = %+v, want a Blocked refuse plan", plan)
	}
	if plan.SyncRemote != "" || plan.BlockedRemote != "" || plan.SyncRemoteRef != "" {
		t.Errorf("refuse plan carries sync remote %q, blocked remote %q, ref %q; want none of them", plan.SyncRemote, plan.BlockedRemote, plan.SyncRemoteRef)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan): %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal plan JSON %s: %v", raw, err)
	}
	for _, key := range []string{"sync_remote", "sync_remote_ref"} {
		if got, ok := decoded[key]; ok {
			t.Errorf("plan JSON carries %s = %v on a refuse plan; full JSON: %s", key, got, raw)
		}
	}
}

// The workspace's sync.remote-ref wins over a user-global one in either
// spelling of the global file: an empty committed value pins the default, an
// absent key lets the global value through. Through the merged reader alone a
// flat `sync.remote-ref:` line in the global file shadows a nested workspace
// value, which is why resolveSyncRemoteRef reads the workspace file first.
func TestResolveSyncRemoteRef_WorkspaceValueWinsOverUserGlobal(t *testing.T) {
	const globalRef = "refs/dolt/units/global"
	for name, body := range map[string]string{
		"nested": "sync:\n  remote-ref: " + globalRef + "\n",
		"flat":   "sync.remote-ref: " + globalRef + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			if err := os.MkdirAll(filepath.Join(home, ".config", "bd"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".config", "bd", "config.yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			t.Run("workspace key absent lets the global ref through", func(t *testing.T) {
				seedBootstrapConfig(t, "git+file:///srv/ledgers", "")
				if got := resolveSyncRemoteRef(); got != globalRef {
					t.Fatalf("resolveSyncRemoteRef = %q, want the user-global %q", got, globalRef)
				}
			})
			t.Run("workspace empty value pins the default", func(t *testing.T) {
				_, beadsDir := seedBootstrapConfig(t, "git+file:///srv/ledgers", "")
				if err := config.SetYamlConfigInDir(beadsDir, syncRemoteRefKey, ""); err != nil {
					t.Fatal(err)
				}
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatal(err)
				}
				if got := resolveSyncRemoteRef(); got != "" {
					t.Fatalf("resolveSyncRemoteRef = %q, want the default pinned by the workspace's empty value", got)
				}
			})
			t.Run("workspace ref wins", func(t *testing.T) {
				seedBootstrapConfig(t, "git+file:///srv/ledgers", "refs/heads/issue-data")
				if got := resolveSyncRemoteRef(); got != "refs/heads/issue-data" {
					t.Fatalf("resolveSyncRemoteRef = %q, want the workspace ref", got)
				}
			})
			// The layers Initialize merges above the committed file keep their
			// rank: config.local.yaml over config.yaml, the environment over
			// both, an empty environment value counting as unset.
			t.Run("config.local.yaml wins over config.yaml", func(t *testing.T) {
				_, beadsDir := seedBootstrapConfig(t, "git+file:///srv/ledgers", "refs/heads/issue-data")
				// The local file is not what bd's writer edits; written by hand.
				if err := os.WriteFile(filepath.Join(beadsDir, config.LocalConfigFileName), []byte("sync:\n  remote-ref: refs/dolt/units/local\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatal(err)
				}
				if got := resolveSyncRemoteRef(); got != "refs/dolt/units/local" {
					t.Fatalf("resolveSyncRemoteRef = %q, want the local layer's ref", got)
				}
			})
			t.Run("environment wins over the workspace", func(t *testing.T) {
				seedBootstrapConfig(t, "git+file:///srv/ledgers", "refs/heads/issue-data")
				t.Setenv(syncRemoteRefEnv, "refs/dolt/units/environment")
				if got := resolveSyncRemoteRef(); got != "refs/dolt/units/environment" {
					t.Fatalf("resolveSyncRemoteRef = %q, want the environment's ref", got)
				}
				t.Setenv(syncRemoteRefEnv, "")
				if got := resolveSyncRemoteRef(); got != "refs/heads/issue-data" {
					t.Fatalf("resolveSyncRemoteRef = %q with an empty %s, want the workspace ref", got, syncRemoteRefEnv)
				}
			})
			// A flat committed key is read by the same path as a nested one.
			t.Run("flat workspace key wins over the global ref", func(t *testing.T) {
				_, beadsDir := writeBootstrapConfig(t, "sync.remote: git+file:///srv/ledgers\nsync.remote-ref: refs/heads/flat-data\n")
				if got := resolveSyncRemoteRef(); got != "refs/heads/flat-data" {
					t.Fatalf("resolveSyncRemoteRef = %q, want the flat workspace ref (%s)", got, beadsDir)
				}
			})
		})
	}
}
