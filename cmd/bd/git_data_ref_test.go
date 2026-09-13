package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// Only a full ref is accepted: a bare branch name or a typo such as
// ref/heads/x is refused instead of becoming a ref nobody asked for. An
// explicit refs/dolt/data is the default and canonicalizes to "".
func TestValidateGitDataRef(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"refs/heads/beads-data", "refs/heads/beads-data", false},
		{"refs/dolt/units/team-12542", "refs/dolt/units/team-12542", false},
		{"  refs/heads/beads-data  ", "refs/heads/beads-data", false},
		{"refs/dolt/data", "", false},
		{"", "", true},
		{"   ", "", true},
		{"beads-data", "", true},
		{"ref/heads/x", "", true},
		{"heads/x", "", true},
		{"refs/heads/has space", "", true},
		// git check-ref-format rules: a pattern or a malformed name must not
		// become a ref that reset-data would expand or git would refuse.
		{"refs/heads/*", "", true},
		{"refs/heads/a?b", "", true},
		{"refs/heads/a[b]", "", true},
		{"refs/heads/a..b", "", true},
		{"refs/heads/a.lock", "", true},
		{"refs/heads/.hidden", "", true},
		{"refs/heads/a/", "", true},
		{"refs/heads/a.", "", true},
		{"refs/heads/a@{1}", "", true},
		{"refs/heads/a//b", "", true},
		{"refs/heads/a~1", "", true},
		{"refs/heads/a^2", "", true},
		{"refs/heads/a:b", "", true},
		{"refs/heads/a\\b", "", true},
		{"refs/heads/tab\tx", "", true},
		{"refs/heads/__dolt_remote_info__", "", true},
		{"refs/heads/feature/with.dots", "refs/heads/feature/with.dots", false},
	}
	for _, tt := range tests {
		got, err := validateGitDataRef(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateGitDataRef(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("validateGitDataRef(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if err != nil && !strings.Contains(err.Error(), "--ref") {
			t.Errorf("error should name the flag: %v", err)
		}
	}
}

// The info-branch pin bd refuses as a data ref is dolt's, the same one
// reset-data deletes and rebuilds.
func TestDoltRemoteInfoRefPinsAgree(t *testing.T) {
	t.Setenv(config.DoltRemoteInfoBranchEnvVar, config.DefaultDoltRemoteInfoBranch)
	if got := config.DoltRemoteInfoRef(); got != gitDoltInfoRef {
		t.Fatalf("config.DoltRemoteInfoRef() = %q, reset-data pins %q", got, gitDoltInfoRef)
	}
}

// Only URLs dolt treats as git-backed can carry a ref: git+ schemes, and
// ssh:// or scp-style URLs ending in .git (normalized to git+ssh by dolt).
// Everything else, git:// included, is refused before dolt sees it.
func TestIsGitBackedDoltRemoteURL(t *testing.T) {
	yes := []string{
		"git+https://github.com/org/repo.git", "git+http://host/repo", "git+ssh://git@host/org/repo.git",
		"git+file:///srv/ledgers", "GIT+FILE:///srv/ledgers",
		"https://github.com/org/repo.git", "https://github.com/org/repo.git?x=1", "http://host/repo.git",
		"ssh://git@host/org/repo.git", "file:///srv/ledgers.git", "git@github.com:org/repo.git",
		"git@host:org/repo.git", "host.example:org/repo.git", "/srv/ledgers.git", "./ledgers.git", "../ledgers.git",
		"github.com/org/repo.git", "srv/ledgers.git",
	}
	no := []string{
		"https://github.com/org/repo", "git://host/repo.git", "ssh://git@host/org/repo", "git@host:org/repo",
		"host:org/repo.git", "C:repo.git", "/srv/ledgers", "dolthub://org/repo", "dolthub://org/repo.git", "file:///tmp/db", "aws://bucket/db.git", "",
	}
	for _, u := range yes {
		if !isGitBackedDoltRemoteURL(u) {
			t.Errorf("isGitBackedDoltRemoteURL(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if isGitBackedDoltRemoteURL(u) {
			t.Errorf("isGitBackedDoltRemoteURL(%q) = true, want false", u)
		}
	}
}

// An adopted origin carries the configured ref only when its URL is
// git-backed; a file:// or bare-path origin gets the default, flagged.
func TestRefForAdoptedRemote(t *testing.T) {
	if ref, ok := refForAdoptedRemote("git+file:///srv/ledgers", "refs/dolt/units/team-12542"); ref != "refs/dolt/units/team-12542" || !ok {
		t.Fatalf("git-backed: got %q, %v", ref, ok)
	}
	if ref, ok := refForAdoptedRemote("/srv/ledgers", "refs/heads/issue-data"); ref != "" || ok {
		t.Fatalf("bare path with a configured ref: got %q, %v; want dropped and flagged", ref, ok)
	}
	if ref, ok := refForAdoptedRemote("/srv/ledgers.git", "refs/heads/issue-data"); ref != "refs/heads/issue-data" || !ok {
		t.Fatalf("a .git path is git-backed for dolt: got %q, %v", ref, ok)
	}
	if ref, ok := refForAdoptedRemote("file:///srv/ledgers", ""); ref != "" || !ok {
		t.Fatalf("no configured ref: got %q, %v", ref, ok)
	}
}

// A git origin becomes a Dolt remote by the usual normalization; the one
// shape that stays non-git-backed, a local path without .git, is spelled
// git+file:// when a ref is in play and left alone otherwise.
func TestGitOriginDoltURL(t *testing.T) {
	cases := map[[2]string]string{
		{"/srv/ledgers", "refs/dolt/units/team-12542"}:           "git+file:///srv/ledgers",
		{"/srv/ledgers", ""}:                                     "/srv/ledgers",
		{"/srv/ledgers.git", "refs/heads/issue-data"}:            "/srv/ledgers.git",
		{"https://github.com/org/repo", "refs/heads/issue-data"}: "git+https://github.com/org/repo",
		{"git@github.com:org/repo.git", "refs/heads/issue-data"}: "git+ssh://git@github.com/org/repo.git",
		{"git+file:///srv/ledgers", "refs/heads/issue-data"}:     "git+file:///srv/ledgers",
	}
	for in, want := range cases {
		if got := gitOriginDoltURL(in[0], in[1]); got != want {
			t.Errorf("gitOriginDoltURL(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// dataRefForRemoteURL is refForAdoptedRemote with the warning: the configured
// ref rides a git-backed URL and is dropped for any other.
func TestDataRefForRemoteURL(t *testing.T) {
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		if got := dataRefForRemoteURL("git+file:///srv/ledgers", ref); got != ref {
			t.Errorf("git+file with %s: got %q", ref, got)
		}
		if got := dataRefForRemoteURL("/srv/ledgers.git", ref); got != ref {
			t.Errorf(".git path with %s: got %q", ref, got)
		}
		for _, u := range []string{"file:///srv/doltremote", "dolthub://org/repo", "http://myserver:7007/mydb", "aws://bucket/db"} {
			if got := dataRefForRemoteURL(u, ref); got != "" {
				t.Errorf("%s with %s: got %q, want the ref dropped", u, ref, got)
			}
		}
	}
	if got := dataRefForRemoteURL("dolthub://org/repo", ""); got != "" {
		t.Errorf("no ref: got %q", got)
	}
}

// Pointing sync.remote at a URL that cannot carry the configured key names
// the stale key; a git-backed URL or no key says nothing.
func TestSyncRemoteRefIgnoredHint(t *testing.T) {
	hint, ok := syncRemoteRefIgnoredHint("file:///srv/doltremote", "refs/heads/issue-data")
	if !ok || !strings.Contains(hint.Message, syncRemoteRefKey) || !strings.Contains(hint.Message, "file:///srv/doltremote") || hint.Command == "" {
		t.Fatalf("non-git URL with a key: hint = %+v, ok = %v", hint, ok)
	}
	if _, ok := syncRemoteRefIgnoredHint("git+ssh://git@host/org/ledgers.git", "refs/dolt/units/team-12542"); ok {
		t.Error("git-backed URL with a key should not warn")
	}
	if _, ok := syncRemoteRefIgnoredHint("file:///srv/doltremote", ""); ok {
		t.Error("no key should not warn")
	}
	if effects := checkConfigSetSideEffects("sync.remote", "git+file:///srv/ledgers"); len(effects) != 0 {
		t.Errorf("sync.remote set with no key configured: effects = %+v", effects)
	}
}

// Only the exact data ref and the info ref survive ls-remote parsing: git
// matches its arguments as patterns, and reset-data must never delete a
// neighbor.
func TestParseDoltDataRefsKeepsExactRefsOnly(t *testing.T) {
	out := []byte("aaa\trefs/dolt/units/team-12542\n" +
		"bbb\trefs/dolt/units/team-12542-archive\n" +
		"ccc\trefs/heads/__dolt_remote_info__\n" +
		"ddd\trefs/dolt/data\n")
	got := parseDoltDataRefs(out, "refs/dolt/units/team-12542")
	want := []string{"refs/dolt/units/team-12542", "refs/heads/__dolt_remote_info__"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDoltDataRefs = %v, want %v", got, want)
	}
	if got := parseDoltDataRefs(out, "refs/dolt/data"); !reflect.DeepEqual(got, []string{"refs/heads/__dolt_remote_info__", "refs/dolt/data"}) {
		t.Fatalf("default ref: parseDoltDataRefs = %v", got)
	}
}

func withNonTerminalStdin(t *testing.T) {
	t.Helper()
	prev := remoteAddStdinIsTerminal
	remoteAddStdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })
}

func neverConfirm(t *testing.T) doltRemoteOverwriteConfirmer {
	t.Helper()
	return func(surface, name, existingURL, newURL string) bool {
		t.Fatalf("confirm must not be called: %q %q %q %q", surface, name, existingURL, newURL)
		return false
	}
}

// A new remote with a ref is added with that ref, for both ref shapes.
func TestEnsureDoltRemoteWithRefAddsNewRemoteOnRef(t *testing.T) {
	for _, ref := range []string{"refs/heads/beads-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			store := &fakeDoltRemoteAddStore{}
			result, err := ensureDoltRemoteWithRef(context.Background(), store, "origin", "git+https://example.com/repo.git", ref, false, neverConfirm(t))
			if err != nil || result.Canceled {
				t.Fatalf("ensureDoltRemoteWithRef = %+v, %v", result, err)
			}
			want := []string{"list", "persisted", "add origin git+https://example.com/repo.git " + ref}
			if !reflect.DeepEqual(store.calls, want) && !reflect.DeepEqual(store.calls, want[:1:1]) {
				// persistedRemoteInfosFor may or may not consult the fake; the add must carry the ref.
				last := store.calls[len(store.calls)-1]
				if last != want[2] {
					t.Fatalf("calls = %v, want the add to carry the ref: %q", store.calls, want[2])
				}
			}
		})
	}
}

// The same URL on the same ref is a no-op, and an unset ref equals an
// explicit refs/dolt/data.
func TestEnsureDoltRemoteWithRefSameURLAndRefIsNoop(t *testing.T) {
	tests := []struct{ existing, requested string }{
		{"refs/dolt/units/team-12542", "refs/dolt/units/team-12542"},
		{"", ""},
		{"", "refs/dolt/data"},
		{"refs/dolt/data", ""},
	}
	for _, tt := range tests {
		store := &fakeDoltRemoteAddStore{remotes: []storage.RemoteInfo{
			{Name: "origin", URL: "git+https://example.com/repo.git", Ref: tt.existing},
		}}
		result, err := ensureDoltRemoteWithRef(context.Background(), store, "origin", "git+https://example.com/repo.git", tt.requested, false, neverConfirm(t))
		if err != nil || result.Canceled {
			t.Fatalf("existing %q requested %q: result %+v, err %v", tt.existing, tt.requested, result, err)
		}
		if want := []string{"list"}; !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("existing %q requested %q: calls = %v, want %v", tt.existing, tt.requested, store.calls, want)
		}
	}
}

// A ref change without a terminal and without --yes is refused, naming both
// refs, and nothing is written: a CI step cannot move a remote back to the
// default ref silently.
func TestEnsureDoltRemoteWithRefDifferentRefNonInteractiveRefuses(t *testing.T) {
	withNonTerminalStdin(t)
	tests := []struct{ existing, requested string }{
		{"refs/dolt/units/team-12542", ""},
		{"", "refs/heads/beads-data"},
		{"refs/heads/beads-data", "refs/dolt/units/team-12542"},
	}
	for _, tt := range tests {
		store := &fakeDoltRemoteAddStore{remotes: []storage.RemoteInfo{
			{Name: "origin", URL: "git+https://example.com/repo.git", Ref: tt.existing},
		}}
		_, err := ensureDoltRemoteWithRef(context.Background(), store, "origin", "git+https://example.com/repo.git", tt.requested, false, neverConfirm(t))
		if err == nil {
			t.Fatalf("existing %q requested %q: want refusal, got nil", tt.existing, tt.requested)
		}
		for _, want := range []string{storage.EffectiveGitDataRef(tt.existing), storage.EffectiveGitDataRef(tt.requested), "--yes"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should contain %q", err, want)
			}
		}
		if want := []string{"list"}; !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("refusal must not write: calls = %v", store.calls)
		}
	}
}

// With --yes the ref change replaces the remote without a prompt.
func TestEnsureDoltRemoteWithRefDifferentRefWithYesReplaces(t *testing.T) {
	withNonTerminalStdin(t)
	store := &fakeDoltRemoteAddStore{remotes: []storage.RemoteInfo{
		{Name: "origin", URL: "git+https://example.com/repo.git", Ref: "refs/heads/beads-data"},
	}}
	result, err := ensureDoltRemoteWithRef(context.Background(), store, "origin", "git+https://example.com/repo.git", "refs/dolt/units/team-12542", true, neverConfirm(t))
	if err != nil || result.Canceled {
		t.Fatalf("ensureDoltRemoteWithRef = %+v, %v", result, err)
	}
	want := []string{"list", "remove origin", "add origin git+https://example.com/repo.git refs/dolt/units/team-12542"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

// On a terminal the ref change prompts with both refs named, and a declined
// prompt cancels without writing.
func TestEnsureDoltRemoteWithRefDifferentRefPromptsOnTerminal(t *testing.T) {
	prev := remoteAddStdinIsTerminal
	remoteAddStdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })

	store := &fakeDoltRemoteAddStore{remotes: []storage.RemoteInfo{
		{Name: "origin", URL: "git+https://example.com/repo.git", Ref: ""},
	}}
	prompted := false
	result, err := ensureDoltRemoteWithRef(context.Background(), store, "origin", "git+https://example.com/repo.git", "refs/dolt/units/team-12542", false, func(surface, name, existingDesc, newDesc string) bool {
		prompted = true
		if !strings.Contains(existingDesc, "refs/dolt/data") || !strings.Contains(newDesc, "refs/dolt/units/team-12542") {
			t.Fatalf("prompt should name both refs: %q -> %q", existingDesc, newDesc)
		}
		return false
	})
	if err != nil {
		t.Fatalf("ensureDoltRemoteWithRef: %v", err)
	}
	if !prompted || !result.Canceled {
		t.Fatalf("declined prompt should cancel: prompted=%v result=%+v", prompted, result)
	}
	if want := []string{"list"}; !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("canceled add must not write: calls = %v", store.calls)
	}
}

// The origin probe looks at the configured ref: data on refs/dolt/units/<key>
// is found there and not on the default, and the other way round.
func TestGitRemoteHasDoltDataRefAtStatus(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitForBootstrapTest(t, "", "init", "--bare", bareDir)
	seed := t.TempDir()
	runGitForBootstrapTest(t, seed, "init", "-b", "main")
	runGitForBootstrapTest(t, seed, "config", "user.email", "test@test.com")
	runGitForBootstrapTest(t, seed, "config", "user.name", "Test User")
	runGitForBootstrapTest(t, seed, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, seed, "push", bareDir, "HEAD:refs/dolt/units/team-12542")

	repoDir := t.TempDir()
	runGitForBootstrapTest(t, repoDir, "init", "-b", "main")
	runGitForBootstrapTest(t, repoDir, "remote", "add", "origin", bareDir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	if has, err := gitRemoteHasDoltDataRefAtStatus("origin", "refs/dolt/units/team-12542"); err != nil || !has {
		t.Fatalf("custom ref: has=%v err=%v, want true", has, err)
	}
	if has, err := gitRemoteHasDoltDataRefAtStatus("origin", ""); err != nil || has {
		t.Fatalf("default ref: has=%v err=%v, want false", has, err)
	}
	if has, err := gitRemoteHasDoltDataRefAtStatus("origin", "refs/heads/beads-data"); err != nil || has {
		t.Fatalf("other branch: has=%v err=%v, want false", has, err)
	}
}
