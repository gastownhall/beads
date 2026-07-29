package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIsNonInteractiveInit tests the non-interactive detection logic.
func TestIsNonInteractiveInit(t *testing.T) {
	// Save original env vars and restore after tests
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	tests := []struct {
		name      string
		flagValue bool
		envCI     string
		envBDNI   string
		want      bool
	}{
		{
			name:      "flag true overrides everything",
			flagValue: true,
			envCI:     "",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=1",
			flagValue: false,
			envCI:     "",
			envBDNI:   "1",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=true",
			flagValue: false,
			envCI:     "",
			envBDNI:   "true",
			want:      true,
		},
		{
			name:      "CI=true",
			flagValue: false,
			envCI:     "true",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "CI=1",
			flagValue: false,
			envCI:     "1",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "CI=false does not trigger",
			flagValue: false,
			envCI:     "false",
			envBDNI:   "",
			// In test env, stdin is not a TTY, so this is still true
			want: true,
		},
		{
			name:      "no flag no env falls back to terminal detection",
			flagValue: false,
			envCI:     "",
			envBDNI:   "",
			// In test environment, stdin is piped (not a TTY), so non-interactive
			want: true,
		},
		{
			name:      "BD_NON_INTERACTIVE=0 forces interactive",
			flagValue: false,
			envCI:     "true",
			envBDNI:   "0",
			want:      false,
		},
		{
			name:      "BD_NON_INTERACTIVE=false forces interactive",
			flagValue: false,
			envCI:     "true",
			envBDNI:   "false",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("CI", tt.envCI)
			os.Setenv("BD_NON_INTERACTIVE", tt.envBDNI)

			got := isNonInteractiveInit(tt.flagValue)
			if got != tt.want {
				t.Errorf("isNonInteractiveInit(%v) with CI=%q BD_NON_INTERACTIVE=%q = %v, want %v",
					tt.flagValue, tt.envCI, tt.envBDNI, got, tt.want)
			}
		})
	}
}

// TestIsNonInteractiveInitPrecedence tests that flag takes precedence over env vars.
func TestIsNonInteractiveInitPrecedence(t *testing.T) {
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	// Flag true should always win
	os.Setenv("CI", "")
	os.Setenv("BD_NON_INTERACTIVE", "")
	if !isNonInteractiveInit(true) {
		t.Error("flag=true should always return true regardless of env")
	}

	// BD_NON_INTERACTIVE should take precedence over CI
	os.Setenv("BD_NON_INTERACTIVE", "1")
	os.Setenv("CI", "")
	if !isNonInteractiveInit(false) {
		t.Error("BD_NON_INTERACTIVE=1 should return true")
	}
}

func TestInitNonInteractiveAutoExportDefaultOffAndOptIn(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()

	runBDForAutoExportInitTest(t, bd, dir, "init", "--prefix", "test", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	if got := strings.TrimSpace(runBDStdoutForAutoExportInitTest(t, bd, dir, "config", "get", "export.auto")); got != "false" {
		t.Fatalf("export.auto default = %q, want false", got)
	}
	if got := strings.TrimSpace(runBDStdoutForAutoExportInitTest(t, bd, dir, "config", "get", "export.git-add")); got != "false" {
		t.Fatalf("export.git-add default = %q, want false", got)
	}

	runBDForAutoExportInitTest(t, bd, dir, "create", "default-off issue", "-p", "2")
	jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Fatalf("default-off create wrote %s; stat err=%v", jsonlPath, err)
	}

	runBDForAutoExportInitTest(t, bd, dir, "config", "set", "export.interval", "1ms")
	runBDForAutoExportInitTest(t, bd, dir, "config", "set", "export.auto", "true")
	if got := strings.TrimSpace(runBDStdoutForAutoExportInitTest(t, bd, dir, "config", "get", "export.auto")); got != "true" {
		t.Fatalf("explicit export.auto = %q, want true", got)
	}
	time.Sleep(10 * time.Millisecond)
	runBDForAutoExportInitTest(t, bd, dir, "create", "explicit export issue", "-p", "2")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("explicit export.auto did not write %s: %v", jsonlPath, err)
	}
	if !strings.Contains(string(data), "explicit export issue") {
		t.Fatalf("JSONL export did not contain created issue:\n%s", data)
	}
}

func runBDStdoutForAutoExportInitTest(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BD_NON_INTERACTIVE=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd %v failed: %v", args, err)
	}
	return string(out)
}

func runBDForAutoExportInitTest(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BD_NON_INTERACTIVE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}
