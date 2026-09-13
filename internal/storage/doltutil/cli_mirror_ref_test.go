package doltutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// initDoltRepo creates an empty Dolt repository in a temp directory with the
// dolt CLI. Skips when the CLI is absent.
func initDoltRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt CLI not available")
	}
	dir := t.TempDir()
	cmd := exec.Command("dolt", "init", "--name", "bd-test", "--email", "bd-test@example.com")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}
	return dir
}

func readRepoState(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".dolt", "repo_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The CLI mirror that server-mode push, pull, and fetch shell out through
// must sit on the same git data ref as the SQL-visible remote. EnsureCLIRemote
// re-materializes the mirror when the ref differs, including a change to or
// from the default, and leaves the state file untouched when URL and ref
// already match.
func TestCLIMirrorFollowsGitDataRef(t *testing.T) {
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			dir := initDoltRepo(t)
			url := "git+https://example.com/repo.git"

			if err := EnsureCLIRemote(dir, "origin", url, ""); err != nil {
				t.Fatalf("EnsureCLIRemote(default ref): %v", err)
			}
			if got, _ := FindCLIRemoteRef(dir, "origin"); got != "" {
				t.Fatalf("default-ref mirror records Ref %q, want empty", got)
			}

			if err := EnsureCLIRemote(dir, "origin", url, ref); err != nil {
				t.Fatalf("EnsureCLIRemote(%s): %v", ref, err)
			}
			if got, _ := FindCLIRemoteRef(dir, "origin"); got != ref {
				t.Fatalf("after ref change, mirror Ref = %q, want %q", got, ref)
			}
			if got := FindCLIRemote(dir, "origin"); !RemoteURLsMatch(got, url) {
				t.Fatalf("after ref change, mirror URL = %q, want %q", got, url)
			}

			before := readRepoState(t, dir)
			if err := EnsureCLIRemote(dir, "origin", url, ref); err != nil {
				t.Fatalf("EnsureCLIRemote(same URL and ref): %v", err)
			}
			if after := readRepoState(t, dir); after != before {
				t.Errorf("matching URL and ref must leave repo_state.json untouched:\nbefore: %s\nafter:  %s", before, after)
			}

			if err := EnsureCLIRemote(dir, "origin", url, ""); err != nil {
				t.Fatalf("EnsureCLIRemote(back to the default): %v", err)
			}
			if got, _ := FindCLIRemoteRef(dir, "origin"); got != "" {
				t.Errorf("back on the default, mirror should record no git_ref, got %q", got)
			}
			if probes := probeRemotesLeft(t, dir); probes != 0 {
				t.Errorf("%d probe remote(s) left behind", probes)
			}

			if err := EnsureCLIRemote(dir, "origin", url, "refs/dolt/data"); err != nil {
				t.Fatalf("EnsureCLIRemote(explicit default): %v", err)
			}
			// Dolt records the explicit value; it is the comparison that treats it
			// as the default.
			if got, _ := FindCLIRemoteRef(dir, "origin"); !storage.RemoteRefsMatch(got, "") {
				t.Errorf("explicit default ref should compare equal to the default, got %q", got)
			}
		})
	}
}

// Dolt refuses --ref for a remote that is not git-backed; the CLI mirror
// surfaces that refusal instead of adding the remote on the default ref.
func TestCLIMirrorRefRefusedForNonGitURL(t *testing.T) {
	dir := initDoltRepo(t)
	err := AddCLIRemoteWithRef(dir, "backup", "file://"+t.TempDir(), "refs/heads/issue-data")
	if err == nil {
		t.Fatal("AddCLIRemoteWithRef on file:// with a ref = nil, want error")
	}
	if !strings.Contains(err.Error(), "git remotes") {
		t.Errorf("error should carry Dolt's --ref refusal, got: %v", err)
	}
	if got := FindCLIRemote(dir, "backup"); got != "" {
		t.Errorf("refused remote must not exist, found %q", got)
	}
}

// probeRemotesLeft counts remotes named like EnsureCLIRemote's ref probe
// still recorded in dir; the probe must always be removed again.
func probeRemotesLeft(t *testing.T, dir string) int {
	t.Helper()
	remotes, err := PersistedRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range remotes {
		if strings.HasPrefix(r.Name, cliRefProbePrefix) {
			n++
		}
	}
	return n
}

// The probe runs before the real remote is removed, so a refused --ref (here
// dolt's own refusal on a file:// URL stands in for the proxied server's)
// leaves the remote exactly as it was and no probe behind. Reordering the
// probe after the removal, or dropping it, fails this test.
func TestCLIMirrorRefusedProbeLeavesRemoteInPlace(t *testing.T) {
	dir := initDoltRepo(t)
	url := "file://" + t.TempDir()
	if err := AddCLIRemote(dir, "origin", url); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}
	err := EnsureCLIRemote(dir, "origin", url, "refs/heads/issue-data")
	if err == nil || !strings.Contains(err.Error(), "cannot record git data ref") {
		t.Fatalf("EnsureCLIRemote with a ref on file:// = %v, want the probe's refusal", err)
	}
	if got := FindCLIRemote(dir, "origin"); !RemoteURLsMatch(got, url) {
		t.Fatalf("origin must survive the refused probe, found %q", got)
	}
	if probes := probeRemotesLeft(t, dir); probes != 0 {
		t.Errorf("%d probe remote(s) left behind", probes)
	}
}

// A state file that cannot be parsed is an error out of EnsureCLIRemote,
// before any dolt mutation: treating it as the default ref would remove a
// ref remote and re-add it on refs/dolt/data. Needs no dolt binary.
func TestCLIMirrorUnreadableStateIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dolt", "repo_state.json"), []byte(`{"remotes": not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindCLIRemoteRef(dir, "origin"); err == nil {
		t.Fatal("FindCLIRemoteRef on a corrupt state file = nil error, want error")
	}
	err := EnsureCLIRemote(dir, "origin", "git+https://example.com/repo.git", "refs/dolt/units/team-12542")
	if err == nil || !strings.Contains(err.Error(), "recorded ref") {
		t.Fatalf("EnsureCLIRemote on a corrupt state file = %v, want the read error", err)
	}
}
