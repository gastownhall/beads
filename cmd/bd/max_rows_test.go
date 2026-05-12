//go:build cgo

// be-u8z9: CLI-layer behavioral tests for BEADS_MAX_ROWS / --max-rows on the
// command paths beyond `bd list`. Each subtest:
//   - inits a fresh rig (own DB so the row counts are exact)
//   - exercises the command via exec.Command(bd, ...)
//   - asserts the process exited with code 2 (cap exceeded) and that stderr
//     names the source ("--max-rows=N" or "BEADS_MAX_ROWS=N").
//
// The doctor-family commands (lint, doctor-conventions, doctor-pollution)
// are env-only by design (designer §4); they do NOT register --max-rows
// as a flag. A separate subtest asserts cobra rejects the flag.
//
// Gating matches the other embedded-dolt CLI tests: requires
// BEADS_TEST_EMBEDDED_DOLT=1 to opt in.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdRunRaw runs bd with the given args and env extras (appended to bdEnv(dir)).
// Returns combined stdout+stderr and the process exit code. Unlike the other
// helpers in this package it does not call t.Fatal on non-zero exits — that's
// the success case for the max-rows tests.
func bdRunRaw(t *testing.T, bd, dir string, envExtras []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	env := bdEnv(dir)
	env = append(env, envExtras...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("bd %s unexpected non-exit error: %v\n%s", strings.Join(args, " "), err, out)
	return string(out), -1
}

// seedReadyIssues creates n top-level (no-dep, no-blocker) issues that all
// appear in `bd ready`. Useful for tests that need exactly n ready rows.
func seedReadyIssues(t *testing.T, bd, dir string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		iss := bdCreate(t, bd, dir, fmt.Sprintf("max-rows seed %d", i), "--type", "task")
		ids[i] = iss.ID
	}
	return ids
}

// TestEmbeddedMaxRowsNonListPaths covers the non-list CLI paths wired up in
// be-x42v.2: ready, dep tree, find-duplicates, graph --all, plus the env-only
// doctor family (lint, doctor --check=conventions, doctor --check=pollution),
// and config show emission of BEADS_MAX_ROWS.
func TestEmbeddedMaxRowsNonListPaths(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ----------- bd ready -----------

	t.Run("ReadyMaxRows_FlagOverCap_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrrdf")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, nil, "ready", "--max-rows", "3")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("stderr missing 'too many rows':\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=3") {
			t.Errorf("stderr missing source --max-rows=3:\n%s", out)
		}
	})

	t.Run("ReadyMaxRows_EnvOverCap_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrrde")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=3"}, "ready")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "BEADS_MAX_ROWS=3") {
			t.Errorf("stderr missing source BEADS_MAX_ROWS=3:\n%s", out)
		}
	})

	// ----------- bd dep tree -----------

	t.Run("DepTreeMaxRows_TreeNodes_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrdt")
		// All tasks: default "blocks" dep type rejects epic→task, but task→task
		// is fine and produces a tree with the same shape for cap purposes.
		root := bdCreate(t, bd, dir, "Tree root", "--type", "task")
		// Add 5 children so the dep tree has 6 nodes total (root + 5).
		for i := 0; i < 5; i++ {
			child := bdCreate(t, bd, dir, fmt.Sprintf("Tree dep %d", i), "--type", "task")
			// `bd dep add A B` makes A depend on B (default dep type is "blocks").
			bdDepAdd(t, bd, dir, root.ID, child.ID)
		}

		// Sanity: tree of size 6, cap of 2 → must exit 2 with source attribution.
		out, code := bdRunRaw(t, bd, dir, nil, "dep", "tree", root.ID, "--max-rows", "2")
		if code != 2 {
			t.Fatalf("expected exit 2 (tree size > cap), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("stderr missing 'too many rows':\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=2") {
			t.Errorf("stderr missing source --max-rows=2:\n%s", out)
		}
	})

	// ----------- bd find-duplicates -----------

	t.Run("FindDuplicatesMaxRows_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrfd")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, nil, "find-duplicates", "--max-rows", "3")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "--max-rows=3") {
			t.Errorf("stderr missing source --max-rows=3:\n%s", out)
		}
	})

	// ----------- bd graph --all -----------

	t.Run("GraphAllMaxRows_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrgr")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, nil, "graph", "--all", "--max-rows", "3")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "--max-rows=3") {
			t.Errorf("stderr missing source --max-rows=3:\n%s", out)
		}
	})

	// ----------- bd lint (env-only) -----------

	t.Run("LintMaxRows_EnvOnly_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrln")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=3"}, "lint")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded via env), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "BEADS_MAX_ROWS=3") {
			t.Errorf("stderr missing source BEADS_MAX_ROWS=3:\n%s", out)
		}
	})

	t.Run("LintMaxRows_NoFlagAccepted", func(t *testing.T) {
		// Designer §4: doctor family is env-opt-in only. The --max-rows
		// flag must NOT be registered on `bd lint`; cobra should reject
		// it as unknown.
		dir, _, _ := bdInit(t, bd, "--prefix", "mrlnf")

		out, code := bdRunRaw(t, bd, dir, nil, "lint", "--max-rows", "1")
		if code == 0 {
			t.Fatalf("expected non-zero exit (cobra rejects unknown flag), got 0\n%s", out)
		}
		if !strings.Contains(out, "unknown flag") {
			t.Errorf("stderr missing 'unknown flag' rejection:\n%s", out)
		}
		// Defense against false positives: if `--max-rows` is wired on lint
		// by mistake, this assertion would not trip — but the unknown-flag
		// check above already covers it.
	})

	// ----------- bd doctor --check=conventions (env-only) -----------
	//
	// bd doctor is hard-gated to server mode (doctor.go:188 prints "not yet
	// supported in embedded mode" and exits 0 before reaching the check
	// dispatch). The embedded-Dolt test rig used here cannot exercise those
	// code paths. The cap logic itself runs through the shared SearchIssues +
	// EnforceMaxRowsCap path already covered by:
	//   - TestEnforceMaxRowsCap_* (internal/storage/issueops/search_test.go)
	//   - TestGetReadyWork_MaxRowsSuite (cmd/bd/ready_max_rows_test.go)
	//   - be-x42v.3 storage parity tests (bd list/search backend matrix)
	// What's _not_ covered without server mode is the env-only resolver
	// (resolveMaxRowsEnvOnly) on these specific commands. doctor_conventions.go
	// and doctor_pollution.go both call it and pipe through handleMaxRowsError
	// identically to lint.go, which IS covered above by LintMaxRows_EnvOnly_Exits2.
	// A server-mode follow-up should add behavioral coverage here.

	t.Run("ConventionsMaxRows_EnvOnly_Exits2", func(t *testing.T) {
		t.Skip("bd doctor requires server mode (doctor.go:188); server-mode parity covered by separate validator bead")
	})

	t.Run("PollutionMaxRows_EnvOnly_Exits2", func(t *testing.T) {
		t.Skip("bd doctor requires server mode (doctor.go:188); server-mode parity covered by separate validator bead")
	})

	// ----------- bd config show -----------

	t.Run("ConfigShow_ListsBeadsMaxRows", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrcs")

		// When BEADS_MAX_ROWS is set in the environment, `bd config show`
		// must surface it in the standalone-env entries (config_show.go
		// collectStandaloneEnvEntries).
		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=42"}, "config", "show")
		if code != 0 {
			t.Fatalf("expected exit 0 from `bd config show`, got %d\n%s", code, out)
		}
		if !strings.Contains(out, "BEADS_MAX_ROWS") {
			t.Errorf("expected `bd config show` to list BEADS_MAX_ROWS in output:\n%s", out)
		}
		if !strings.Contains(out, "42") {
			t.Errorf("expected `bd config show` to display the value 42:\n%s", out)
		}

		// When the env var is unset, the entry must be absent (designer §4:
		// opt-in only; default-disabled).
		out2, code2 := bdRunRaw(t, bd, dir, nil, "config", "show")
		if code2 != 0 {
			t.Fatalf("expected exit 0 from `bd config show` (unset), got %d\n%s", code2, out2)
		}
		if strings.Contains(out2, "BEADS_MAX_ROWS") {
			t.Errorf("expected BEADS_MAX_ROWS to be absent when env is unset:\n%s", out2)
		}
	})
}
