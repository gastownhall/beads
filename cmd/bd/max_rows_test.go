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

	"github.com/steveyegge/beads/internal/types"
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

	// `bd ready --json` and `bd list --ready --json` route through
	// GetReadyWorkWithCountsInTx, a separate query path from the plain
	// `bd ready` above (GetReadyWorkInTx). Mirrors
	// TestEmbeddedMaxRowsList/Flag_OverCap's --json coverage for `bd list`.
	t.Run("ReadyMaxRowsJSON_FlagOverCap_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrrjf")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, nil, "ready", "--json", "--max-rows", "3")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("stderr missing 'too many rows':\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=3") {
			t.Errorf("stderr missing source --max-rows=3:\n%s", out)
		}
		if idx := strings.Index(out, "Error: too many rows"); idx >= 0 {
			if strings.Contains(out[:idx], "[") {
				t.Errorf("stdout JSON output leaked before error message:\nfull=%s", out)
			}
		}
	})

	t.Run("ListReadyMaxRowsJSON_FlagOverCap_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrrjl")
		seedReadyIssues(t, bd, dir, 6)

		out, code := bdRunRaw(t, bd, dir, nil, "list", "--ready", "--json", "--max-rows", "3")
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

	// be-x42v.4 follow-up (review SHOULD-FIX 8): confirms the cap still
	// fires end-to-end for --include-ephemeral after propagating MaxRows
	// into getReadyWispsInTx's wisp query (internal/storage/issueops/
	// ready_work.go) — that change bounds the query itself but must not
	// change whether the post-merge cap check in GetReadyWorkInTx trips.
	t.Run("ReadyIncludeEphemeralMaxRows_FlagOverCap_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrre")
		for i := 0; i < 6; i++ {
			bdCreate(t, bd, dir, fmt.Sprintf("Ephemeral ready %d", i), "--type", "task", "--ephemeral")
		}

		out, code := bdRunRaw(t, bd, dir, nil, "ready", "--include-ephemeral", "--max-rows", "3")
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

	// be-x42v.4 round-3 follow-up: GetReadyWorkWithCountsInTx (the `--json`
	// path) independently bounds each of the issues and wisps queries by
	// EffectiveSearchLimit(Limit, MaxRows), so with --include-ephemeral the
	// PRE-TRIM merge of two tables can hold up to ~2x that per-table bound
	// even though the caller's own --limit already keeps the DELIVERED page
	// well within the cap. limit=2, cap=3, 2 ready rows in each table: each
	// table's query returns 2 (2<=limit, no overage sniff at the per-table
	// level — EffectiveSearchLimit(2,3)=2), the pre-trim merge is 4>cap=3,
	// but the page actually handed back to the caller is trimmed to 2<=cap.
	// Must exit 0, not trip the cap on the untrimmed merge.
	t.Run("ReadyIncludeEphemeralMaxRowsJSON_MergedUnderLimit_NoError", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrrm")
		for i := 0; i < 2; i++ {
			bdCreate(t, bd, dir, fmt.Sprintf("Plain ready %d", i), "--type", "task")
		}
		for i := 0; i < 2; i++ {
			bdCreate(t, bd, dir, fmt.Sprintf("Ephemeral ready %d", i), "--type", "task", "--ephemeral")
		}

		out, code := bdRunRaw(t, bd, dir, nil, "ready", "--json", "--include-ephemeral",
			"--limit", "2", "--max-rows", "3")
		if code != 0 {
			t.Fatalf("expected exit 0 (delivered page under cap despite pre-trim merge overage), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("delivered-page-under-cap should not emit cap error:\n%s", out)
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

	// be-x42v.4 follow-up (review SHOULD-FIX 7): `bd graph <issue>` without
	// --all never resolved the cap at all before the fix — loadGraphSubgraph
	// is a BFS over per-ID GetDependents/GetDependencies lookups with no
	// IssueFilter to thread MaxRows through, so this checks the final
	// connected-component node count post-hoc, mirroring dep tree.
	t.Run("GraphSingleIssueMaxRows_NodeCount_Exits2", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrgs")
		root := bdCreate(t, bd, dir, "Graph root", "--type", "task")
		for i := 0; i < 5; i++ {
			child := bdCreate(t, bd, dir, fmt.Sprintf("Graph dep %d", i), "--type", "task")
			bdDepAdd(t, bd, dir, root.ID, child.ID)
		}

		// Connected component of size 6 (root + 5), cap of 2 → exit 2.
		out, code := bdRunRaw(t, bd, dir, nil, "graph", root.ID, "--max-rows", "2")
		if code != 2 {
			t.Fatalf("expected exit 2 (subgraph size > cap), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("stderr missing 'too many rows':\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=2") {
			t.Errorf("stderr missing source --max-rows=2:\n%s", out)
		}
	})

	t.Run("GraphSingleIssueMaxRows_UnderCap_NoError", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mrgu")
		root := bdCreate(t, bd, dir, "Graph root under cap", "--type", "task")

		out, code := bdRunRaw(t, bd, dir, nil, "graph", root.ID, "--max-rows", "5")
		if code != 0 {
			t.Fatalf("expected exit 0 (single-node subgraph under cap), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("under-cap single-issue graph should not emit cap error:\n%s", out)
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

// countListIDs returns the number of issue IDs of the form "<prefix>-<n>"
// observed in the combined `bd list` output. Used to verify that --limit
// actually clipped the result set without depending on JSON parsing of the
// pretty-printed array (which spans many lines).
func countListIDs(out, prefix string) int {
	// Each list row prints the issue ID once; counting prefix occurrences in
	// raw stdout is a robust enough check for our small fixtures.
	return strings.Count(out, prefix+"-")
}

// TestEmbeddedMaxRowsList covers the 10 designer §6.1 behavioral scenarios
// for `bd list`. Builder bead: be-x42v.2 (CLI wiring). All subtests share a
// single rig of 21 open task issues — the dataset size from the designer's
// fixture.
func TestEmbeddedMaxRowsList(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mrl")

	const totalRows = 21
	for i := 0; i < totalRows; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("List max-rows %d", i), "--type", "task")
	}

	t.Run("Disabled_NoEnv", func(t *testing.T) {
		// Baseline: with no flag and no env var, `bd list` returns all rows.
		out, code := bdRunRaw(t, bd, dir, nil, "list")
		if code != 0 {
			t.Fatalf("expected exit 0 (no cap), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("baseline `bd list` should not emit cap error:\n%s", out)
		}
		if got := countListIDs(out, "mrl"); got < totalRows {
			t.Errorf("expected at least %d rows in output, got %d", totalRows, got)
		}
	})

	t.Run("Flag_UnderCap", func(t *testing.T) {
		// 21 rows under cap=100: success.
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--max-rows", "100")
		if code != 0 {
			t.Fatalf("expected exit 0 (under cap), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("under-cap `bd list` should not emit cap error:\n%s", out)
		}
	})

	t.Run("Flag_OverCap", func(t *testing.T) {
		// 21 rows over cap=5: exit 2, stderr includes --max-rows=5, stdout
		// empty (designer §2.3: stdout is suppressed so `jq` downstream
		// doesn't trip on partial JSON).
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--json", "--max-rows", "5")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("expected 'too many rows' in stderr:\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=5") {
			t.Errorf("expected source `--max-rows=5` in stderr:\n%s", out)
		}
		// stdout-emptiness check: there should be no JSON `[` array opener
		// in the merged output before the error. Cap-exceeded must not emit
		// a partial array.
		if idx := strings.Index(out, "Error: too many rows"); idx >= 0 {
			before := out[:idx]
			if strings.Contains(before, "[") {
				t.Errorf("stdout JSON output leaked before error message:\nbefore=%q\nfull=%s", before, out)
			}
		}
	})

	t.Run("Env_OverCap", func(t *testing.T) {
		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=5"}, "list")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded via env), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "BEADS_MAX_ROWS=5") {
			t.Errorf("expected source `BEADS_MAX_ROWS=5` in stderr:\n%s", out)
		}
	})

	t.Run("Flag_OverridesEnv", func(t *testing.T) {
		// env says cap=5, flag says cap=100 — flag wins, expect success.
		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=5"},
			"list", "--max-rows", "100")
		if code != 0 {
			t.Fatalf("expected exit 0 (flag overrides env), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("flag-override should not emit cap error:\n%s", out)
		}
	})

	t.Run("Flag_Zero_OverridesEnv", func(t *testing.T) {
		// Explicit `--max-rows 0` disables the cap even when env is set
		// (designer §2.1: ops shells with a global env can run a known-
		// unbounded query without unsetting).
		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=5"},
			"list", "--max-rows", "0")
		if code != 0 {
			t.Fatalf("expected exit 0 (--max-rows 0 disables), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("explicit `--max-rows 0` should disable the cap:\n%s", out)
		}
	})

	t.Run("BadEnv_LogsAndIgnores", func(t *testing.T) {
		// env=banana → warning to stderr, normal output, exit 0
		// (designer §2.1: fail-open on bad env so a typo in a global shell
		// doesn't break automation).
		out, code := bdRunRaw(t, bd, dir, []string{"BEADS_MAX_ROWS=banana"}, "list")
		if code != 0 {
			t.Fatalf("expected exit 0 (bad env ignored), got %d\n%s", code, out)
		}
		// max_rows.go:79 emits a "Warning: BEADS_MAX_ROWS=\"banana\" is not
		// a non-negative integer; ignoring." line.
		if !strings.Contains(out, "Warning") || !strings.Contains(out, "BEADS_MAX_ROWS") {
			t.Errorf("expected warning about bad env in stderr:\n%s", out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("bad env should not trigger cap error:\n%s", out)
		}
	})

	t.Run("Negative_FlagRejected", func(t *testing.T) {
		// max_rows.go:55 calls FatalError on negative — exit 1 with message.
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--max-rows", "-1")
		if code != 1 {
			t.Fatalf("expected exit 1 (usage error), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "must be non-negative") {
			t.Errorf("expected 'must be non-negative' in stderr:\n%s", out)
		}
	})

	t.Run("LimitSet_CapTighter", func(t *testing.T) {
		// limit=100, cap=5: 21 rows are scanned (LIMIT cap+1=6 sniffs
		// overage), cap fires, exit 2. The --limit flag does not protect
		// callers who set a generous limit but also opt into the cap.
		out, code := bdRunRaw(t, bd, dir, nil, "list",
			"--limit", "100", "--max-rows", "5")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap tighter than limit), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "--max-rows=5") {
			t.Errorf("expected source `--max-rows=5` in stderr:\n%s", out)
		}
	})

	t.Run("LimitSet_CapLooser", func(t *testing.T) {
		// limit=5, cap=100: EffectiveSearchLimit returns 5 (limit wins when
		// under cap), no overage detection, 5 rows returned, exit 0.
		out, code := bdRunRaw(t, bd, dir, nil, "list",
			"--limit", "5", "--max-rows", "100")
		if code != 0 {
			t.Fatalf("expected exit 0 (limit under cap), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("limit-under-cap should not emit cap error:\n%s", out)
		}
		if got := countListIDs(out, "mrl"); got != 5 {
			t.Errorf("expected exactly 5 issue IDs in output, got %d:\n%s", got, out)
		}
	})

	// be-x42v.4 follow-up (review MUST-FIX 4, revised round-3): --limit N
	// --max-rows N with more than N matches must truncate to N and exit 0
	// (the standard >N-matches truncation semantics), not error.
	//
	// Round-2 fix (regressed, since replaced): unconditionally skipped the
	// withFetchOneExtra probe-row bump when Limit>=MaxRows. That avoided
	// the false cap error but also stopped fetching the extra row entirely,
	// so len(results) could never exceed effectiveLimit and the GH#3212
	// truncation notice silently stopped firing for this case — a partial
	// result started presenting as complete.
	//
	// Round-3 fix: still fetch the probe row (Limit bumps to N+1 as
	// normal), but when Limit==MaxRows exactly, bump MaxRows to N+1 too, in
	// lockstep, so EnforceMaxRowsCap's comparison excludes the probe row
	// from the cap check while the CLI's own len(results)>effectiveLimit
	// truncation detection still sees it. See withFetchOneExtra's doc
	// comment in list.go for why this can't false-negative a real
	// violation. Covered directly (bump values, not exit behavior) by
	// TestWithFetchOneExtra_LimitEqualsCap_BumpsBothForTruncationProbe
	// below, since printTruncationHint's text is gated on
	// ui.IsStderrTerminal() and unconditionally suppressed for this
	// subprocess harness's piped stderr (see list_embedded_test.go's
	// limit_truncation_hint subtest) — these end-to-end subtests can only
	// assert the absence of the false cap error and the delivered count.
	t.Run("LimitEqualsCap_TruncatesNotErrors", func(t *testing.T) {
		out, code := bdRunRaw(t, bd, dir, nil, "list",
			"--limit", "5", "--max-rows", "5")
		if code != 0 {
			t.Fatalf("expected exit 0 (limit==cap truncates, does not error), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("limit==cap should not emit cap error:\n%s", out)
		}
		if got := countListIDs(out, "mrl"); got != 5 {
			t.Errorf("expected exactly 5 issue IDs in output, got %d:\n%s", got, out)
		}
	})

	t.Run("LimitEqualsCap_JSON_TruncatesNotErrors", func(t *testing.T) {
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--json",
			"--limit", "5", "--max-rows", "5")
		if code != 0 {
			t.Fatalf("expected exit 0 (limit==cap truncates under --json), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("limit==cap under --json should not emit cap error:\n%s", out)
		}
	})

	// be-x42v.4 round-4 follow-up: the round-3 fix to
	// finishReadyWorkWithCounts (trim to Limit before EnforceMaxRowsCap)
	// wasn't mirrored onto the sibling bd list search paths
	// (searchInTx in search.go, finishSearchIssuesWithCounts in
	// search_counts.go), which still capped the pre-trim merged
	// issues+wisps set. `--include-infra` disables the default SkipWisps
	// escape hatch, so the wisps merge actually runs. limit=2, cap=5, 3
	// durable + 3 wisp issues: each leg's query is independently bounded
	// by EffectiveSearchLimit, so the merge can produce up to 6 rows
	// pre-trim (>cap=5) even though the delivered page (trimmed to 2) is
	// well within the cap. Covers both bd list output modes (opus's live
	// repro found both non-JSON and --json tripping the false cap error).
	t.Run("IncludeInfraMergedUnderLimit_NoError", func(t *testing.T) {
		mDir, _, _ := bdInit(t, bd, "--prefix", "mrii")
		for i := 0; i < 3; i++ {
			bdCreate(t, bd, mDir, fmt.Sprintf("Durable infra %d", i), "--type", "task")
		}
		for i := 0; i < 3; i++ {
			bdCreate(t, bd, mDir, fmt.Sprintf("Wisp infra %d", i), "--type", "task", "--ephemeral")
		}

		out, code := bdRunRaw(t, bd, mDir, nil, "list", "--include-infra",
			"--limit", "2", "--max-rows", "5")
		if code != 0 {
			t.Fatalf("expected exit 0 (delivered page under cap despite pre-trim merge overage), got %d\n%s", code, out)
		}
		if strings.Contains(out, "too many rows") {
			t.Errorf("delivered-page-under-cap should not emit cap error:\n%s", out)
		}

		outJSON, codeJSON := bdRunRaw(t, bd, mDir, nil, "list", "--include-infra", "--json",
			"--limit", "2", "--max-rows", "5")
		if codeJSON != 0 {
			t.Fatalf("expected exit 0 under --json (delivered page under cap despite pre-trim merge overage), got %d\n%s", codeJSON, outJSON)
		}
		if strings.Contains(outJSON, "too many rows") {
			t.Errorf("delivered-page-under-cap under --json should not emit cap error:\n%s", outJSON)
		}
	})

	// be-x42v.4 round-5 follow-up: searchInTx's merge branch concatenates
	// two independently ORDER BY'd legs (`append(filtered, wispResults...)`)
	// and round-4's trimToSearchLimit then sliced that unsorted
	// concatenation — with Limit small enough that durable rows alone fill
	// it, a higher-ranked wisp was silently dropped from the page even
	// though the CLI's pre-round-4 behavior (return the full merged set,
	// let the caller sort-then-trim) surfaced it correctly. Default sort is
	// priority ASC: 3 durable issues at P2, one wisp at P0 (highest rank)
	// — with --limit 1 the P0 wisp must be the sole delivered row, not one
	// of the P2 durable rows the unsorted concatenation happened to put
	// first. Must fail against 77227aa67.
	t.Run("IncludeInfraMerged_WispOutranksDurable_InDeliveredPage", func(t *testing.T) {
		mDir, _, _ := bdInit(t, bd, "--prefix", "mrwo")
		for i := 0; i < 3; i++ {
			bdCreate(t, bd, mDir, fmt.Sprintf("Durable p2 %d", i), "--type", "task", "--priority", "2")
		}
		wisp := bdCreate(t, bd, mDir, "Wisp p0 outranks", "--type", "task", "--priority", "0", "--ephemeral")

		out, code := bdRunRaw(t, bd, mDir, nil, "list", "--include-infra", "--limit", "1")
		if code != 0 {
			t.Fatalf("expected exit 0, got %d\n%s", code, out)
		}
		if !strings.Contains(out, wisp.ID) {
			t.Errorf("P0 wisp %s must be the sole row in a --limit 1 page (default sort is priority ASC), got:\n%s", wisp.ID, out)
		}

		outJSON, codeJSON := bdRunRaw(t, bd, mDir, nil, "list", "--include-infra", "--json", "--limit", "1")
		if codeJSON != 0 {
			t.Fatalf("expected exit 0 under --json, got %d\n%s", codeJSON, outJSON)
		}
		if !strings.Contains(outJSON, wisp.ID) {
			t.Errorf("P0 wisp %s must be the sole row in a --limit 1 --json page, got:\n%s", wisp.ID, outJSON)
		}
	})

	// be-x42v.4 follow-up (review MUST-FIX 5): the initial query under
	// `bd list --watch` must surface a cap violation with exit 2, same as
	// non-watch `bd list`. Before the fix, watchIssues logged the error to
	// stderr itself and returned void, so the process always exited 0 —
	// the failure never reached RunE/handleMaxRowsError. This subprocess
	// exercises only the initial-query failure path (returns before
	// entering the polling loop), so it terminates without needing Ctrl+C.
	t.Run("Watch_FlagOverCap_Exits2", func(t *testing.T) {
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--watch", "--max-rows", "5")
		if code != 2 {
			t.Fatalf("expected exit 2 (cap exceeded under --watch), got %d\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") {
			t.Errorf("expected 'too many rows' in stderr:\n%s", out)
		}
		if !strings.Contains(out, "--max-rows=5") {
			t.Errorf("expected source `--max-rows=5` in stderr:\n%s", out)
		}
	})
}

// TestWithFetchOneExtra_LimitEqualsCap_BumpsBothForTruncationProbe verifies
// the exact bump mechanism withFetchOneExtra uses to reconcile the
// >N-matches truncation probe (GH#3212) with a MaxRows cap equal to the
// user's --limit (be-x42v.4 round-3 follow-up).
//
// The end-to-end LimitEqualsCap_TruncatesNotErrors subprocess test in
// TestEmbeddedMaxRowsList can only assert the *absence* of the false cap
// error and the trimmed row count — it can't observe the truncation
// *notice* text, which printTruncationHint gates on ui.IsStderrTerminal()
// and is unconditionally suppressed for a subprocess's piped stderr (see
// list_embedded_test.go's limit_truncation_hint subtest, which documents
// the same TTY constraint). This unit test instead asserts the underlying
// signal directly: with Limit==MaxRows, both must bump by one so the query
// still over-fetches by one row (restoring len(results) > effectiveLimit
// detection) while EnforceMaxRowsCap doesn't trip on that extra row.
func TestWithFetchOneExtra_LimitEqualsCap_BumpsBothForTruncationProbe(t *testing.T) {
	got := withFetchOneExtra(types.IssueFilter{Limit: 5, MaxRows: 5, MaxRowsSource: "--max-rows"})
	if got.Limit != 6 {
		t.Errorf("Limit == MaxRows: Limit = %d, want 6 (bumped so the query still fetches the truncation-detection probe row)", got.Limit)
	}
	if got.MaxRows != 6 {
		t.Errorf("Limit == MaxRows: MaxRows = %d, want 6 (bumped in lockstep so the probe row alone doesn't trip EnforceMaxRowsCap)", got.MaxRows)
	}
	if got.MaxRowsSource != "--max-rows" {
		t.Errorf("MaxRowsSource must be preserved unchanged, got %q", got.MaxRowsSource)
	}
}

// TestWithFetchOneExtra_LimitOverCap_OnlyBumpsLimit covers the tighter-cap
// case (--limit 100 --max-rows 5, LimitSet_CapTighter): MaxRows must NOT
// bump, or a genuine cap violation would report the wrong Cap value (N+1
// instead of the user's true --max-rows=N) in the error message.
func TestWithFetchOneExtra_LimitOverCap_OnlyBumpsLimit(t *testing.T) {
	got := withFetchOneExtra(types.IssueFilter{Limit: 100, MaxRows: 5})
	if got.Limit != 101 {
		t.Errorf("Limit = %d, want 101", got.Limit)
	}
	if got.MaxRows != 5 {
		t.Errorf("MaxRows must stay unbumped so a real cap violation reports the true cap; got %d, want 5", got.MaxRows)
	}
}

// TestWithFetchOneExtra_LimitUnderCap_OnlyBumpsLimit covers the
// looser-cap case (--limit 5 --max-rows 100, LimitSet_CapLooser): the
// probe-row bump alone never crosses EffectiveSearchLimit's `limit >
// maxRows` branch here, so no MaxRows adjustment is needed.
func TestWithFetchOneExtra_LimitUnderCap_OnlyBumpsLimit(t *testing.T) {
	got := withFetchOneExtra(types.IssueFilter{Limit: 5, MaxRows: 100})
	if got.Limit != 6 {
		t.Errorf("Limit = %d, want 6", got.Limit)
	}
	if got.MaxRows != 100 {
		t.Errorf("MaxRows must stay unbumped, got %d, want 100", got.MaxRows)
	}
}

// TestWithFetchOneExtra_NoLimit_Unaffected covers the unlimited case
// (Limit == 0): withFetchOneExtra is a no-op regardless of MaxRows.
func TestWithFetchOneExtra_NoLimit_Unaffected(t *testing.T) {
	got := withFetchOneExtra(types.IssueFilter{Limit: 0, MaxRows: 5})
	if got.Limit != 0 || got.MaxRows != 5 {
		t.Errorf("unlimited Limit must pass through unchanged, got Limit=%d MaxRows=%d", got.Limit, got.MaxRows)
	}
}
