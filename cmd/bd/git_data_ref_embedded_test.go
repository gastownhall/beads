//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end coverage of the git data ref through the bd binary on the
// embedded engine: a remote added with --ref pushes to that ref and never to
// refs/dolt/data, bootstrap reads the ref from sync.remote-ref or --ref, the
// origin probe follows it, the code-repository guard yields to a git+ URL
// with a ref, and a ref change without a terminal needs --yes. Run with
// BEADS_TEST_EMBEDDED_DOLT=1.

func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
}

// bareGitRemoteNamed is setupBareGitRemote with the bare directory named by
// the caller: with a .git suffix the URL trips bootstrap's code-repository
// guard, without it the URL passes.
func bareGitRemoteNamed(t *testing.T, name string) string {
	t.Helper()
	base := t.TempDir()
	remoteDir := filepath.Join(base, name)
	resetDataRunGit(t, base, "init", "--bare", "-b", "main", remoteDir)
	seedDir := filepath.Join(base, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resetDataRunGit(t, seedDir, "init", "-b", "main")
	resetDataRunGit(t, seedDir, "config", "core.hooksPath", ".git/hooks")
	resetDataRunGit(t, seedDir, "-c", "user.name=bd-test", "-c", "user.email=bd-test@example.com",
		"commit", "--allow-empty", "-m", "init")
	resetDataRunGit(t, seedDir, "push", remoteDir, "main")
	return remoteDir
}

// runBDIn runs bd with args in dir and returns stdout, stderr, and the error.
func runBDIn(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// remoteRefsOf runs `bd dolt remote list --json` and maps name to ref.
func remoteRefsOf(t *testing.T, bd, dir string) map[string]string {
	t.Helper()
	stdout, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "list", "--json")
	if err != nil {
		t.Fatalf("bd dolt remote list --json: %v\nstderr:\n%s", err, stderr)
	}
	var remotes []doltRemoteListJSON
	if err := json.Unmarshal([]byte(stdout), &remotes); err != nil {
		t.Fatalf("parse remote list: %v\n%s", err, stdout)
	}
	refs := map[string]string{}
	for _, r := range remotes {
		refs[r.Name] = r.Ref
	}
	return refs
}

// bootstrapWorkspace prepares an empty workspace directory whose
// .beads/config.yaml carries the given body, for `bd bootstrap`.
func bootstrapWorkspace(t *testing.T, configYAML string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type bootstrapPlanJSON struct {
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	SyncRemote    string `json:"sync_remote"`
	SyncRemoteRef string `json:"sync_remote_ref"`
}

func bootstrapPlanOf(t *testing.T, bd, dir string, extra ...string) bootstrapPlanJSON {
	t.Helper()
	args := append([]string{"bootstrap", "--dry-run", "--json"}, extra...)
	stdout, stderr, err := runBDIn(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	var plan bootstrapPlanJSON
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("parse plan: %v\n%s", err, stdout)
	}
	return plan
}

func TestEmbeddedGitDataRefRemoteAddPushesToRefAndBootstrapReadsIt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bd := buildEmbeddedBD(t)

	shapes := map[string]string{
		"branch": "refs/heads/issue-data",
		"unit":   "refs/dolt/units/team-12542",
	}
	for name, ref := range shapes {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gr")
			remoteDir := bareGitRemoteNamed(t, name)
			remoteURL := "git+file://" + remoteDir

			stdout, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", ref)
			if err != nil {
				t.Fatalf("remote add --ref: %v\nstderr:\n%s", err, stderr)
			}
			if !strings.Contains(stdout, ref) {
				t.Errorf("remote add should report the ref, got:\n%s", stdout)
			}
			if got := remoteRefsOf(t, bd, dir)["origin"]; got != ref {
				t.Fatalf("remote list ref = %q, want %q", got, ref)
			}
			if got := bdConfig(t, bd, dir, "get", syncRemoteRefKey); !strings.Contains(got, ref) {
				t.Errorf("config %s = %q, want %q", syncRemoteRefKey, got, ref)
			}

			if _, stderr, err := bdDoltSeparate(t, bd, dir, "push"); err != nil {
				t.Fatalf("bd dolt push: %v\nstderr:\n%s", err, stderr)
			}
			if lsRemoteRef(t, remoteDir, ref) == "" {
				t.Fatalf("push did not create %s on the git remote", ref)
			}
			if got := lsRemoteRef(t, remoteDir, "refs/dolt/data"); got != "" {
				t.Errorf("push created refs/dolt/data (%s) although origin is on %s", got, ref)
			}
			if lsRemoteRef(t, remoteDir, "refs/heads/main") == "" {
				t.Error("refs/heads/main disappeared from the git remote")
			}

			// reset-data rebuilds the configured ref, never refs/dolt/data.
			stdout, stderr, err = bdDoltSeparate(t, bd, dir, "remote", "reset-data", "origin", "--yes")
			if err != nil {
				t.Fatalf("reset-data: %v\nstderr:\n%s", err, stderr)
			}
			if !strings.Contains(stdout, ref) {
				t.Errorf("reset-data should report the configured ref, got:\n%s", stdout)
			}
			if lsRemoteRef(t, remoteDir, ref) == "" {
				t.Errorf("%s missing after reset-data; the force-push should have rebuilt it", ref)
			}
			if got := lsRemoteRef(t, remoteDir, "refs/dolt/data"); got != "" {
				t.Errorf("reset-data created refs/dolt/data (%s)", got)
			}

			// A second workspace bootstraps from the key, a third from --ref;
			// both record the ref on origin and persist the key.
			t.Run("bootstrap from sync.remote-ref", func(t *testing.T) {
				ws := bootstrapWorkspace(t, "sync.remote: "+remoteURL+"\n"+syncRemoteRefKey+": "+ref+"\n")
				plan := bootstrapPlanOf(t, bd, ws)
				if plan.Action != "sync" || plan.SyncRemoteRef != ref {
					t.Fatalf("plan = %+v, want sync on %s", plan, ref)
				}
				if _, stderr, err := runBDIn(t, bd, ws, "bootstrap", "--yes"); err != nil {
					t.Fatalf("bd bootstrap: %v\nstderr:\n%s", err, stderr)
				}
				if got := remoteRefsOf(t, bd, ws)["origin"]; got != ref {
					t.Errorf("bootstrapped origin ref = %q, want %q", got, ref)
				}
				if _, stderr, err := runBDIn(t, bd, ws, "list"); err != nil {
					t.Fatalf("bd list after bootstrap: %v\nstderr:\n%s", err, stderr)
				}
			})
			t.Run("bootstrap from --ref persists the key", func(t *testing.T) {
				ws := bootstrapWorkspace(t, "sync.remote: "+remoteURL+"\n")
				if _, stderr, err := runBDIn(t, bd, ws, "bootstrap", "--yes", "--ref", ref); err != nil {
					t.Fatalf("bd bootstrap --ref: %v\nstderr:\n%s", err, stderr)
				}
				if got := remoteRefsOf(t, bd, ws)["origin"]; got != ref {
					t.Errorf("bootstrapped origin ref = %q, want %q", got, ref)
				}
				if got := bdConfig(t, bd, ws, "get", syncRemoteRefKey); !strings.Contains(got, ref) {
					t.Errorf("config %s after bootstrap --ref = %q, want %q", syncRemoteRefKey, got, ref)
				}
			})
			// bd init in a git clone of the repository, whose committed
			// config carries the key and nothing else about a remote: the
			// origin probe follows the ref, the clone comes from it, and the
			// origin bd configures lists the ref. The origin here is a local
			// path without .git, the shape bd must spell git+file:// for dolt.
			t.Run("bd init follows the key on the git origin", func(t *testing.T) {
				clone := filepath.Join(t.TempDir(), "clone")
				resetDataRunGit(t, filepath.Dir(clone), "clone", remoteDir, clone)
				resetDataRunGit(t, clone, "config", "core.hooksPath", ".git/hooks")
				if err := os.MkdirAll(filepath.Join(clone, ".beads"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(clone, ".beads", "config.yaml"), []byte(syncRemoteRefKey+": "+ref+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runBDInit(t, bd, clone, "--prefix", "in")
				if got := remoteRefsOf(t, bd, clone)["origin"]; got != ref {
					t.Fatalf("origin after bd init: ref = %q, want %q", got, ref)
				}
				stdout, stderr, err := runBDIn(t, bd, clone, "context", "--json")
				if err != nil || !strings.Contains(stdout, ref) {
					t.Fatalf("bd context after init should show the ref: %v\n%s\n%s", err, stdout, stderr)
				}
			})
			_ = beadsDir
		})
	}
}

// The code-repository guard: a .git-suffixed git+file:// sync.remote is
// rejected without a ref and admitted with one; a forge URL without a .git
// suffix stays rejected with a ref because dolt does not treat it as
// git-backed.
func TestEmbeddedGitDataRefBootstrapGuardYieldsToRef(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bd := buildEmbeddedBD(t)
	remoteURL := "git+file://" + setupBareGitRemote(t) // .../remote.git

	ws := bootstrapWorkspace(t, "sync.remote: "+remoteURL+"\n")
	plan := bootstrapPlanOf(t, bd, ws)
	if plan.Action != "none" || !strings.Contains(plan.Reason, "git code-repository URL") {
		t.Fatalf("without a ref: plan = %+v, want none with the guard's reason", plan)
	}
	plan = bootstrapPlanOf(t, bd, ws, "--ref", "refs/dolt/units/team-12542")
	if plan.Action != "sync" || plan.SyncRemoteRef != "refs/dolt/units/team-12542" {
		t.Fatalf("with --ref: plan = %+v, want sync on the ref", plan)
	}

	ws = bootstrapWorkspace(t, "sync.remote: "+remoteURL+"\n"+syncRemoteRefKey+": refs/heads/issue-data\n")
	plan = bootstrapPlanOf(t, bd, ws)
	if plan.Action != "sync" || plan.SyncRemoteRef != "refs/heads/issue-data" {
		t.Fatalf("with the key: plan = %+v, want sync on refs/heads/issue-data", plan)
	}

	ws = bootstrapWorkspace(t, "sync.remote: https://gitlab.com/org/repo\n"+syncRemoteRefKey+": refs/heads/issue-data\n")
	plan = bootstrapPlanOf(t, bd, ws)
	if plan.Action != "none" {
		t.Fatalf("forge URL without .git with a ref: plan = %+v, want none", plan)
	}

	// A ref beside a sync.remote dolt cannot carry one on: the key is dropped
	// from the plan with a warning, an explicit --ref is refused.
	ws = bootstrapWorkspace(t, "sync.remote: file://"+t.TempDir()+"\n"+syncRemoteRefKey+": refs/heads/issue-data\n")
	plan = bootstrapPlanOf(t, bd, ws)
	if plan.Action != "sync" || plan.SyncRemoteRef != "" {
		t.Fatalf("key beside a file:// remote: plan = %+v, want sync with the ref dropped", plan)
	}
	if _, stderr, err := runBDIn(t, bd, ws, "bootstrap", "--yes", "--ref", "refs/dolt/units/team-12542"); err == nil || !strings.Contains(stderr, "not a git-backed") {
		t.Fatalf("--ref beside a file:// remote must exit non-zero naming the URL: err=%v\n%s", err, stderr)
	}
}

// The origin probe looks at the configured ref: a clone whose git origin
// holds Dolt data on refs/dolt/units/<key> sees it with the ref and not
// without.
func TestEmbeddedGitDataRefOriginProbeFollowsRef(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bd := buildEmbeddedBD(t)
	const ref = "refs/dolt/units/team-12542"

	pubDir, _, _ := bdInit(t, bd, "--prefix", "pr")
	remoteDir := bareGitRemoteNamed(t, "ledgers")
	if _, stderr, err := bdDoltSeparate(t, bd, pubDir, "remote", "add", "origin", "git+file://"+remoteDir, "--ref", ref); err != nil {
		t.Fatalf("remote add: %v\n%s", err, stderr)
	}
	if _, stderr, err := bdDoltSeparate(t, bd, pubDir, "push"); err != nil {
		t.Fatalf("push: %v\n%s", err, stderr)
	}

	clone := t.TempDir()
	resetDataRunGit(t, clone, "init", "-b", "main")
	resetDataRunGit(t, clone, "config", "core.hooksPath", ".git/hooks")
	resetDataRunGit(t, clone, "remote", "add", "origin", remoteDir)

	// Without a ref the probe finds nothing on refs/dolt/data, so bootstrap
	// sees no workspace at all and exits non-zero with a none plan.
	stdout, _, err := runBDIn(t, bd, clone, "bootstrap", "--dry-run", "--json")
	if err == nil {
		t.Fatalf("without a ref the probe must not see data on refs/dolt/data:\n%s", stdout)
	}
	if strings.Contains(stdout, `"action":"sync"`) || strings.Contains(stdout, `"action": "sync"`) {
		t.Fatalf("without a ref the plan must not be sync:\n%s", stdout)
	}
	plan := bootstrapPlanOf(t, bd, clone, "--ref", ref)
	if plan.Action != "sync" || plan.SyncRemoteRef != ref {
		t.Fatalf("with --ref: plan = %+v, want sync on %s", plan, ref)
	}
}

// Re-adding origin on another ref without a terminal is refused without
// --yes, and goes through with it.
func TestEmbeddedGitDataRefRefChangeNeedsYesWithoutTerminal(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rc")
	remoteDir := bareGitRemoteNamed(t, "ledgers")
	remoteURL := "git+file://" + remoteDir

	// The repository's default branch is never a data ref; a branch that
	// already exists is taken only with --yes, since the first push replaces
	// its tip with Dolt storage.
	_, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/heads/main")
	if err == nil || !strings.Contains(stderr, "default branch") {
		t.Fatalf("--ref refs/heads/main must be refused as the default branch: err=%v\n%s", err, stderr)
	}
	resetDataRunGit(t, remoteDir, "branch", "issue-data", "main")
	_, stderr, err = bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/heads/issue-data")
	if err == nil || !strings.Contains(stderr, "already exists") || !strings.Contains(stderr, "--yes") {
		t.Fatalf("an existing branch without a terminal must be refused naming --yes: err=%v\n%s", err, stderr)
	}
	if _, found := remoteRefsOf(t, bd, dir)["origin"]; found {
		t.Fatal("a refused add must not create the remote")
	}
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/heads/issue-data", "--yes"); err != nil {
		t.Fatalf("first add: %v\n%s", err, stderr)
	}
	_, stderr, err = bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/dolt/units/team-12542")
	if err == nil {
		t.Fatal("ref change without a terminal and without --yes should be refused")
	}
	for _, want := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542", "--yes"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal should mention %q:\n%s", want, stderr)
		}
	}
	if got := remoteRefsOf(t, bd, dir)["origin"]; got != "refs/heads/issue-data" {
		t.Fatalf("refused change must leave the ref alone, got %q", got)
	}

	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/dolt/units/team-12542", "--yes"); err != nil {
		t.Fatalf("ref change with --yes: %v\n%s", err, stderr)
	}
	if got := remoteRefsOf(t, bd, dir)["origin"]; got != "refs/dolt/units/team-12542" {
		t.Fatalf("after --yes, ref = %q", got)
	}
	if got := bdConfig(t, bd, dir, "get", syncRemoteRefKey); !strings.Contains(got, "refs/dolt/units/team-12542") {
		t.Errorf("config %s = %q after the change", syncRemoteRefKey, got)
	}

	// Back to the default: the key is cleared and the remote lists no ref,
	// also when an unrelated config set has made the key's spelling nested.
	bdConfig(t, bd, dir, "set", "export.auto", "true")
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/dolt/data", "--yes"); err != nil {
		t.Fatalf("back to default: %v\n%s", err, stderr)
	}
	if got := remoteRefsOf(t, bd, dir)["origin"]; got != "" {
		t.Errorf("after --ref refs/dolt/data, ref = %q, want empty", got)
	}
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", syncRemoteRefKey)); strings.Contains(got, "refs/") {
		t.Errorf("config %s should be unset after --ref refs/dolt/data, got %q", syncRemoteRefKey, got)
	}

	// A ref on a non-git URL is refused by bd before dolt sees it.
	_, stderr, err = bdDoltSeparate(t, bd, dir, "remote", "add", "backup", "file://"+t.TempDir(), "--ref", "refs/heads/issue-data")
	if err == nil || !strings.Contains(stderr, "git-backed remotes only") {
		t.Fatalf("ref on file:// should be refused by bd: err=%v\n%s", err, stderr)
	}

	// remove origin keeps the key (the workspace's statement of where the
	// data lives), so remove followed by add cannot move the destination to
	// the default ref unasked; the committed key then reaches the origin bd
	// dolt push adopts from the git origin.
	bdConfig(t, bd, dir, "set", syncRemoteRefKey, "refs/dolt/units/team-12542")
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "remove", "origin"); err != nil {
		t.Fatalf("remote remove origin: %v\n%s", err, stderr)
	}
	if got := bdConfig(t, bd, dir, "get", syncRemoteRefKey); !strings.Contains(got, "refs/dolt/units/team-12542") {
		t.Fatalf("remove origin must keep %s, got %q", syncRemoteRefKey, got)
	}
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--allow-git-origin"); err != nil {
		t.Fatalf("re-add after remove: %v\n%s", err, stderr)
	}
	if got := remoteRefsOf(t, bd, dir)["origin"]; got != "refs/dolt/units/team-12542" {
		t.Fatalf("re-add after remove landed on %q, want the kept key", got)
	}
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "remove", "origin"); err != nil {
		t.Fatalf("remote remove origin again: %v\n%s", err, stderr)
	}
	// The git origin URL is what adoption derives the Dolt remote from; a
	// git+file:// spelling is git-backed for dolt, a bare path without .git
	// would be routed to DoltHub by dolt and carries no ref.
	resetDataRunGit(t, dir, "remote", "add", "origin", remoteURL)
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "push", "--yes"); err != nil {
		t.Fatalf("push with adoption: %v\n%s", err, stderr)
	}
	if got := remoteRefsOf(t, bd, dir)["origin"]; got != "refs/dolt/units/team-12542" {
		t.Fatalf("adopted origin ref = %q, want the committed key", got)
	}
	if lsRemoteRef(t, strings.TrimPrefix(remoteURL, "git+file://"), "refs/dolt/units/team-12542") == "" {
		t.Fatal("adopted push did not land on the committed ref")
	}

	// bd config set applies the ref rules.
	if out := bdConfigFail(t, bd, dir, "set", syncRemoteRefKey, "issue-data"); !strings.Contains(out, "refs/") {
		t.Fatalf("config set of a bare name should be refused naming the rule, got:\n%s", out)
	}

	// bd config unset clears the key in the nested spelling remote add
	// writes, not only a flat line. (The git origin added above for the
	// adoption step now matches the URL, hence --allow-git-origin.)
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL, "--ref", "refs/heads/issue-data", "--yes", "--allow-git-origin"); err != nil {
		t.Fatalf("re-add before unset: %v\n%s", err, stderr)
	}
	bdConfig(t, bd, dir, "unset", syncRemoteRefKey)
	if got := strings.TrimSpace(bdConfig(t, bd, dir, "get", syncRemoteRefKey)); strings.Contains(got, "refs/") {
		t.Fatalf("config unset should clear %s in either spelling, got %q", syncRemoteRefKey, got)
	}

	// A malformed --ref is refused in the argument phase, before any store
	// is opened: with no workspace at all the answer is still the ref error.
	cmd := exec.Command(bd, "dolt", "remote", "add", "origin", remoteURL, "--ref", "refs/heads/*")
	cmd.Dir = t.TempDir()
	cmd.Env = append(bdEnv(cmd.Dir), "BEADS_DIR="+filepath.Join(t.TempDir(), "missing", ".beads"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not a valid git ref name") {
		t.Fatalf("malformed --ref without a workspace: err=%v\n%s", err, out)
	}
}
