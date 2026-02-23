package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/ui"
)

// runArtifactsCheck runs detailed classic artifact detection.
// With --clean, removes safe-to-delete artifacts after confirmation.
func runArtifactsCheck(path string, clean bool, yes bool) {
	report := doctor.ScanForArtifacts(path)

	if report.TotalCount == 0 {
		if !jsonOutput {
			fmt.Println("No classic artifacts detected.")
		} else {
			outputJSON(map[string]interface{}{
				"total_count": 0,
			})
		}
		return
	}

	if jsonOutput {
		result := map[string]interface{}{
			"total_count":       report.TotalCount,
			"safe_delete_count": report.SafeDeleteCount,
			"sqlite_artifacts":  len(report.SQLiteArtifacts),
			"cruft_beads_dirs":  len(report.CruftBeadsDirs),
			"redirect_issues":   len(report.RedirectIssues),
		}

		var findings []map[string]interface{}
		for _, lists := range [][]doctor.ArtifactFinding{
			report.SQLiteArtifacts,
			report.CruftBeadsDirs, report.RedirectIssues,
		} {
			for _, f := range lists {
				findings = append(findings, map[string]interface{}{
					"path":        f.Path,
					"type":        f.Type,
					"description": f.Description,
					"safe_delete": f.SafeDelete,
				})
			}
		}
		result["findings"] = findings
		outputJSON(result)
		return
	}

	// Human-readable output
	fmt.Printf("Found %d classic artifact(s) (%d safe to delete):\n\n", report.TotalCount, report.SafeDeleteCount)

	if len(report.SQLiteArtifacts) > 0 {
		fmt.Printf("SQLite Artifacts (%d):\n", len(report.SQLiteArtifacts))
		for _, f := range report.SQLiteArtifacts {
			safeTag := ""
			if f.SafeDelete {
				safeTag = " [safe]"
			}
			fmt.Printf("  %s%s\n", f.Path, safeTag)
			fmt.Printf("    %s\n", ui.RenderMuted(f.Description))
		}
		fmt.Println()
	}

	if len(report.CruftBeadsDirs) > 0 {
		fmt.Printf("Cruft .beads Directories (%d):\n", len(report.CruftBeadsDirs))
		for _, f := range report.CruftBeadsDirs {
			safeTag := ""
			if f.SafeDelete {
				safeTag = " [safe]"
			}
			fmt.Printf("  %s%s\n", f.Path, safeTag)
			fmt.Printf("    %s\n", ui.RenderMuted(f.Description))
		}
		fmt.Println()
	}

	if len(report.RedirectIssues) > 0 {
		fmt.Printf("Redirect Issues (%d):\n", len(report.RedirectIssues))
		for _, f := range report.RedirectIssues {
			fmt.Printf("  %s\n", f.Path)
			fmt.Printf("    %s\n", ui.RenderMuted(f.Description))
		}
		fmt.Println()
	}

	if !clean {
		fmt.Println("Run 'bd doctor --check=artifacts --clean' to remove safe-to-delete artifacts.")
		return
	}

	if report.SafeDeleteCount == 0 {
		fmt.Println("No artifacts are safe to auto-delete. Manual review required.")
		return
	}

	// Confirmation prompt
	if !yes {
		fmt.Printf("Delete %d safe-to-delete artifact(s)? [y/N] ", report.SafeDeleteCount)
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return
		}
	}

	// Perform cleanup
	if err := fix.ClassicArtifacts(path); err != nil {
		FatalError("during cleanup: %v", err)
	}
}
