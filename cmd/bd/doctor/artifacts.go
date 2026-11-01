package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
)

// ArtifactFinding represents a single detected artifact that may need cleanup.
type ArtifactFinding struct {
	Path        string // Absolute path to the artifact
	Type        string // "sqlite", "cruft-beads", "redirect"
	Description string // Human-readable description
	SafeDelete  bool   // Whether this is safe to delete without data loss
}

// ArtifactReport contains all findings from an artifact scan.
type ArtifactReport struct {
	SQLiteArtifacts []ArtifactFinding
	CruftBeadsDirs  []ArtifactFinding
	RedirectIssues  []ArtifactFinding
	TotalCount      int
	SafeDeleteCount int
}

// CheckClassicArtifacts scans for beads classic artifacts left after the Dolt
// migration: leftover SQLite files and cruft in .beads directories that should
// be redirect-only, as fix.PlanArtifacts classifies them. It offers --fix only
// when something is safe to delete.
//
// The scan is rooted at the given path and looks for .beads/ directories recursively,
// checking each for artifacts that indicate incomplete migration cleanup.
func CheckClassicArtifacts(path string) DoctorCheck {
	report, err := ScanForArtifacts(path)
	if err != nil {
		return DoctorCheck{
			Name:     "Classic Artifacts",
			Status:   StatusWarning,
			Message:  "Artifact scan failed",
			Detail:   err.Error(),
			Category: CategoryMaintenance,
		}
	}

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
		report.SQLiteArtifacts,
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

	// Offer --fix only when it would remove something: findings that need
	// manual review stay, and a fix that removes nothing would report them
	// fixed.
	fixHint := "Run 'bd doctor --fix' to clean up, or 'bd doctor --check=artifacts' for details"
	if report.SafeDeleteCount == 0 {
		fixHint = ""
		details = append(details, "  Nothing here is safe to delete automatically; review these by hand ('bd doctor --check=artifacts' for details)")
	}

	return DoctorCheck{
		Name:     "Classic Artifacts",
		Status:   StatusWarning,
		Message:  msg,
		Detail:   strings.Join(details, "\n"),
		Fix:      fixHint,
		Category: CategoryMaintenance,
	}
}

// ScanForArtifacts performs a recursive scan of the given path for classic beads artifacts.
// Returns an error if the walk itself fails (e.g., root path doesn't exist or is inaccessible).
// Individual unreadable subdirectories are skipped without error.
//
// It reports what the cleanup (fix.ClassicArtifacts) acts on: both walk the
// same .beads directories (fix.WalkBeadsDirs) and take each one's findings
// from the same plan (fix.PlanArtifacts), so the cleanup removes exactly the
// findings this marks safe to delete.
func ScanForArtifacts(rootPath string) (ArtifactReport, error) {
	var report ArtifactReport

	walkErr := fix.WalkBeadsDirs(rootPath, func(beadsDir string) {
		scanBeadsDir(beadsDir, &report)
	})
	if walkErr != nil {
		return report, fmt.Errorf("scanning artifacts at %s: %w", rootPath, walkErr)
	}

	report.TotalCount = len(report.SQLiteArtifacts) +
		len(report.CruftBeadsDirs) + len(report.RedirectIssues)

	for _, findings := range [][]ArtifactFinding{
		report.SQLiteArtifacts,
		report.CruftBeadsDirs, report.RedirectIssues,
	} {
		for _, f := range findings {
			if f.SafeDelete {
				report.SafeDeleteCount++
			}
		}
	}

	return report, nil
}

// scanBeadsDir reports a single .beads directory's findings from
// fix.PlanArtifacts, which reads its metadata as discovery reads it, without
// migrating a legacy config.json into metadata.json, and skips a workspace in
// a format this build cannot open.
func scanBeadsDir(beadsDir string, report *ArtifactReport) {
	findings, skip := fix.PlanArtifacts(beadsDir)
	if skip {
		return
	}
	for _, f := range findings {
		finding := ArtifactFinding{Path: f.Path, Type: f.Type, Description: f.Description, SafeDelete: f.SafeDelete}
		switch f.Type {
		case fix.ArtifactSQLite:
			report.SQLiteArtifacts = append(report.SQLiteArtifacts, finding)
		case fix.ArtifactCruft:
			report.CruftBeadsDirs = append(report.CruftBeadsDirs, finding)
		}
	}

	if hasRedirectFile(beadsDir) {
		validateRedirect(beadsDir, report)
	}
}

// hasRedirectFile returns true if the .beads directory has a redirect file.
func hasRedirectFile(beadsDir string) bool {
	_, err := os.Stat(filepath.Join(beadsDir, "redirect"))
	return err == nil
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
		return
	}

	// gastownhall/beads#4692: FollowRedirect ignores a redirect whose target
	// has no metadata.json and no recognizable database (a stray
	// worktree-depth redirect, e.g. a relic of the "bd worktree create"
	// write-site removed in #3051, can point past the real .beads dir into
	// an empty location). Flag that here too as an actionable warning, not
	// SafeDelete cruft -- the fix is to correct or remove the redirect file,
	// not to delete anything automatically.
	if !hasDatabaseOrMetadata(resolvedTarget) {
		report.RedirectIssues = append(report.RedirectIssues, ArtifactFinding{
			Path:        redirectPath,
			Type:        "redirect",
			Description: fmt.Sprintf("redirect target has no database or metadata.json (ignored by bd): %s", target),
			SafeDelete:  false,
		})
	}
}

// hasDatabaseOrMetadata reports whether dir contains a metadata.json or a
// recognizable Dolt/SQLite database. Mirrors the target-validity check
// FollowRedirect (internal/beads) applies before honoring a redirect;
// duplicated locally (rather than importing internal/beads) to keep this
// package's existing pattern of small, self-contained filesystem checks
// (see hasRedirectFile above).
func hasDatabaseOrMetadata(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "metadata.json")); err == nil {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, "dolt")); err == nil && info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, "embeddeddolt")); err == nil && info.IsDir() {
		return true
	}
	dbMatches, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	for _, match := range dbMatches {
		baseName := filepath.Base(match)
		if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
			return true
		}
	}
	return false
}
