package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
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

	// Check database permissions
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); err == nil {
		// Try to open database
		db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
		if err != nil {
			return DoctorCheck{
				Name:    "Permissions",
				Status:  StatusError,
				Message: "Database file exists but cannot be opened",
				Fix:     "Run 'bd doctor --fix' to fix permissions",
			}
		}
		_ = db.Close() // Intentionally ignore close error

		// Try a write test
		db, err = sql.Open("sqlite", sqliteConnString(dbPath, true))
		if err == nil {
			_, err = db.Exec("SELECT 1")
			_ = db.Close() // Intentionally ignore close error
			if err != nil {
				return DoctorCheck{
					Name:    "Permissions",
					Status:  StatusError,
					Message: "Database file is not readable",
					Fix:     "Run 'bd doctor --fix' to fix permissions",
				}
			}
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

// FixUntrackedJSONL stages and commits untracked .beads/*.jsonl files
func FixUntrackedJSONL(path string) error {
	return fix.UntrackedJSONL(path)
}
