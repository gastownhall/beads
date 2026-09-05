package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestNormalizeRepoPathForStorage covers what `bd repo add` persists for each
// shape of argument. The relative cases are the be-d0t regression: a stored
// relative path re-resolves against whatever cwd a later command runs from.
func TestNormalizeRepoPathForStorage(t *testing.T) {
	tmpDir := t.TempDir()
	// macOS /var is a symlink to /private/var, and t.TempDir hands back the
	// unresolved form while os.Getwd returns the resolved one. Compare against
	// what the process actually reports as its working directory.
	t.Chdir(tmpDir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare name", "gastown", filepath.Join(cwd, "gastown")},
		{"dot relative", "./sibling", filepath.Join(cwd, "sibling")},
		{"parent relative", "../other-repo", filepath.Join(filepath.Dir(cwd), "other-repo")},
		{"absolute kept verbatim", "/srv/beads-planning", "/srv/beads-planning"},
		{"tilde kept verbatim", "~/beads-planning", "~/beads-planning"},
		{"bare tilde kept verbatim", "~", "~"},
		{"remote url kept verbatim", "https://github.com/steveyegge/beads.git", "https://github.com/steveyegge/beads.git"},
		{"empty kept verbatim", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRepoPathForStorage(tt.input)
			if err != nil {
				t.Fatalf("normalizeRepoPathForStorage(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("normalizeRepoPathForStorage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestNormalizeRepoPathForStorage_SurvivesCwdChange is the defect itself: the
// value `bd repo add` persists must name the same directory when a later
// command resolves it from somewhere else.
func TestNormalizeRepoPathForStorage_SurvivesCwdChange(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "planning")
	if err := os.MkdirAll(filepath.Join(target, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "workspace", "nested")
	if err := os.MkdirAll(subdir, 0o750); err != nil {
		t.Fatal(err)
	}

	// `bd repo add ../../planning` run from root/workspace/nested.
	t.Chdir(subdir)
	stored, err := normalizeRepoPathForStorage(filepath.Join("..", "..", "planning"))
	if err != nil {
		t.Fatalf("normalizeRepoPathForStorage: %v", err)
	}

	// A later command — `bd repo sync`, `bd doctor` — from a different cwd.
	t.Chdir(root)
	resolved, err := filepath.Abs(expandRepoTilde(stored))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resolved, ".beads")); err != nil {
		t.Fatalf("stored path %q does not resolve to the added workspace from a different cwd: %v", stored, err)
	}
}

func TestExpandRepoTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"tilde slash", "~/beads-planning", filepath.Join(home, "beads-planning")},
		{"bare tilde", "~", home},
		{"absolute untouched", "/srv/planning", "/srv/planning"},
		{"relative untouched", "../other-repo", "../other-repo"},
		{"empty untouched", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandRepoTilde(tt.input); got != tt.want {
				t.Errorf("expandRepoTilde(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestResolveConfiguredRepoPath checks that `bd repo remove` still finds the
// entry it is asked to drop, whether that entry was stored verbatim (added
// before paths were canonicalized, or added as "~/foo") or canonicalized.
func TestResolveConfiguredRepoPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	legacyRelative := "legacy-relative-entry"
	contents := "repos:\n  primary: \".\"\n  additional:\n    - \"~/beads-planning\"\n    - \"" +
		filepath.Join(cwd, "planning") + "\"\n    - \"" + legacyRelative + "\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"verbatim tilde entry", "~/beads-planning", "~/beads-planning"},
		{"relative arg matches stored absolute", "planning", filepath.Join(cwd, "planning")},
		{"legacy relative entry still removable", legacyRelative, legacyRelative},
		{"absolute arg matches itself", filepath.Join(cwd, "planning"), filepath.Join(cwd, "planning")},
		{"unconfigured arg returned as typed", "../nowhere", "../nowhere"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveConfiguredRepoPath(configPath, tt.input); got != tt.want {
				t.Errorf("resolveConfiguredRepoPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	t.Run("missing config returns arg unchanged", func(t *testing.T) {
		missing := filepath.Join(tmpDir, "does-not-exist.yaml")
		if got := resolveConfiguredRepoPath(missing, "planning"); got != "planning" {
			t.Errorf("resolveConfiguredRepoPath with missing config = %q, want %q", got, "planning")
		}
	})

	// Guard the assumption the resolution rests on: config.ListRepos returns the
	// entries verbatim, so a canonicalized argument can be compared to them.
	repos, err := config.ListRepos(configPath)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos.Additional) != 3 {
		t.Fatalf("expected 3 configured repos, got %d (%v)", len(repos.Additional), repos.Additional)
	}
}
