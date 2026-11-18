package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// embeddedStubProseForTest is the prose stub as printEmbeddedUnsupported
// printed it before GH#8113; the nil host-issue path must stay byte-identical.
func embeddedStubProseForTest(label string) string {
	return "Note: 'bd " + label + "' is not yet supported in embedded mode.\n\n" +
		"For embedded mode troubleshooting:\n" +
		"  • Verify database exists:  ls -la .beads/embeddeddolt/\n" +
		"  • Check bd version:        bd version\n" +
		"  • If it will not open:     preserve it first, then recover: " + doctor.EngineWontOpenRecoveryURL + "\n" +
		"  • Switch to server mode:   bd init --server\n" +
		"\n" +
		"Checks available in embedded mode:\n" +
		"  • bd doctor --check=artifacts\n" +
		"  • bd doctor --check=conventions\n" +
		"  • bd doctor --check=export\n" +
		"  • bd doctor --check=pollution\n"
}

func setDoctorOutputModeForTest(t *testing.T, json, agent bool) {
	t.Helper()
	oldJSON, oldAgent := jsonOutput, doctorAgent
	jsonOutput, doctorAgent = json, agent
	t.Cleanup(func() { jsonOutput, doctorAgent = oldJSON, oldAgent })
}

func decodeEmbeddedStubForTest(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("stub is not JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stub is more than one JSON object:\n%s", out)
	}
	if data, ok := obj["data"].(map[string]interface{}); ok {
		return data
	}
	return obj
}

func TestPrintEmbeddedUnsupportedWithHostIssues(t *testing.T) {
	issue := doctorCheck{
		Name:     doctor.MacOSLegacyUserConfigCheckName,
		Status:   statusWarning,
		Message:  "bd no longer reads ~/Library/Application Support/bd/config.yaml",
		Detail:   "Settings there that differ from your current user config: actor",
		Fix:      "If you had turned metrics off, run: bd metrics off\nMove it to where bd reads it: mkdir -p /h/.config/bd && mv '/h/Library/Application Support/bd/config.yaml' /h/.config/bd/config.yaml",
		Category: doctor.CategoryData,
	}

	t.Run("nil prose is byte-identical", func(t *testing.T) {
		setDoctorOutputModeForTest(t, false, false)
		for _, label := range []string{"doctor", "doctor --perf", "doctor --check=validate"} {
			want := embeddedStubProseForTest(label)
			if got := captureStderrDuring(t, func() { printEmbeddedUnsupported(label) }); got != want {
				t.Errorf("%s: printEmbeddedUnsupported =\n%q\nwant\n%q", label, got, want)
			}
			if got := captureStderrDuring(t, func() { printEmbeddedUnsupportedWith(label, nil) }); got != want {
				t.Errorf("%s: nil host issues =\n%q\nwant\n%q", label, got, want)
			}
		}
	})

	t.Run("nil JSON has no host_checks", func(t *testing.T) {
		for _, agent := range []bool{false, true} {
			setDoctorOutputModeForTest(t, !agent, agent)
			want := captureStderrDuring(t, func() { printEmbeddedUnsupported("doctor") })
			got := captureStderrDuring(t, func() { printEmbeddedUnsupportedWith("doctor", nil) })
			if got != want {
				t.Errorf("agent=%v: nil host issues changed the JSON stub", agent)
			}
			if _, ok := decodeEmbeddedStubForTest(t, got)["host_checks"]; ok {
				t.Errorf("agent=%v: host_checks present with no issues", agent)
			}
		}
	})

	t.Run("prose shows the issue above Note", func(t *testing.T) {
		setDoctorOutputModeForTest(t, false, false)
		var stdout string
		got := captureStderrDuring(t, func() {
			stdout = captureStdout(t, func() error {
				printEmbeddedUnsupportedWith("doctor", []doctorCheck{issue})
				return nil
			})
		})
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		note := strings.Index(got, "Note: 'bd doctor'")
		if note < 0 || !strings.HasSuffix(got, embeddedStubProseForTest("doctor")) {
			t.Fatalf("stub tail changed:\n%s", got)
		}
		head := got[:note]
		for _, want := range []string{
			issue.Name + ": " + issue.Message,
			issue.Detail,
			"If you had turned metrics off, run: bd metrics off\n",
			"        Move it to where bd reads it: mkdir -p /h/.config/bd && mv '/h/Library/Application Support/bd/config.yaml' /h/.config/bd/config.yaml\n",
		} {
			if !strings.Contains(head, want) {
				t.Errorf("issue block missing %q:\n%s", want, head)
			}
		}
		if !strings.HasSuffix(head, "\n\n") {
			t.Errorf("no blank line between the issue and Note:\n%q", head)
		}
	})

	t.Run("prose escapes control characters", func(t *testing.T) {
		setDoctorOutputModeForTest(t, false, false)
		bad := issue
		bad.Detail = "evil\x1b[2Kdetail"
		got := captureStderrDuring(t, func() { printEmbeddedUnsupportedWith("doctor", []doctorCheck{bad}) })
		if strings.Contains(got, "\x1b[2K") {
			t.Fatalf("raw escape printed:\n%q", got)
		}
	})

	t.Run("JSON carries host_checks", func(t *testing.T) {
		setDoctorOutputModeForTest(t, true, false)
		got := captureStderrDuring(t, func() { printEmbeddedUnsupportedWith("doctor", []doctorCheck{issue}) })
		obj := decodeEmbeddedStubForTest(t, got)
		if obj["code"] != "embedded_unsupported" {
			t.Errorf("code = %v", obj["code"])
		}
		checks, ok := obj["host_checks"].([]interface{})
		if !ok || len(checks) != 1 {
			t.Fatalf("host_checks = %#v", obj["host_checks"])
		}
		c := checks[0].(map[string]interface{})
		if c["name"] != issue.Name || c["status"] != statusWarning || c["fix"] != issue.Fix || c["category"] != doctor.CategoryData {
			t.Errorf("host check = %#v", c)
		}
	})
}

func TestHostLevelDoctorIssuesOnlyReportsMacProblems(t *testing.T) {
	for _, tc := range []struct {
		goos   string
		legacy bool
		want   int
	}{
		{goos: "darwin", legacy: true, want: 1},
		{goos: "darwin"},
		{goos: "linux", legacy: true},
		{goos: "windows", legacy: true},
	} {
		t.Run(fmt.Sprintf("%s legacy=%v", tc.goos, tc.legacy), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			if tc.legacy {
				legacy := filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
				if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(legacy, []byte("actor: alice\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			setMacOSLegacyUserConfigGOOSForTest(t, tc.goos)
			if got := hostLevelDoctorIssues(); len(got) != tc.want {
				t.Errorf("%d issues %+v, want %d", len(got), got, tc.want)
			}
		})
	}
}

func setMacOSLegacyUserConfigGOOSForTest(t *testing.T, goos string) {
	t.Helper()
	old := macOSLegacyUserConfigGOOS
	macOSLegacyUserConfigGOOS = goos
	t.Cleanup(func() { macOSLegacyUserConfigGOOS = old })
}
