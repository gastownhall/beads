//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// TestDoctorBareEmbeddedGateShowsHostIssues runs doctor's RunE through the
// embedded-mode gate with hostLevelDoctorIssues stubbed: bare bd doctor shows
// the host issue above "Note:" in prose and as host_checks in JSON (GH#8113),
// and --deep, --server, --migration and --fix keep the plain stub. It fails
// if the gate goes back to printEmbeddedUnsupported("doctor").
func TestDoctorBareEmbeddedGateShowsHostIssues(t *testing.T) {
	issue := doctorCheck{
		Name:     doctor.MacOSLegacyUserConfigCheckName,
		Status:   statusWarning,
		Message:  "bd no longer reads ~/Library/Application Support/bd/config.yaml",
		Detail:   "Settings there that differ from your current user config: actor",
		Fix:      "Move it to where bd reads it: mkdir -p /h/.config/bd && mv '/h/Library/Application Support/bd/config.yaml' /h/.config/bd/config.yaml",
		Category: doctor.CategoryData,
	}
	oldIssues := hostLevelDoctorIssues
	hostLevelDoctorIssues = func() []doctorCheck { return []doctorCheck{issue} }
	t.Cleanup(func() { hostLevelDoctorIssues = oldIssues })

	oldServer, oldProxied := serverMode, proxiedServerMode
	serverMode, proxiedServerMode = false, false
	t.Cleanup(func() { serverMode, proxiedServerMode = oldServer, oldProxied })
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DIR", "")

	oldDeep, oldServerFlag, oldMigration, oldFix := doctorDeep, doctorServer, doctorMigration, doctorFix
	oldPerf, oldCheck, oldHealth := perfMode, doctorCheckFlag, checkHealthMode
	t.Cleanup(func() {
		doctorDeep, doctorServer, doctorMigration, doctorFix = oldDeep, oldServerFlag, oldMigration, oldFix
		perfMode, doctorCheckFlag, checkHealthMode = oldPerf, oldCheck, oldHealth
	})
	perfMode, doctorCheckFlag, checkHealthMode = false, "", false

	run := func(t *testing.T, json bool) string {
		t.Helper()
		setDoctorOutputModeForTest(t, json, false)
		if !isEmbeddedMode() {
			t.Fatal("precondition: the test process is not in embedded mode")
		}
		dir := t.TempDir()
		var runErr error
		out := captureStderrDuring(t, func() { runErr = doctorCmd.RunE(doctorCmd, []string{dir}) })
		if runErr != nil {
			t.Fatalf("doctor RunE: %v\n%s", runErr, out)
		}
		return out
	}
	reset := func() { doctorDeep, doctorServer, doctorMigration, doctorFix = false, false, "", false }

	t.Run("bare prose", func(t *testing.T) {
		reset()
		out := run(t, false)
		note := strings.Index(out, "Note: 'bd doctor' is not yet supported in embedded mode.")
		if note < 0 {
			t.Fatalf("not the embedded stub:\n%s", out)
		}
		if i := strings.Index(out, issue.Name+": "+issue.Message); i < 0 || i > note {
			t.Fatalf("host issue missing or below Note:\n%s", out)
		}
	})
	t.Run("bare JSON", func(t *testing.T) {
		reset()
		obj := decodeEmbeddedStubForTest(t, run(t, true))
		checks, ok := obj["host_checks"].([]interface{})
		if !ok || len(checks) != 1 || checks[0].(map[string]interface{})["name"] != issue.Name {
			t.Fatalf("host_checks = %#v", obj["host_checks"])
		}
	})
	for _, mode := range []struct {
		name string
		set  func()
	}{
		{"deep", func() { doctorDeep = true }},
		{"server", func() { doctorServer = true }},
		{"migration", func() { doctorMigration = "pre" }},
		{"fix", func() { doctorFix = true }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			reset()
			mode.set()
			out := run(t, false)
			if !strings.Contains(out, "Note: 'bd doctor' is not yet supported in embedded mode.") {
				t.Fatalf("not the embedded stub:\n%s", out)
			}
			if strings.Contains(out, issue.Name) {
				t.Fatalf("host issue shown with --%s:\n%s", mode.name, out)
			}
		})
	}
}
