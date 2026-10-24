package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSidecarIsIgnoredForNewAndExistingWorkspaces closes the loop on the
// machine-local sidecar: bd writes .beads/config.local.yaml, so bd must also
// ignore it. Both halves matter — the template covers repositories initialized
// from now on, and requiredPatterns is what retrofits the ones that already
// exist. Without the second, an existing checkout trades one self-dirtying
// file for another.
func TestSidecarIsIgnoredForNewAndExistingWorkspaces(t *testing.T) {
	const sidecar = "config.local.yaml"

	t.Run("new workspace gets it from the template", func(t *testing.T) {
		if !strings.Contains(GitignoreTemplate, sidecar) {
			t.Errorf("%q missing from the .beads/.gitignore template", sidecar)
		}
	})

	t.Run("existing workspace gets it from doctor --fix", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		gitignorePath := filepath.Join(beadsDir, ".gitignore")
		// An older .beads/.gitignore, plus a local rule that must survive.
		existing := "dolt/\n.env\nredirect\n# local rule\nmy-scratch/\n"
		if err := os.WriteFile(gitignorePath, []byte(existing), 0o600); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		if err := EnsureGitignoreForBeadsDir(beadsDir); err != nil {
			t.Fatalf("EnsureGitignoreForBeadsDir: %v", err)
		}

		b, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		got := string(b)
		if !strings.Contains(got, sidecar) {
			t.Errorf("%q not appended to an existing .beads/.gitignore:\n%s", sidecar, got)
		}
		if !strings.Contains(got, "my-scratch/") {
			t.Errorf("append clobbered a local rule:\n%s", got)
		}
	})
}
