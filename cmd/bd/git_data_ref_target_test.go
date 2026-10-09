package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// targetTestGit runs git in dir with a repository-local hooks path, so a
// developer's global core.hooksPath cannot reach the fixture.
func targetTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=.git/hooks",
		"GIT_AUTHOR_NAME=bd-test", "GIT_AUTHOR_EMAIL=bd-test@example.com",
		"GIT_COMMITTER_NAME=bd-test", "GIT_COMMITTER_EMAIL=bd-test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bareRepoWithMain is a bare repository whose default branch main holds one
// commit, the shape of a code repository a data ref could collide with.
func bareRepoWithMain(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "code.git")
	targetTestGit(t, base, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	targetTestGit(t, seed, "init", "-b", "main")
	targetTestGit(t, seed, "commit", "--allow-empty", "-m", "init")
	targetTestGit(t, seed, "push", bare, "main")
	return bare
}

// The probe reads the remote's default branch and whether the ref exists.
func TestProbeGitDataRefTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	url := "git+file://" + bare

	got, err := probeGitDataRefTarget(url, "refs/heads/main")
	if err != nil || !got.Exists || !got.IsHead {
		t.Fatalf("main: %+v, %v; want existing default branch", got, err)
	}
	got, err = probeGitDataRefTarget(url, "refs/heads/issue-data")
	if err != nil || got.Exists || got.IsHead {
		t.Fatalf("absent branch: %+v, %v", got, err)
	}
	targetTestGit(t, bare, "branch", "issue-data", "main")
	got, err = probeGitDataRefTarget(url, "refs/heads/issue-data")
	if err != nil || !got.Exists || got.IsHead {
		t.Fatalf("existing branch: %+v, %v; want existing, not the default", got, err)
	}
	got, err = probeGitDataRefTarget(url, "refs/dolt/units/team-12542")
	if err != nil || got.Exists {
		t.Fatalf("absent custom ref: %+v, %v", got, err)
	}
	if _, err := probeGitDataRefTarget("git+file://"+filepath.Join(t.TempDir(), "missing.git"), "refs/heads/x"); err == nil {
		t.Fatal("an unreachable repository must be an error, not a clean probe")
	}
	// The error names the repository without its credentials: the URL can be
	// CI's git origin with a token in its userinfo.
	_, err = probeGitDataRefTarget("git+https://x-access-token:SECRETTOKEN@127.0.0.1:1/org/repo.git", "refs/heads/main")
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the probe error must name the host and never the credentials: %v", err)
	}
	// A scheme-less host:port/path.git is https:// for dolt and an scp address
	// for git; the probe reads dolt's endpoint. git's own URL rewriting sends
	// that https URL to the bare repository.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+bare+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://reviewhost:1234/code.git")
	got, err = probeGitDataRefTarget("reviewhost:1234/code.git", "refs/heads/main")
	if err != nil || !got.IsHead {
		t.Fatalf("scheme-less host:port URL must be probed as dolt stores it (https): %+v, %v", got, err)
	}
}

// The guard: the default branch is refused, an existing branch or tag needs
// yes (or a prompt, which a missing terminal turns into a refusal naming
// --yes), an absent ref and a ref outside refs/heads and refs/tags pass
// without a prompt, and a non-git URL is never probed.
func TestGuardGitDataRefTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	targetTestGit(t, bare, "branch", "issue-data", "main")
	targetTestGit(t, bare, "tag", "v1", "main")
	url := "git+file://" + bare

	prev := remoteAddStdinIsTerminal
	remoteAddStdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })
	noPrompt := func(string) bool { t.Fatal("prompt must not be shown here"); return false }

	if _, err := guardGitDataRefTarget(url, "refs/heads/main", true, noPrompt); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("default branch must be refused even with yes: %v", err)
	}
	for _, ref := range []string{"refs/heads/issue-data", "refs/tags/v1"} {
		if _, err := guardGitDataRefTarget(url, ref, false, noPrompt); err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("%s exists: without a terminal and without yes it must be refused naming --yes: %v", ref, err)
		}
		if canceled, err := guardGitDataRefTarget(url, ref, true, noPrompt); err != nil || canceled {
			t.Fatalf("%s exists with yes: %v %v", ref, canceled, err)
		}
	}
	for _, ref := range []string{"refs/heads/beads-data", "refs/dolt/units/team-12542", ""} {
		if canceled, err := guardGitDataRefTarget(url, ref, false, noPrompt); err != nil || canceled {
			t.Fatalf("%q: %v %v", ref, canceled, err)
		}
	}
	// With a terminal, the prompt decides.
	remoteAddStdinIsTerminal = func() bool { return true }
	if canceled, err := guardGitDataRefTarget(url, "refs/heads/issue-data", false, func(string) bool { return false }); err != nil || !canceled {
		t.Fatalf("declined prompt: canceled=%v err=%v", canceled, err)
	}
	if canceled, err := guardGitDataRefTarget(url, "refs/heads/issue-data", false, func(string) bool { return true }); err != nil || canceled {
		t.Fatalf("accepted prompt: canceled=%v err=%v", canceled, err)
	}
	// A non-git URL is not probed at all (an unreachable one would error).
	if canceled, err := guardGitDataRefTarget("dolthub://org/repo", "refs/heads/main", false, noPrompt); err != nil || canceled {
		t.Fatalf("non-git URL: %v %v", canceled, err)
	}
	// A probe that fails: a warning with a terminal; a refusal without one,
	// yes or not, since the default branch cannot be ruled out.
	missing := "git+file://" + filepath.Join(t.TempDir(), "missing.git")
	if canceled, err := guardGitDataRefTarget(missing, "refs/heads/x", false, noPrompt); err != nil || canceled {
		t.Fatalf("failed probe with a terminal warns and goes on: %v %v", canceled, err)
	}
	remoteAddStdinIsTerminal = func() bool { return false }
	for _, yes := range []bool{false, true} {
		if _, err := guardGitDataRefTarget(missing, "refs/heads/x", yes, noPrompt); err == nil || !strings.Contains(err.Error(), "could not check") {
			t.Fatalf("yes=%v: a failed probe without a terminal must be refused: %v", yes, err)
		}
	}
}

// guardedSyncRemoteRef serves the paths that wire origin without a prompt:
// the default branch is refused outright, an existing ref and a ref the probe
// cannot read are refused with the caller's remedy, whatever the terminal
// state; a free ref and a ref outside refs/heads and refs/tags pass without a
// probe, and a URL that is not git-backed takes no ref. The two remedies name
// a command that runs on their path, with the URL's credentials dropped.
func TestGuardedSyncRemoteRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	targetTestGit(t, bare, "branch", "issue-data", "main")
	url := "git+file://" + bare
	missing := "git+file://" + filepath.Join(t.TempDir(), "missing.git")
	remedies := func(string, string) gitDataRefRemedy { return gitDataRefRemedy{take: "TAKE", move: "MOVE"} }
	prev := remoteAddStdinIsTerminal
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })

	for _, terminal := range []bool{false, true} {
		remoteAddStdinIsTerminal = func() bool { return terminal }
		t.Setenv(syncRemoteRefEnv, "refs/heads/main")
		if _, err := guardedSyncRemoteRef(url, remedies); err == nil || !strings.Contains(err.Error(), "default branch") || !strings.HasSuffix(err.Error(), "MOVE") {
			t.Fatalf("terminal=%v: the default branch is refused with the remedy that moves the ref: %v", terminal, err)
		}
		t.Setenv(syncRemoteRefEnv, "refs/heads/issue-data")
		if _, err := guardedSyncRemoteRef(url, remedies); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.HasSuffix(err.Error(), "TAKE") {
			t.Fatalf("terminal=%v: an existing ref is refused with the remedy that takes it, never prompted: %v", terminal, err)
		}
		t.Setenv(syncRemoteRefEnv, "refs/heads/beads-data")
		if ref, err := guardedSyncRemoteRef(url, remedies); err != nil || ref != "refs/heads/beads-data" {
			t.Fatalf("terminal=%v: a free ref: %q %v", terminal, ref, err)
		}
		if _, err := guardedSyncRemoteRef(missing, remedies); err == nil || !strings.Contains(err.Error(), "could not check") || !strings.HasSuffix(err.Error(), "TAKE") {
			t.Fatalf("terminal=%v: a branch the probe cannot read is refused on an unattended path: %v", terminal, err)
		}
		t.Setenv(syncRemoteRefEnv, "refs/dolt/units/team-12542")
		if ref, err := guardedSyncRemoteRef(missing, remedies); err != nil || ref != "refs/dolt/units/team-12542" {
			t.Fatalf("terminal=%v: a ref outside refs/heads is not probed: %q %v", terminal, ref, err)
		}
	}
	// A remote that is not git-backed takes no ref and is not probed.
	t.Setenv(syncRemoteRefEnv, "refs/heads/main")
	if ref, err := guardedSyncRemoteRef("dolthub://org/repo", remedies); err != nil || ref != "" {
		t.Fatalf("non-git URL: %q %v", ref, err)
	}

	// The remedies paste into a shell as one command, credential-free: the
	// URL and the ref are quoted, so a space, a quote, or a $ in a URL cannot
	// split or expand it.
	const tokenURL = "git+https://x-access-token:SECRETTOKEN@github.com/org/it's $HOME.git"
	wantAdd := `bd dolt remote add origin 'git+https://github.com/org/it'\''s $HOME.git' --ref 'refs/heads/issue-data'`
	if got := remoteAddRemedies(tokenURL, "refs/heads/issue-data"); !strings.Contains(got.take, wantAdd+" --yes") || strings.Contains(got.take, "SECRETTOKEN") || !strings.Contains(got.move, syncRemoteRefKey) {
		t.Errorf("remoteAddRemedies = %+v, want the take remedy to contain %q", got, wantAdd+" --yes")
	}
	got := sqlRemoteAddRemedies(tokenURL, "refs/heads/issue-data")
	for _, want := range []string{"bd sql '", `CALL DOLT_REMOTE("add", "--ref", "refs/heads/issue-data", "origin", "git+https://github.com/org/it'\''s $HOME.git")`} {
		if !strings.Contains(got.take, want) {
			t.Errorf("sqlRemoteAddRemedies.take = %q, want it to contain %q", got.take, want)
		}
	}
	if strings.Contains(got.take, "SECRETTOKEN") {
		t.Errorf("sqlRemoteAddRemedies carries the credentials: %q", got.take)
	}
	if got := replaceOriginRemedies(tokenURL, "refs/heads/issue-data"); !strings.Contains(got.take, wantAdd+" --yes") || !strings.Contains(got.move, "--ref '<ref>'") || strings.Contains(got.move, syncRemoteRefKey) {
		t.Errorf("replaceOriginRemedies = %+v: the way out of a move is a re-add, not the key", got)
	}
	// init does not run twice, and the next push adopts the git origin, not
	// sync.remote, so its remedies name the add itself, never the key alone.
	if got := initOriginRemedies(tokenURL, "refs/heads/issue-data"); !strings.Contains(got.take, wantAdd+" --yes") || !strings.HasPrefix(got.move, "Add origin with a --ref") || !strings.Contains(got.move, "--ref '<ref>'") || strings.Contains(got.move+got.take, syncRemoteRefKey) {
		t.Errorf("initOriginRemedies = %+v: the way on from init is the add, not the key", got)
	}
}

// bd init wires origin behind the same guard: with a terminal and in
// interactive mode it asks about an existing ref and refuses the default
// branch; otherwise both are refusals; a divergence the user already
// authorized takes an existing ref without asking and still refuses the
// default branch, and an unreadable probe whenever nobody is watching (a pty
// under --non-interactive included). Either way a refusal or a declined
// prompt leaves origin unconfigured, and a free ref is wired.
func TestGuardInitDoltRemoteRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	targetTestGit(t, bare, "branch", "issue-data", "main")
	url := "git+file://" + bare
	prev := remoteAddStdinIsTerminal
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })
	noPrompt := func(string) bool { t.Fatal("prompt must not be shown here"); return false }

	for _, tc := range []struct {
		name        string
		interactive bool
		terminal    bool
	}{{"non-interactive", false, true}, {"no terminal", true, false}} {
		remoteAddStdinIsTerminal = func() bool { return tc.terminal }
		if guardInitDoltRemoteRef(url, "refs/heads/main", tc.interactive, false, noPrompt) {
			t.Errorf("%s: the default branch must not be wired", tc.name)
		}
		if guardInitDoltRemoteRef(url, "refs/heads/issue-data", tc.interactive, false, noPrompt) {
			t.Errorf("%s: an existing ref must not be wired without a prompt", tc.name)
		}
		if !guardInitDoltRemoteRef(url, "refs/heads/beads-data", tc.interactive, false, noPrompt) {
			t.Errorf("%s: a free ref is wired", tc.name)
		}
	}
	remoteAddStdinIsTerminal = func() bool { return true }
	if guardInitDoltRemoteRef(url, "refs/heads/main", true, false, noPrompt) {
		t.Error("interactive: the default branch must be refused before any prompt")
	}
	if guardInitDoltRemoteRef(url, "refs/heads/issue-data", true, false, func(string) bool { return false }) {
		t.Error("interactive: a declined prompt must not wire the ref")
	}
	if !guardInitDoltRemoteRef(url, "refs/heads/issue-data", true, false, func(string) bool { return true }) {
		t.Error("interactive: an accepted prompt wires the ref")
	}
	if !guardInitDoltRemoteRef(url, "refs/dolt/units/team-12542", false, false, noPrompt) || !guardInitDoltRemoteRef("dolthub://org/repo", "", false, false, noPrompt) {
		t.Error("a ref outside refs/heads, or no ref, needs no check")
	}
	// An authorized divergence: the existing ref is the data the user chose
	// to replace, so it is taken without a prompt, with or without a
	// terminal; the default branch and an unreadable probe are still refused.
	missing := "git+file://" + filepath.Join(t.TempDir(), "missing.git")
	for _, terminal := range []bool{false, true} {
		remoteAddStdinIsTerminal = func() bool { return terminal }
		if !guardInitDoltRemoteRef(url, "refs/heads/issue-data", false, true, noPrompt) {
			t.Errorf("terminal=%v: a consented divergence takes the existing ref", terminal)
		}
		if guardInitDoltRemoteRef(url, "refs/heads/main", false, true, noPrompt) {
			t.Errorf("terminal=%v: consent does not reach the default branch", terminal)
		}
	}
	// Unattended, consent does not reach a ref the probe cannot read: a pty
	// under --non-interactive is still nobody reading the warning.
	for _, tc := range []struct {
		name        string
		interactive bool
		terminal    bool
	}{{"no terminal", true, false}, {"non-interactive with a terminal", false, true}, {"neither", false, false}} {
		remoteAddStdinIsTerminal = func() bool { return tc.terminal }
		if guardInitDoltRemoteRef(missing, "refs/heads/issue-data", tc.interactive, true, noPrompt) {
			t.Errorf("%s: consent does not reach a ref the probe cannot read", tc.name)
		}
	}
	// Attended, consent stands in for --yes: an unreadable probe warns and
	// goes on, as bd dolt remote add --yes does with a terminal.
	remoteAddStdinIsTerminal = func() bool { return true }
	if !guardInitDoltRemoteRef(missing, "refs/heads/issue-data", true, true, noPrompt) {
		t.Error("interactive with a terminal: consent admits a ref the probe cannot read, with a warning")
	}
}

// bd config apply adding a fresh origin and proxied bd init wire origin
// without a prompt and go through the guard: with a terminal attached, where
// the interactive paths would ask, both still refuse, naming the remedy of
// their path; a refused ref adds nothing, and a free ref is added on that
// ref. (Direct bd init and the apply update path have their own tests.)
func TestOriginPathsWithoutPromptRunGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	targetTestGit(t, bare, "branch", "issue-data", "main")
	url := "git+file://" + bare
	prev := remoteAddStdinIsTerminal
	remoteAddStdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })
	ctx := context.Background()

	st := &fakeOriginRemoteWriter{}
	var created []string
	create := func(_ context.Context, name, u, ref string) error {
		created = append(created, name+" "+u+" ref="+ref)
		return nil
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/issue-data"} {
		t.Setenv(syncRemoteRefEnv, ref)
		if _, err := addOriginRemote(ctx, st, url); err == nil || len(st.calls) != 0 {
			t.Fatalf("apply on %s: must be refused before the add: err=%v calls=%q", ref, err, st.calls)
		} else if ref != "refs/heads/main" && !strings.Contains(err.Error(), "bd dolt remote add origin ") {
			t.Fatalf("apply on %s: the remedy is bd dolt remote add --yes: %v", ref, err)
		}
		if err := createProxiedOriginRemote(ctx, create, url); err == nil || len(created) != 0 {
			t.Fatalf("proxied init on %s: must be refused before the create: err=%v created=%q", ref, err, created)
		} else if ref != "refs/heads/main" && !strings.Contains(err.Error(), "bd sql '") {
			t.Fatalf("proxied init on %s: the remedy is the SQL route, since remote add is refused over the proxy: %v", ref, err)
		}
	}

	t.Setenv(syncRemoteRefEnv, "refs/heads/beads-data")
	ref, err := addOriginRemote(ctx, st, url)
	if err != nil || ref != "refs/heads/beads-data" || strings.Join(st.calls, ";") != "add origin "+url+" ref=refs/heads/beads-data" {
		t.Fatalf("apply on a free ref: ref=%q err=%v calls=%q", ref, err, st.calls)
	}
	if err := createProxiedOriginRemote(ctx, create, url); err != nil || strings.Join(created, ";") != "origin "+url+" ref=refs/heads/beads-data" {
		t.Fatalf("proxied init on a free ref: err=%v created=%q", err, created)
	}
}

// ensureDoltRemoteGuarded runs the guard only when a remote is created or
// replaced, never for a re-add that changes nothing.
func TestEnsureDoltRemoteGuardedRunsGuardOnWritesOnly(t *testing.T) {
	ctx := t.Context()
	calls := 0
	guard := func() (bool, error) { calls++; return false, nil }
	st := &fakeDoltRemoteAddStore{}
	const url = "git+file:///srv/ledgers.git"

	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/issue-data", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("create: guard ran %d times, want 1", calls)
	}
	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/issue-data", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("unchanged re-add: guard ran %d times, want still 1", calls)
	}
	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/other", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("replace: guard ran %d times, want 2", calls)
	}
	refusing := func() (bool, error) { return false, os.ErrPermission }
	if _, err := ensureDoltRemoteGuarded(ctx, st, "backup", url, "refs/heads/main", true, nil, refusing); err == nil {
		t.Fatal("a refusing guard must stop the add")
	}
	if _, found := findDoltRemote(st.remotes, "backup"); found {
		t.Fatal("a refused add must not create the remote")
	}
	canceling := func() (bool, error) { return true, nil }
	res, err := ensureDoltRemoteGuarded(ctx, st, "backup", url, "refs/heads/x", false, nil, canceling)
	if err != nil || !res.Canceled {
		t.Fatalf("canceled guard: %+v %v", res, err)
	}
}
