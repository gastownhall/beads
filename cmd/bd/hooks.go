package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const hookVersionPrefix = "# bd-hooks-version: "

// HookStatus represents the status of a single git hook
type HookStatus struct {
	Name      string
	Installed bool
	Version   string
	Outdated  bool
}

// CheckGitHooks checks the status of bd git hooks in .git/hooks/
func CheckGitHooks() ([]HookStatus, error) {
	hooks := []string{"pre-commit", "post-merge", "pre-push"}
	statuses := make([]HookStatus, 0, len(hooks))

	for _, hookName := range hooks {
		status := HookStatus{
			Name: hookName,
		}

		// Check if hook exists
		hookPath := filepath.Join(".git", "hooks", hookName)
		version, err := getHookVersion(hookPath)
		if err != nil {
			// Hook doesn't exist or couldn't be read
			status.Installed = false
		} else {
			status.Installed = true
			status.Version = version
			
			// Check if outdated (compare to current bd version)
			if version != "" && version != Version {
				status.Outdated = true
			}
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// getHookVersion extracts the version from a hook file
func getHookVersion(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Read first few lines looking for version marker
	lineCount := 0
	for scanner.Scan() && lineCount < 10 {
		line := scanner.Text()
		if strings.HasPrefix(line, hookVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, hookVersionPrefix))
			return version, nil
		}
		lineCount++
	}

	// No version found (old hook)
	return "", nil
}

// FormatHookWarnings returns a formatted warning message if hooks are outdated
func FormatHookWarnings(statuses []HookStatus) string {
	var warnings []string
	
	missingCount := 0
	outdatedCount := 0
	
	for _, status := range statuses {
		if !status.Installed {
			missingCount++
		} else if status.Outdated {
			outdatedCount++
		}
	}
	
	if missingCount > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️  Git hooks not installed (%d missing)", missingCount))
		warnings = append(warnings, "   Run: examples/git-hooks/install.sh")
	}
	
	if outdatedCount > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️  Git hooks are outdated (%d hooks)", outdatedCount))
		warnings = append(warnings, "   Run: examples/git-hooks/install.sh")
	}
	
	if len(warnings) > 0 {
		return strings.Join(warnings, "\n")
	}
	
	return ""
}
