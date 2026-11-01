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
// The gate is applied to each enriched command list; see doctor.EnrichForAgent.
func buildAgentResult(result doctorResult, gate doctor.FixGate) agentDoctorResult {
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
			}, gate))
		case statusWarning:
			warnCount++
			ar.Diagnostics = append(ar.Diagnostics, doctor.EnrichForAgent(doctor.DoctorCheck{
				Name:     check.Name,
				Status:   check.Status,
				Message:  check.Message,
				Detail:   check.Detail,
				Fix:      check.Fix,
				Category: check.Category,
			}, gate))
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
	d = d.Printed()
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
			fmt.Printf("  - %s\n", agentCodeSpan(cmd))
		}
	}

	if len(d.SourceFiles) > 0 {
		fmt.Printf("- **Source:** %s\n", strings.Join(d.SourceFiles, ", "))
	}

	fmt.Println()
}

// agentCodeSpan shows cmd as a Markdown code span. A command without a
// backtick gets the single-backtick span it always had. One that holds a
// backtick (a check's Fix can carry a value remedy.CommandArg single-quoted
// from a committed file name) gets a fence longer than its longest backtick
// run, so the backtick cannot close the span (CommonMark). The fence length
// is even: pasted into sh, bash or zsh, an even fence is only empty command
// substitutions, so the span runs its command as written and a backtick
// inside the command's single quotes stays literal. That is all the fence
// does for a paste: a backtick the command leaves outside quotes still starts
// a substitution, so bd's own commands and advice hold none (the fix gate's
// Reason names the 'bd doctor --fix' it withholds in single quotes).
func agentCodeSpan(cmd string) string {
	longest, run := 0, 0
	for _, r := range cmd {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	if longest == 0 {
		return "`" + cmd + "`"
	}
	n := longest + 1
	if n%2 != 0 {
		n++
	}
	fence := strings.Repeat("`", n)
	if strings.HasPrefix(cmd, "`") || strings.HasSuffix(cmd, "`") {
		// CommonMark strips one space from each side of such a span.
		cmd = " " + cmd + " "
	}
	return fence + cmd + fence
}
