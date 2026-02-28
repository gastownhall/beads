package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// CheckInstallation verifies that .beads directory exists
func CheckInstallation(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		// Auto-detect prefix from directory name
		prefix := filepath.Base(path)
		prefix = strings.TrimRight(prefix, "-")

		return DoctorCheck{
			Name:    "Installation",
			Status:  StatusError,
			Message: "No .beads/ directory found",
			Fix:     fmt.Sprintf("Run 'bd init --prefix %s' to initialize beads", prefix),
		}
	}

	return DoctorCheck{
		Name:    "Installation",
		Status:  StatusOK,
		Message: ".beads/ directory found",
	}
}

// CheckPermissions verifies that .beads directory and database are readable/writable
func CheckPermissions(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Check if .beads/ is writable
	testFile := filepath.Join(beadsDir, ".doctor-test-write")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		return DoctorCheck{
			Name:    "Permissions",
			Status:  StatusError,
			Message: ".beads/ directory is not writable",
			Fix:     "Run 'bd doctor --fix' to fix permissions",
		}
	}
	_ = os.Remove(testFile) // Clean up test file (intentionally ignore error)

	// Check Dolt database directory permissions
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		doltPath := getDatabasePath(beadsDir)
		if info, err := os.Stat(doltPath); err == nil {
			if !info.IsDir() {
				return DoctorCheck{
					Name:    "Permissions",
					Status:  StatusError,
					Message: "dolt/ is not a directory",
					Fix:     "Run 'bd doctor --fix' to fix permissions",
				}
			}
			// Try to open Dolt store read-only to verify accessibility
			ctx := context.Background()
			store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
			if err != nil {
				return DoctorCheck{
					Name:    "Permissions",
					Status:  StatusError,
					Message: "Dolt database exists but cannot be opened",
					Detail:  err.Error(),
					Fix:     "Run 'bd doctor --fix' to fix permissions",
				}
			}
			_ = store.Close()
		}
	}

	return DoctorCheck{
		Name:    "Permissions",
		Status:  StatusOK,
		Message: "All permissions OK",
	}
}

// CheckUntrackedBeadsFiles checks for untracked .beads/*.jsonl files that should be committed.
// In sync-branch mode, JSONL files are intentionally untracked in working branches
// and only committed to the dedicated sync branch (GH#858).
func CheckUntrackedBeadsFiles(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Untracked Files",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Untracked Files",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Run git status --porcelain to find untracked files in .beads/
	cmd := exec.Command("git", "status", "--porcelain", ".beads/")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Untracked Files",
			Status:  StatusWarning,
			Message: "Unable to check git status",
			Detail:  err.Error(),
		}
	}

	// Parse output for untracked JSONL files (lines starting with "??")
	var untrackedJSONL []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Untracked files start with "?? "
		if strings.HasPrefix(line, "?? ") {
			file := strings.TrimPrefix(line, "?? ")
			// Only care about .jsonl files
			if strings.HasSuffix(file, ".jsonl") {
				untrackedJSONL = append(untrackedJSONL, filepath.Base(file))
			}
		}
	}

	if len(untrackedJSONL) == 0 {
		return DoctorCheck{
			Name:    "Untracked Files",
			Status:  StatusOK,
			Message: "All .beads/*.jsonl files are tracked",
		}
	}

	return DoctorCheck{
		Name:    "Untracked Files",
		Status:  StatusWarning,
		Message: fmt.Sprintf("Untracked JSONL files: %s", strings.Join(untrackedJSONL, ", ")),
		Detail:  "These files should be committed to propagate changes to other clones",
		Fix:     "Run 'bd doctor --fix' to stage and commit untracked files, or manually: git add .beads/*.jsonl && git commit",
	}
}

// FixPermissions fixes file permission issues in the .beads directory
func FixPermissions(path string) error {
	return fix.Permissions(path)
}
