package beads

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withSSHConfigHome points HOME at a temporary home whose .ssh/config maps
// each alias to hostName, and returns the home directory. Tests using it
// must not run in parallel: they mutate the process environment.
func withSSHConfigHome(t *testing.T, aliases map[string]string) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for alias, hostName := range aliases {
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  User git\n\n", alias, hostName)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home
}

// injectSSHResolver replaces the ssh -G seam for the duration of the test.
func injectSSHResolver(t *testing.T, fn func(user, host, port string) (string, error)) {
	t.Helper()
	old := sshGResolveHost
	sshGResolveHost = fn
	t.Cleanup(func() { sshGResolveHost = old })
}

// fingerprintRepoForRemote creates a git repository whose origin remote is
// remote and returns its path.
func fingerprintRepoForRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	initFingerprintGitRepo(t, dir, remote)
	return dir
}

// TestComputeRepoIDForPath_SSHAliasesOfSameRepoAgree covers GH#5990's core
// ask: checkouts of one repository reached through supported SSH host
// aliases must fingerprint identically to the canonical-host SSH spellings
// and to the equivalent HTTPS URL, including matching port semantics. It
// exercises the real ssh -G path against a pinned $HOME config.
func TestComputeRepoIDForPath_SSHAliasesOfSameRepoAgree(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary on PATH; alias resolution cannot run")
	}
	withSSHConfigHome(t, map[string]string{
		"alias-a":         "github.com",
		"alias-b":         "github.com",
		"github-town":     "github.com",
		"github.com-work": "github.com",
	})

	repos := map[string]string{
		"https equivalent":  "https://github.com/org/repo.git",
		"canonical scp":     "git@github.com:org/repo.git",
		"canonical ssh://":  "ssh://git@github.com/org/repo.git",
		"alias-a scp":       "git@alias-a:org/repo.git",
		"alias-b scp":       "git@alias-b:org/repo.git",
		"hyphen alias":      "git@github-town:org/repo.git",
		"dotted alias":      "git@github.com-work:org/repo.git",
		"alias ssh://":      "ssh://git@alias-a/org/repo.git",
		"https custom port": "https://github.com:2222/org/repo.git",
		"alias custom port": "ssh://git@alias-a:2222/org/repo.git",
	}
	dirs := make(map[string]string, len(repos))
	for name, remote := range repos {
		dirs[name] = fingerprintRepoForRemote(t, remote)
	}

	want, err := ComputeRepoIDForPath(dirs["https equivalent"])
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(https): %v", err)
	}
	wantPort, err := ComputeRepoIDForPath(dirs["https custom port"])
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(https custom port): %v", err)
	}
	if want == wantPort {
		t.Fatal("https default-port and custom-port fingerprints unexpectedly equal")
	}

	for name, dir := range dirs {
		got, err := ComputeRepoIDForPath(dir)
		if err != nil {
			t.Errorf("ComputeRepoIDForPath(%s): %v", name, err)
			continue
		}
		wantID := want
		if name == "https custom port" || name == "alias custom port" {
			wantID = wantPort
		}
		if got != wantID {
			t.Errorf("repo id via %s = %s, want %s", name, got[:8], wantID[:8])
		}
	}
}

// TestComputeRepoFingerprint_AliasResolution drives the seam directly so the
// hash inputs are exact: full IDs, not prefixes, for the resolved and legacy
// values of every SSH spelling.
func TestComputeRepoFingerprint_AliasResolution(t *testing.T) {
	cases := []struct {
		name         string
		remote       string
		resolveTo    string // seam result; "" means the seam errors
		wantID       string // canonical string hashed for ID
		wantLegacy   string // canonical string hashed for LegacyID
		wantAlias    string
		wantHostName string
	}{
		{
			name:         "scp alias resolves",
			remote:       "git@alias-a:org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "scp alias without user",
			remote:       "alias-a:org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "written host case and trailing dot normalize",
			remote:       "git@Alias-A:Org/Repo.git",
			resolveTo:    "GitHub.COM.",
			wantID:       "github.com/Org/Repo",
			wantLegacy:   "alias-a/Org/Repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "ssh scheme with explicit port keeps port semantics",
			remote:       "ssh://git@alias-a:2222/org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com:2222/org/repo",
			wantLegacy:   "alias-a:2222/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "ssh scheme default port drops port",
			remote:       "ssh://git@alias-a:22/org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "git+ssh scheme",
			remote:       "git+ssh://git@alias-a/org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "ssh+git scheme",
			remote:       "ssh+git://git@alias-a/org/repo.git",
			resolveTo:    "github.com",
			wantID:       "github.com/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "github.com",
		},
		{
			name:         "resolved IP host is adopted",
			remote:       "git@alias-a:org/repo.git",
			resolveTo:    "10.1.2.3",
			wantID:       "10.1.2.3/org/repo",
			wantLegacy:   "alias-a/org/repo",
			wantAlias:    "alias-a",
			wantHostName: "10.1.2.3",
		},
		{
			name:       "hostname equal to written host is not an adoption",
			remote:     "git@alias-a:org/repo.git",
			resolveTo:  "alias-a",
			wantID:     "alias-a/org/repo",
			wantLegacy: "alias-a/org/repo",
		},
		{
			name:       "resolver error keeps legacy",
			remote:     "git@alias-a:org/repo.git",
			wantID:     "alias-a/org/repo",
			wantLegacy: "alias-a/org/repo",
		},
		{
			name:       "invalid resolved hostname keeps legacy",
			remote:     "git@alias-a:org/repo.git",
			resolveTo:  "not a host",
			wantID:     "alias-a/org/repo",
			wantLegacy: "alias-a/org/repo",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			injectSSHResolver(t, func(user, host, port string) (string, error) {
				if tt.resolveTo == "" {
					return "", errors.New("lookup unavailable")
				}
				return tt.resolveTo, nil
			})
			dir := fingerprintRepoForRemote(t, tt.remote)
			fp, err := ComputeRepoFingerprintForPath(dir)
			if err != nil {
				t.Fatalf("ComputeRepoFingerprintForPath: %v", err)
			}
			if fp.ID != repoIDHash(tt.wantID) {
				t.Errorf("ID = %s, want hash of %q", fp.ID, tt.wantID)
			}
			if fp.LegacyID != repoIDHash(tt.wantLegacy) {
				t.Errorf("LegacyID = %s, want hash of %q", fp.LegacyID, tt.wantLegacy)
			}
			if fp.SSHAlias != tt.wantAlias {
				t.Errorf("SSHAlias = %q, want %q", fp.SSHAlias, tt.wantAlias)
			}
			if fp.SSHHostName != tt.wantHostName {
				t.Errorf("SSHHostName = %q, want %q", fp.SSHHostName, tt.wantHostName)
			}
			if fp.Source != RepoIDSourceRemote {
				t.Errorf("Source = %q, want remote", fp.Source)
			}
		})
	}
}

// TestComputeRepoFingerprint_PathFallbackUnchanged pins that a checkout with
// no origin remote still fingerprints from the repository root path, with no
// SSH machinery involved.
func TestComputeRepoFingerprint_PathFallbackUnchanged(t *testing.T) {
	called := false
	injectSSHResolver(t, func(user, host, port string) (string, error) {
		called = true
		return "github.com", nil
	})
	dir := fingerprintRepoForRemote(t, "")
	fp, err := ComputeRepoFingerprintForPath(dir)
	if err != nil {
		t.Fatalf("ComputeRepoFingerprintForPath: %v", err)
	}
	if fp.Source != RepoIDSourcePath {
		t.Fatalf("Source = %q, want path", fp.Source)
	}
	if fp.ID != fp.LegacyID || fp.ID == "" {
		t.Fatalf("path fallback must set ID == LegacyID, got %q / %q", fp.ID, fp.LegacyID)
	}
	if fp.SSHAlias != "" || fp.SSHHostName != "" {
		t.Fatalf("path fallback must not report alias fields, got %q / %q", fp.SSHAlias, fp.SSHHostName)
	}
	if called {
		t.Fatal("resolver must not run without an origin remote")
	}
}

func TestParseSSHConfigHostName(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
		ok   bool
	}{
		{name: "plain", out: "user git\nhostname github.com\nport 22\n", want: "github.com", ok: true},
		{name: "uppercase key", out: "HOSTNAME github.com\n", want: "github.com", ok: true},
		{name: "mixed case key", out: "HostName github.com\n", want: "github.com", ok: true},
		{name: "crlf", out: "hostname github.com\r\nport 22\r\n", want: "github.com", ok: true},
		{name: "first hostname wins", out: "hostname github.com\nhostname evil.example\n", want: "github.com", ok: true},
		{name: "tab separated", out: "hostname\tgithub.com\n", want: "github.com", ok: true},
		{name: "absent", out: "user git\nport 22\n", ok: false},
		{name: "empty output", out: "", ok: false},
		{name: "empty value", out: "hostname \n", ok: false},
		{name: "value with space", out: "hostname two words\n", ok: false},
		{name: "option-like value", out: "hostname -oProxyCommand=evil\n", ok: false},
		{name: "bare key line", out: "hostname\n", ok: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSSHConfigHostName([]byte(tt.out))
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseSSHConfigHostName(%q) = (%q, %v), want (%q, %v)", tt.out, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestSplitSSHRemoteMatchesCanonicalize guards the parity the resolution
// depends on: for every SSH-family remote, rebuilding the canonical string
// from the split (lowercased host + port suffix + tail) must equal what
// canonicalizeGitURL itself produced, and non-SSH remotes must not split.
func TestSplitSSHRemoteMatchesCanonicalize(t *testing.T) {
	sshRemotes := []string{
		"git@github.com:user/repo.git",
		"git@github.com:user/repo",
		"git@github.com:user/repo/",
		"git@GitHub.COM:User/Repo.git",
		"github.com:user/repo.git",
		"deploy@gitlab.example.com:group/project.git",
		"git@alias-a:org/repo.git",
		"weird:user@host:org/repo.git",
		"git@host:",
		"ssh://git@github.com/user/repo.git",
		"ssh://git@github.com:22/user/repo.git",
		"ssh://git@gitlab.example.com:2222/user/repo.git",
		"git+ssh://git@alias-a/org/repo.git",
		"ssh+git://git@alias-a:2222/org/repo.git",
		"ssh://alias-a/org/repo.git",
	}
	for _, remote := range sshRemotes {
		split, ok := splitSSHRemote(remote)
		if !ok {
			t.Errorf("splitSSHRemote(%q) = false, want true", remote)
			continue
		}
		canonical, err := canonicalizeGitURL(remote)
		if err != nil {
			t.Errorf("canonicalizeGitURL(%q): %v", remote, err)
			continue
		}
		if got := split.legacyCanonicalFromSplit(); got != canonical {
			t.Errorf("split canonical for %q = %q, want %q", remote, got, canonical)
		}
	}

	for _, remote := range []string{
		"https://github.com/user/repo.git",
		"http://github.com/user/repo.git",
		"git://github.com/user/repo.git",
		"file:///srv/git/repo.git",
		"/srv/git/repo.git",
		"../relative/repo",
		"C:/Users/me/repo",
		"C:\\Users\\me\\repo",
	} {
		if _, ok := splitSSHRemote(remote); ok {
			t.Errorf("splitSSHRemote(%q) = true, want false", remote)
		}
	}

	// An IP-literal scp remote is an SSH transport and does split; lookup
	// policy must exclude it instead.
	split, ok := splitSSHRemote("git@10.1.2.3:org/repo.git")
	if !ok {
		t.Fatal("splitSSHRemote(IP scp) = false, want true")
	}
	if got := sshHostLookupPolicyFor(split.host); got != sshLookupNone {
		t.Errorf("sshHostLookupPolicyFor(%q) = %v, want sshLookupNone", split.host, got)
	}
}
