//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runPrimeBinary runs the bd binary built by buildBDUnderTest in the given
// working directory with a clean env (HOME isolated), capturing stdout.
// stderr is captured separately so JSON-validity checks aren't polluted by
// auto-pull or warning lines.
func runPrimeBinary(t *testing.T, binPath, workDir string, args ...string) (stdout []byte, stderr []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	full := append([]string{"prime"}, args...)
	cmd := exec.CommandContext(ctx, binPath, full...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=", // Suppress Linear auto-pull noise
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// bd prime exits 0 on every path we care about (silent-success
		// contract). A non-zero exit is itself a failure.
		t.Fatalf("bd %v in %s: %v\nstdout: %s\nstderr: %s", full, workDir, err, outBuf.String(), errBuf.String())
	}
	return outBuf.Bytes(), errBuf.Bytes()
}

// initBeadsWorkspace creates a minimal beads workspace at workDir using `bd
// init --prefix`. We don't need a Dolt server or any issues — just the
// .beads/ directory so FindBeadsDir succeeds.
func initBeadsWorkspace(t *testing.T, binPath, workDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "init", "--prefix", "test")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init in %s: %v\n%s", workDir, err, out)
	}
}

type primeEnvelope struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func parseEnvelope(t *testing.T, raw []byte) primeEnvelope {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	var env primeEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, raw)
	}
	if env.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", env.HookSpecificOutput.HookEventName)
	}
	return env
}

func writePrimeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write PRIME.md: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove PRIME.md: %v", err)
		}
	})
}

// TestPrimeBinaryPortfolio runs the binary-level Prime scenarios sequentially
// against shared workspaces. The scenarios retain their individual names as
// subtests because each remains an independently observable CLI contract.
func TestPrimeBinaryPortfolio(t *testing.T) {
	binPath := buildBDUnderTest(t)
	localWorkDir := t.TempDir()
	initBeadsWorkspace(t, binPath, localWorkDir)
	redirectedRoot := t.TempDir()
	initBeadsWorkspace(t, binPath, redirectedRoot)
	redirectedBeads := filepath.Join(redirectedRoot, ".beads")
	noWorkspaceWorkDir := t.TempDir()

	t.Run("TestPrime_HookJSON_DefaultPath", func(t *testing.T) {
		stdout, _ := runPrimeBinary(t, binPath, localWorkDir, "--hook-json")
		env := parseEnvelope(t, stdout)

		if env.HookSpecificOutput.AdditionalContext == "" {
			t.Fatal("expected non-empty additionalContext for default path")
		}
		// Sanity-check that the generated content actually flowed through.
		// The CLI/MCP variants both lead with one of these phrases.
		ctx := env.HookSpecificOutput.AdditionalContext
		if !strings.Contains(ctx, "Beads") {
			t.Errorf("additionalContext should contain generated bd prime markdown, got: %q", firstN(ctx, 200))
		}
	})

	t.Run("TestPrime_HookJSON_LocalPrimeOverride", func(t *testing.T) {
		primeScenarioHookJSONLocalPrimeOverride(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_HookJSON_NotJSON_WithoutFlag", func(t *testing.T) {
		primeScenarioHookJSONNotJSONWithoutFlag(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_HookJSON_StealthCompose", func(t *testing.T) {
		primeScenarioHookJSONStealthCompose(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_HookJSON_GlobalPrimeOverride", func(t *testing.T) {
		primeScenarioHookJSONGlobalPrimeOverride(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_HookJSON_RedirectedPrimeOverride", func(t *testing.T) {
		primeScenarioHookJSONRedirectedPrimeOverride(t, binPath, noWorkspaceWorkDir, redirectedBeads)
	})
	t.Run("TestPrime_HookJSON_NoBeadsWorkspace", func(t *testing.T) {
		primeScenarioHookJSONNoBeadsWorkspace(t, binPath, noWorkspaceWorkDir)
	})
	t.Run("TestPrime_NoTelemetryInIsolatedHome", func(t *testing.T) {
		primeScenarioNoTelemetryInIsolatedHome(t, binPath, localWorkDir)
	})

	rememberInWorkspace(t, binPath, localWorkDir, "prime-mem-key", "remember this insight")

	t.Run("TestPrime_CustomPrimeMd_AppendsMemories", func(t *testing.T) {
		primeScenarioCustomPrimeMdAppendsMemories(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_MemoriesOnly_WithCustomPrimeMd", func(t *testing.T) {
		primeScenarioMemoriesOnlyWithCustomPrimeMd(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_NoMemories_DefaultPath", func(t *testing.T) {
		primeScenarioNoMemoriesDefaultPath(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_NoMemories_CustomPrimeMd", func(t *testing.T) {
		primeScenarioNoMemoriesCustomPrimeMd(t, binPath, localWorkDir)
	})
	t.Run("TestPrime_NoMemories_MemoriesOnlyWins", func(t *testing.T) {
		primeScenarioNoMemoriesMemoriesOnlyWins(t, binPath, localWorkDir)
	})
}

// TestPrime_HookJSON_LocalPrimeOverride: with --hook-json and a
// .beads/PRIME.md file present, output is the JSON envelope with that file's
// contents in additionalContext (verbatim).
func primeScenarioHookJSONLocalPrimeOverride(t *testing.T, binPath, workDir string) {
	const custom = "# Custom local PRIME.md override\nBe excellent.\n"
	primePath := filepath.Join(workDir, ".beads", "PRIME.md")
	writePrimeFile(t, primePath, custom)

	stdout, _ := runPrimeBinary(t, binPath, workDir, "--hook-json")
	env := parseEnvelope(t, stdout)

	if env.HookSpecificOutput.AdditionalContext != custom {
		t.Errorf("additionalContext = %q, want %q", env.HookSpecificOutput.AdditionalContext, custom)
	}
}

// TestPrime_HookJSON_NotJSON_WithoutFlag is a regression guard: without
// --hook-json, prime output is raw markdown — NOT a JSON envelope.
// This is the binary-level companion to the in-process unit test in
// prime_test.go and protects the existing Claude/CLI contract.
func primeScenarioHookJSONNotJSONWithoutFlag(t *testing.T, binPath, workDir string) {
	stdout, _ := runPrimeBinary(t, binPath, workDir)
	out := strings.TrimSpace(string(stdout))
	if strings.HasPrefix(out, "{") {
		t.Fatalf("bd prime (no flag) emitted JSON-looking content; raw markdown expected: %q", firstN(out, 200))
	}
	var any map[string]interface{}
	if err := json.Unmarshal([]byte(out), &any); err == nil {
		t.Fatal("bd prime (no flag) output should not be valid JSON")
	}
}

// TestPrime_HookJSON_StealthCompose: --hook-json composed with --stealth
// emits the JSON envelope, and additionalContext is in stealth mode (no raw
// `git push` instructions in the close protocol).
func primeScenarioHookJSONStealthCompose(t *testing.T, binPath, workDir string) {
	stdout, _ := runPrimeBinary(t, binPath, workDir, "--hook-json", "--stealth")
	env := parseEnvelope(t, stdout)

	ctx := env.HookSpecificOutput.AdditionalContext
	if ctx == "" {
		t.Fatal("expected non-empty additionalContext under --stealth --hook-json")
	}
	// Stealth mode: close protocol must not steer agents to git push.
	// (Local-only also suppresses git ops, but stealth is the explicit user
	// signal we care about here.)
	if strings.Contains(ctx, "git push") {
		t.Errorf("stealth mode should not include 'git push' in additionalContext, got snippet: %q", firstN(ctx, 400))
	}
	// And the close-protocol section must still exist.
	if !strings.Contains(ctx, "bd close") {
		t.Errorf("stealth mode should still teach 'bd close', got snippet: %q", firstN(ctx, 400))
	}
}

// TestPrime_HookJSON_GlobalPrimeOverride: with --hook-json and a
// ~/.config/beads/PRIME.md file present (XDG path), output is the JSON
// envelope wrapping that file's contents. This exercises the third
// custom-PRIME.md path through the wrapper.
func primeScenarioHookJSONGlobalPrimeOverride(t *testing.T, binPath, workDir string) {
	const custom = "# Global PRIME override\nGreetings from XDG.\n"

	// resolveGlobalPrimePath uses os.UserConfigDir, which on Linux honors
	// XDG_CONFIG_HOME. On macOS it returns ~/Library/Application Support
	// regardless of XDG, so we set HOME and also stage the macOS path to
	// be cross-platform-safe.
	xdg := t.TempDir()
	home := t.TempDir()

	xdgBeadsDir := filepath.Join(xdg, "beads")
	if err := os.MkdirAll(xdgBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir xdg beads dir: %v", err)
	}
	writePrimeFile(t, filepath.Join(xdgBeadsDir, "PRIME.md"), custom)
	// Cross-platform staging for macOS UserConfigDir.
	macConfigDir := filepath.Join(home, "Library", "Application Support", "beads")
	if err := os.MkdirAll(macConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir mac config dir: %v", err)
	}
	writePrimeFile(t, filepath.Join(macConfigDir, "PRIME.md"), custom)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "prime", "--hook-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdg,
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd prime --hook-json: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}

	env := parseEnvelope(t, outBuf.Bytes())
	if env.HookSpecificOutput.AdditionalContext != custom {
		t.Errorf("additionalContext = %q, want %q", env.HookSpecificOutput.AdditionalContext, custom)
	}
}

// TestPrime_HookJSON_RedirectedPrimeOverride: with --hook-json and a
// PRIME.md staged at <beadsDir>/PRIME.md where <beadsDir> is NOT the local
// .beads directory (i.e. relocated via BEADS_DIR), the output is the JSON
// envelope wrapping that file's contents. This exercises the redirected
// path independently from the local path so DoD #2 ("ALL FOUR output paths
// wrap correctly") is fully covered end-to-end.
func primeScenarioHookJSONRedirectedPrimeOverride(t *testing.T, binPath, workDir, relocatedBeads string) {
	const custom = "# Redirected PRIME override\nFrom a relocated beadsDir.\n"
	writePrimeFile(t, filepath.Join(relocatedBeads, "PRIME.md"), custom)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "prime", "--hook-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR="+relocatedBeads, // forces FindBeadsDir to return this absolute path
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd prime --hook-json: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}

	env := parseEnvelope(t, outBuf.Bytes())
	if env.HookSpecificOutput.AdditionalContext != custom {
		t.Errorf("additionalContext = %q, want %q", env.HookSpecificOutput.AdditionalContext, custom)
	}
}

// TestPrime_HookJSON_NoBeadsWorkspace: when bd prime would otherwise emit
// nothing (no beads workspace resolved), --hook-json still emits the empty
// JSON envelope so Gemini's strict stdout-must-be-JSON contract is honored.
func primeScenarioHookJSONNoBeadsWorkspace(t *testing.T, binPath, workDir string) {
	// workDir is a freshly created tmpdir with NO beads workspace, so
	// FindBeadsDir cannot walk up into the test repo.
	home := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "prime", "--hook-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd prime --hook-json: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}

	env := parseEnvelope(t, outBuf.Bytes())
	if env.HookSpecificOutput.AdditionalContext != "" {
		t.Errorf("additionalContext should be empty when no beads workspace, got: %q",
			firstN(env.HookSpecificOutput.AdditionalContext, 200))
	}
}

// rememberInWorkspace stores a persistent memory via `bd remember` so prime can
// inject it. Memories live in the workspace store, so the same isolated env as
// initBeadsWorkspace/runPrimeBinary resolves the same database.
func rememberInWorkspace(t *testing.T, binPath, workDir, key, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "remember", content, "--key", key)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd remember in %s: %v\n%s", workDir, err, out)
	}
}

// TestPrime_CustomPrimeMd_AppendsMemories: a custom .beads/PRIME.md replaces the
// default workflow text, but persistent memories must still be appended so
// `bd remember` keeps working under a custom template (GH#3941).
func primeScenarioCustomPrimeMdAppendsMemories(t *testing.T, binPath, workDir string) {
	const custom = "# Custom local PRIME.md override\nBe excellent.\n"
	primePath := filepath.Join(workDir, ".beads", "PRIME.md")
	writePrimeFile(t, primePath, custom)

	stdout, _ := runPrimeBinary(t, binPath, workDir)
	out := string(stdout)

	if !strings.Contains(out, "Be excellent.") {
		t.Errorf("custom PRIME.md content missing from output: %q", firstN(out, 300))
	}
	if !strings.Contains(out, "Persistent Memories") {
		t.Errorf("memories section should be appended under custom PRIME.md, got: %q", firstN(out, 600))
	}
	if !strings.Contains(out, "remember this insight") {
		t.Errorf("memory content should appear under custom PRIME.md, got: %q", firstN(out, 600))
	}
}

// TestPrime_MemoriesOnly_WithCustomPrimeMd: --memories-only must return only the
// memories section even when a custom PRIME.md exists; the PRIME.md content must
// NOT leak into the output (GH#3941). This is the primary memory-injection path
// for PreCompact hooks, which a custom PRIME.md previously broke.
func primeScenarioMemoriesOnlyWithCustomPrimeMd(t *testing.T, binPath, workDir string) {
	const custom = "# Custom local PRIME.md override\nBe excellent.\n"
	primePath := filepath.Join(workDir, ".beads", "PRIME.md")
	writePrimeFile(t, primePath, custom)

	stdout, _ := runPrimeBinary(t, binPath, workDir, "--memories-only")
	out := string(stdout)

	if strings.Contains(out, "Be excellent.") {
		t.Errorf("--memories-only must not include custom PRIME.md content, got: %q", firstN(out, 300))
	}
	if !strings.Contains(out, "remember this insight") {
		t.Errorf("--memories-only should include memory content under custom PRIME.md, got: %q", firstN(out, 300))
	}
}

// TestPrime_NoMemories_DefaultPath: --no-memories omits the persistent memories
// section from the default (generated) prime output. A control run without the
// flag confirms the memory is otherwise present, so the assertion has signal.
func primeScenarioNoMemoriesDefaultPath(t *testing.T, binPath, workDir string) {
	// Control: without --no-memories, the memory is injected.
	ctrl, _ := runPrimeBinary(t, binPath, workDir, "--full")
	if !strings.Contains(string(ctrl), "remember this insight") {
		t.Fatalf("control run should include memory, got: %q", firstN(string(ctrl), 400))
	}

	// With --no-memories, the memories section is omitted.
	stdout, _ := runPrimeBinary(t, binPath, workDir, "--full", "--no-memories")
	out := string(stdout)
	if strings.Contains(out, "remember this insight") {
		t.Errorf("--no-memories should omit memory content, got: %q", firstN(out, 400))
	}
	if strings.Contains(out, "Persistent Memories") {
		t.Errorf("--no-memories should omit the Persistent Memories section, got: %q", firstN(out, 400))
	}
	// The generated workflow context should still be present.
	if !strings.Contains(out, "Beads") {
		t.Errorf("--no-memories should still emit workflow context, got: %q", firstN(out, 400))
	}
}

// TestPrime_NoMemories_CustomPrimeMd: --no-memories suppresses the memories that
// GH#3941 appends under a custom PRIME.md; the custom content itself is unaffected.
func primeScenarioNoMemoriesCustomPrimeMd(t *testing.T, binPath, workDir string) {
	const custom = "# Custom local PRIME.md override\nBe excellent.\n"
	writePrimeFile(t, filepath.Join(workDir, ".beads", "PRIME.md"), custom)

	stdout, _ := runPrimeBinary(t, binPath, workDir, "--no-memories")
	out := string(stdout)
	if !strings.Contains(out, "Be excellent.") {
		t.Errorf("custom PRIME.md content should be present, got: %q", firstN(out, 300))
	}
	if strings.Contains(out, "remember this insight") || strings.Contains(out, "Persistent Memories") {
		t.Errorf("--no-memories should omit memories under custom PRIME.md, got: %q", firstN(out, 400))
	}
}

// TestPrime_NoMemories_MemoriesOnlyWins: when both --memories-only and
// --no-memories are set, --memories-only wins and memories are still returned.
func primeScenarioNoMemoriesMemoriesOnlyWins(t *testing.T, binPath, workDir string) {
	stdout, _ := runPrimeBinary(t, binPath, workDir, "--memories-only", "--no-memories")
	out := string(stdout)
	if !strings.Contains(out, "remember this insight") {
		t.Errorf("--memories-only should win over --no-memories and include memories, got: %q", firstN(out, 400))
	}
}

// TestPrime_NoTelemetryInIsolatedHome is the regression guard for wy-12x1p:
// a `bd` subprocess launched by this suite must not write a telemetry queue
// into its isolated HOME. That queue is the visible half of the real problem —
// the other half is the DETACHED `bd send-metrics` child metrics.CloseAndFlush
// spawns alongside it, which outlives its parent and keeps mutating
// $HOME/.beads/eventsData while Go's t.TempDir cleanup is trying to delete the
// tree. The result was an intermittent, assertion-free
// "TempDir RemoveAll cleanup: ... directory not empty" that reddened the whole
// cmd/bd package (the TestPrime_HookJSON_* tests were the observed victims).
//
// The fix is the metrics opt-out set process-wide in testMainInner; this test
// pins it from the outside, because the failure it prevents is load-dependent
// and would otherwise creep back unnoticed. The assertion is deterministic:
// metrics.Init creates the eventsData dir eagerly (and synchronously, in the
// parent) whenever metrics are enabled, so its absence proves the flusher was
// never armed.
func primeScenarioNoTelemetryInIsolatedHome(t *testing.T, binPath, workDir string) {
	home := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "prime", "--hook-json")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd prime --hook-json: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}

	eventsData := filepath.Join(home, ".beads", "eventsData")
	if _, err := os.Stat(eventsData); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(eventsData)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("telemetry queue %s exists after a suite-launched bd run (stat err: %v, entries: %v); "+
			"metrics are enabled for this subprocess, so a detached `bd send-metrics` child is racing t.TempDir cleanup — "+
			"see the metrics opt-out in testMainInner (wy-12x1p)", eventsData, err, names)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
