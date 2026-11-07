package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

// isolateGlobalGitConfig redirects git's config search path to a fresh temp home
// so an ambient global `git config --global beads.role` cannot reach the role
// checks, and returns that home.
//
// The role reads run git with gitenv.ScrubRoutingAndSuppression, which strips
// every GIT_CONFIG* entry from the subprocess environment, so the usual
// GIT_CONFIG_GLOBAL=/dev/null suppression cannot work here — the search path
// itself has to move. System scope (/etc/gitconfig, or PROGRAMDATA on Windows)
// stays visible either way: that scrub takes GIT_CONFIG_NOSYSTEM with it, so no
// env-based way to close system scope exists from a test in this package. It is
// inert for beads.role today and the limitation is shared with every caller on
// this boundary; only a production-side config-scope override could close it.
func isolateGlobalGitConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestCheckBeadsRole_NotConfigured(t *testing.T) {
	// Isolate from ambient global git config (e.g. a developer machine with
	// `git config --global beads.role contributor` set).
	isolateGlobalGitConfig(t)

	// Create a temp directory with git init but no beads.role config
	tmpDir := newGitRepo(t)

	// Check role - should return warning since not configured
	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Name != "Role Configuration" {
		t.Errorf("expected name 'Role Configuration', got %q", check.Name)
	}
	if check.Fix != "git config beads.role maintainer" {
		t.Errorf("expected fix 'git config beads.role maintainer', got %q", check.Fix)
	}
}

func TestCheckBeadsRole_Maintainer(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to maintainer
	cmd := gitCommand("config", "beads.role", "maintainer")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "Configured as maintainer" {
		t.Errorf("expected message 'Configured as maintainer', got %q", check.Message)
	}
}

func TestCheckBeadsRole_Contributor(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to contributor
	cmd := gitCommand("config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "Configured as contributor" {
		t.Errorf("expected message 'Configured as contributor', got %q", check.Message)
	}
}

func TestCheckBeadsRole_InvalidValue(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to an invalid value
	cmd := gitCommand("config", "beads.role", "admin")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Fix != "bd init" {
		t.Errorf("expected fix 'bd init', got %q", check.Fix)
	}
}

func TestCheckBeadsRole_NotGitRepo(t *testing.T) {
	// Isolate from ambient global git config: "git config --get" reads global
	// scope even outside a git repo, so without this an ambient
	// `git config --global beads.role` leaks into a check that should be N/A.
	isolateGlobalGitConfig(t)

	tmpDir, err := os.MkdirTemp("", "beads-role-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Don't initialize git - just a plain directory
	check := CheckBeadsRole(tmpDir)

	// Should return OK/N/A since we're not in a git repo — the role may
	// be correctly configured in a worktree (e.g., rig roots use .repo.git).
	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "N/A (not a git repository)" {
		t.Errorf("expected message 'N/A (not a git repository)', got %q", check.Message)
	}
}

func TestCheckBeadsRole_NonexistentPath(t *testing.T) {
	// Test with a path that doesn't exist — git will report "not a git repository"
	check := CheckBeadsRole(filepath.Join(os.TempDir(), "nonexistent-beads-test-dir"))

	// Should return OK/N/A since the path is not a git repository. Status alone
	// cannot separate that from an ambient role leaking in: "Configured as
	// maintainer" is StatusOK too, so the message is the discriminating claim.
	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "N/A (not a git repository)" {
		t.Errorf("expected message 'N/A (not a git repository)', got %q", check.Message)
	}
}

func TestCheckBeadsRoleIgnoresInheritedRouting(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("BEADS_DIR", "")
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)
	for _, tc := range []struct {
		name, role, global, status, message string
	}{
		{"repository", "maintainer", "", StatusOK, "Configured as maintainer"},
		{"inline", "maintainer", "", StatusOK, "Configured as maintainer"},
		{"global_override", "", "maintainer", StatusOK, "Configured as maintainer"},
		{"invalid", "admin", "", StatusWarning, "Invalid beads.role value: \"admin\""},
		{"absent", "", "", StatusWarning, "beads.role not configured"},
		{"nonrepository", "", "", StatusOK, "N/A (not a git repository)"},
		{"default_global", "", "contributor", StatusOK, "Configured as contributor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateGlobalGitConfig(t)
			if tc.global != "" {
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\nrole = "+tc.global+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target, decoy := newGitRepo(t), newGitRepo(t)
			runGit := func(repo string, args ...string) {
				t.Helper()
				cmd := gitCommand(args...)
				cmd.Dir, cmd.Env = repo, gitenv.ScrubRouting(os.Environ())
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture git %v: %v: %s", args, err, out)
				}
			}
			if tc.role != "" {
				runGit(target, "config", "beads.role", tc.role)
			}
			if tc.name != "nonrepository" {
				runGit(decoy, "config", "beads.role", "decoy-role")
			} else {
				target = t.TempDir()
			}
			poison := map[string]string{"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy}
			switch tc.name {
			case "inline", "default_global":
				poison = map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "beads.role", "GIT_CONFIG_VALUE_0": "injected-role"}
			case "global_override":
				config := filepath.Join(home, "nondefault")
				if err := os.WriteFile(config, []byte("[beads]\nrole = injected-role\n"), 0600); err != nil {
					t.Fatal(err)
				}
				poison = map[string]string{"GIT_CONFIG_GLOBAL": config}
			}
			for key, value := range poison {
				t.Setenv(key, value)
			}
			for _, api := range []struct {
				name string
				run  func(string) DoctorCheck
			}{
				{"standalone", CheckBeadsRole},
				{"nil shared store", func(path string) DoctorCheck { return CheckBeadsRoleWithStore(path, nil) }},
				{"empty shared store", func(path string) DoctorCheck { return CheckBeadsRoleWithStore(path, &SharedStore{}) }},
			} {
				check := api.run(target)
				if check.Status != tc.status || check.Message != tc.message || check.Name != "Role Configuration" || check.Category != CategoryData {
					t.Errorf("%s: got %+v; want status=%q message=%q", api.name, check, tc.status, tc.message)
				}
			}
			for key, value := range poison {
				if os.Getenv(key) != value {
					t.Errorf("doctor changed parent environment %s", key)
				}
			}
		})
	}
}

// TestCheckBeadsRole_MalformedGitConfigIsReported pins bd-a2qnf4 (GH#6765):
// a repository whose local Git config cannot be parsed must surface the read
// failure, not be reported as an unconfigured role or as "not a git
// repository". Absence stays benign (TestCheckBeadsRole_NotConfigured).
func TestCheckBeadsRole_MalformedGitConfigIsReported(t *testing.T) {
	isolateGlobalGitConfig(t)
	tmpDir := newGitRepo(t)

	gitDir := filepath.Join(tmpDir, ".git")
	cfg, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		t.Fatalf("read git config: %v", err)
	}
	cfg = append(cfg, []byte("\n[beads\n\trole = maintainer\n")...)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), cfg, 0o644); err != nil {
		t.Fatalf("write malformed git config: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status == StatusOK {
		t.Errorf("malformed git config reported as OK: status=%s message=%q", check.Status, check.Message)
	}
	if check.Message == "N/A (not a git repository)" || check.Message == "beads.role not configured" {
		t.Errorf("malformed git config folded into absence: status=%s message=%q", check.Status, check.Message)
	}
}

// TestCheckBeadsRoleReadFailureWarnsWithDiagnostic extends the repro above to
// both entry points: a failed read is a warning that carries git's diagnostic
// and points at the config files, and it is decided before the database
// fallback or the config-sensitive repository probe can answer. A supplied
// path that does not exist is still N/A, as before.
func TestCheckBeadsRoleReadFailureWarnsWithDiagnostic(t *testing.T) {
	apis := []struct {
		name string
		run  func(string) DoctorCheck
	}{
		{"standalone", CheckBeadsRole},
		{"nil shared store", func(path string) DoctorCheck { return CheckBeadsRoleWithStore(path, nil) }},
		{"empty shared store", func(path string) DoctorCheck { return CheckBeadsRoleWithStore(path, &SharedStore{}) }},
	}

	t.Run("malformed config", func(t *testing.T) {
		isolateGlobalGitConfig(t)
		repo := newGitRepo(t)
		configPath := filepath.Join(repo, ".git", "config")
		cfg, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, append(cfg, "\n[beads\n"...), 0o600); err != nil {
			t.Fatal(err)
		}
		// The database fallback would answer here if it were consulted.
		if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, api := range apis {
			check := api.run(repo)
			if check.Status != StatusWarning || check.Name != "Role Configuration" || check.Category != CategoryData {
				t.Errorf("%s: got %+v, want a Role Configuration warning", api.name, check)
			}
			if !strings.Contains(check.Message, "could not be read") {
				t.Errorf("%s: message %q does not report a read failure", api.name, check.Message)
			}
			if !strings.Contains(check.Detail, "bad config") {
				t.Errorf("%s: detail %q does not carry git's diagnostic", api.name, check.Detail)
			}
			if check.Fix != "git config --list --show-origin" {
				t.Errorf("%s: fix %q, want the config inspection command", api.name, check.Fix)
			}
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		isolateGlobalGitConfig(t)
		missing := filepath.Join(t.TempDir(), "missing")
		for _, api := range apis {
			if check := api.run(missing); check.Status != StatusOK || check.Message != "N/A (not a git repository)" {
				t.Errorf("%s: got %+v, want N/A for a path that does not exist", api.name, check)
			}
		}
	})

	// Git-free usage: with no git executable the read never ran, so the check
	// answers as it always has (N/A here), not with a read failure.
	t.Run("no git executable", func(t *testing.T) {
		isolateGlobalGitConfig(t)
		repo := newGitRepo(t)
		t.Setenv("PATH", t.TempDir())
		for _, api := range apis {
			if check := api.run(repo); check.Status != StatusOK || check.Message != "N/A (not a git repository)" {
				t.Errorf("%s: got %+v, want N/A without a git executable", api.name, check)
			}
		}
	})

	// The benign-noise control (GH#6763): git traces even an unset key's
	// lookup to stderr, so with tracing inherited an unset role must still be
	// reported as not configured, not as a read failure.
	t.Run("stderr tracing over an unset role", func(t *testing.T) {
		isolateGlobalGitConfig(t)
		repo := newGitRepo(t)
		t.Setenv("GIT_TRACE", "1")
		for _, api := range apis {
			if check := api.run(repo); check.Status != StatusWarning || check.Message != "beads.role not configured" {
				t.Errorf("%s: got %+v, want the not-configured warning", api.name, check)
			}
		}
	})
}
