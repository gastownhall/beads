package doctor

import (
	"os"
	"path/filepath"
	"strings"
)

// GitignoreTemplate is the canonical .beads/.gitignore content
// Uses whitelist approach: ignore everything by default, explicitly allow tracked files.
// This prevents confusion about which files to commit (fixes GitHub #473).
const GitignoreTemplate = `# Ignore all .beads/ contents by default (local workspace files)
# Only files explicitly whitelisted below will be tracked in git
*

# === Files tracked in git (shared across clones) ===

# This gitignore file itself
!.gitignore

# Issue data in JSONL format (the main data file)
!issues.jsonl

# Repository metadata (database name, JSONL filename)
!metadata.json

# Configuration template (sync branch, integrations)
!config.yaml

# Documentation for contributors
!README.md
`

// requiredPatterns are patterns that MUST be in .beads/.gitignore
// With the whitelist approach, we check for the blanket ignore and whitelisted files
var requiredPatterns = []string{
	"*",            // Blanket ignore (whitelist approach)
	"!.gitignore",  // Whitelist the gitignore itself
	"!issues.jsonl",
	"!metadata.json",
	"!config.yaml", // Fixed: was incorrectly !config.json before #473
}

// CheckGitignore checks if .beads/.gitignore is up to date
func CheckGitignore() DoctorCheck {
	gitignorePath := filepath.Join(".beads", ".gitignore")
	
	// Check if file exists
	content, err := os.ReadFile(gitignorePath) // #nosec G304 -- path is hardcoded
	if err != nil {
		return DoctorCheck{
			Name:    "Gitignore",
			Status:  "warning",
			Message: ".beads/.gitignore not found",
			Fix:     "Run: bd init (safe to re-run) or bd doctor --fix",
		}
	}

	// Check for required patterns
	contentStr := string(content)
	var missing []string
	for _, pattern := range requiredPatterns {
		if !strings.Contains(contentStr, pattern) {
			missing = append(missing, pattern)
		}
	}

	if len(missing) > 0 {
		return DoctorCheck{
			Name:    "Gitignore",
			Status:  "warning",
			Message: "Outdated .beads/.gitignore (needs whitelist patterns)",
			Detail:  "Missing: " + strings.Join(missing, ", "),
			Fix:     "Run: bd doctor --fix or bd init (safe to re-run)",
		}
	}

	return DoctorCheck{
		Name:    "Gitignore",
		Status:  "ok",
		Message: "Up to date",
	}
}

// FixGitignore updates .beads/.gitignore to the current template
func FixGitignore() error {
	gitignorePath := filepath.Join(".beads", ".gitignore")

	// If file exists and is read-only, fix permissions first
	if info, err := os.Stat(gitignorePath); err == nil {
		if info.Mode().Perm()&0200 == 0 { // No write permission for owner
			if err := os.Chmod(gitignorePath, 0600); err != nil {
				return err
			}
		}
	}

	// Write canonical template with secure file permissions
	if err := os.WriteFile(gitignorePath, []byte(GitignoreTemplate), 0600); err != nil {
		return err
	}

	// Ensure permissions are set correctly (some systems respect umask)
	if err := os.Chmod(gitignorePath, 0600); err != nil {
		return err
	}

	return nil
}
