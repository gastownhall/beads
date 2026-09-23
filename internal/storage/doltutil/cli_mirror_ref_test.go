package doltutil

import (
	"fmt"
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

// deadPID returns the pid of a process that has exited: the test binary run
// with a pattern no test matches. The kernel does not reuse a pid within the
// time this test takes.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run a short-lived process: %v", err)
	}
	return cmd.Process.Pid
}

// A probe left behind by a run killed between its add and its remove is
// swept before the next change to the mirror, so it does not stay in the
// state file (and in every remote listing) until removed by hand: this
// process's own leftover always, a dead run's where the platform can tell it
// from a live one (sweepsOtherRunsProbes). A remote that only shares the
// prefix, and the probe of a run still alive, are not touched.
func TestCLIMirrorSweepsLeftoverProbes(t *testing.T) {
	dir := initDoltRepo(t)
	url := "git+https://example.com/repo.git"
	if err := AddCLIRemote(dir, "origin", url); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}
	ownLeftover := fmt.Sprintf("%s%d", cliRefProbePrefix, os.Getpid())
	deadLeftover := fmt.Sprintf("%s%d", cliRefProbePrefix, deadPID(t))
	liveProbe := fmt.Sprintf("%s%d", cliRefProbePrefix, os.Getppid())
	userRemote := cliRefProbePrefix + "backup"
	for _, name := range []string{ownLeftover, deadLeftover, liveProbe, userRemote} {
		if err := AddCLIRemoteWithRef(dir, name, url, "refs/heads/issue-data"); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}
	if err := EnsureCLIRemote(dir, "origin", url, "refs/dolt/units/team-12542"); err != nil {
		t.Fatalf("EnsureCLIRemote: %v", err)
	}
	remotes, err := PersistedRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	for _, r := range remotes {
		left[r.Name] = true
	}
	if left[ownLeftover] {
		t.Errorf("%s should have been swept", ownLeftover)
	}
	if left[deadLeftover] == sweepsOtherRunsProbes {
		t.Errorf("%s left=%v, want the opposite: a dead run's probe is swept only where the platform can tell it is dead", deadLeftover, left[deadLeftover])
	}
	for _, kept := range []string{liveProbe, userRemote, "origin"} {
		if !left[kept] {
			t.Errorf("%s must not be swept", kept)
		}
	}
	if got, _ := FindCLIRemoteRef(dir, "origin"); got != "refs/dolt/units/team-12542" {
		t.Errorf("origin Ref = %q after re-materialization", got)
	}

	// The sweep runs before any change to the mirror, a move back to the
	// default ref included, and not only ahead of a probe. The own name is
	// used so the case holds on every platform.
	if err := AddCLIRemoteWithRef(dir, ownLeftover, url, "refs/heads/issue-data"); err != nil {
		t.Fatalf("plant %s again: %v", ownLeftover, err)
	}
	if err := EnsureCLIRemote(dir, "origin", url, ""); err != nil {
		t.Fatalf("EnsureCLIRemote(back to the default): %v", err)
	}
	remotes, err = PersistedRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range remotes {
		if r.Name == ownLeftover {
			t.Errorf("%s should have been swept by the move to the default ref", ownLeftover)
		}
	}
}

// An invalid ref is refused by EnsureCLIRemote before the mirror is touched
// even when a remote exists, the case where the check used to run inside the
// probe and come back worded as the server refusing parameters; the existing
// remote is left as it was.
func TestCLIMirrorInvalidRefIsRefusedBeforeProbe(t *testing.T) {
	dir := initDoltRepo(t)
	url := "git+https://example.com/repo.git"
	if err := AddCLIRemote(dir, "origin", url); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}
	err := EnsureCLIRemote(dir, "origin", url, "refs/heads/issue data")
	if err == nil || !strings.Contains(err.Error(), "invalid git data ref") || strings.Contains(err.Error(), "refuses remote parameters") {
		t.Errorf("EnsureCLIRemote with an invalid ref over an existing remote = %v, want the argument check's own error", err)
	}
	if got := FindCLIRemote(dir, "origin"); got != url {
		t.Errorf("origin URL after the refusal = %q, want %q untouched", got, url)
	}
	if got, err := FindCLIRemoteRef(dir, "origin"); err != nil || got != "" {
		t.Errorf("origin Ref after the refusal = %q, %v, want the default ref untouched", got, err)
	}
}

// leftoverProbe admits this process's own name and, where the platform can
// tell (sweepsOtherRunsProbes), the exact probe shape for a pid that is not
// running; a prefix alone, a padded or signed number, and a live pid are not
// leftovers. Needs no dolt binary.
func TestLeftoverProbe(t *testing.T) {
	own := fmt.Sprintf("%s%d", cliRefProbePrefix, os.Getpid())
	dead := deadPID(t)
	if !leftoverProbe(own, own) {
		t.Errorf("leftoverProbe(%q) = false, want true", own)
	}
	if deadName := fmt.Sprintf("%s%d", cliRefProbePrefix, dead); leftoverProbe(deadName, own) != sweepsOtherRunsProbes {
		t.Errorf("leftoverProbe(%q) = %v, want %v on this platform", deadName, !sweepsOtherRunsProbes, sweepsOtherRunsProbes)
	}
	for _, name := range []string{
		cliRefProbePrefix + "backup", cliRefProbePrefix, cliRefProbePrefix + "0", cliRefProbePrefix + "-5",
		fmt.Sprintf("%s0%d", cliRefProbePrefix, dead), fmt.Sprintf("%s%d ", cliRefProbePrefix, dead),
		fmt.Sprintf("%s%d", cliRefProbePrefix, os.Getppid()), "origin",
	} {
		if leftoverProbe(name, own) {
			t.Errorf("leftoverProbe(%q) = true, want false", name)
		}
	}
}

// When the state file cannot be read at sweep time, the sweep reports that
// read, not a refusal of remote parameters by the server. The sweep is called
// directly because through EnsureCLIRemote the same file is read a moment
// earlier by FindCLIRemoteRef, which refuses first. Needs no dolt binary: the
// error is returned before any dolt invocation.
func TestCLIMirrorSweepReportsUnreadableState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dolt", "repo_state.json"), []byte(`{"remotes": not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := sweepLeftoverProbes(dir)
	if err == nil {
		t.Fatal("sweepLeftoverProbes on a corrupt state file = nil, want the read error")
	}
	if !strings.Contains(err.Error(), "recorded remotes") || strings.Contains(err.Error(), "refuses remote parameters") {
		t.Fatalf("sweepLeftoverProbes error must name the state-file read, not the server's refusal: %v", err)
	}
}

// A leftover under this process's own name that cannot be removed is reported
// as that removal, never as the server refusing parameters: here the state
// file names it but no dolt repository backs it, so the removal fails.
// Reached through EnsureCLIRemote: the failed CLI listing is discarded, the
// state file is read and shows no origin, and the sweep runs before the add.
// Needs no dolt binary (without one, the removal fails on the missing
// binary).
func TestCLIMirrorOwnLeftoverRemovalFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	own := ownCLIRefProbe()
	state := `{"head":"refs/heads/main","remotes":{"` + own + `":{"name":"` + own + `","url":"git+https://example.com/repo.git","fetch_specs":[],"params":{"git_ref":"refs/heads/issue-data"}}},"backups":{},"branches":{}}`
	if err := os.WriteFile(filepath.Join(dir, ".dolt", "repo_state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EnsureCLIRemote(dir, "origin", "git+https://example.com/repo.git", "refs/heads/issue-data")
	if err == nil || !strings.Contains(err.Error(), "remove leftover ref probe remote "+own) || strings.Contains(err.Error(), "refuses remote parameters") {
		t.Fatalf("EnsureCLIRemote = %v, want the own-leftover removal failure", err)
	}
}

// An invalid ref is refused by EnsureCLIRemote itself, before the state file
// is read and before any dolt invocation, and not under the probe's wording
// about a server refusing parameters. The state file is made unreadable so
// that only the check at the top of EnsureCLIRemote can produce this error:
// without it, the read of the recorded ref fails first. Needs no dolt binary.
func TestCLIMirrorInvalidRefIsRefusedUnwrapped(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dolt", "repo_state.json"), []byte(`{"remotes": not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EnsureCLIRemote(dir, "origin", "git+https://example.com/repo.git", "refs/heads/issue data")
	if err == nil || !strings.Contains(err.Error(), "invalid git data ref") || strings.Contains(err.Error(), "recorded ref") || strings.Contains(err.Error(), "refuses remote parameters") {
		t.Fatalf("EnsureCLIRemote with an invalid ref = %v, want the argument check's own error before the state file is read", err)
	}
}

// The argument check refuses the bytes git's ref rules refuse, a space, an
// ASCII control character, DEL, plus a leading dash so the value cannot be
// mistaken for an option; dolt accepts all of them through exec, so this is
// bd policy at the argv boundary. A non-ASCII space (U+00A0, U+2003, U+0085)
// is a valid ref byte for git check-ref-format and for dolt remote add
// --ref, and passes: this layer adds no ref-name rules of its own. Both ref
// shapes and the empty default pass.
func TestValidateGitDataRefArg(t *testing.T) {
	for _, ok := range []string{
		"", "refs/heads/issue-data", "refs/dolt/units/team-12542", "refs/dolt/data",
		"refs/heads/issue\u00a0data", "refs/heads/issue\u2003data", "refs/heads/issue\u0085data",
	} {
		if err := ValidateGitDataRefArg(ok); err != nil {
			t.Errorf("ValidateGitDataRefArg(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"-refs/heads/x", "--ref", "refs/heads/issue data", "refs/heads/issue\tdata", "refs/heads/issue\ndata",
		"refs/heads/issue\x7fdata", "refs/heads/issue\x1bdata", "refs/heads/issue\x00data",
	} {
		if err := ValidateGitDataRefArg(bad); err == nil {
			t.Errorf("ValidateGitDataRefArg(%q) = nil, want an error", bad)
		}
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
