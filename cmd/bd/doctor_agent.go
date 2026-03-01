package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// agentDoctorResult is the top-level output for --agent mode.
type agentDoctorResult struct {
	Path        string                   `json:"path"`
	OverallOK   bool                     `json:"overall_ok"`
	CLIVersion  string                   `json:"cli_version"`
	Summary     string                   `json:"summary"`
	Diagnostics []doctor.AgentDiagnostic `json:"diagnostics"`
	PassedCount int                      `json:"passed_count"`
}

// buildAgentResult converts a standard doctorResult to an agent-enriched result.
// Only non-OK checks are included in diagnostics — agents don't need 45 "ok" entries.
func buildAgentResult(result doctorResult) agentDoctorResult {
	ar := agentDoctorResult{
		Path:       result.Path,
		OverallOK:  result.OverallOK,
		CLIVersion: result.CLIVersion,
	}

	var errorCount, warnCount int
	for _, check := range result.Checks {
		switch check.Status {
		case statusOK:
			ar.PassedCount++
		case statusError:
			errorCount++
			ar.Diagnostics = append(ar.Diagnostics, doctor.EnrichForAgent(doctor.DoctorCheck{
				Name:     check.Name,
				Status:   check.Status,
				Message:  check.Message,
				Detail:   check.Detail,
				Fix:      check.Fix,
				Category: check.Category,
			}))
		case statusWarning:
			warnCount++
			ar.Diagnostics = append(ar.Diagnostics, doctor.EnrichForAgent(doctor.DoctorCheck{
				Name:     check.Name,
				Status:   check.Status,
				Message:  check.Message,
				Detail:   check.Detail,
				Fix:      check.Fix,
				Category: check.Category,
			}))
		}
	}

	if ar.OverallOK {
		ar.Summary = fmt.Sprintf("All %d checks passed. No issues found.", ar.PassedCount)
	} else {
		parts := []string{}
		if errorCount > 0 {
			parts = append(parts, fmt.Sprintf("%d error(s)", errorCount))
		}
		if warnCount > 0 {
			parts = append(parts, fmt.Sprintf("%d warning(s)", warnCount))
		}
		ar.Summary = fmt.Sprintf("%s found, %d checks passed.", strings.Join(parts, " and "), ar.PassedCount)
	}

	return ar
}

// printAgentDiagnostics outputs agent-facing prose diagnostics.
// Designed for LLM consumption: structured natural language, not table formatting.
func printAgentDiagnostics(ar agentDoctorResult) {
	fmt.Printf("## bd doctor (agent diagnostic mode) v%s\n\n", ar.CLIVersion)
	fmt.Printf("**Path:** %s\n", ar.Path)
	fmt.Printf("**Status:** %s\n\n", ar.Summary)

	if ar.OverallOK && len(ar.Diagnostics) == 0 {
		fmt.Println("All checks passed. No action needed.")
		return
	}

	// Print errors first, then warnings
	for _, d := range ar.Diagnostics {
		if d.Status == statusError {
			printAgentDiagnostic(d)
		}
	}
	for _, d := range ar.Diagnostics {
		if d.Status == statusWarning {
			printAgentDiagnostic(d)
		}
	}

	// Footer
	fmt.Printf("---\n%d other checks passed.\n", ar.PassedCount)
}

func printAgentDiagnostic(d doctor.AgentDiagnostic) {
	severity := strings.ToUpper(d.Status)
	fmt.Printf("### %s: %s", severity, d.Name)
	if d.Category != "" {
		fmt.Printf(" [%s]", d.Category)
	}
	fmt.Println()

	fmt.Printf("Severity: %s\n\n", d.Severity)
	fmt.Println(d.Explanation)
	fmt.Println()

	fmt.Printf("- **Observed:** %s\n", strings.ReplaceAll(d.Observed, "\n", "\n  "))
	fmt.Printf("- **Expected:** %s\n", d.Expected)

	if len(d.Commands) > 0 {
		fmt.Println("- **To fix:**")
		for _, cmd := range d.Commands {
			fmt.Printf("  - `%s`\n", cmd)
		}
	}

	if len(d.SourceFiles) > 0 {
		fmt.Printf("- **Source:** %s\n", strings.Join(d.SourceFiles, ", "))
	}

	fmt.Println()
}
