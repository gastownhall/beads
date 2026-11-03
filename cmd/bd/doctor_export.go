package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
)

// exportMissingSampleLimit bounds the missing ids a report names.
const exportMissingSampleLimit = 5

// exportCheckReport is what `bd doctor --check=export` found: how the JSONL
// export at Path compares with the issues auto-export would write to it.
type exportCheckReport struct {
	Path        string   `json:"path"`
	AutoExport  bool     `json:"export_auto"`
	FilePresent bool     `json:"file_present"`
	InScope     int      `json:"in_scope"`
	InFile      int      `json:"in_file"`
	Missing     int      `json:"missing"`
	MissingIDs  []string `json:"missing_ids,omitempty"`
}

// runExportCheck reports store issues that the JSONL export lacks (GH#6955).
// The export is a passive snapshot: export.interval only spaces automatic
// exports out, so nothing else tells a user the file has fallen behind. It
// only reads; `bd export -o <path>` is the refresh it suggests. A stale file
// is a warning and exits 0.
func runExportCheck(path string) error {
	// doctor opts out of PersistentPreRun DB init via skipStoreAnnotation;
	// ensureDirectMode opens the store auto-export would read (GH#3597).
	if err := ensureDirectMode("export check requires direct mode"); err != nil {
		return HandleError("%v", err)
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return HandleError("export check: no .beads directory found")
	}
	report, err := collectExportCheck(getRootContext(), beadsDir)
	if err != nil {
		return HandleError("export check: %v", err)
	}
	check := report.check()

	if jsonOutput {
		return outputJSON(struct {
			Path      string            `json:"path"`
			Checks    []doctorCheck     `json:"checks"`
			OverallOK bool              `json:"overall_ok"`
			Export    exportCheckReport `json:"export"`
		}{
			Path:      path,
			Checks:    []doctorCheck{check},
			OverallOK: check.Status == statusOK,
			Export:    report,
		})
	}

	fmt.Println()
	fmt.Println(ui.RenderCategory("Export"))
	c := check.printed()
	icon := ui.RenderPassIcon()
	if c.Status != statusOK {
		icon = ui.RenderWarnIcon()
	}
	fmt.Printf("  %s  %s%s\n", icon, c.Name, ui.RenderMuted(" "+c.Message))
	if c.Detail != "" {
		fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(c.Detail))
	}
	if c.Fix != "" {
		fmt.Printf("     %s\n", ui.RenderMuted("Fix: "+c.Fix))
	}
	return nil
}

// collectExportCheck compares the ids auto-export would write (its filter and
// the export.exclude_owners config) with the issue ids in the export file.
// Ids the file has and the store does not are not staleness, and neither are
// the rows the filter leaves out, so neither counts.
func collectExportCheck(ctx context.Context, beadsDir string) (exportCheckReport, error) {
	report := exportCheckReport{
		Path:       autoExportPath(beadsDir),
		AutoExport: config.GetBool("export.auto"),
	}
	issues, err := store.SearchIssues(ctx, "", autoExportFilter(ctx))
	if err != nil {
		return report, fmt.Errorf("search issues: %w", err)
	}
	if ownerExcludes := buildOwnerExcludeSet(ctx, storeExportSource{}, nil); len(ownerExcludes) > 0 {
		issues = filterOutOwners(issues, ownerExcludes)
	}
	if _, err := os.Stat(report.Path); err == nil {
		report.FilePresent = true
	} else if !os.IsNotExist(err) {
		return report, remedy.PrintedError(err)
	}
	records, err := issueRecordsInJSONL(report.Path)
	if err != nil {
		return report, fmt.Errorf("read %s: %w", remedy.PrintedValue(report.Path), remedy.PrintedError(err))
	}

	inFile := make(map[string]bool, len(records))
	for _, r := range records {
		inFile[r.ID] = true
	}
	var missing []string
	for _, issue := range issues {
		if !inFile[issue.ID] {
			missing = append(missing, issue.ID)
		}
	}
	sort.Strings(missing)
	report.InScope = len(issues)
	report.InFile = len(records)
	report.Missing = len(missing)
	if len(missing) > exportMissingSampleLimit {
		missing = missing[:exportMissingSampleLimit]
	}
	report.MissingIDs = missing
	return report, nil
}

// check renders the report as a doctor check. A file that is absent while
// auto-export is off is not stale: nothing promised to write it.
func (r exportCheckReport) check() doctorCheck {
	c := doctorCheck{Name: "Export Freshness", Status: statusOK, Category: "export"}
	switch {
	case !r.FilePresent && !r.AutoExport:
		c.Message = "No export file (export.auto is off)"
		return c
	case r.Missing == 0:
		c.Message = fmt.Sprintf("%s lists all %d in-scope issues", remedy.PrintedValue(r.Path), r.InScope)
		return c
	}
	c.Status = statusWarning
	if r.FilePresent {
		c.Message = fmt.Sprintf("%d in-scope issues missing from %s", r.Missing, remedy.PrintedValue(r.Path))
	} else {
		c.Message = fmt.Sprintf("%s does not exist; %d in-scope issues are not exported", remedy.PrintedValue(r.Path), r.Missing)
	}
	samples := make([]string, len(r.MissingIDs))
	for i, id := range r.MissingIDs {
		samples[i] = remedy.PrintedWord(id)
	}
	c.Detail = "Missing: " + strings.Join(samples, ", ")
	if r.Missing > len(r.MissingIDs) {
		c.Detail += fmt.Sprintf(" (and %d more)", r.Missing-len(r.MissingIDs))
	}
	arg, note := remedy.CommandArg(runtime.GOOS, r.Path, "<path>")
	c.Fix = "Refresh the export: bd export -o " + arg + note
	return c
}
