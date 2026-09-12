package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

func TestCheckBeadsRole_NotConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

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
	cmd := exec.Command("git", "config", "beads.role", "maintainer")
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
	cmd := exec.Command("git", "config", "beads.role", "contributor")
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
	cmd := exec.Command("git", "config", "beads.role", "admin")
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

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

	// Should return OK/N/A since the path is not a git repository
	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
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
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			if tc.global != "" {
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\nrole = "+tc.global+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target, decoy := newGitRepo(t), newGitRepo(t)
			runGit := func(repo string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
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
