package main

import (
	"reflect"
	"testing"
)

// exportCoverageFor returns the coverage result of in-scope store IDs vs the
// issue IDs found in the passive export file, computed for test cases.
func exportCoverageFor(t *testing.T, inScope, inFile []string, fileExists, exportAuto bool) exportCoverageResult {
	t.Helper()
	return computeExportCoverage(exportCoverageResult{
		Check:      "export",
		Path:       "/repo/.beads/issues.jsonl",
		ExportAuto: exportAuto,
	}, inScope, inFile, fileExists)
}

func TestComputeExportCoverageFullyCoveredIsOK(t *testing.T) {
	result := exportCoverageFor(t,
		[]string{"bd-a1", "bd-b2", "bd-c3"},
		[]string{"bd-a1", "bd-c3", "bd-b2"},
		true, false)
	if result.Status != statusOK {
		t.Errorf("status = %q, want %q for a fully covered in-scope set", result.Status, statusOK)
	}
	if result.InScope != 3 || result.InFile != 3 || result.Missing != 0 {
		t.Errorf("in_scope = %d, in_file = %d, missing = %d, want 3, 3, 0", result.InScope, result.InFile, result.Missing)
	}
	// A present file is compared even with auto-export disabled: an
	// explicitly requested diagnostic must not bless a stale manually
	// maintained file just because automatic maintenance is off.
	if result.Status != statusOK || exportCoverageFor(t, []string{"bd-a1", "bd-b2"}, []string{"bd-a1"}, true, false).Status != statusWarning {
		t.Error("export_auto=false must not silence a coverage gap in a present file")
	}
}

func TestComputeExportCoverageMissingIDsWarnWithBoundedSamples(t *testing.T) {
	inScope := []string{"bd-a0", "bd-a1", "bd-a2", "bd-a3", "bd-a4", "bd-a5", "bd-a6"}
	inFile := []string{"bd-a6"} // covered elsewhere: 6 missing
	result := exportCoverageFor(t, inScope, inFile, true, true)
	if result.Status != statusWarning {
		t.Errorf("status = %q, want %q", result.Status, statusWarning)
	}
	if result.Missing != 6 {
		t.Errorf("missing = %d, want 6 (the full size, not the sample count)", result.Missing)
	}
	wantSamples := []string{"bd-a0", "bd-a1", "bd-a2", "bd-a3", "bd-a4"}
	if !reflect.DeepEqual(result.MissingIDs, wantSamples) {
		t.Errorf("missing_ids = %v, want the first %d sorted IDs %v", result.MissingIDs, doctorExportMissingIDSamples, wantSamples)
	}
}

func TestComputeExportCoverageExtraFileIDsNeverWarn(t *testing.T) {
	// Deletes or a manual wider export put extra IDs in the file; none of
	// them is a missing store record, and the in-scope set is covered.
	result := exportCoverageFor(t,
		[]string{"bd-a1"},
		[]string{"bd-a1", "bd-gone", "bd-manual"},
		true, true)
	if result.Status != statusOK {
		t.Errorf("status = %q, want %q — extra file IDs alone are informational", result.Status, statusOK)
	}
	if result.Missing != 0 || len(result.MissingIDs) != 0 {
		t.Errorf("missing = %d, missing_ids = %v, want zero", result.Missing, result.MissingIDs)
	}
}

func TestComputeExportCoverageMissingFileRules(t *testing.T) {
	// Auto-export enabled and content to cover: every in-scope ID is
	// missing, and that is a warning, not an error.
	warn := exportCoverageFor(t, []string{"bd-a1", "bd-b2"}, nil, false, true)
	if warn.Status != statusWarning {
		t.Errorf("status = %q, want %q for a missing file with auto-export enabled and nonempty scope", warn.Status, statusWarning)
	}
	if warn.Missing != 2 {
		t.Errorf("missing = %d, want 2 — the whole in-scope set", warn.Missing)
	}

	// Empty scope: nothing was promised, nothing is missing.
	ok := exportCoverageFor(t, nil, nil, false, true)
	if ok.Status != statusOK {
		t.Errorf("status = %q, want %q for a missing file with empty scope", ok.Status, statusOK)
	}

	// Auto-export disabled: explicitly not applicable — the diagnostic
	// must not claim parity for a file nobody promised, and it must say
	// why via the message.
	na := exportCoverageFor(t, []string{"bd-a1"}, nil, false, false)
	if na.Status != statusNotApplicable {
		t.Errorf("status = %q, want %q for a missing file with auto-export disabled", na.Status, statusNotApplicable)
	}
	if na.Message == "" {
		t.Error("message is empty; the N/A result must include the explanation")
	}
	if na.Missing != 0 || len(na.MissingIDs) != 0 {
		t.Errorf("N/A result reports missing = %d, missing_ids = %v — no parity claim may hide there", na.Missing, na.MissingIDs)
	}
}
