package routing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRedirectRelativePath(t *testing.T) {
	// Create a temporary directory structure:
	//   tmpdir/
	//     project/
	//       .beads/
	//         redirect  (contains "target/.beads")
	//       target/
	//         .beads/
	//           beads.db
	tmpdir := t.TempDir()

	projectDir := filepath.Join(tmpdir, "project")
	beadsDir := filepath.Join(projectDir, ".beads")
	targetBeadsDir := filepath.Join(projectDir, "target", ".beads")

	// Create directories
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a dummy file in target so it exists
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "beads.db"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Write redirect file with path relative to project root (parent of .beads)
	redirectContent := "target/.beads\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(redirectContent), 0644); err != nil {
		t.Fatal(err)
	}

	// resolveRedirect should resolve "target/.beads" relative to project/ (parent of .beads/),
	// NOT relative to .beads/ itself
	result := resolveRedirect(beadsDir)

	if result != targetBeadsDir {
		t.Errorf("resolveRedirect resolved to wrong path\n  got:  %s\n  want: %s", result, targetBeadsDir)
	}
}

func TestResolveRedirectAbsolutePath(t *testing.T) {
	tmpdir := t.TempDir()

	beadsDir := filepath.Join(tmpdir, "source", ".beads")
	targetDir := filepath.Join(tmpdir, "target", ".beads")

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Absolute path redirect
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(targetDir+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result := resolveRedirect(beadsDir)
	if result != targetDir {
		t.Errorf("resolveRedirect with absolute path\n  got:  %s\n  want: %s", result, targetDir)
	}
}

func TestResolveRedirectNoFile(t *testing.T) {
	tmpdir := t.TempDir()
	beadsDir := filepath.Join(tmpdir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	result := resolveRedirect(beadsDir)
	if result != beadsDir {
		t.Errorf("resolveRedirect without redirect file should return original\n  got:  %s\n  want: %s", result, beadsDir)
	}
}

func TestResolveRedirectConsistentWithFollowRedirect(t *testing.T) {
	// This test verifies that resolveRedirect (routing) resolves paths
	// the same way as FollowRedirect (beads.go) - from the parent of .beads,
	// not from .beads itself.
	tmpdir := t.TempDir()

	// Structure: tmpdir/rig/.beads/redirect -> "mayor/rig/.beads"
	//            tmpdir/rig/mayor/rig/.beads/ (target)
	rigDir := filepath.Join(tmpdir, "rig")
	beadsDir := filepath.Join(rigDir, ".beads")
	targetDir := filepath.Join(rigDir, "mayor", "rig", ".beads")

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// This is the exact pattern used in Gas Town: "mayor/rig/.beads"
	redirectContent := "mayor/rig/.beads\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(redirectContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := resolveRedirect(beadsDir)
	if result != targetDir {
		t.Errorf("resolveRedirect should resolve like FollowRedirect (from parent of .beads)\n  got:  %s\n  want: %s", result, targetDir)
	}
}
