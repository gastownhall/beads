package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// FindBeadsDir walks up from the working directory with no upper boundary, so a
// repository under $HOME that has no .beads of its own resolves to ~/.beads.
// Reset then removes it. These pin that it refuses instead — and that the
// refusal is narrow enough not to break the case where the home directory
// genuinely is the repository.
func TestRefuseGlobalBeadsDir(t *testing.T) {
	home := filepath.Join("/", "home", "someone")
	globalBeads := filepath.Join(home, ".beads")
	project := filepath.Join(home, "dev", "project")

	tests := []struct {
		name      string
		beadsDir  string
		repoRoot  string
		home      string
		wantError bool
	}{
		{
			// The incident shape: a reset inside a project whose walk climbed
			// out of the repository and landed on the user's global directory.
			name:      "project reset that resolved to the global beads dir",
			beadsDir:  globalBeads,
			repoRoot:  project,
			home:      home,
			wantError: true,
		},
		{
			// Not an over-reach: here ~/.beads IS the repository's beads dir,
			// and removing it is exactly what was asked for.
			name:      "home directory is itself the repository",
			beadsDir:  globalBeads,
			repoRoot:  home,
			home:      home,
			wantError: false,
		},
		{
			name:      "ordinary repo-local beads dir",
			beadsDir:  filepath.Join(project, ".beads"),
			repoRoot:  project,
			home:      home,
			wantError: false,
		},
		{
			// A directory that merely lives under $HOME is not the global one.
			name:      "beads dir under home but not the global path",
			beadsDir:  filepath.Join(home, ".beads-backup"),
			repoRoot:  project,
			home:      home,
			wantError: false,
		},
		{
			name:      "no home directory: nothing global to protect",
			beadsDir:  globalBeads,
			repoRoot:  project,
			home:      "",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := refuseGlobalBeadsDir(tt.beadsDir, tt.repoRoot, tt.home)
			if tt.wantError && err == nil {
				t.Fatalf("refuseGlobalBeadsDir() = nil, want a refusal")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("refuseGlobalBeadsDir() = %v, want nil", err)
			}
			if err == nil {
				return
			}
			// The message has to name both paths: the whole failure mode is a
			// user who cannot tell which directory the command was about.
			for _, want := range []string{tt.repoRoot, tt.beadsDir} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal message does not name %q:\n%s", want, err)
				}
			}
		})
	}
}

// Trailing separators and unclean paths are a normal way for these to arrive,
// and a guard that a separator defeats is not a guard.
func TestRefuseGlobalBeadsDir_NormalizesPaths(t *testing.T) {
	home := filepath.Join("/", "home", "someone")
	beadsDir := filepath.Join(home, "dev", "..", ".beads") + string(filepath.Separator)

	if err := refuseGlobalBeadsDir(beadsDir, filepath.Join(home, "dev", "project"), home); err == nil {
		t.Fatalf("refuseGlobalBeadsDir(%q) = nil, want a refusal", beadsDir)
	}
}
