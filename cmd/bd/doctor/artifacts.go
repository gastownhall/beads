package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactFinding represents a single detected artifact that may need cleanup.
type ArtifactFinding struct {
	Path        string // Absolute path to the artifact
	Type        string // "jsonl", "sqlite", "cruft-beads", "redirect"
	Description string // Human-readable description
	SafeDelete  bool   // Whether this is safe to delete without data loss
}

// ArtifactReport contains all findings from an artifact scan.
type ArtifactReport struct {
	JSONLArtifacts  []ArtifactFinding
	SQLiteArtifacts []ArtifactFinding
	CruftBeadsDirs  []ArtifactFinding
	RedirectIssues  []ArtifactFinding
	TotalCount      int
	SafeDeleteCount int
}

// CheckClassicArtifacts scans for beads classic artifacts that should be cleaned up
// after the Dolt migration. This includes stale JSONL files in dolt-native directories,
// leftover SQLite database files, cruft .beads directories that should be redirect-only,
// and invalid redirect files.
//
// The scan is rooted at the given path and looks for .beads/ directories recursively,
// checking each for artifacts that indicate incomplete migration cleanup.
func CheckClassicArtifacts(path string) DoctorCheck {
	report := ScanForArtifacts(path)

	if report.TotalCount == 0 {
		return DoctorCheck{
			Name:     "Classic Artifacts",
			Status:   StatusOK,
			Message:  "No classic artifacts detected",
			Category: CategoryMaintenance,
		}
	}

	// Build summary message
	var parts []string
	if len(report.JSONLArtifacts) > 0 {
		parts = append(parts, fmt.Sprintf("%d JSONL artifact(s)", len(report.JSONLArtifacts)))
	}
	if len(report.SQLiteArtifacts) > 0 {
		parts = append(parts, fmt.Sprintf("%d SQLite artifact(s)", len(report.SQLiteArtifacts)))
	}
	if len(report.CruftBeadsDirs) > 0 {
		parts = append(parts, fmt.Sprintf("%d cruft .beads dir(s)", len(report.CruftBeadsDirs)))
	}
	if len(report.RedirectIssues) > 0 {
		parts = append(parts, fmt.Sprintf("%d redirect issue(s)", len(report.RedirectIssues)))
	}

	msg := strings.Join(parts, ", ")

	// Build detail showing examples
	var details []string
	for _, findings := range [][]ArtifactFinding{
		report.JSONLArtifacts, report.SQLiteArtifacts,
		report.CruftBeadsDirs, report.RedirectIssues,
	} {
		for i, f := range findings {
			if i >= 3 {
				details = append(details, fmt.Sprintf("  ... and %d more %s artifact(s)", len(findings)-3, f.Type))
				break
			}
			details = append(details, fmt.Sprintf("  %s: %s", f.Path, f.Description))
		}
	}

	return DoctorCheck{
		Name:     "Classic Artifacts",
		Status:   StatusWarning,
		Message:  msg,
		Detail:   strings.Join(details, "\n"),
		Fix:      "Run 'bd doctor --fix' to clean up, or 'bd doctor --check=artifacts' for details",
		Category: CategoryMaintenance,
	}
}

// ScanForArtifacts performs a recursive scan of the given path for classic beads artifacts.
func ScanForArtifacts(rootPath string) ArtifactReport {
	var report ArtifactReport

	// Walk the directory tree looking for .beads/ directories
	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip directories we can't read
		}

		// Skip .git directories (but not .git/beads-worktrees)
		base := filepath.Base(path)
		if base == ".git" && info.IsDir() {
			// Allow descent into .git to find beads-worktrees
			return nil
		}

		// Skip node_modules and similar
		if info.IsDir() && (base == "node_modules" || base == "vendor" || base == "__pycache__") {
			return filepath.SkipDir
		}

		// We only care about directories named ".beads"
		if !info.IsDir() || base != ".beads" {
			return nil
		}

		// Found a .beads directory - scan it
		scanBeadsDir(path, &report)

		// Don't descend into .beads/ itself (we've scanned it)
		return filepath.SkipDir
	})

	report.TotalCount = len(report.JSONLArtifacts) + len(report.SQLiteArtifacts) +
		len(report.CruftBeadsDirs) + len(report.RedirectIssues)

	for _, findings := range [][]ArtifactFinding{
		report.JSONLArtifacts, report.SQLiteArtifacts,
		report.CruftBeadsDirs, report.RedirectIssues,
	} {
		for _, f := range findings {
			if f.SafeDelete {
				report.SafeDeleteCount++
			}
		}
	}

	return report
}

// scanBeadsDir checks a single .beads directory for artifacts.
func scanBeadsDir(beadsDir string, report *ArtifactReport) {
	// Check if this is a dolt-native directory (has dolt/ subdirectory)
	hasDolt := isDoltNative(beadsDir)

	// Check if this should be a redirect-only directory
	isRedirectExpected := isRedirectExpectedDir(beadsDir)

	// Check if it has a redirect file
	hasRedirect := hasRedirectFile(beadsDir)

	// 1. Check for JSONL artifacts in dolt-native directories
	if hasDolt {
		scanJSONLArtifacts(beadsDir, report)
	}

	// 2. Check for SQLite artifacts
	scanSQLiteArtifacts(beadsDir, report)

	// 3. Check for cruft .beads directories (should be redirect-only)
	if isRedirectExpected {
		scanCruftBeadsDir(beadsDir, hasRedirect, report)
	}

	// 4. Validate redirect files
	if hasRedirect {
		validateRedirect(beadsDir, report)
	}
}

// isDoltNative returns true if the .beads directory contains a dolt/ subdirectory.
func isDoltNative(beadsDir string) bool {
	info, err := os.Stat(filepath.Join(beadsDir, "dolt"))
	return err == nil && info.IsDir()
}

// isRedirectExpectedDir returns true if this .beads directory should contain
// only a redirect file (i.e., it's in a worktree, polecat, crew, or refinery subdirectory).
func isRedirectExpectedDir(beadsDir string) bool {
	// The parent of .beads is the project dir
	// We need to determine if this is a "leaf" .beads that should redirect
	// to a "canonical" .beads (typically in mayor/rig/ or main worktree)

	parent := filepath.Dir(beadsDir)
	parentName := filepath.Base(parent)
	grandparent := filepath.Dir(parent)
	grandparentName := filepath.Base(grandparent)

	// Pattern: */polecats/*/.beads/ (polecat worktree)
	if grandparentName == "polecats" {
		return true
	}

	// Pattern: */crew/*/.beads/ (crew workspace)
	if grandparentName == "crew" {
		return true
	}

	// Pattern: */refinery/rig/.beads/ (refinery rig)
	if parentName == "rig" && grandparentName == "refinery" {
		return true
	}

	// Pattern: .git/beads-worktrees/*/.beads/
	if grandparentName == "beads-worktrees" {
		return true
	}

	// Check if this is a rig-root .beads/ (e.g., gastown/.beads/)
	// that should redirect to mayor/rig/.beads/
	// A rig-root .beads has a sibling "mayor/" or "polecats/" directory
	if hasSibling(parent, "mayor") || hasSibling(parent, "polecats") {
		// This looks like a rig root, check if there's a canonical location
		canonicalDir := filepath.Join(parent, "mayor", "rig", ".beads")
		if _, err := os.Stat(canonicalDir); err == nil {
			return true
		}
	}

	return false
}

// hasSibling returns true if the directory has a sibling with the given name.
func hasSibling(dir string, siblingName string) bool {
	sibling := filepath.Join(dir, siblingName)
	info, err := os.Stat(sibling)
	return err == nil && info.IsDir()
}

// hasRedirectFile returns true if the .beads directory has a redirect file.
func hasRedirectFile(beadsDir string) bool {
	_, err := os.Stat(filepath.Join(beadsDir, "redirect"))
	return err == nil
}

// scanJSONLArtifacts checks for stale JSONL files in a dolt-native .beads directory.
func scanJSONLArtifacts(beadsDir string, report *ArtifactReport) {
	jsonlFiles := []struct {
		name string
		desc string
	}{
		// Note: issues.jsonl is NOT an artifact — the pre-commit hook exports
		// Dolt → JSONL on every git commit so the file is tracked in git.
		{"issues.jsonl.new", "JSONL export artifact"},
		{"beads.left.jsonl", "merge leftover"},
		{"interactions.jsonl", "interactions log (usually empty)"},
	}

	for _, jf := range jsonlFiles {
		path := filepath.Join(beadsDir, jf.name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		// Skip empty files for interactions.jsonl since they're harmless
		if jf.name == "interactions.jsonl" && info.Size() == 0 {
			continue
		}

		report.JSONLArtifacts = append(report.JSONLArtifacts, ArtifactFinding{
			Path:        path,
			Type:        "jsonl",
			Description: jf.desc,
			SafeDelete:  jf.name != "issues.jsonl", // issues.jsonl needs care
		})
	}
}

// scanSQLiteArtifacts checks for leftover SQLite database files.
// Only flags SQLite files as artifacts if Dolt is the active backend.
// If SQLite is still the active backend, beads.db is the live database.
func scanSQLiteArtifacts(beadsDir string, report *ArtifactReport) {
	if !isDoltNative(beadsDir) {
		return
	}

	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		// beads.db and its WAL/SHM files
		if name == "beads.db" || name == "beads.db-shm" || name == "beads.db-wal" {
			report.SQLiteArtifacts = append(report.SQLiteArtifacts, ArtifactFinding{
				Path:        filepath.Join(beadsDir, name),
				Type:        "sqlite",
				Description: "SQLite database file (Dolt is active backend)",
				SafeDelete:  name == "beads.db-shm" || name == "beads.db-wal",
			})
			continue
		}

		// beads.backup-*.db pre-migration backups
		if strings.HasPrefix(name, "beads.backup-") && strings.HasSuffix(name, ".db") {
			report.SQLiteArtifacts = append(report.SQLiteArtifacts, ArtifactFinding{
				Path:        filepath.Join(beadsDir, name),
				Type:        "sqlite",
				Description: "pre-migration backup",
				SafeDelete:  true,
			})
		}
	}
}

// scanCruftBeadsDir checks if a .beads directory that should be redirect-only
// contains extra files beyond the redirect file.
func scanCruftBeadsDir(beadsDir string, hasRedirect bool, report *ArtifactReport) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return
	}

	// Count non-redirect entries
	var extraFiles []string
	for _, entry := range entries {
		name := entry.Name()
		// redirect file is expected
		if name == "redirect" {
			continue
		}
		// .gitkeep is harmless
		if name == ".gitkeep" {
			continue
		}
		extraFiles = append(extraFiles, name)
	}

	if len(extraFiles) == 0 {
		return
	}

	desc := fmt.Sprintf("should be redirect-only but contains: %s", strings.Join(extraFiles, ", "))
	if len(extraFiles) > 5 {
		desc = fmt.Sprintf("should be redirect-only but contains %d extra files", len(extraFiles))
	}

	report.CruftBeadsDirs = append(report.CruftBeadsDirs, ArtifactFinding{
		Path:        beadsDir,
		Type:        "cruft-beads",
		Description: desc,
		SafeDelete:  hasRedirect, // Safe only if redirect is already in place
	})
}

// validateRedirect checks that a redirect file points to a valid target.
func validateRedirect(beadsDir string, report *ArtifactReport) {
	redirectPath := filepath.Join(beadsDir, "redirect")
	data, err := os.ReadFile(redirectPath) // #nosec G304 - path constructed from walked dir
	if err != nil {
		report.RedirectIssues = append(report.RedirectIssues, ArtifactFinding{
			Path:        redirectPath,
			Type:        "redirect",
			Description: "redirect file unreadable",
			SafeDelete:  false,
		})
		return
	}

	target := strings.TrimSpace(string(data))

	// Skip comments, find first path line
	lines := strings.Split(target, "\n")
	target = ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			target = line
			break
		}
	}

	if target == "" {
		report.RedirectIssues = append(report.RedirectIssues, ArtifactFinding{
			Path:        redirectPath,
			Type:        "redirect",
			Description: "redirect file is empty",
			SafeDelete:  false,
		})
		return
	}

	// Resolve relative paths
	resolvedTarget := target
	if !filepath.IsAbs(target) {
		projectRoot := filepath.Dir(beadsDir)
		resolvedTarget = filepath.Join(projectRoot, target)
	}

	// Check target exists
	info, err := os.Stat(resolvedTarget)
	if err != nil {
		report.RedirectIssues = append(report.RedirectIssues, ArtifactFinding{
			Path:        redirectPath,
			Type:        "redirect",
			Description: fmt.Sprintf("redirect target does not exist: %s", target),
			SafeDelete:  false,
		})
		return
	}

	if !info.IsDir() {
		report.RedirectIssues = append(report.RedirectIssues, ArtifactFinding{
			Path:        redirectPath,
			Type:        "redirect",
			Description: fmt.Sprintf("redirect target is not a directory: %s", target),
			SafeDelete:  false,
		})
	}
}
