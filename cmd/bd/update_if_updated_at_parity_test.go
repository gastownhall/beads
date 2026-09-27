//go:build cgo

package main

import (
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Cross-mode parity oracle for the `--if-updated-at` generation fence
// (agent-forge-y30h.3.30.2 conformance lane; spec §3.5 T20).
//
// The fence only protects a supervisor if EVERY transport enforces it. The
// proxied-server hop is the dangerous one: the guard rides the use-case RPC
// layer, and a port that silently drops the new parameter turns the fence
// into a no-op — every release succeeds, exactly the race the flag exists to
// close. So the same five-step case body runs against a classic embedded
// workspace and a proxied one, and the recorded outcomes must match field by
// field. Both halves also assert the contract absolutely, because parity with
// a shared bug is still a bug.
//
// The cases exec the built binary by argv only: nothing here references the
// flag's Go plumbing, so the suite compiles before the flag ships and fails
// (loudly, on "unknown flag") until both transports accept it.

// stampGuardOutcome is everything a fenced mutation lets a caller observe,
// recorded once per transport. Comparing two of these IS the parity
// assertion: exit classes, whether the field mutation landed, whether the
// generation advanced, and whether each refusal left the full row untouched.
type stampGuardOutcome struct {
	matchCode          int
	matchApplied       bool // the verb's own field mutation landed
	matchStampAdvanced bool // updated_at moved off the stamp that authorized it

	staleCode         int
	staleNamesCurrent bool // refusal carries the CURRENT updated_at (bead: "reports current updated_at")
	staleZeroMutation bool // full-row read-back equals the pre-command row

	repeatCode         int
	repeatZeroMutation bool

	emptyCode         int
	emptyZeroMutation bool

	invalidCode         int
	invalidZeroMutation bool
}

// timestampRe matches RFC3339 timestamps embedded in CLI output. bd renders
// updated_at with second precision in UTC (e.g. 2026-09-27T20:35:45Z), but
// the regexp deliberately tolerates fractional seconds and explicit offsets:
// the contract under test is "the refusal reports the CURRENT updated_at",
// not one canonical rendering.
var timestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// outputCarriesStamp parses the timestamps out of a command's output and
// reports whether the wanted one appears — "the refusal names the current
// generation" as a parsed fact, not a wording guess (the exact sentinel text
// is the implementer's choice, spec D7/A3).
func outputCarriesStamp(t *testing.T, out, stamp string) bool {
	t.Helper()
	for _, ts := range timestampRe.FindAllString(out, -1) {
		if ts == stamp {
			return true
		}
	}
	return false
}

// waitPastStampBoundary sleeps across the next whole-second boundary. bd
// truncates updated_at to whole seconds, so two writes inside one second
// mint identical stamps — and a generation-fence test that needs "the stamp
// advanced" (or "the same stamp no longer authorizes") must cross a boundary
// first or the case degenerates into a false match.
func waitPastStampBoundary(t *testing.T) {
	t.Helper()
	until := time.Now().Truncate(time.Second).Add(1100 * time.Millisecond)
	time.Sleep(time.Until(until))
}

// stampOf renders a row's updated_at the way a caller reads it back from
// `bd show --json` — the exact string a supervisor would hand to
// --if-updated-at (spec D4).
func stampOf(t *testing.T, row *types.Issue) string {
	t.Helper()
	return row.UpdatedAt.UTC().Format(time.RFC3339)
}

// rowUnchanged is the zero-mutation check: a FULL-ROW read-back equality
// (spec §2), never just updated_at — a refusal that mutated some other column
// must fail. The pointer indirection compares the pointees.
func rowUnchanged(before, after *types.Issue) bool {
	return reflect.DeepEqual(before, after)
}

// assertStampGuardParity reports every field on which the two transports
// disagree, rather than one opaque struct diff (same shape as
// assertUnclaimGuardParity).
func assertStampGuardParity(t *testing.T, classic, proxied stampGuardOutcome) {
	t.Helper()
	type field struct {
		name             string
		classic, proxied any
	}
	for _, f := range []field{
		{"match exit code", classic.matchCode, proxied.matchCode},
		{"match applied", classic.matchApplied, proxied.matchApplied},
		{"match stamp advanced", classic.matchStampAdvanced, proxied.matchStampAdvanced},
		{"stale exit code", classic.staleCode, proxied.staleCode},
		{"stale names current stamp", classic.staleNamesCurrent, proxied.staleNamesCurrent},
		{"stale zero mutation", classic.staleZeroMutation, proxied.staleZeroMutation},
		{"repeat exit code", classic.repeatCode, proxied.repeatCode},
		{"repeat zero mutation", classic.repeatZeroMutation, proxied.repeatZeroMutation},
		{"empty-guard exit code", classic.emptyCode, proxied.emptyCode},
		{"empty-guard zero mutation", classic.emptyZeroMutation, proxied.emptyZeroMutation},
		{"invalid-stamp exit code", classic.invalidCode, proxied.invalidCode},
		{"invalid-stamp zero mutation", classic.invalidZeroMutation, proxied.invalidZeroMutation},
	} {
		if f.classic != f.proxied {
			t.Errorf("cross-mode divergence on %s: classic=%v proxied=%v", f.name, f.classic, f.proxied)
		}
	}
}

// TestProxiedServerUpdateIfUpdatedAtParity runs the five-step fenced-update
// case against both transports: match applies and advances the generation;
// the same stamp applied a second time refuses (the guard is one-shot per
// generation — T3); a heartbeat-bumped row refuses the pre-bump stamp while
// naming the CURRENT one (T4, the regression this fence exists for); and an
// empty or unparseable stamp is a usage error, never a silent pass (D2/D3).
func TestProxiedServerUpdateIfUpdatedAtParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "uuc", "uup")
	outcomes := make(map[string]stampGuardOutcome, len(envs))

	for _, env := range envs {
		var got stampGuardOutcome

		// An unassigned, open row; the supervisor reads its stamp.
		id := env.create(t, "Stamp-fenced update")
		before := env.show(t, id)
		stamp := stampOf(t, before)

		// 1. Matching stamp: applies, and the write moves the generation.
		waitPastStampBoundary(t)
		_, stderr, code := env.run(t, "update", id, "--priority", "1", "--if-updated-at", stamp)
		got.matchCode = code
		after := env.show(t, id)
		got.matchApplied = after.Priority == 1
		got.matchStampAdvanced = !after.UpdatedAt.Equal(before.UpdatedAt)

		// 2. Repeat application: the identical stamp must NOT authorize a
		// second mutation — a fence that stayed valid after firing would let
		// one stale supervisor release clobber unlimited later work.
		_, _, code = env.run(t, "update", id, "--priority", "2", "--if-updated-at", stamp)
		got.repeatCode = code
		got.repeatZeroMutation = rowUnchanged(after, env.show(t, id))

		// 3. Stale stamp: a heartbeat (identity untouched, generation bumped)
		// lands after the supervisor's read; the fenced update must refuse,
		// name the CURRENT updated_at, and leave the row exactly as found.
		waitPastStampBoundary(t)
		env.mustRun(t, "update", id, "--append-notes", "heartbeat")
		current := env.show(t, id)
		currentStamp := stampOf(t, current)
		stdout, stderr, code := env.run(t, "update", id, "--priority", "2", "--if-updated-at", stamp)
		staleOut := stdout + stderr
		got.staleCode = code
		got.staleNamesCurrent = outputCarriesStamp(t, staleOut, currentStamp)
		got.staleZeroMutation = rowUnchanged(current, env.show(t, id))

		// 4. Explicitly empty stamp: an unset variable that expanded into the
		// flag must be a usage error, not an unconditional write (D3 — note
		// the family asymmetry: --if-assignee "" is a VALID "expected
		// unassigned" guard, so this is pinned per-flag).
		_, _, code = env.run(t, "update", id, "--priority", "2", "--if-updated-at", "")
		got.emptyCode = code
		got.emptyZeroMutation = rowUnchanged(current, env.show(t, id))

		// 5. Unparseable stamp: validation precedes store access (D2).
		_, _, code = env.run(t, "update", id, "--priority", "2", "--if-updated-at", "not-a-timestamp")
		got.invalidCode = code
		got.invalidZeroMutation = rowUnchanged(current, env.show(t, id))

		outcomes[env.mode] = got

		// Per-mode absolute expectations.
		if got.matchCode != 0 {
			t.Errorf("[%s] matching --if-updated-at exit = %d, want 0\nstderr:\n%s", env.mode, got.matchCode, stderr)
		}
		if !got.matchApplied {
			t.Errorf("[%s] matching --if-updated-at did not apply the priority write", env.mode)
		}
		if !got.matchStampAdvanced {
			t.Errorf("[%s] authorized write did not advance updated_at off %s — the fence would never re-arm", env.mode, stamp)
		}
		if got.repeatCode != ExitGuardMismatch {
			t.Errorf("[%s] repeat application with a spent stamp exit = %d, want %d", env.mode, got.repeatCode, ExitGuardMismatch)
		}
		if !got.repeatZeroMutation {
			t.Errorf("[%s] repeat application mutated the row", env.mode)
		}
		if got.staleCode != ExitGuardMismatch {
			t.Errorf("[%s] stale --if-updated-at exit = %d, want %d", env.mode, got.staleCode, ExitGuardMismatch)
		}
		if !got.staleNamesCurrent {
			t.Errorf("[%s] stale refusal must report the CURRENT updated_at %s (parsed from the output), got:\n%s",
				env.mode, currentStamp, staleOut)
		}
		if !got.staleZeroMutation {
			t.Errorf("[%s] stale --if-updated-at mutated the row", env.mode)
		}
		if got.emptyCode != 1 {
			t.Errorf("[%s] --if-updated-at '' exit = %d, want 1 (usage error, not %d)", env.mode, got.emptyCode, ExitGuardMismatch)
		}
		if !got.emptyZeroMutation {
			t.Errorf("[%s] --if-updated-at '' mutated the row", env.mode)
		}
		if got.invalidCode != 1 {
			t.Errorf("[%s] --if-updated-at not-a-timestamp exit = %d, want 1 (usage error)", env.mode, got.invalidCode)
		}
		if !got.invalidZeroMutation {
			t.Errorf("[%s] --if-updated-at not-a-timestamp mutated the row", env.mode)
		}
	}

	assertStampGuardParity(t, outcomes["classic"], outcomes["proxied"])
}

// TestProxiedServerUnclaimIfUpdatedAtParity runs the fenced-release case
// against both transports. This is the AF unclaim-redispatch shape
// (T2/T5/T22): a supervisor releases a worker's claim fenced on the stamp it
// read. Stale exits 13 here by design (D1) even though the LEGACY unclaim
// --if-assignee mismatch keeps its documented exit 1 — the new guard adopts
// update's taxonomy, and a proxied exit that disagrees with the embedded one
// for the same refusal is exactly the divergence this lane prevents.
func TestProxiedServerUnclaimIfUpdatedAtParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "uua", "uub")
	outcomes := make(map[string]stampGuardOutcome, len(envs))

	fencedRelease := func(env crossModeEnv, id, holder, stamp string) (string, string, int) {
		return env.run(t, "unclaim", id, "--actor", "supervisor-x",
			"--if-assignee", holder, "--if-updated-at", stamp)
	}

	for _, env := range envs {
		var got stampGuardOutcome

		// A live claim held by worker-a; the supervisor reads its stamp.
		held := env.create(t, "Stamp-fenced release")
		env.mustRun(t, "update", held, "--assignee", "worker-a", "--status", "in_progress")
		before := env.show(t, held)
		stamp := stampOf(t, before)

		// 1. Matching stamp: the supervisor release fires — unclaim's normal
		// postconditions, untouched by the flag.
		waitPastStampBoundary(t)
		_, stderr, code := fencedRelease(env, held, "worker-a", stamp)
		got.matchCode = code
		after := env.show(t, held)
		got.matchApplied = after.Assignee == "" && after.Status == types.StatusOpen
		got.matchStampAdvanced = !after.UpdatedAt.Equal(before.UpdatedAt)

		// 2. Repeat application: the release already fired; both guards are
		// now stale (holder gone, generation advanced). Spec A1 pins 13: the
		// stamp refusal dominates the legacy unclaim assignee-exit-1 wording
		// because every failure in the run is a stale guard.
		_, _, code = fencedRelease(env, held, "worker-a", stamp)
		got.repeatCode = code
		got.repeatZeroMutation = rowUnchanged(after, env.show(t, held))

		// 3. Same-assignee heartbeat race (T22 — the residual-window
		// regression this feature exists for): worker-b holds the claim,
		// the supervisor reads the stamp, THEN the holder heartbeats.
		// Identity is unchanged, the generation is not; the fenced release
		// must refuse with 13, report the CURRENT updated_at, and leave the
		// live claim byte-for-byte intact.
		live := env.create(t, "Heartbeated release")
		env.mustRun(t, "update", live, "--assignee", "worker-b", "--status", "in_progress")
		waitPastStampBoundary(t)
		readStamp := stampOf(t, env.show(t, live))
		env.mustRun(t, "update", live, "--append-notes", "heartbeat")
		current := env.show(t, live)
		currentStamp := stampOf(t, current)
		stdout, stderr, code := fencedRelease(env, live, "worker-b", readStamp)
		staleOut := stdout + stderr
		got.staleCode = code
		got.staleNamesCurrent = outputCarriesStamp(t, staleOut, currentStamp)
		got.staleZeroMutation = rowUnchanged(current, env.show(t, live))

		// 4. Empty stamp on unclaim: usage error, claim intact (D3).
		_, _, code = env.run(t, "unclaim", live, "--if-updated-at", "")
		got.emptyCode = code
		got.emptyZeroMutation = rowUnchanged(current, env.show(t, live))

		// 5. Unparseable stamp on unclaim: usage error, claim intact (D2).
		_, _, code = env.run(t, "unclaim", live, "--if-updated-at", "not-a-timestamp")
		got.invalidCode = code
		got.invalidZeroMutation = rowUnchanged(current, env.show(t, live))

		outcomes[env.mode] = got

		// Per-mode absolute expectations.
		if got.matchCode != 0 {
			t.Errorf("[%s] matching fenced release exit = %d, want 0\nstderr:\n%s", env.mode, got.matchCode, stderr)
		}
		if !got.matchApplied {
			t.Errorf("[%s] matching fenced release did not clear the claim: assignee=%q status=%q",
				env.mode, after.Assignee, after.Status)
		}
		if !got.matchStampAdvanced {
			t.Errorf("[%s] authorized release did not advance updated_at off %s", env.mode, stamp)
		}
		if got.repeatCode != ExitGuardMismatch {
			t.Errorf("[%s] repeat fenced release exit = %d, want %d (D1/A1: the stamp guard exits 13 on unclaim too)",
				env.mode, got.repeatCode, ExitGuardMismatch)
		}
		if !got.repeatZeroMutation {
			t.Errorf("[%s] repeat fenced release mutated the row", env.mode)
		}
		if got.staleCode != ExitGuardMismatch {
			t.Errorf("[%s] stale fenced release exit = %d, want %d (D1: the new stamp guard adopts ExitGuardMismatch on BOTH verbs)",
				env.mode, got.staleCode, ExitGuardMismatch)
		}
		if !got.staleNamesCurrent {
			t.Errorf("[%s] stale refusal must report the CURRENT updated_at %s (parsed from the output), got:\n%s",
				env.mode, currentStamp, staleOut)
		}
		if !got.staleZeroMutation {
			t.Errorf("[%s] stale fenced release disturbed the live claim", env.mode)
		}
		if got.emptyCode != 1 {
			t.Errorf("[%s] unclaim --if-updated-at '' exit = %d, want 1 (usage error)", env.mode, got.emptyCode)
		}
		if !got.emptyZeroMutation {
			t.Errorf("[%s] unclaim --if-updated-at '' mutated the row", env.mode)
		}
		if got.invalidCode != 1 {
			t.Errorf("[%s] unclaim --if-updated-at not-a-timestamp exit = %d, want 1 (usage error)", env.mode, got.invalidCode)
		}
		if !got.invalidZeroMutation {
			t.Errorf("[%s] unclaim --if-updated-at not-a-timestamp mutated the row", env.mode)
		}
	}

	assertStampGuardParity(t, outcomes["classic"], outcomes["proxied"])
}
