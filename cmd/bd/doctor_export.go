package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// statusNotApplicable marks a doctor check that can make no claim about the
// inspected workspace. Unlike an OK pass, it promises nothing; consumers
// must not treat it as a clean bill of health. Used by --check=export when
// there is no file to inspect and auto-export is disabled (GH#6955).
const statusNotApplicable = "not_applicable"

// doctorExportMissingIDSamples bounds the sorted missing-ID samples in the
// export coverage warning; the missing count carries the full size.
const doctorExportMissingIDSamples = 5

// exportCoverageResult is the structured payload of `bd doctor --check=export`:
// a coverage check of the passive export file against the in-scope store IDs.
// It reports ID coverage only — not content equality, and not backup
// validity (GH#6955).
type exportCoverageResult struct {
	Check      string   `json:"check"`
	Status     string   `json:"status"` // statusOK, statusWarning, or statusNotApplicable
	Path       string   `json:"path"`
	ExportAuto bool     `json:"export_auto"`
	InScope    int      `json:"in_scope"`
	InFile     int      `json:"in_file"`
	Missing    int      `json:"missing"`
	MissingIDs []string `json:"missing_ids"`
	Message    string   `json:"message,omitempty"`
}

// computeExportCoverage applies the coverage rules shared by every
// --check=export result (and pinned by the unit and embedded tests):
//
//   - A present file is compared even when auto-export is disabled: an
//     explicitly requested diagnostic must not declare a stale manually
//     maintained file healthy because automatic maintenance is off.
//   - In-scope store IDs absent from the file are missing. Extra file IDs
//     alone never warn: deletes or a manual wider export are not missing
//     store records.
//   - A missing file warns only when auto-export is enabled (the file was
//     promised) and scope is nonempty. With auto-export disabled it is
//     explicitly not applicable — no parity claim for an absent file.
//
// Pure so the rules stay testable without a store.
func computeExportCoverage(result exportCoverageResult, inScope []string, inFileIDs []string, fileExists bool) exportCoverageResult {
	sort.Strings(inScope)
	result.InScope = len(inScope)
	if fileExists {
		result.InFile = len(inFileIDs)
		fileSet := make(map[string]struct{}, len(inFileIDs))
		for _, id := range inFileIDs {
			fileSet[id] = struct{}{}
		}
		var missing []string
		for _, id := range inScope {
			if _, ok := fileSet[id]; !ok {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		if len(missing) == 0 {
			// Extra file IDs at this point are deletes or a manual wider
			// export — informational, never missing store records.
			result.Missing = 0
			result.MissingIDs = []string{}
			result.Status = statusOK
			return result
		}
		result.Status = statusWarning
		result.Missing = len(missing)
		result.MissingIDs = exportCoverageSamples(missing)
		return result
	}

	// No file to compare against.
	if !result.ExportAuto {
		result.Status = statusNotApplicable
		result.Message = "no passive export file; auto-export disabled"
		return result
	}
	if len(inScope) == 0 {
		result.Status = statusOK
		result.Missing = 0
		result.MissingIDs = []string{}
		result.Message = "no issues in scope; nothing to export"
		return result
	}
	result.Status = statusWarning
	result.Missing = len(inScope)
	result.MissingIDs = exportCoverageSamples(inScope)
	return result
}

// exportCoverageSamples returns at most doctorExportMissingIDSamples sorted
// IDs.
func exportCoverageSamples(ids []string) []string {
	if len(ids) > doctorExportMissingIDSamples {
		ids = ids[:doctorExportMissingIDSamples]
	}
	return append([]string{}, ids...)
}

// runExportCoverageCheck implements `bd doctor --check=export`: it compares
// in-scope store issue IDs against the IDs in the passive export file
// resolved exactly like the auto-export writer resolves it (export.path
// config, global default, .beads/issues.jsonl) — coverage only, prosecuted
// as a warning, never an error, so it stays composable in scripts
// (GH#6955). Read-only even when combined with --fix/--clean; the existing
// export overwrite protections stay authoritative for any refresh.
func runExportCoverageCheck(absPath string) error {
	// Same-workspace rule: never compare one workspace's store against
	// another's JSONL. The global store binds to the workspace found from
	// CWD (ensureDirectMode), so a positional path naming a different
	// workspace is rejected rather than silently mis-inspected.
	targetBeads := beads.FindBeadsDirFrom(absPath)
	if targetBeads == "" {
		return HandleErrorWithHint(
			fmt.Sprintf("no beads workspace found at %s", absPath),
			"Run bd doctor --check=export from inside the workspace to inspect",
		)
	}
	storeBeads := beads.FindBeadsDir()
	if storeBeads == "" || utils.CanonicalizePath(targetBeads) != utils.CanonicalizePath(storeBeads) {
		return HandleErrorWithHint(
			fmt.Sprintf("refusing to compare workspaces: %s belongs to %s, but the store would open the workspace at %s", absPath, targetBeads, storeBeads),
			"Run bd doctor --check=export from inside the named workspace",
		)
	}
	if err := ensureDirectMode("export coverage check requires direct mode"); err != nil {
		return HandleError("%v", err)
	}

	ctx := rootCtx
	fullPath := autoExportJSONLPath(storeBeads)
	result := exportCoverageResult{
		Check:      "export",
		Path:       fullPath,
		ExportAuto: config.GetBool("export.auto"),
	}

	// In-scope selection shared with the full auto-export writer: the same
	// filter (unlimited, infra/template/ephemeral exclusions) and the same
	// config-based owner exclusions, so the file is only expected to cover
	// what the writer would put in it.
	filter, _ := buildAutoExportFilter(ctx)
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleError("fetching issues: %v", err)
	}
	inScope := make([]string, 0, len(issues))
	for _, issue := range issues {
		inScope = append(inScope, issue.ID)
	}
	if ownerExcludes := buildOwnerExcludeSet(ctx, storeExportSource{}, nil); len(ownerExcludes) > 0 {
		issues = filterOutOwners(issues, ownerExcludes)
		// Recompute IDs from the owner-filtered slice.
		inScope = inScope[:0]
		for _, issue := range issues {
			inScope = append(inScope, issue.ID)
		}
	}

	// Read the inspected file. Parse failures must surface as command
	// errors: a malformed JSONL is not evidence of a trustworthy comparison
	// and must not become an OK or N/A parity result.
	var inFileIDs []string
	fileExists := true
	if _, statErr := os.Stat(fullPath); statErr == nil {
		records, recordsErr := issueRecordsInJSONL(fullPath)
		if recordsErr != nil {
			return HandleError("reading export file %s: %v", fullPath, recordsErr)
		}
		inFileIDs = make([]string, 0, len(records))
		for _, rec := range records {
			inFileIDs = append(inFileIDs, rec.ID)
		}
	} else if !os.IsNotExist(statErr) {
		return HandleError("inspecting export file %s: %v", fullPath, statErr)
	} else {
		fileExists = false
	}

	result = computeExportCoverage(result, inScope, inFileIDs, fileExists)

	if jsonOutput {
		return outputJSON(result)
	}
	printExportCoverage(result)
	return nil
}

// printExportCoverage renders the human form of the export coverage result:
// existing small status symbols, the resolved path, the missing count with
// bounded samples, and the refresh action. A warning is still a successful
// inspection (exit 0).
func printExportCoverage(result exportCoverageResult) {
	switch result.Status {
	case statusOK:
		fmt.Printf("%s export coverage: all %d in-scope issue IDs present in %s\n",
			ui.RenderPassIcon(), result.InScope, result.Path)
		if result.Message != "" {
			fmt.Printf("  %s%s\n", ui.TreeLast, ui.RenderMuted(result.Message))
		}
	case statusWarning:
		fmt.Printf("%s %d in-scope issue IDs missing from %s\n",
			ui.RenderWarnIcon(), result.Missing, result.Path)
		fmt.Printf("  %s%s\n", ui.TreeLast, ui.RenderMuted("missing: "+strings.Join(result.MissingIDs, ", ")))
		fmt.Printf("  %sRefresh with: %s\n", ui.TreeLast, ui.RenderCommand("bd export -o "+result.Path))
	default: // statusNotApplicable
		fmt.Printf("%s %s (%s)\n",
			ui.RenderSkipIcon(), result.Message, result.Path)
	}
}
