package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// stubPrimeConfigBool makes primeConfigBool read from a map for one test.
func stubPrimeConfigBool(t *testing.T, values map[string]bool) {
	t.Helper()
	orig := primeConfigBool
	primeConfigBool = func(key string) bool { return values[key] }
	t.Cleanup(func() { primeConfigBool = orig })
}

// stagePrimeWorkspace makes a workspace whose briefing is a known custom
// PRIME.md with no memories, so an enabled bd prime visibly prints.
func stagePrimeWorkspace(t *testing.T, briefing string) {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"config.yaml": "", "PRIME.md": briefing} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)
	origNoMemories, origHookJSON := primeNoMemories, primeHookJSONMode
	primeNoMemories = true
	t.Cleanup(func() { primeNoMemories, primeHookJSONMode = origNoMemories, origHookJSON })
}

func hookEnvelope(t *testing.T, content string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := outputHookJSON(&buf, content); err != nil {
		t.Fatalf("outputHookJSON: %v", err)
	}
	return buf.String()
}

func TestPrimeDisabledReadsTheConfigKey(t *testing.T) {
	stubPrimeConfigBool(t, map[string]bool{})
	if primeDisabled() {
		t.Fatal("bd prime must be on unless prime.disabled is set")
	}
	stubPrimeConfigBool(t, map[string]bool{"prime.disabled": true})
	if !primeDisabled() {
		t.Fatal("prime.disabled=true must turn bd prime off")
	}
}

// With prime.disabled, bd prime adds nothing to the session, exactly as it
// does outside a workspace, even though a workspace with a briefing exists.
func TestPrimeDisabledPrintsNothingInAWorkspace(t *testing.T) {
	const briefing = "custom briefing\n"
	stagePrimeWorkspace(t, briefing)
	run := func(hookJSON bool) string {
		primeHookJSONMode = hookJSON
		return captureStdout(t, func() error { return primeCmd.RunE(primeCmd, nil) })
	}

	stubPrimeConfigBool(t, map[string]bool{})
	if got := run(false); got != briefing {
		t.Fatalf("enabled bd prime printed %q, want the workspace briefing %q", got, briefing)
	}
	if got := run(true); got != hookEnvelope(t, briefing) {
		t.Fatalf("enabled bd prime --hook-json printed %q", got)
	}

	stubPrimeConfigBool(t, map[string]bool{"prime.disabled": true})
	if got := run(false); got != "" {
		t.Fatalf("disabled bd prime printed %q, want nothing", got)
	}
	if got := run(true); got != hookEnvelope(t, "") {
		t.Fatalf("disabled bd prime --hook-json printed %q, want the empty envelope", got)
	}
}
