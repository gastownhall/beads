package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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
		result.Message = exportRefreshMessage(result.Path)
		return result
	}

	// No file to compare against.
	if !result.ExportAuto {
		result.Status = statusNotApplicable
		result.MissingIDs = []string{}
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
	result.Message = exportRefreshMessage(result.Path)
	return result
}

// exportRefreshMessage is the refresh guidance a warning result carries, so
// JSON consumers get the same action the human output prints.
func exportRefreshMessage(path string) string {
	cmd, note := exportRefreshCommand(path)
	return "refresh with: " + cmd + note
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
// (GH#6955). Read-only even when combined with --fix/--clean: the store
// opens read-only (openExportCheckStore), and the invocation also skips the
// post-command maintenance net, whose auto-export would otherwise refresh
// and stage the file this check just reported on. The existing export
// overwrite protections stay authoritative for any refresh. explicitPath
// says absPath came from a positional argument.
func runExportCoverageCheck(absPath string, explicitPath bool) error {
	// The store opens the workspace bd selected for this invocation
	// (BEADS_DIR, --db, or discovery from CWD).
	storeBeads := beads.FindBeadsDir()
	// Same-workspace rule: never compare one workspace's store against
	// another's JSONL, so a positional path naming a different workspace is
	// rejected rather than silently mis-inspected. Without one, the target
	// is the selected workspace itself: rediscovering it from its parent
	// directory would miss a BEADS_DIR that is the account home's .beads
	// (GH#4402) or is not named .beads at all.
	if explicitPath {
		targetBeads := beads.FindBeadsDirFrom(absPath)
		if targetBeads == "" {
			return HandleErrorWithHint(
				fmt.Sprintf("no beads workspace found at %s", remedy.PrintedValue(absPath)),
				"Run bd doctor --check=export from inside the workspace to inspect",
			)
		}
		if storeBeads == "" || utils.CanonicalizePath(targetBeads) != utils.CanonicalizePath(storeBeads) {
			return HandleErrorWithHint(
				fmt.Sprintf("refusing to compare workspaces: %s belongs to %s, but the store would open the workspace at %s",
					remedy.PrintedValue(absPath), remedy.PrintedValue(targetBeads), remedy.PrintedValue(storeBeads)),
				"Run bd doctor --check=export from inside the named workspace",
			)
		}
	}
	// No post-run auto-export, git add, backup or push behind this check.
	commandSkipsPostRunMaintenance.Store(true)
	if err := openExportCheckStore(rootCtx, storeBeads); err != nil {
		return err
	}

	result, err := collectExportCoverage(rootCtx, storeBeads)
	if err != nil {
		return HandleError("export check: %v", err)
	}
	if jsonOutput {
		return outputJSON(result)
	}
	printExportCoverage(result)
	return nil
}

// openExportCheckStore installs the store --check=export reads: beadsDir's,
// opened by newReadOnlyStoreFromConfig. That open writes nothing (no data
// directory, CREATE DATABASE, remote-migrate gate or schema migration), the
// store refuses writes, and PersistentPostRunE closes it. An embedded
// workspace that holds no database yet, a fresh clone say (embeddeddolt/ is
// gitignored), is refused before any open, on the probe a routed write uses
// (embeddeddolt.HasRepository, see newExistingDoltStoreFromConfig): a
// created empty database would cover every in-scope ID, and bd bootstrap
// would then take it for the real one. Metadata the open would refuse, or
// cannot load, is left to the open to name. It returns the rendered error.
func openExportCheckStore(ctx context.Context, beadsDir string) error {
	lockStore()
	active := isStoreActive() && getStore() != nil
	unlockStore()
	if active {
		return nil
	}
	if beadsDir == "" {
		return HandleError("%v.\nHint: run 'bd init' to create a database in the current directory", ErrNoBeadsDatabase)
	}
	if _, err := checkDiscoveredBackend(beadsDir); err == nil {
		if cfg, err := configfile.LoadForDiscovery(beadsDir); err == nil && selectsEmbeddedStore(beadsDir, cfg) && !embeddeddolt.HasRepository(beadsDir) {
			return HandleErrorWithHint(
				fmt.Sprintf("no embedded database at %s; the export check reads the store and does not create one",
					remedy.PrintedValue(filepath.Join(beadsDir, "embeddeddolt"))),
				"Run bd bootstrap to set up this workspace's database, then rerun bd doctor --check=export",
			)
		}
	}
	s, err := newReadOnlyStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return HandleError("%v\nHint: %s", openStoreError(backendNameForErrorFraming(beadsDir), err), diagHint())
	}
	lockStore()
	setStore(s)
	setStoreActive(true)
	unlockStore()
	return nil
}

// collectExportCoverage reads the in-scope store IDs and the IDs in the
// passive export file under beadsDir and applies computeExportCoverage.
// Store-read, stat and parse failures are returned as errors: a malformed
// JSONL is not evidence of a trustworthy comparison and must not become an
// OK or N/A parity result.
func collectExportCoverage(ctx context.Context, beadsDir string) (exportCoverageResult, error) {
	fullPath := autoExportJSONLPath(beadsDir)
	result := exportCoverageResult{
		Check:      "export",
		Path:       fullPath,
		ExportAuto: config.GetBool("export.auto"),
	}

	// In-scope selection shared with the full auto-export writer: the same
	// filter (unlimited, infra/template/ephemeral exclusions) and the same
	// config-based owner exclusions, so the file is only expected to cover
	// what the writer would put in it.
	issues, err := store.SearchIssues(ctx, "", autoExportFilter(ctx))
	if err != nil {
		return result, fmt.Errorf("fetching issues: %w", err)
	}
	if ownerExcludes := buildOwnerExcludeSet(ctx, storeExportSource{}, nil); len(ownerExcludes) > 0 {
		issues = filterOutOwners(issues, ownerExcludes)
	}
	inScope := make([]string, 0, len(issues))
	for _, issue := range issues {
		inScope = append(inScope, issue.ID)
	}

	var inFileIDs []string
	fileExists := true
	if _, statErr := os.Stat(fullPath); statErr == nil {
		records, recordsErr := issueRecordsInJSONL(fullPath)
		if recordsErr != nil {
			return result, fmt.Errorf("reading export file %s: %w", remedy.PrintedValue(fullPath), remedy.PrintedError(recordsErr))
		}
		inFileIDs = make([]string, 0, len(records))
		for _, rec := range records {
			inFileIDs = append(inFileIDs, rec.ID)
		}
	} else if !os.IsNotExist(statErr) {
		return result, fmt.Errorf("inspecting export file: %w", remedy.PrintedError(statErr))
	} else {
		fileExists = false
	}

	return computeExportCoverage(result, inScope, inFileIDs, fileExists), nil
}

// exportRefreshCommand is the refresh action for the export file at path,
// with the path quoted for the user's shell, or a placeholder plus an
// escaped note when it cannot be pasted as one argument.
func exportRefreshCommand(path string) (cmd, note string) {
	arg, note := remedy.CommandArg(runtime.GOOS, path, "<path>")
	return "bd export -o " + arg, note
}

// printExportCoverage renders the human form of the export coverage result:
// existing small status symbols, the resolved path, the missing count with
// bounded samples, and the refresh action. A warning is still a successful
// inspection (exit 0). Paths and IDs are spelled by remedy so a stored value
// cannot redraw the terminal or the refresh command.
func printExportCoverage(result exportCoverageResult) {
	path := remedy.PrintedValue(result.Path)
	switch result.Status {
	case statusOK:
		fmt.Printf("%s export coverage: all %d in-scope issue IDs present in %s\n",
			ui.RenderPassIcon(), result.InScope, path)
		if result.Message != "" {
			fmt.Printf("  %s%s\n", ui.TreeLast, ui.RenderMuted(result.Message))
		}
	case statusWarning:
		fmt.Printf("%s %d in-scope issue IDs missing from %s\n",
			ui.RenderWarnIcon(), result.Missing, path)
		samples := make([]string, len(result.MissingIDs))
		for i, id := range result.MissingIDs {
			samples[i] = remedy.PrintedWord(id)
		}
		detail := "missing: " + strings.Join(samples, ", ")
		if result.Missing > len(samples) {
			detail += fmt.Sprintf(" (and %d more)", result.Missing-len(samples))
		}
		fmt.Printf("  %s%s\n", ui.TreeLast, ui.RenderMuted(detail))
		cmd, note := exportRefreshCommand(result.Path)
		fmt.Printf("  %sRefresh with: %s%s\n", ui.TreeLast, ui.RenderCommand(cmd), note)
	default: // statusNotApplicable
		fmt.Printf("%s %s (%s)\n",
			ui.RenderSkipIcon(), result.Message, path)
	}
}
