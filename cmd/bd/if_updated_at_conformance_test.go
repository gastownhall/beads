//go:build cgo

package main

// Black-box conformance tests for the `--if-updated-at` generation fence on
// `bd update` / `bd unclaim` (bead agent-forge-y30h.3.30.2).
//
// The tests drive the compiled `bd` binary through argv only — they reference
// no Go symbols of the flag — so this file compiles before the flag exists,
// and every flag-exercising test here is EXPECTED TO FAIL with
// "unknown flag: --if-updated-at" until the implementation lands. The two
// tripwires (absent-flag regression, #5442 label pin) pass today by design:
// they pin standing behavior the implementation must not regress.
//
// One test per case of the conformance spec (y30h-3302-test-spec.md); the
// comment on each test maps it to its spec case and the pinned D-decision
// (D1–D10). Exit taxonomy under test: 0 = guard matched (or benign no-op),
// 13 = stale-guard refusal with ZERO mutation on update (ExitGuardMismatch),
// 1 = usage error / not-found / mixed batch. Per-verb exit taxonomy (bead
// y30h.3.30.2): unclaim rides the SilentExit release contract —
// stamp-mismatch exits 1; update exits 13.
//
// Harness mirrors the existing family-test conventions
// (update_conditional_embedded_test.go, unclaim_conditional_embedded_test.go,
// cross_mode_parity_harness_test.go): embedded Dolt binary built once
// (buildEmbeddedBD), a fresh store per test (bdInit), NO_COLOR=1 for
// deterministic stderr, process exit codes asserted alongside stderr text,
// and FULL-ROW read-back for every zero-mutation claim.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ===== harness helpers =====

// ifupEnv is bdEnv plus NO_COLOR=1 so the refusal stderr asserted below is
// free of ANSI sequencing (spec §2 harness requirements).
func ifupEnv(dir string) []string {
	return append(bdEnv(dir), "NO_COLOR=1")
}

// ifupGate skips unless the embedded-Dolt integration lane is enabled and
// returns the once-built bd binary (family-test convention).
func ifupGate(t *testing.T) string {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	return buildEmbeddedBD(t)
}

// ifupRun executes the bd binary and returns stdout, stderr and the process
// exit code. Flock contention (embedded Dolt is one writer at a time) is
// retried like bdRunWithFlockRetry; every other failure keeps its real exit
// code, because the exit taxonomy IS the contract under test here.
func ifupRun(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = ifupEnv(dir)
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		err := cmd.Run()
		if err == nil {
			return outBuf.String(), errBuf.String(), 0
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("bd %s could not run: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, outBuf.String(), errBuf.String())
		}
		combined := outBuf.String() + errBuf.String()
		if isEmbeddedLockOutput(combined) && attempt < 9 {
			time.Sleep(time.Duration(500*(1<<min(attempt, 4))) * time.Millisecond)
			continue
		}
		return outBuf.String(), errBuf.String(), ee.ExitCode()
	}
}

// ifupMustRun is ifupRun for a step whose failure would make the rest of the
// case meaningless (seeding, mostly).
func ifupMustRun(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := ifupRun(t, bd, dir, args...)
	if code != 0 {
		t.Fatalf("bd %s failed with exit %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// ifupStamp reads the row's updated_at VERBATIM from `bd show <id> --json`
// (spec §2: the rendered string IS the stamp — `bd create --json` reports
// sub-second precision and must not be used). Observed form: RFC3339 UTC,
// second precision, e.g. "2026-09-27T20:35:45Z".
func ifupStamp(t *testing.T, bd, dir, id string) string {
	t.Helper()
	out := bdShowJSON(t, bd, dir, id)
	var rows []struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("parse bd show %s --json: %v\nraw:\n%s", id, err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s --json returned no rows:\n%s", id, out)
	}
	if rows[0].UpdatedAt == "" {
		t.Fatalf("bd show %s --json has an empty updated_at:\n%s", id, out)
	}
	return rows[0].UpdatedAt
}

// ifupAssertUnchanged is the zero-mutation proof: FULL-ROW read-back equality
// (spec §2 — a refusal that mutated ANY column must fail the test).
func ifupAssertUnchanged(t *testing.T, bd, dir, id string, before *types.Issue, phase string) {
	t.Helper()
	after := bdShow(t, bd, dir, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s: refused fence mutated the row\nbefore: %+v\nafter:  %+v", phase, before, after)
	}
}

// ifupSeedOpen seeds one unassigned+open issue and returns
// (id, currentStamp, staleStamp, rowAfterBump). The row is bumped once after
// the stale stamp is read, so `stale` is a valid earlier generation and
// `current` is the row's live generation; `row` is the zero-mutation baseline.
func ifupSeedOpen(t *testing.T, bd, dir, title string) (string, string, string, *types.Issue) {
	t.Helper()
	issue := bdCreate(t, bd, dir, title, "--type", "task")
	stale := ifupStamp(t, bd, dir, issue.ID)
	bdUpdate(t, bd, dir, issue.ID, "--append-notes", "heartbeat")
	current := ifupStamp(t, bd, dir, issue.ID)
	return issue.ID, current, stale, bdShow(t, bd, dir, issue.ID)
}

// ifupSeedClaimed is ifupSeedOpen for a claim held by worker-a
// (status in_progress) — the supervisor-release baseline.
func ifupSeedClaimed(t *testing.T, bd, dir, title string) (string, string, string, *types.Issue) {
	t.Helper()
	issue := bdCreate(t, bd, dir, title, "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker-a", "--status", "in_progress")
	stale := ifupStamp(t, bd, dir, issue.ID)
	bdUpdate(t, bd, dir, issue.ID, "--append-notes", "heartbeat")
	current := ifupStamp(t, bd, dir, issue.ID)
	return issue.ID, current, stale, bdShow(t, bd, dir, issue.ID)
}

// ifupImportBatchRows mints len(titles) rows sharing ONE updated_at
// generation, deterministically, via `bd import` (the importer preserves the
// JSONL updated_at verbatim and upserts; it works in both the embedded and
// the proxied mode). Touch-based convergence is NOT an option here: every
// row write lands its own updated_at second (measured ~1s per row on a
// batch), so no two rows can be RELIABLY brought onto one stamp by writing —
// the flake bd-fence-parity observed under load. The stamp is a fixed
// constant, far from any real write time. runImport runs `bd import` with
// the seed file path (embedded dir or crossModeEnv).
func ifupImportBatchRows(t *testing.T, prefix string, titles []string, claimed bool, runImport func(path string)) ([]string, string) {
	t.Helper()
	const stamp = "2026-09-27T00:00:00Z"
	ids := make([]string, len(titles))
	lines := make([]string, len(titles))
	for i, title := range titles {
		ids[i] = fmt.Sprintf("%s-imp%d", prefix, i+1)
		row := map[string]interface{}{
			"id":         ids[i],
			"title":      title,
			"status":     "open",
			"issue_type": "task",
			"priority":   2,
			"created_at": stamp,
			"updated_at": stamp,
		}
		if claimed {
			row["status"] = "in_progress"
			row["assignee"] = "worker-a"
		}
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal seed row: %v", err)
		}
		lines[i] = string(line)
	}
	path := filepath.Join(t.TempDir(), "batch-seed.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write seed jsonl: %v", err)
	}
	runImport(path)
	return ids, stamp
}

// ifupExtractJSON pulls the JSON record out of combined output that may carry
// non-JSON preamble, descending into the {schema_version, data} envelope when
// the JSON envelope mode is on (reportUpdateFailures emits both shapes).
func ifupExtractJSON(t *testing.T, combined string) map[string]interface{} {
	t.Helper()
	start := strings.Index(combined, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output:\n%s", combined)
	}
	dec := json.NewDecoder(strings.NewReader(combined[start:]))
	var payload map[string]interface{}
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("parse JSON record: %v\nraw:\n%s", err, combined[start:])
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return data
	}
	return payload
}

// ===== §3.1 happy path =====

// TestIfUpdatedAtUpdateMatchAppliesAndAdvancesGeneration — spec §3.1 T1.
// An exact stamp permits the mutation: exit 0, priority applied, and the
// generation ADVANCED (updated_at != stamp). Empty stderr on success (D4
// stamp source, D5 per-row semantics).
func TestIfUpdatedAtUpdateMatchAppliesAndAdvancesGeneration(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz1")

	id, _, stale, _ := ifupSeedOpen(t, bd, dir, "Fence match")
	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", stale)
	if code != 0 {
		t.Fatalf("exact-stamp update exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "rror") {
		t.Errorf("exact-stamp update wrote to stderr:\n%s", stderr)
	}
	row := bdShow(t, bd, dir, id)
	if row.Priority != 1 {
		t.Errorf("matched fence did not apply the update: priority = %d, want 1", row.Priority)
	}
	if got := ifupStamp(t, bd, dir, id); got == stale {
		t.Errorf("matched fence did not advance the generation: updated_at still %s", got)
	}
}

// TestIfUpdatedAtUnclaimMatchSupervisorShape — spec §3.1 T2. The supervisor
// release shape (unclaim another actor's claim via --if-assignee naming the
// holder) composes with the stamp guard: exact stamp → released, unclaim's
// normal postconditions (assignee cleared, status open), generation advanced.
func TestIfUpdatedAtUnclaimMatchSupervisorShape(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz2")

	id, _, stale, _ := ifupSeedClaimed(t, bd, dir, "Fence unclaim match")
	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
		"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", stale)
	if code != 0 {
		t.Fatalf("exact-stamp unclaim exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "rror") {
		t.Errorf("exact-stamp unclaim wrote to stderr:\n%s", stderr)
	}
	row := bdShow(t, bd, dir, id)
	if row.Assignee != "" || row.Status != types.StatusOpen {
		t.Errorf("after fenced release: assignee=%q status=%q, want empty/open", row.Assignee, row.Status)
	}
	if got := ifupStamp(t, bd, dir, id); got == stale {
		t.Errorf("fenced release did not advance the generation: updated_at still %s", got)
	}
}

// TestIfUpdatedAtMatchIsOneShotPerGeneration — spec §3.1 T3. Replaying the
// SAME stamp after a successful fenced write must refuse (13): the mutation
// bumped the generation, so a fence that accepted the old stamp twice would
// not actually be a fence. Nothing is written on the replay.
func TestIfUpdatedAtMatchIsOneShotPerGeneration(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz3")

	id, _, stale, _ := ifupSeedOpen(t, bd, dir, "One-shot fence")
	ifupMustRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", stale)
	after := ifupStamp(t, bd, dir, id)

	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "2", "--if-updated-at", stale)
	if code != ExitGuardMismatch {
		t.Fatalf("replayed stale stamp exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
	}
	if row := bdShow(t, bd, dir, id); row.Priority != 1 {
		t.Errorf("replayed fence mutated the row: priority = %d, want 1", row.Priority)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, stale) || !strings.Contains(combined, after) {
		t.Errorf("replay refusal must name expected stamp %s and current stamp %s, got:\n%s", stale, after, combined)
	}
}

// TestIfUpdatedAtNewStampRefencesAfterWrite — spec §3.1 T23. Fencing is
// per-generation, not per-identity: reading the NEW stamp after a successful
// fenced write re-arms the fence for the next write.
func TestIfUpdatedAtNewStampRefencesAfterWrite(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz4")

	id, _, _, _ := ifupSeedOpen(t, bd, dir, "Re-fence with new stamp")
	ifupMustRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", ifupStamp(t, bd, dir, id))
	fresh := ifupStamp(t, bd, dir, id)

	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "3", "--if-updated-at", fresh)
	if code != 0 {
		t.Fatalf("new-stamp update exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if row := bdShow(t, bd, dir, id); row.Priority != 3 {
		t.Errorf("re-fenced update did not apply: priority = %d, want 3", row.Priority)
	}
}

// ===== §3.2 stale snapshot =====

// TestIfUpdatedAtUpdateStaleRefusesWithZeroMutation — spec §3.2 T4, pinning
// D7. A stale stamp refuses with exit 13, a FULL-ROW read-back identical to
// the pre-command state, and a stderr sentinel naming BOTH the current
// updated_at and the expected (stale) stamp, in the family's per-ID failure
// shape ("1 of 1 issues failed to update").
func TestIfUpdatedAtUpdateStaleRefusesWithZeroMutation(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz5")

	id, current, stale, baseline := ifupSeedOpen(t, bd, dir, "Stale update refusal")
	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", stale)
	if code != ExitGuardMismatch {
		t.Fatalf("stale-stamp update exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, id, baseline, "stale-stamp update refusal")
	combined := stdout + stderr
	if !strings.Contains(combined, "mismatch") {
		t.Errorf("refusal must carry a mismatch sentinel, got:\n%s", combined)
	}
	if !strings.Contains(combined, current) || !strings.Contains(combined, stale) {
		t.Errorf("refusal must name current updated_at %s and expected stamp %s, got:\n%s", current, stale, combined)
	}
	if !strings.Contains(combined, "1 of 1 issues failed to update") {
		t.Errorf("refusal must follow the family failure shape, got:\n%s", combined)
	}
}

// TestIfUpdatedAtUnclaimStaleExits1 — spec §3.2 T5. Per-verb exit taxonomy
// (bead y30h.3.30.2): unclaim rides the SilentExit release contract —
// stamp-mismatch exits 1; update exits 13 — the same class as the legacy
// unclaim --if-assignee mismatch. A stale stamp leaves the claim
// byte-identical.
func TestIfUpdatedAtUnclaimStaleExits1(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz6")

	id, current, stale, baseline := ifupSeedClaimed(t, bd, dir, "Stale unclaim refusal")
	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
		"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", stale)
	// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
	if code != 1 {
		t.Fatalf("stale-stamp unclaim exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, id, baseline, "stale-stamp unclaim refusal")
	combined := stdout + stderr
	if !strings.Contains(combined, "Error unclaiming") {
		t.Errorf("unclaim refusal must keep the family single-line shape, got:\n%s", combined)
	}
	if !strings.Contains(combined, "mismatch") || !strings.Contains(combined, current) || !strings.Contains(combined, stale) {
		t.Errorf("refusal must carry sentinel + current %s + expected %s, got:\n%s", current, stale, combined)
	}
}

// TestIfUpdatedAtSameAssigneeHeartbeatRaceRegression — spec §3.2 T22, the
// regression this feature exists for (bead acceptance: "stale same-assignee
// heartbeat race is covered by a regression test"). The holder's identity is
// UNCHANGED since the supervisor read the row; only its heartbeat bumped the
// generation. The fenced supervisor release must refuse (1) and leave the
// LIVE claim untouched — the unfenced release would clobber it.
func TestIfUpdatedAtSameAssigneeHeartbeatRaceRegression(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz7")

	issue := bdCreate(t, bd, dir, "Heartbeat race", "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker-a", "--status", "in_progress")
	supervisorStamp := ifupStamp(t, bd, dir, issue.ID)
	// The holder heartbeats: same identity, new generation.
	bdUpdate(t, bd, dir, issue.ID, "--append-notes", "alive")
	liveStamp := ifupStamp(t, bd, dir, issue.ID)
	baseline := bdShow(t, bd, dir, issue.ID)

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", issue.ID,
		"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", supervisorStamp)
	// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
	if code != 1 {
		t.Fatalf("heartbeat-race release exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, issue.ID, baseline, "heartbeat-race refusal")
	combined := stdout + stderr
	if !strings.Contains(combined, supervisorStamp) || !strings.Contains(combined, liveStamp) {
		t.Errorf("refusal must name expected %s and current %s, got:\n%s", supervisorStamp, liveStamp, combined)
	}
}

// TestIfUpdatedAtGoneRowUnderFenceIsOtherFailure — spec §3.2 T6, pinning A6:
// a fence against a row that is not there is a not-found failure (exit 1,
// "other failure" class per D6), never a stale-guard refusal, and never
// best-effort success.
func TestIfUpdatedAtGoneRowUnderFenceIsOtherFailure(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz8")

	_, _, stale, _ := ifupSeedOpen(t, bd, dir, "Fence anchor")
	ghost := "yz8-nope99"

	stdout, stderr, code := ifupRun(t, bd, dir, "update", ghost, "--priority", "1", "--if-updated-at", stale)
	if code != 1 {
		t.Fatalf("fenced update of a missing row exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, ghost) {
		t.Errorf("not-found error must name the missing id, got:\n%s", combined)
	}
	if strings.Contains(combined, "mismatch") {
		t.Errorf("a not-found row must not be reported as a stamp mismatch, got:\n%s", combined)
	}

	stdout, stderr, code = ifupRun(t, bd, dir, "unclaim", ghost, "--if-updated-at", stale)
	if code != 1 {
		t.Fatalf("fenced unclaim of a missing row exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "mismatch") {
		t.Errorf("unclaim not-found must not be reported as a stamp mismatch, got:\n%s", stdout+stderr)
	}
}

// ===== §3.3 AND-composition matrix (update) =====

// TestIfUpdatedAtUpdateGuardCompositionMatrix — spec §3.3 T7–T14, pinning D8.
// Guards AND-compose: every supplied guard must hold. Baseline row: assignee
// worker-a, status open. A = --if-assignee, S = --if-status, U =
// --if-updated-at; M = matching value, X = deliberately stale. Every refusing
// cell: exit exactly 13, zero mutation (full-row read-back), and exactly ONE
// guard reason named. Precedence pins: assignee > status is observed family
// behavior (T11/T14); status > stamp and assignee > stamp pin the RECOMMENDED
// D8 order — if the implementer chose a different slot for the stamp guard,
// update these two cells deliberately, do not loosen them.
func TestIfUpdatedAtUpdateGuardCompositionMatrix(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yz9")

	cells := []struct {
		name       string
		a, s, u    string // "M" match, "X" stale
		wantCode   int
		reason     string // substring the refusal must name ("" = stamps, checked separately)
		stampNames bool   // refusal must name both current and stale stamps
		noReason   string // substring that must NOT appear (single-reason pin)
	}{
		{"T7_all_match_applies", "M", "M", "M", 0, "", false, ""},
		{"T8_stale_assignee", "X", "M", "M", ExitGuardMismatch, "ghost", false, ""},
		{"T9_stale_status", "M", "X", "M", ExitGuardMismatch, "in_progress", false, ""},
		{"T10_stale_stamp", "M", "M", "X", ExitGuardMismatch, "", true, ""},
		{"T11_assignee_beats_status", "X", "X", "M", ExitGuardMismatch, "ghost", false, ""},
		{"T12_status_beats_stamp", "M", "X", "X", ExitGuardMismatch, "in_progress", false, ""},
		{"T13_assignee_beats_stamp", "X", "M", "X", ExitGuardMismatch, "ghost", false, ""},
		{"T14_all_stale_single_reason", "X", "X", "X", ExitGuardMismatch, "ghost", false, "in_progress"},
	}

	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			issue := bdCreate(t, bd, dir, "Matrix "+cell.name, "--type", "task")
			bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker-a") // status stays open
			stale := ifupStamp(t, bd, dir, issue.ID)
			bdUpdate(t, bd, dir, issue.ID, "--append-notes", "bump")
			current := ifupStamp(t, bd, dir, issue.ID)
			baseline := bdShow(t, bd, dir, issue.ID)

			val := func(g string, match, staleVal string) string {
				if g == "M" {
					return match
				}
				return staleVal
			}
			args := []string{"update", issue.ID, "--priority", "1",
				"--if-assignee", val(cell.a, "worker-a", "ghost"),
				"--if-status", val(cell.s, "open", "in_progress"),
				"--if-updated-at", val(cell.u, current, stale)}

			stdout, stderr, code := ifupRun(t, bd, dir, args...)
			combined := stdout + stderr
			if code != cell.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, cell.wantCode, stdout, stderr)
			}
			row := bdShow(t, bd, dir, issue.ID)
			if cell.wantCode == 0 {
				if row.Priority != 1 {
					t.Errorf("matched matrix cell did not apply: priority = %d, want 1", row.Priority)
				}
				if got := ifupStamp(t, bd, dir, issue.ID); got == current {
					t.Errorf("matched matrix cell did not advance the generation: updated_at still %s", got)
				}
				return
			}
			ifupAssertUnchanged(t, bd, dir, issue.ID, baseline, cell.name)
			if !strings.Contains(combined, "mismatch") {
				t.Errorf("refusal must carry a mismatch sentinel, got:\n%s", combined)
			}
			if cell.stampNames {
				if !strings.Contains(combined, current) || !strings.Contains(combined, stale) {
					t.Errorf("stamp refusal must name current %s and expected %s, got:\n%s", current, stale, combined)
				}
			}
			if cell.reason != "" && !strings.Contains(combined, cell.reason) {
				t.Errorf("refusal must name the failing guard value %q, got:\n%s", cell.reason, combined)
			}
			if cell.noReason != "" && strings.Contains(combined, cell.noReason) {
				t.Errorf("refusal must name exactly ONE reason; %q must not appear, got:\n%s", cell.noReason, combined)
			}
		})
	}

	// Universal invariant spot-check in JSON mode (spec §3.3: guard_mismatch
	// true on refusing cells; T29 pins the full record shape).
	t.Run("T10_json_guard_mismatch_flag", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Matrix JSON stamp", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker-a")
		stale := ifupStamp(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--append-notes", "bump")

		stdout, stderr, code := ifupRun(t, bd, dir, "update", issue.ID, "--priority", "1",
			"--if-status", "open", "--if-updated-at", stale, "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("json stale cell exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		payload := ifupExtractJSON(t, stdout+stderr)
		failed, ok := payload["failed"].([]interface{})
		if !ok || len(failed) != 1 {
			t.Fatalf("json record must carry one failed entry, got:\n%s", stdout+stderr)
		}
		entry, _ := failed[0].(map[string]interface{})
		if entry["guard_mismatch"] != true {
			t.Errorf("json failed entry must carry guard_mismatch=true, got: %v", entry["guard_mismatch"])
		}
	})
}

// ===== §3.3 unclaim composition subset =====

// TestIfUpdatedAtUnclaimGuardComposition — spec §3.3 T15a–d, pinning A1 and
// A10. On unclaim the stamp guard composes with --if-assignee. Per-verb exit
// taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract
// — stamp-mismatch exits 1; update exits 13 — so both refusal classes share
// unclaim's documented exit 1; zero mutation and the named stamps are what
// distinguish them.
func TestIfUpdatedAtUnclaimGuardComposition(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yza")

	t.Run("T15a_both_match_releases", func(t *testing.T) {
		id, _, _, _ := ifupSeedClaimed(t, bd, dir, "Compose match")
		stamp := ifupStamp(t, bd, dir, id)
		stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
			"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", stamp)
		if code != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		row := bdShow(t, bd, dir, id)
		if row.Assignee != "" || row.Status != types.StatusOpen {
			t.Errorf("after composed release: assignee=%q status=%q, want empty/open", row.Assignee, row.Status)
		}
	})

	t.Run("T15b_stale_assignee_keeps_legacy_exit_1", func(t *testing.T) {
		id, _, _, _ := ifupSeedClaimed(t, bd, dir, "Compose legacy assignee")
		stamp := ifupStamp(t, bd, dir, id)
		baseline := bdShow(t, bd, dir, id)
		stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
			"--actor", "supervisor-x", "--if-assignee", "bob", "--if-updated-at", stamp)
		if code != 1 {
			t.Fatalf("legacy assignee mismatch exit = %d, want 1 (A10: not weakened by the new flag)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "T15b")
		if !strings.Contains(stdout+stderr, "bob") {
			t.Errorf("legacy refusal must name the expected holder, got:\n%s", stdout+stderr)
		}
	})

	t.Run("T15c_stale_stamp_exits_1", func(t *testing.T) {
		id, current, stale, _ := ifupSeedClaimed(t, bd, dir, "Compose stale stamp")
		baseline := bdShow(t, bd, dir, id)
		stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
			"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", stale)
		// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
		if code != 1 {
			t.Fatalf("stale stamp exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "T15c")
		combined := stdout + stderr
		if !strings.Contains(combined, current) || !strings.Contains(combined, stale) {
			t.Errorf("refusal must name current %s and expected %s, got:\n%s", current, stale, combined)
		}
	})

	t.Run("T15d_both_stale_exits_1", func(t *testing.T) {
		id, _, stale, _ := ifupSeedClaimed(t, bd, dir, "Compose both stale")
		baseline := bdShow(t, bd, dir, id)
		stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
			"--actor", "supervisor-x", "--if-assignee", "bob", "--if-updated-at", stale)
		// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
		if code != 1 {
			t.Fatalf("both-stale exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "T15d")
		// D8 precedence: with every guard stale the assignee precheck diagnoses
		// first — the refusal names the guard that fired, not the stamps
		// (stamps are asserted by T15c, where the stamp guard is the only one).
		combined := stdout + stderr
		if !strings.Contains(combined, "assignee mismatch") {
			t.Errorf("both-stale refusal must diagnose the first failed guard (assignee), got:\n%s", combined)
		}
	})
}

// ===== §3.4 batch applier =====

// TestIfUpdatedAtBatchPartialApplicationOnStaleRow — spec §3.4 T16, pinning
// D5. Batch semantics stay per-ID, NOT transactional: with one stale row, the
// two matching rows ARE updated, the stale row is untouched, and the run
// exits 13 (every failure is a stale guard, D6). If the implementation ships
// all-or-nothing for guarded batches, this test fails ON PURPOSE — that is a
// behavior change to the batch applier and must be justified in the PR, not
// smuggled in.
func TestIfUpdatedAtBatchPartialApplicationOnStaleRow(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzb")

	ids, stamp := ifupImportBatchRows(t, "yzb", []string{"Batch P", "Batch Q", "Batch R"}, false,
		func(path string) { ifupMustRun(t, bd, dir, "import", path) })
	p, q, r := ids[0], ids[1], ids[2]
	for _, id := range []string{p, q} {
		if got := ifupStamp(t, bd, dir, id); got != stamp {
			t.Fatalf("imported %s stamp = %s, want the common %s", id, got, stamp)
		}
	}
	bdUpdate(t, bd, dir, r, "--append-notes", "bump") // R goes stale
	rBaseline := bdShow(t, bd, dir, r)

	stdout, stderr, code := ifupRun(t, bd, dir, "update", p, q, r, "--priority", "1", "--if-updated-at", stamp)
	if code != ExitGuardMismatch {
		t.Fatalf("batch with one stale row exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
	}
	for _, id := range []string{p, q} {
		if row := bdShow(t, bd, dir, id); row.Priority != 1 {
			t.Errorf("partial application expected: %s priority = %d, want 1 (D5: per-ID, not all-or-nothing)", id, row.Priority)
		}
	}
	ifupAssertUnchanged(t, bd, dir, r, rBaseline, "T16 stale row")
	combined := stdout + stderr
	if !strings.Contains(combined, "1 of 3 issues failed to update") {
		t.Errorf("batch refusal must report 1 of 3, got:\n%s", combined)
	}
	if !strings.Contains(combined, r) {
		t.Errorf("batch refusal must carry a per-ID bullet for the stale row %s, got:\n%s", r, combined)
	}
}

// TestIfUpdatedAtBatchAllStaleWritesNothing — spec §3.4 T17. All rows stale:
// exit 13, ZERO rows mutated, three per-ID failure bullets.
func TestIfUpdatedAtBatchAllStaleWritesNothing(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzc2")

	ids, stamp := ifupImportBatchRows(t, "yzc2", []string{"AllStale P", "AllStale Q", "AllStale R"}, false,
		func(path string) { ifupMustRun(t, bd, dir, "import", path) })
	for _, id := range ids {
		bdUpdate(t, bd, dir, id, "--append-notes", "bump")
	}
	baselines := make(map[string]*types.Issue, len(ids))
	for _, id := range ids {
		baselines[id] = bdShow(t, bd, dir, id)
	}

	stdout, stderr, code := ifupRun(t, bd, dir, "update", ids[0], ids[1], ids[2], "--priority", "1", "--if-updated-at", stamp)
	if code != ExitGuardMismatch {
		t.Fatalf("all-stale batch exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
	}
	for _, id := range ids {
		ifupAssertUnchanged(t, bd, dir, id, baselines[id], "T17")
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "3 of 3 issues failed to update") {
		t.Errorf("all-stale batch must report 3 of 3, got:\n%s", combined)
	}
	for _, id := range ids {
		if !strings.Contains(combined, id) {
			t.Errorf("all-stale batch must carry a per-ID bullet for %s, got:\n%s", id, combined)
		}
	}
}

// TestIfUpdatedAtBatchMixedFailureClassesExit1 — spec §3.4 T18, pinning D6:
// one stale row + one unknown id is a MIXED run; the non-stale failure makes
// the conservative exit 1 (not 13), nothing is written, and stderr carries
// BOTH per-ID bullets.
func TestIfUpdatedAtBatchMixedFailureClassesExit1(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzd")

	issue := bdCreate(t, bd, dir, "Mixed batch", "--type", "task")
	stamp := ifupStamp(t, bd, dir, issue.ID)
	bdUpdate(t, bd, dir, issue.ID, "--append-notes", "bump")
	baseline := bdShow(t, bd, dir, issue.ID)
	ghost := "yzd-nope99"

	stdout, stderr, code := ifupRun(t, bd, dir, "update", issue.ID, ghost, "--priority", "1", "--if-updated-at", stamp)
	if code != 1 {
		t.Fatalf("mixed batch exit = %d, want 1 (D6)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, issue.ID, baseline, "T18")
	combined := stdout + stderr
	if !strings.Contains(combined, issue.ID) || !strings.Contains(combined, ghost) {
		t.Errorf("mixed batch must carry BOTH per-ID bullets (%s stale, %s not-found), got:\n%s", issue.ID, ghost, combined)
	}
}

// TestIfUpdatedAtUnclaimBatchStaleExits1 — spec §3.4 T19, pinning A7:
// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit
// release contract — stamp-mismatch exits 1; update exits 13 — so a stale
// row in an unclaim batch exits 1 with the batch's per-ID failure bullets.
// Matching rows release, the stale row's claim survives.
func TestIfUpdatedAtUnclaimBatchStaleExits1(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yze")

	ids, stamp := ifupImportBatchRows(t, "yze", []string{"UnclaimBatch P", "UnclaimBatch Q", "UnclaimBatch R"}, true,
		func(path string) { ifupMustRun(t, bd, dir, "import", path) })
	p, q, r := ids[0], ids[1], ids[2]
	bdUpdate(t, bd, dir, r, "--append-notes", "bump")
	rBaseline := bdShow(t, bd, dir, r)

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", p, q, r,
		"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", stamp)
	// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
	if code != 1 {
		t.Fatalf("unclaim batch with one stale row exit = %d, want 1 (A7)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, id := range []string{p, q} {
		row := bdShow(t, bd, dir, id)
		if row.Assignee != "" || row.Status != types.StatusOpen {
			t.Errorf("batch partial release expected: %s assignee=%q status=%q, want empty/open", id, row.Assignee, row.Status)
		}
	}
	ifupAssertUnchanged(t, bd, dir, r, rBaseline, "T19 stale claim")
}

// ===== §3.7 input validation / usage errors =====

// TestIfUpdatedAtEmptyStampIsUsageError — spec §3.7 T24, pinning D3. An empty
// stamp is NOT a generation: both verbs reject it as a usage error (exit 1)
// and write nothing. This must be pinned per-flag, because the family
// asymmetry (--if-assignee "" is a VALID "expected unassigned" guard) must
// not leak into --if-updated-at.
func TestIfUpdatedAtEmptyStampIsUsageError(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzf")

	id, _, _, baseline := ifupSeedOpen(t, bd, dir, "Empty stamp")
	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", "")
	if code != 1 {
		t.Fatalf("empty stamp update exit = %d, want 1 (D3)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, id, baseline, "T24 update")
	combined := stdout + stderr
	if !strings.Contains(combined, "invalid") || !strings.Contains(combined, "--if-updated-at") {
		t.Errorf("empty stamp must be a usage error naming the flag, got:\n%s", combined)
	}

	claimID, _, _, _ := ifupSeedClaimed(t, bd, dir, "Empty stamp unclaim")
	claimBaseline := bdShow(t, bd, dir, claimID)
	stdout, stderr, code = ifupRun(t, bd, dir, "unclaim", claimID, "--if-updated-at", "")
	if code != 1 {
		t.Fatalf("empty stamp unclaim exit = %d, want 1 (D3)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, claimID, claimBaseline, "T24 unclaim")
	if !strings.Contains(stdout+stderr, "--if-updated-at") {
		t.Errorf("empty stamp unclaim must name the flag, got:\n%s", stdout+stderr)
	}
}

// TestIfUpdatedAtUnparseableStampIsUsageError — spec §3.7 T25, pinning D2.
// An unparseable stamp is a usage error (exit 1) in the family's
// validation-message style, validated BEFORE any store access — so it fails
// the same way even when the target row does not exist.
func TestIfUpdatedAtUnparseableStampIsUsageError(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzg")

	for _, bad := range []string{"not-a-timestamp", "2026-13-45T99:99:99Z", "1760000000"} {
		t.Run(bad, func(t *testing.T) {
			id, _, _, baseline := ifupSeedOpen(t, bd, dir, "Unparseable "+bad)
			stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", bad)
			if code != 1 {
				t.Fatalf("unparseable stamp %q exit = %d, want 1 (D2)\nstdout:\n%s\nstderr:\n%s", bad, code, stdout, stderr)
			}
			ifupAssertUnchanged(t, bd, dir, id, baseline, "T25 "+bad)
			combined := stdout + stderr
			if !strings.Contains(combined, "invalid") || !strings.Contains(combined, bad) {
				t.Errorf("refusal must name the invalid value %q in the family style, got:\n%s", bad, combined)
			}
		})
	}

	// Validation precedes store access: the absent row must not turn the
	// usage error into a not-found error.
	stdout, stderr, code := ifupRun(t, bd, dir, "update", "yzg-nope99", "--priority", "1", "--if-updated-at", "not-a-timestamp")
	if code != 1 {
		t.Fatalf("unparseable stamp on absent row exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "invalid") {
		t.Errorf("validation must precede store access; expected the invalid-stamp error, got:\n%s", stdout+stderr)
	}
}

// TestIfUpdatedAtEquivalentInstantSpelling — spec §3.7 T26, pinning D4/A2.
// The spec permits TWO conformant comparison rules: (a) verbatim string
// equality, or (b) canonical re-render (accepts equivalent RFC3339 spellings
// like +00:00). Either must be CHOSEN and documented; this test accepts
// exactly one of the two verdicts (0 under (b), 13 under (a)) and fails on
// anything else. The implementer must state the choice (and its doc line) in
// the PR.
func TestIfUpdatedAtEquivalentInstantSpelling(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzh")

	id, _, _, _ := ifupSeedOpen(t, bd, dir, "Equivalent instant")
	stamp := ifupStamp(t, bd, dir, id)
	if !strings.HasSuffix(stamp, "Z") {
		t.Fatalf("expected the RFC3339 UTC Z spelling, got %q", stamp)
	}
	variant := strings.TrimSuffix(stamp, "Z") + "+00:00"

	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", variant)
	row := bdShow(t, bd, dir, id)
	switch code {
	case 0:
		// D4(b): canonical re-render accepted the equivalent instant.
		if row.Priority != 1 {
			t.Errorf("accepted spelling did not apply the update: priority = %d, want 1", row.Priority)
		}
		t.Logf("implementation chose D4(b) canonical comparison — it MUST be documented in the PR")
	case ExitGuardMismatch:
		// D4(a): verbatim string equality refused the equivalent spelling.
		if row.Priority == 1 {
			t.Errorf("refused spelling still mutated the row: priority = 1")
		}
		t.Logf("implementation chose D4(a) verbatim comparison — it MUST be documented in the PR")
	default:
		t.Fatalf("equivalent-instant spelling exit = %d, want 0 (D4b) or 13 (D4a)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestIfUpdatedAtCombinationRules — spec §3.7 T27, pinning D9/A5. A guard
// without a field update keeps the family's benign no-op ("No updates
// specified", exit 0); the fence cannot combine with --claim (its own CAS) or
// with unclaim --force ("release regardless") — both combinations are usage
// errors that write nothing.
func TestIfUpdatedAtCombinationRules(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzi")

	t.Run("guard_only_is_benign_noop", func(t *testing.T) {
		id, stamp, _, baseline := ifupSeedOpen(t, bd, dir, "Guard only")
		stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--if-updated-at", stamp)
		if code != 0 {
			t.Fatalf("guard-only update exit = %d, want 0 (observed family no-op)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "No updates specified") {
			t.Errorf("guard-only update must keep the observed no-op message, got:\n%s", stdout+stderr)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "guard-only noop")
	})

	t.Run("claim_combo_is_usage_error", func(t *testing.T) {
		id, stamp, _, baseline := ifupSeedOpen(t, bd, dir, "Claim combo")
		stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--claim", "--if-updated-at", stamp)
		if code != 1 {
			t.Fatalf("--claim + fence exit = %d, want 1 (D9)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "--claim") {
			t.Errorf("rejection must name --claim, got:\n%s", stdout+stderr)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "claim combo")
	})

	t.Run("force_combo_is_usage_error", func(t *testing.T) {
		id, _, _, _ := ifupSeedClaimed(t, bd, dir, "Force combo")
		baseline := bdShow(t, bd, dir, id)
		stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id, "--force", "--if-updated-at", ifupStamp(t, bd, dir, id))
		if code != 1 {
			t.Fatalf("--force + fence exit = %d, want 1 (D9)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		combined := stdout + stderr
		if !strings.Contains(combined, "force") || !strings.Contains(combined, "if-updated-at") {
			t.Errorf("rejection must name both flags, got:\n%s", combined)
		}
		ifupAssertUnchanged(t, bd, dir, id, baseline, "force combo")
	})
}

// ===== §3.7 T28 — absent-flag regression =====

// TestIfUpdatedAtAbsentFlagStaysUnconditional — spec §3.7 T28 (Draft B
// acceptance 5). Guards are purely additive: without any --if-* flag both
// verbs keep today's unconditional behavior. This test passes BEFORE the
// implementation too — it is a tripwire the implementation must not regress.
func TestIfUpdatedAtAbsentFlagStaysUnconditional(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzj")

	a := bdCreate(t, bd, dir, "Unconditional update", "--type", "task")
	bdUpdate(t, bd, dir, a.ID, "--priority", "1")
	if row := bdShow(t, bd, dir, a.ID); row.Priority != 1 {
		t.Errorf("plain update regressed: priority = %d, want 1", row.Priority)
	}

	b := bdCreate(t, bd, dir, "Unconditional unclaim", "--type", "task")
	bdUpdate(t, bd, dir, b.ID, "--assignee", "worker-a", "--status", "in_progress")
	ifupMustRun(t, bd, dir, "unclaim", b.ID, "--actor", "worker-a")
	row := bdShow(t, bd, dir, b.ID)
	if row.Assignee != "" || row.Status != types.StatusOpen {
		t.Errorf("plain unclaim regressed: assignee=%q status=%q, want empty/open", row.Assignee, row.Status)
	}
}

// ===== §3.7 T29 — JSON-mode refusal contract =====

// TestIfUpdatedAtJSONRefusalCarriesGuardMismatch — spec §3.7 T29, pinning D7.
// In --json mode a stale-stamp refusal exits 13 and the failure record's
// failed[] entry carries id, the per-ID error string with the sentinel, and
// guard_mismatch: true (the same typed flag the assignee/status guards set),
// alongside schema_version.
func TestIfUpdatedAtJSONRefusalCarriesGuardMismatch(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzk")

	id, current, stale, _ := ifupSeedOpen(t, bd, dir, "JSON refusal")
	stdout, stderr, code := ifupRun(t, bd, dir, "update", id, "--priority", "1", "--if-updated-at", stale, "--json")
	if code != ExitGuardMismatch {
		t.Fatalf("json stale refusal exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
	}

	payload := ifupExtractJSON(t, stdout+stderr)
	errMsg, _ := payload["error"].(string)
	if !strings.Contains(errMsg, "1 of 1 issues failed") {
		t.Errorf("json record error summary missing, got: %q", errMsg)
	}
	if _, ok := payload["schema_version"]; !ok {
		t.Errorf("json record must carry schema_version, got keys of:\n%s", stdout+stderr)
	}
	failed, ok := payload["failed"].([]interface{})
	if !ok || len(failed) != 1 {
		t.Fatalf("json record must carry one failed entry, got:\n%s", stdout+stderr)
	}
	entry, _ := failed[0].(map[string]interface{})
	if entry["id"] != id {
		t.Errorf("failed entry id = %v, want %s", entry["id"], id)
	}
	if entry["guard_mismatch"] != true {
		t.Errorf("failed entry guard_mismatch = %v, want true (D7)", entry["guard_mismatch"])
	}
	entryErr, _ := entry["error"].(string)
	if !strings.Contains(entryErr, "mismatch") || !strings.Contains(entryErr, current) || !strings.Contains(entryErr, stale) {
		t.Errorf("per-ID error must carry sentinel + current %s + expected %s, got: %q", current, stale, entryErr)
	}
}

// ===== §3.6 T21 — the #5442 pin =====

// TestIfUpdatedAtLabelOnlyDoesNotBumpGeneration_5442 — spec §3.6 T21, pinning
// A8 / upstream issue #5442: label-ONLY mutations do not bump updated_at
// (verified on 1.3.1-rc.1 and again on this checkout). Consequence pinned
// here too: a fence read BEFORE the label writes still matches afterwards —
// the generation-blind spot. DO NOT "fix" the behavior in the flag PR; and
// note PR #5793 plans to change it. If upstream fixes #5442 in the same
// release window, this test flips in exactly three places: both `stamp ==
// T0` assertions become `!=`, and the consequence case below expects 13.
// The implementer must re-check the tracker state at PR time.
func TestIfUpdatedAtLabelOnlyDoesNotBumpGeneration_5442(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yzl")

	issue := bdCreate(t, bd, dir, "Label generation pin", "--type", "task")
	t0 := ifupStamp(t, bd, dir, issue.ID)

	ifupMustRun(t, bd, dir, "update", issue.ID, "--add-label", "some-label")
	row := bdShow(t, bd, dir, issue.ID)
	if !hasLabel(row, "some-label") {
		t.Fatalf("label write did not land: labels = %v", row.Labels)
	}
	if got := ifupStamp(t, bd, dir, issue.ID); got != t0 {
		t.Errorf("#5442 pin flipped: label-only mutation CHANGED updated_at (%s -> %s); update this test per its comment", t0, got)
	}

	ifupMustRun(t, bd, dir, "update", issue.ID, "--add-label", "second-label")
	if got := ifupStamp(t, bd, dir, issue.ID); got != t0 {
		t.Errorf("#5442 pin flipped on the second label: updated_at %s -> %s", t0, got)
	}

	// Consequence: the PRE-label stamp still authorizes the write, because
	// the labels never bumped the generation. This asserts the blind spot.
	stdout, stderr, code := ifupRun(t, bd, dir, "update", issue.ID, "--priority", "1", "--if-updated-at", t0)
	if code != 0 {
		t.Fatalf("pre-label stamp fence exit = %d, want 0 while #5442 stands\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// ===== §3.8 T30 — help-surface contract (the consumer-critical probe) =====

// TestIfUpdatedAtHelpSurfaceOnBothVerbs — spec §3.8 T30, pinning D10. The
// AgentForge consumer probes ONLY `bd unclaim --help` for the literal
// substring `--if-updated-at` and then emits the flag on BOTH verbs, so the
// flag must ship registered and NON-HIDDEN on both in the same release (a
// hidden flag does not render in cobra help and would fail the probe). Also
// pins the per-verb docs contract (bead y30h.3.30.2): unclaim rides the
// SilentExit release contract — stamp-mismatch exits 1; update exits 13 — so
// unclaim's "Exit status" paragraph must NOT promise 13.
func TestIfUpdatedAtHelpSurfaceOnBothVerbs(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir := t.TempDir()

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", "--help")
	unclaimHelp := stdout + stderr
	if code != 0 {
		t.Fatalf("bd unclaim --help exit = %d\n%s", code, unclaimHelp)
	}
	if !strings.Contains(unclaimHelp, "--if-updated-at") {
		t.Errorf("bd unclaim --help must advertise --if-updated-at verbatim (the AF probe greps for it; hidden flags fail the probe)")
	}
	// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
	if !strings.Contains(unclaimHelp, "Exit status") || strings.Contains(unclaimHelp, "13") {
		t.Errorf("unclaim help must keep exit 13 out of its Exit status paragraph (stamp-mismatch exits 1, not 13), got:\n%s", unclaimHelp)
	}

	stdout, stderr, code = ifupRun(t, bd, dir, "update", "--help")
	updateHelp := stdout + stderr
	if code != 0 {
		t.Fatalf("bd update --help exit = %d\n%s", code, updateHelp)
	}
	if !strings.Contains(updateHelp, "--if-updated-at") {
		t.Errorf("bd update --help must advertise --if-updated-at verbatim (D10: same release as unclaim — AF emits it there without a separate probe)")
	}
}

// ===== §3.5 T20 — transport parity (classic ≡ proxied-server) =====

// ifupParityOutcome is everything the parity scenario observes per transport.
// Comparing two of these IS the parity assertion (cross-mode harness
// convention): exit codes, row state, and the greppable refusal semantics.
type ifupParityOutcome struct {
	matchCode     int
	matchPriority int
	matchAdvanced bool

	staleCode      int
	staleUnchanged bool
	staleHasCur    bool
	staleHasOld    bool

	claimRaceCode int
	claimIntact   bool

	batchCode    int
	batchPartial bool // matching rows applied (D5)
	batchSafe    bool // stale row untouched

	jsonGuardFlag bool
}

// ifupEnvStamp reads the verbatim updated_at from a crossModeEnv workspace.
func ifupEnvStamp(t *testing.T, env crossModeEnv, id string) string {
	t.Helper()
	out := env.mustRun(t, "show", id, "--json")
	var rows []struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 || rows[0].UpdatedAt == "" {
		t.Fatalf("[%s] parse show --json for %s: %v\nraw:\n%s", env.mode, id, err, out)
	}
	return rows[0].UpdatedAt
}

// ifupParityScenario runs the condensed T1/T4/T22/T16 case bodies + one JSON
// stale refusal UNCHANGED against one transport (spec §3.5 T20: a transport
// that silently drops the fence parameter would make every stale case
// "match", which the absolute assertions below turn into a failure).
func ifupParityScenario(t *testing.T, env crossModeEnv) ifupParityOutcome {
	t.Helper()
	var got ifupParityOutcome

	// T1: exact stamp applies and advances the generation.
	match := env.create(t, "Parity fence match")
	stamp := ifupEnvStamp(t, env, match)
	env.mustRun(t, "update", match, "--priority", "1", "--if-updated-at", stamp)
	got.matchCode = 0
	row := env.show(t, match)
	got.matchPriority = row.Priority
	got.matchAdvanced = ifupEnvStamp(t, env, match) != stamp

	// T4: stale stamp refuses, writes nothing, names both stamps.
	staleRow := env.create(t, "Parity fence stale")
	oldStamp := ifupEnvStamp(t, env, staleRow)
	env.mustRun(t, "update", staleRow, "--append-notes", "heartbeat")
	curStamp := ifupEnvStamp(t, env, staleRow)
	staleBaseline := env.show(t, staleRow)
	_, stderr, code := env.run(t, "update", staleRow, "--priority", "2", "--if-updated-at", oldStamp)
	got.staleCode = code
	got.staleUnchanged = reflect.DeepEqual(staleBaseline, env.show(t, staleRow))
	got.staleHasCur = strings.Contains(stderr, curStamp)
	got.staleHasOld = strings.Contains(stderr, oldStamp)

	// T22: same-assignee heartbeat race — the live claim must survive.
	race := env.create(t, "Parity heartbeat race")
	env.mustRun(t, "update", race, "--assignee", "worker-a", "--status", "in_progress")
	raceOld := ifupEnvStamp(t, env, race)
	env.mustRun(t, "update", race, "--append-notes", "alive")
	raceBaseline := env.show(t, race)
	_, _, code = env.run(t, "unclaim", race,
		"--actor", "supervisor-x", "--if-assignee", "worker-a", "--if-updated-at", raceOld)
	got.claimRaceCode = code
	raceAfter := env.show(t, race)
	got.claimIntact = raceAfter.Assignee == "worker-a" && raceAfter.Status == types.StatusInProgress &&
		reflect.DeepEqual(raceBaseline, raceAfter)

	// T16 (condensed): per-ID batch, one stale row → partial application.
	probe := env.create(t, "Parity batch prefix probe")
	batchPrefix := probe[:strings.Index(probe, "-")]
	batchIDs, batchStamp := ifupImportBatchRows(t, batchPrefix,
		[]string{"Parity batch P", "Parity batch Q", "Parity batch R"}, false,
		func(path string) { env.mustRun(t, "import", path) })
	env.mustRun(t, "update", batchIDs[2], "--append-notes", "bump")
	rBaseline := env.show(t, batchIDs[2])
	_, _, code = env.run(t, "update", batchIDs[0], batchIDs[1], batchIDs[2],
		"--priority", "1", "--if-updated-at", batchStamp)
	got.batchCode = code
	got.batchPartial = env.show(t, batchIDs[0]).Priority == 1
	got.batchSafe = reflect.DeepEqual(rBaseline, env.show(t, batchIDs[2]))

	// T29: JSON-mode refusal carries guard_mismatch.
	_, stderr, code = env.run(t, "update", staleRow, "--priority", "2", "--if-updated-at", oldStamp, "--json")
	if code == ExitGuardMismatch {
		payload := ifupExtractJSON(t, stderr)
		if failed, ok := payload["failed"].([]interface{}); ok && len(failed) == 1 {
			entry, _ := failed[0].(map[string]interface{})
			got.jsonGuardFlag = entry["guard_mismatch"] == true
		}
	}

	return got
}

// TestProxiedServerIfUpdatedAtParity — spec §3.5 T20. The fenced mutation
// must be indistinguishable across transports: the same case bodies run
// against a classic embedded workspace and a proxied-server workspace, and
// every observable (exit codes 0/13/1, zero-mutation outcomes, sentinel
// semantics, JSON guard_mismatch) must match. Gated on
// BEADS_TEST_PROXIED_SERVER=1 like the other proxied lanes; the embedded
// halves of the same cases are covered unconditionally by the tests above.
func TestProxiedServerIfUpdatedAtParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "yzm", "yzn")
	for i := range envs {
		envs[i].env = append(envs[i].env, "NO_COLOR=1")
	}
	outcomes := make(map[string]ifupParityOutcome, len(envs))
	for _, env := range envs {
		outcomes[env.mode] = ifupParityScenario(t, env)
	}

	// Per-mode absolute contract — parity with a shared bug is still a bug.
	for mode, got := range outcomes {
		if got.matchCode != 0 || got.matchPriority != 1 || !got.matchAdvanced {
			t.Errorf("[%s] exact-stamp match must apply and advance the generation, got %+v", mode, got)
		}
		if got.staleCode != ExitGuardMismatch {
			t.Errorf("[%s] stale stamp exit = %d, want %d", mode, got.staleCode, ExitGuardMismatch)
		}
		if !got.staleUnchanged {
			t.Errorf("[%s] stale refusal mutated the row", mode)
		}
		if !got.staleHasCur || !got.staleHasOld {
			t.Errorf("[%s] stale refusal must name current and expected stamps on stderr", mode)
		}
		// per-verb exit taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit release contract — stamp-mismatch exits 1; update exits 13.
		if got.claimRaceCode != 1 || !got.claimIntact {
			t.Errorf("[%s] heartbeat-race release must exit 1 with the claim intact, got code=%d intact=%v",
				mode, got.claimRaceCode, got.claimIntact)
		}
		if got.batchCode != ExitGuardMismatch || !got.batchPartial || !got.batchSafe {
			t.Errorf("[%s] one-stale batch must exit %d with partial application (D5), got %+v", mode, ExitGuardMismatch, got)
		}
		if !got.jsonGuardFlag {
			t.Errorf("[%s] json refusal must carry guard_mismatch=true (D7)", mode)
		}
	}

	// Cross-mode parity, field by field.
	classic, proxied := outcomes["classic"], outcomes["proxied"]
	fields := []struct {
		name          string
		classic, prox interface{}
	}{
		{"match exit", classic.matchCode, proxied.matchCode},
		{"match priority", classic.matchPriority, proxied.matchPriority},
		{"match advanced", classic.matchAdvanced, proxied.matchAdvanced},
		{"stale exit", classic.staleCode, proxied.staleCode},
		{"stale unchanged", classic.staleUnchanged, proxied.staleUnchanged},
		{"stale names current", classic.staleHasCur, proxied.staleHasCur},
		{"stale names expected", classic.staleHasOld, proxied.staleHasOld},
		{"claim race exit", classic.claimRaceCode, proxied.claimRaceCode},
		{"claim intact", classic.claimIntact, proxied.claimIntact},
		{"batch exit", classic.batchCode, proxied.batchCode},
		{"batch partial", classic.batchPartial, proxied.batchPartial},
		{"batch stale safe", classic.batchSafe, proxied.batchSafe},
		{"json guard_mismatch", classic.jsonGuardFlag, proxied.jsonGuardFlag},
	}
	for _, f := range fields {
		if f.classic != f.prox {
			t.Errorf("cross-mode divergence on %s: classic=%v proxied=%v", f.name, f.classic, f.prox)
		}
	}
}
