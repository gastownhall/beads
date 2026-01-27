// Package doctor provides diagnostic checks for beads installations.
package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// SyncDivergenceIssue represents a specific type of sync divergence detected.
type SyncDivergenceIssue struct {
	Type        string // "jsonl_git_mismatch", "sqlite_mtime_stale", "uncommitted_beads"
	Description string
	FixCommand  string
}

// CheckSyncDivergence checks for sync divergence issues between JSONL, SQLite, and git.
// This is part of GH#885 fix: recovery mechanism.
//
// Detects:
// 1. JSONL on disk differs from git HEAD version
// 2. SQLite last_import_time does not match JSONL mtime
// 3. Uncommitted .beads/ changes exist
func CheckSyncDivergence(path string) DoctorCheck {
	// Check if we're in a git repository
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:     "Sync Divergence",
			Status:   StatusOK,
			Message:  "N/A (not a git repository)",
			Category: CategoryData,
		}
	}

	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Sync Divergence",
			Status:   StatusOK,
			Message:  "N/A (no .beads directory)",
			Category: CategoryData,
		}
	}

	backend := configfile.BackendSQLite
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		backend = cfg.GetBackend()
	}

	var issues []SyncDivergenceIssue

	// Check 1: JSONL differs from git HEAD
	jsonlIssue := checkJSONLGitDivergence(path, beadsDir)
	if jsonlIssue != nil {
		issues = append(issues, *jsonlIssue)
	}

	// Check 2: SQLite last_import_time vs JSONL mtime (SQLite only).
	// Dolt backend does not maintain SQLite metadata; this SQLite-only check doesn't apply.
	if backend == configfile.BackendSQLite {
		mtimeIssue := checkSQLiteMtimeDivergence(path, beadsDir)
		if mtimeIssue != nil {
			issues = append(issues, *mtimeIssue)
		}
	}

	// Check 3: Uncommitted .beads/ changes
	uncommittedIssue := checkUncommittedBeadsChanges(path, beadsDir)
	if uncommittedIssue != nil {
		issues = append(issues, *uncommittedIssue)
	}

	if len(issues) == 0 {
		msg := "JSONL, SQLite, and git are in sync"
		if backend == configfile.BackendDolt {
			msg = "JSONL, Dolt, and git are in sync"
		}
		return DoctorCheck{
			Name:     "Sync Divergence",
			Status:   StatusOK,
			Message:  msg,
			Category: CategoryData,
		}
	}

	// Build detail and fix messages
	var details []string
	var fixes []string
	for _, issue := range issues {
		details = append(details, issue.Description)
		if issue.FixCommand != "" {
			fixes = append(fixes, issue.FixCommand)
		}
	}

	status := StatusWarning
	if len(issues) > 1 {
		// Multiple divergence issues are more serious
		status = StatusError
	}

	return DoctorCheck{
		Name:     "Sync Divergence",
		Status:   status,
		Message:  fmt.Sprintf("%d sync divergence issue(s) detected", len(issues)),
		Detail:   strings.Join(details, "\n"),
		Fix:      strings.Join(fixes, " OR "),
		Category: CategoryData,
	}
}

// checkJSONLGitDivergence checks if JSONL on disk differs from git HEAD version.
func checkJSONLGitDivergence(path, beadsDir string) *SyncDivergenceIssue {
	// Find JSONL file
	jsonlPath := findJSONLFile(beadsDir)
	if jsonlPath == "" {
		return nil // No JSONL file
	}

	// Get relative path for git commands
	relPath, err := filepath.Rel(path, jsonlPath)
	if err != nil {
		return nil
	}

	// Check if file is tracked by git
	cmd := exec.Command("git", "ls-files", "--error-unmatch", relPath) // #nosec G204 -- relPath is derived from validated file path
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// File not tracked by git
		return nil
	}

	// Compare current file with HEAD
	cmd = exec.Command("git", "diff", "--quiet", "HEAD", "--", relPath) // #nosec G204 -- relPath is derived from validated file path
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Exit code non-zero means there are differences
		// In dolt-native mode, Dolt is source of truth - JSONL should be restored from git HEAD
		fixCmd := "git add .beads/ && git commit -m 'sync beads'"
		if config.GetSyncMode() == config.SyncModeDoltNative {
			fixCmd = fmt.Sprintf("git restore %s", relPath)
		}
		return &SyncDivergenceIssue{
			Type:        "jsonl_git_mismatch",
			Description: fmt.Sprintf("JSONL file differs from git HEAD: %s", filepath.Base(jsonlPath)),
			FixCommand:  fixCmd,
		}
	}

	return nil
}

// checkSQLiteMtimeDivergence checks if SQLite last_import_time matches JSONL mtime.
func checkSQLiteMtimeDivergence(_, beadsDir string) *SyncDivergenceIssue { //nolint:unparam // path reserved for future use
	// Get database path
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil // No database
	}

	// Find JSONL file
	jsonlPath := findJSONLFile(beadsDir)
	if jsonlPath == "" {
		return nil // No JSONL file
	}

	// Get JSONL mtime
	jsonlInfo, err := os.Stat(jsonlPath)
	if err != nil {
		return nil
	}
	jsonlMtime := jsonlInfo.ModTime()

	// Get last_import_time from database
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return nil
	}
	defer db.Close()

	var lastImportTimeStr string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'last_import_time'").Scan(&lastImportTimeStr)
	if err != nil {
		// No last_import_time recorded - this is a potential issue
		return &SyncDivergenceIssue{
			Type:        "sqlite_mtime_stale",
			Description: "No last_import_time recorded in database (may need sync)",
			FixCommand:  "bd sync --import-only",
		}
	}

	// Parse last_import_time
	lastImportTime, err := time.Parse(time.RFC3339, lastImportTimeStr)
	if err != nil {
		// Try Unix timestamp format
		var unixTs int64
		if _, err := fmt.Sscanf(lastImportTimeStr, "%d", &unixTs); err == nil {
			lastImportTime = time.Unix(unixTs, 0)
		} else {
			return nil // Can't parse, skip this check
		}
	}

	// Compare times with a 2-second tolerance (filesystem mtime precision varies)
	timeDiff := jsonlMtime.Sub(lastImportTime)
	if timeDiff < 0 {
		timeDiff = -timeDiff
	}

	if timeDiff > 2*time.Second {
		if jsonlMtime.After(lastImportTime) {
			return &SyncDivergenceIssue{
				Type:        "sqlite_mtime_stale",
				Description: fmt.Sprintf("JSONL is newer than last import (%s > %s)", jsonlMtime.Format(time.RFC3339), lastImportTime.Format(time.RFC3339)),
				FixCommand:  "bd sync --import-only",
			}
		}
		return &SyncDivergenceIssue{
			Type:        "sqlite_mtime_stale",
			Description: fmt.Sprintf("Database import time is newer than JSONL (%s > %s)", lastImportTime.Format(time.RFC3339), jsonlMtime.Format(time.RFC3339)),
			FixCommand:  "bd export",
		}
	}

	return nil
}

// checkUncommittedBeadsChanges checks if there are uncommitted changes in .beads/ directory.
func checkUncommittedBeadsChanges(path, beadsDir string) *SyncDivergenceIssue {
	// Get relative path of beads dir
	relBeadsDir, err := filepath.Rel(path, beadsDir)
	if err != nil {
		relBeadsDir = ".beads"
	}

	// Check for uncommitted changes in .beads/
	cmd := exec.Command("git", "status", "--porcelain", "--", relBeadsDir) // #nosec G204 -- relBeadsDir is derived from validated path
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return nil // Can't run git status
	}

	status := strings.TrimSpace(string(out))
	if status == "" {
		return nil // No uncommitted changes
	}

	// Count changed files
	lines := strings.Split(status, "\n")
	fileCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			fileCount++
		}
	}

	fixCmd := "bd sync"
	// For dolt backend, bd sync/import-only workflows don't apply.
	// In dolt-native mode, Dolt is source of truth - JSONL should be restored from git HEAD.
	// In other dolt modes, recommend a plain git commit.
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		if config.GetSyncMode() == config.SyncModeDoltNative {
			fixCmd = fmt.Sprintf("git restore %s", relBeadsDir)
		} else {
			fixCmd = "git add .beads/ && git commit -m 'sync beads'"
		}
	}

	return &SyncDivergenceIssue{
		Type:        "uncommitted_beads",
		Description: fmt.Sprintf("Uncommitted .beads/ changes (%d file(s))", fileCount),
		FixCommand:  fixCmd,
	}
}

// findJSONLFile finds the JSONL file in the beads directory.
func findJSONLFile(beadsDir string) string {
	// Check metadata.json for custom JSONL name
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			p := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	// Try standard names
	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		p := filepath.Join(beadsDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}
