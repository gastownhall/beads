//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// batchParityOutcome is everything a `bd batch` caller can observe, recorded
// once per backend: exit codes, the summary and per-line report, and the state
// the batch left in the database. Ids never appear — the two workspaces mint
// their own — so rows are named by role and edges are counted.
type batchParityOutcome struct {
	// --dry-run parses and echoes without executing.
	dryCode        int
	dryEchoedLines int
	dryTail        string
	dryTouchedA    bool

	// Empty input is a no-op success.
	emptyCode int
	emptyTail string

	// The committed batch.
	commitCode    int
	commitSummary string
	commitOps     []string
	aStatus       types.Status
	aPriority     int
	bStatus       types.Status
	cDeps         int
	createdFound  bool

	// A failing line rolls the WHOLE batch back.
	rollbackCode      int
	rollbackNamesLine bool
	rollbackAPriority int
	rollbackCReopened bool

	// Grammar errors are refused before any write.
	badGrammarCode      int
	badGrammarNamesLine bool
	badGrammarBStatus   types.Status

	// dep remove closes the loop on the edge the batch added.
	removeCode int
	cDepsAfter int
}

// TestProxiedServerBatchParity is the cross-mode oracle for `bd batch` ported
// to proxied-server mode.
//
// `bd batch` exists because a shell loop of N `bd` invocations is N
// transactions and N Dolt commits; the command's whole value is that it is ONE
// of each, all-or-nothing. So the interesting assertions are not "the ops
// happened" but the transactional ones: a failing line at the END must undo the
// successful lines BEFORE it, and a grammar error must be refused before
// anything is written at all. Both are checked here on both backends, along
// with the dry-run, empty-input and per-line report contracts that scripts
// parse.
//
// TWO KNOWN DIVERGENCES are deliberately outside this oracle, both named at
// their site (see the rollback step below and batch_proxied_server.go's dep
// cases): an out-of-range `priority=` is written by the classic batch and
// refused by the proxied one, and a batched `dep add`/`dep remove` records a
// dependency history event here and not there. Neither changes the rows the
// batch lands, and both are classic-batch inconsistencies with the CLI verbs
// rather than something the port introduced.
func TestProxiedServerBatchParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "bpc", "bpp")
	outcomes := make(map[string]batchParityOutcome, len(envs))

	for _, env := range envs {
		var got batchParityOutcome

		a := env.create(t, "Batch A")
		b := env.create(t, "Batch B")
		c := env.create(t, "Batch C")

		// Comments and blank lines are ignored; the four real lines cover every
		// verb in the grammar except dep remove (exercised at the end).
		script := fmt.Sprintf(""+
			"# a comment line\n"+
			"\n"+
			"update %s status=in_progress priority=1\n"+
			"close %s done via batch\n"+
			"dep add %s %s blocks\n"+
			"create task 2 \"Batch created issue\"\n", a, b, c, a)

		// 1. --dry-run: parse and echo, execute nothing.
		stdout, _, code := env.runStdin(t, script, "batch", "--dry-run")
		got.dryCode = code
		got.dryEchoedLines = strings.Count(stdout, "line ")
		got.dryTail = lastNonEmptyLine(stdout)
		got.dryTouchedA = env.show(t, a).Status != types.StatusOpen

		// 2. Empty input is a no-op success, matching `bd list ... | bd batch`
		// on an empty list.
		stdout, _, code = env.runStdin(t, "", "batch")
		got.emptyCode = code
		got.emptyTail = lastNonEmptyLine(stdout)

		// 3. The real batch.
		stdout, stderr, code := env.runStdin(t, script, "batch")
		got.commitCode = code
		if code != 0 {
			t.Fatalf("[%s] batch failed with exit %d\nstdout:\n%s\nstderr:\n%s", env.mode, code, stdout, stderr)
		}
		got.commitSummary = firstLineWithPrefix(stdout, "batch: ")
		got.commitOps = batchReportOps(stdout)

		rowA := env.show(t, a)
		got.aStatus, got.aPriority = rowA.Status, rowA.Priority
		got.bStatus = env.show(t, b).Status
		got.cDeps = len(env.depList(t, c))
		got.createdFound = env.hasIssueTitled(t, "Batch created issue")

		// 4. A failing line rolls back the successful lines before it. The
		// first line here would otherwise be a visible write (priority 3 on A,
		// which the batch above set to 1) and the second reopens C; both must
		// be gone after the third line fails on a non-existent id.
		//
		// KNOWN PRE-EXISTING DIVERGENCE, deliberately not exercised here: an
		// OUT-OF-RANGE priority (`priority=5`) is written by the classic batch
		// and refused by the proxied one, because the domain update funnel
		// validates the range and issueops' transaction path does not. `bd
		// update --priority 5` rejects it on both paths — only the batch
		// grammar reaches the unvalidated seam — so this is a hole in classic
		// batch rather than something the port introduced. Closing it is its
		// own change; using an in-range value here keeps this case about
		// rollback.
		badID := "zz-nope-999"
		rollback := fmt.Sprintf(""+
			"update %s priority=3\n"+
			"update %s status=in_progress\n"+
			"close %s\n", a, c, badID)
		stdout, stderr, code = env.runStdin(t, rollback, "batch")
		got.rollbackCode = code
		got.rollbackNamesLine = strings.Contains(stdout+stderr, "line 3")
		got.rollbackAPriority = env.show(t, a).Priority
		got.rollbackCReopened = env.show(t, c).Status != types.StatusOpen

		// 5. A grammar error is refused by the shared parser BEFORE either
		// backend is reached, so nothing is written even though line 1 is valid.
		bad := fmt.Sprintf("close %s\nfrobnicate %s\n", b, b)
		stdout, stderr, code = env.runStdin(t, bad, "batch")
		got.badGrammarCode = code
		got.badGrammarNamesLine = strings.Contains(stdout+stderr, "line 2")
		got.badGrammarBStatus = env.show(t, b).Status

		// 6. dep remove takes the edge back out.
		stdout, stderr, code = env.runStdin(t, fmt.Sprintf("dep remove %s %s\n", c, a), "batch")
		got.removeCode = code
		if code != 0 {
			t.Fatalf("[%s] batch dep remove failed with exit %d\nstdout:\n%s\nstderr:\n%s", env.mode, code, stdout, stderr)
		}
		got.cDepsAfter = len(env.depList(t, c))

		outcomes[env.mode] = got

		// Per-mode absolute expectations: parity with a shared bug is still a bug.
		if got.dryCode != 0 || got.dryEchoedLines != 4 {
			t.Errorf("[%s] dry-run: exit=%d echoed=%d, want 0/4", env.mode, got.dryCode, got.dryEchoedLines)
		}
		if got.dryTouchedA {
			t.Errorf("[%s] dry-run executed the batch", env.mode)
		}
		if got.emptyCode != 0 || !strings.Contains(got.emptyTail, "0 operations") {
			t.Errorf("[%s] empty input: exit=%d tail=%q, want 0 / a 0-operations no-op", env.mode, got.emptyCode, got.emptyTail)
		}
		if !strings.Contains(got.commitSummary, "4 operations committed") {
			t.Errorf("[%s] commit summary = %q, want a 4-operations commit", env.mode, got.commitSummary)
		}
		if want := "update,close,dep.add,create"; strings.Join(got.commitOps, ",") != want {
			t.Errorf("[%s] per-line report ops = %v, want %s", env.mode, got.commitOps, want)
		}
		if got.aStatus != types.StatusInProgress || got.aPriority != 1 {
			t.Errorf("[%s] batch update: status=%q priority=%d, want in_progress/1", env.mode, got.aStatus, got.aPriority)
		}
		if got.bStatus != types.StatusClosed {
			t.Errorf("[%s] batch close: status=%q, want closed", env.mode, got.bStatus)
		}
		if got.cDeps != 1 {
			t.Errorf("[%s] batch dep add: %d edges on C, want 1", env.mode, got.cDeps)
		}
		if !got.createdFound {
			t.Errorf("[%s] batch create: the created issue is missing", env.mode)
		}
		if got.rollbackCode == 0 {
			t.Errorf("[%s] a failing batch line must exit non-zero", env.mode)
		}
		if got.rollbackAPriority != 1 {
			t.Errorf("[%s] rollback did not undo the earlier line: priority=%d, want 1", env.mode, got.rollbackAPriority)
		}
		if got.rollbackCReopened {
			t.Errorf("[%s] rollback did not undo the status line on C", env.mode)
		}
		if got.badGrammarCode == 0 || !got.badGrammarNamesLine {
			t.Errorf("[%s] grammar error: exit=%d namesLine=%v, want non-zero and 'line 2'",
				env.mode, got.badGrammarCode, got.badGrammarNamesLine)
		}
		if got.badGrammarBStatus != types.StatusClosed {
			// B was already closed by the committed batch; a re-close from a
			// script that never ran must not have changed anything either way.
			t.Errorf("[%s] grammar error wrote something: B status=%q", env.mode, got.badGrammarBStatus)
		}
		if got.cDepsAfter != 0 {
			t.Errorf("[%s] batch dep remove: %d edges left on C, want 0", env.mode, got.cDepsAfter)
		}
	}

	assertBatchParity(t, outcomes["classic"], outcomes["proxied"])
}

func assertBatchParity(t *testing.T, classic, proxied batchParityOutcome) {
	t.Helper()
	type field struct {
		name             string
		classic, proxied any
	}
	fields := []field{
		{"dry-run exit code", classic.dryCode, proxied.dryCode},
		{"dry-run echoed lines", classic.dryEchoedLines, proxied.dryEchoedLines},
		{"dry-run tail", classic.dryTail, proxied.dryTail},
		{"dry-run executed anything", classic.dryTouchedA, proxied.dryTouchedA},
		{"empty-input exit code", classic.emptyCode, proxied.emptyCode},
		{"empty-input tail", classic.emptyTail, proxied.emptyTail},
		{"commit exit code", classic.commitCode, proxied.commitCode},
		{"commit summary", classic.commitSummary, proxied.commitSummary},
		{"commit per-line ops", strings.Join(classic.commitOps, ","), strings.Join(proxied.commitOps, ",")},
		{"A status", classic.aStatus, proxied.aStatus},
		{"A priority", classic.aPriority, proxied.aPriority},
		{"B status", classic.bStatus, proxied.bStatus},
		{"C dependency count", classic.cDeps, proxied.cDeps},
		{"created issue present", classic.createdFound, proxied.createdFound},
		{"rollback exit code", classic.rollbackCode, proxied.rollbackCode},
		{"rollback names failing line", classic.rollbackNamesLine, proxied.rollbackNamesLine},
		{"A priority after rollback", classic.rollbackAPriority, proxied.rollbackAPriority},
		{"C reopened after rollback", classic.rollbackCReopened, proxied.rollbackCReopened},
		{"grammar-error exit code", classic.badGrammarCode, proxied.badGrammarCode},
		{"grammar-error names line", classic.badGrammarNamesLine, proxied.badGrammarNamesLine},
		{"B status after grammar error", classic.badGrammarBStatus, proxied.badGrammarBStatus},
		{"dep-remove exit code", classic.removeCode, proxied.removeCode},
		{"C dependency count after remove", classic.cDepsAfter, proxied.cDepsAfter},
	}
	for _, f := range fields {
		if f.classic != f.proxied {
			t.Errorf("cross-mode divergence on %s: classic=%v proxied=%v", f.name, f.classic, f.proxied)
		}
	}
}

// depList reads the dependency records of one issue as a flat array, the shape
// `bd dep list --json` documents.
func (e crossModeEnv) depList(t *testing.T, id string) []map[string]any {
	t.Helper()
	out := e.mustRun(t, "dep", "list", id, "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("[%s] no JSON array in dep list output:\n%s", e.mode, out)
	}
	var records []map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &records); err != nil {
		t.Fatalf("[%s] parse dep list JSON: %v\n%s", e.mode, err, out[start:])
	}
	return records
}

// hasIssueTitled reports whether any issue in the workspace carries title.
func (e crossModeEnv) hasIssueTitled(t *testing.T, title string) bool {
	t.Helper()
	out := e.mustRun(t, "list", "--all", "--json")
	start := strings.Index(out, "[")
	if start < 0 {
		return false
	}
	var rows []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &rows); err != nil {
		t.Fatalf("[%s] parse list JSON: %v\n%s", e.mode, err, out[start:])
	}
	for _, row := range rows {
		if row.Title == title {
			return true
		}
	}
	return false
}

// batchReportOps pulls the op names out of the per-line report `bd batch`
// prints after a successful commit ("  line 3: update bd-x").
func batchReportOps(stdout string) []string {
	var ops []string
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "line ") {
			continue
		}
		_, rest, ok := strings.Cut(trimmed, ": ")
		if !ok {
			continue
		}
		op, _, _ := strings.Cut(rest, " ")
		ops = append(ops, op)
	}
	return ops
}

func firstLineWithPrefix(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
