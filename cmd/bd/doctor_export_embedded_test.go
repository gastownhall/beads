//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type doctorExportJSON struct {
	Checks []doctorCheck     `json:"checks"`
	Export exportCheckReport `json:"export"`
}

// runDoctorExportCheck runs `bd doctor --check=export --json` in dir, fails
// the test unless it exits 0, and decodes its report.
func runDoctorExportCheck(t *testing.T, bd, dir string) doctorExportJSON {
	t.Helper()
	cmd := exec.Command(bd, "doctor", "--check=export", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd doctor --check=export --json: %v (want exit 0 even when stale)\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var got doctorExportJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode doctor JSON: %v\n%s", err, stdout.String())
	}
	if len(got.Checks) != 1 {
		t.Fatalf("doctor JSON has %d checks, want 1:\n%s", len(got.Checks), stdout.String())
	}
	return got
}

// TestEmbeddedDoctorExportCheckReportsMissing runs the export staleness check
// end to end on an embedded workspace (GH#6955): an issue created after the
// last export is reported missing, while an ephemeral issue (which export
// leaves out) and an id only the file has are not.
func TestEmbeddedDoctorExportCheckReportsMissing(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt doctor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dx")
	exportPath := filepath.Join(beadsDir, "issues.jsonl")

	exported := strings.TrimSpace(bdCreateSilent(t, bd, dir, "exported issue"))
	bdExport(t, bd, dir, "-o", exportPath)
	f, err := os.OpenFile(exportPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"dx-gone","title":"only in the file","issue_type":"task"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	stale := strings.TrimSpace(bdCreateSilent(t, bd, dir, "created after the export"))
	bdCreateSilent(t, bd, dir, "ephemeral issue", "--ephemeral")

	got := runDoctorExportCheck(t, bd, dir)
	if got.Export.Path != exportPath || !got.Export.FilePresent {
		t.Fatalf("export report = %+v, want the present file %s", got.Export, exportPath)
	}
	if got.Export.InScope != 2 || got.Export.Missing != 1 || !reflect.DeepEqual(got.Export.MissingIDs, []string{stale}) {
		t.Fatalf("export report = %+v, want 2 in scope (%s, %s) and only %s missing", got.Export, exported, stale, stale)
	}
	c := got.Checks[0]
	if c.Status != statusWarning {
		t.Fatalf("check status = %q, want %q: %+v", c.Status, statusWarning, c)
	}
	if !strings.Contains(c.Detail, stale) || !strings.Contains(c.Fix, "bd export -o") {
		t.Fatalf("check = %+v, want %s sampled and the bd export -o refresh", c, stale)
	}
}

// TestEmbeddedDoctorExportCheckCleanAfterManualExport pins the refresh the
// check suggests: once `bd export -o` rewrites a stale export, every
// in-scope id is in the file and the check reports nothing missing.
func TestEmbeddedDoctorExportCheckCleanAfterManualExport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt doctor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dr")
	exportPath := filepath.Join(beadsDir, "issues.jsonl")

	bdCreateSilent(t, bd, dir, "first issue")
	bdExport(t, bd, dir, "-o", exportPath)
	stale := strings.TrimSpace(bdCreateSilent(t, bd, dir, "second issue"))
	if got := runDoctorExportCheck(t, bd, dir); got.Export.Missing != 1 || !reflect.DeepEqual(got.Export.MissingIDs, []string{stale}) {
		t.Fatalf("before the refresh: export report = %+v, want only %s missing", got.Export, stale)
	}

	bdExport(t, bd, dir, "-o", exportPath)
	got := runDoctorExportCheck(t, bd, dir)
	if got.Export.InScope != 2 || got.Export.Missing != 0 || len(got.Export.MissingIDs) != 0 {
		t.Fatalf("after bd export -o: export report = %+v, want both issues present and none missing", got.Export)
	}
	if c := got.Checks[0]; c.Status != statusOK || c.Fix != "" {
		t.Fatalf("after bd export -o: check = %+v, want ok with no fix", c)
	}
}
