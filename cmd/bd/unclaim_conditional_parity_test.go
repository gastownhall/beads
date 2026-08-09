//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// unclaimGuardOutcome is everything a caller of `bd unclaim --if-assignee` can
// observe, recorded once per backend. Comparing two of these IS the parity
// assertion: exit codes, the row state each step left behind, and whether the
// refusal named the actual holder (the machine-greppable half of the contract).
type unclaimGuardOutcome struct {
	staleCode        int
	staleNamesHolder bool
	staleAssignee    string
	staleStatus      types.Status

	matchCode     int
	matchAssignee string
	matchStatus   types.Status

	repeatCode int

	emptyGuardCode     int
	emptyGuardRejected bool
	emptyGuardAssignee string
	emptyGuardStatus   types.Status

	forceComboCode     int
	forceComboRejected bool
	forceComboAssignee string
	forceComboStatus   types.Status
}

// TestProxiedServerUnclaimIfAssigneeParity is the cross-mode oracle for the
// conditional release ported to proxied-server mode: the same six steps run
// against a classic embedded workspace and a proxied one, and every observable
// must match.
//
// The compare-and-swap is the point. A supervisor returning a specific worker's
// bead must never clobber a claim that has since moved on, so the refusal has
// to leave the row EXACTLY as it found it — asserted here on both backends, in
// the same test, rather than inferred from two suites that could drift apart.
//
// EXIT CODE, deliberately pinned: a stale guard exits 1 on both paths. `bd
// unclaim` has never had `bd update`'s ExitGuardMismatch (13) verdict — its
// help says so outright ("1 when any release failed (including an
// --if-assignee mismatch)") — and this port does not invent one for the proxied
// path, because a proxied exit code that differs from the embedded one for the
// same refusal is precisely the divergence this lane exists to prevent. If
// unclaim ever adopts 13 it must adopt it in BOTH modes, and this assertion
// changes once, deliberately.
func TestProxiedServerUnclaimIfAssigneeParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "ucc", "ucp")
	outcomes := make(map[string]unclaimGuardOutcome, len(envs))

	for _, env := range envs {
		var got unclaimGuardOutcome

		// A live claim held by alice.
		held := env.create(t, "Conditional release")
		env.mustRun(t, "update", held, "--assignee", "alice", "--status", "in_progress")

		// 1. Stale expectation: refuses, names the holder, writes nothing.
		stdout, stderr, code := env.run(t, "unclaim", held, "--if-assignee", "bob")
		got.staleCode = code
		got.staleNamesHolder = strings.Contains(stdout+stderr, "alice")
		row := env.show(t, held)
		got.staleAssignee, got.staleStatus = row.Assignee, row.Status

		// 2. Matching expectation: releases.
		_, _, code = env.run(t, "unclaim", held, "--if-assignee", "alice")
		got.matchCode = code
		row = env.show(t, held)
		got.matchAssignee, got.matchStatus = row.Assignee, row.Status

		// 3. Releasing an already-released issue is a distinct failure, not a
		// silent success — the exactly-once property a release-if-current
		// supervisor depends on.
		_, _, got.repeatCode = env.run(t, "unclaim", held, "--if-assignee", "alice")

		// 4. An explicitly empty --if-assignee (an unset variable that expanded
		// into the flag) must be REJECTED, not silently downgraded to an
		// unconditional release. Run as the holder, so the pre-fix behavior
		// would actually have released the claim.
		second := env.create(t, "Empty expectation")
		env.mustRun(t, "update", second, "--assignee", "alice", "--status", "in_progress")
		stdout, stderr, code = env.run(t, "unclaim", second, "--if-assignee", "", "--actor", "alice")
		got.emptyGuardCode = code
		got.emptyGuardRejected = strings.Contains(stdout+stderr, "if-assignee requires a non-empty assignee")
		row = env.show(t, second)
		got.emptyGuardAssignee, got.emptyGuardStatus = row.Assignee, row.Status

		// 5. --force and --if-assignee encode contradictory intent and are
		// mutually exclusive; the combination writes nothing.
		stdout, stderr, code = env.run(t, "unclaim", second, "--force", "--if-assignee", "alice")
		got.forceComboCode = code
		combined := stdout + stderr
		got.forceComboRejected = strings.Contains(combined, "force") && strings.Contains(combined, "if-assignee")
		row = env.show(t, second)
		got.forceComboAssignee, got.forceComboStatus = row.Assignee, row.Status

		outcomes[env.mode] = got

		// Per-mode absolute expectations. Parity with a shared bug is still a
		// bug, so each mode is also checked against the contract itself.
		if got.staleCode == 0 {
			t.Errorf("[%s] stale --if-assignee must fail, got exit 0", env.mode)
		}
		if got.staleCode != 1 {
			t.Errorf("[%s] stale --if-assignee exit = %d, want 1 (bd unclaim does not use ExitGuardMismatch=%d)",
				env.mode, got.staleCode, ExitGuardMismatch)
		}
		if !got.staleNamesHolder {
			t.Errorf("[%s] stale --if-assignee error must name the current holder alice", env.mode)
		}
		if got.staleAssignee != "alice" || got.staleStatus != types.StatusInProgress {
			t.Errorf("[%s] stale --if-assignee touched the row: assignee=%q status=%q, want alice/in_progress",
				env.mode, got.staleAssignee, got.staleStatus)
		}
		if got.matchCode != 0 {
			t.Errorf("[%s] matching --if-assignee exit = %d, want 0", env.mode, got.matchCode)
		}
		if got.matchAssignee != "" || got.matchStatus != types.StatusOpen {
			t.Errorf("[%s] after matching --if-assignee: assignee=%q status=%q, want empty/open",
				env.mode, got.matchAssignee, got.matchStatus)
		}
		if got.repeatCode == 0 {
			t.Errorf("[%s] re-releasing an already-released issue must fail, got exit 0", env.mode)
		}
		if got.emptyGuardCode == 0 || !got.emptyGuardRejected {
			t.Errorf("[%s] --if-assignee '' must be rejected (exit=%d rejected=%v)",
				env.mode, got.emptyGuardCode, got.emptyGuardRejected)
		}
		if got.emptyGuardAssignee != "alice" || got.emptyGuardStatus != types.StatusInProgress {
			t.Errorf("[%s] --if-assignee '' released the claim: assignee=%q status=%q",
				env.mode, got.emptyGuardAssignee, got.emptyGuardStatus)
		}
		if got.forceComboCode == 0 || !got.forceComboRejected {
			t.Errorf("[%s] --force with --if-assignee must be rejected (exit=%d rejected=%v)",
				env.mode, got.forceComboCode, got.forceComboRejected)
		}
		if got.forceComboAssignee != "alice" || got.forceComboStatus != types.StatusInProgress {
			t.Errorf("[%s] --force --if-assignee released the claim: assignee=%q status=%q",
				env.mode, got.forceComboAssignee, got.forceComboStatus)
		}
	}

	assertUnclaimGuardParity(t, outcomes["classic"], outcomes["proxied"])
}

// assertUnclaimGuardParity reports every field on which the two backends
// disagree, rather than one opaque struct diff.
func assertUnclaimGuardParity(t *testing.T, classic, proxied unclaimGuardOutcome) {
	t.Helper()
	type field struct {
		name             string
		classic, proxied any
	}
	for _, f := range []field{
		{"stale exit code", classic.staleCode, proxied.staleCode},
		{"stale names holder", classic.staleNamesHolder, proxied.staleNamesHolder},
		{"stale assignee", classic.staleAssignee, proxied.staleAssignee},
		{"stale status", classic.staleStatus, proxied.staleStatus},
		{"match exit code", classic.matchCode, proxied.matchCode},
		{"match assignee", classic.matchAssignee, proxied.matchAssignee},
		{"match status", classic.matchStatus, proxied.matchStatus},
		{"repeat exit code", classic.repeatCode, proxied.repeatCode},
		{"empty-guard exit code", classic.emptyGuardCode, proxied.emptyGuardCode},
		{"empty-guard rejected", classic.emptyGuardRejected, proxied.emptyGuardRejected},
		{"empty-guard assignee", classic.emptyGuardAssignee, proxied.emptyGuardAssignee},
		{"empty-guard status", classic.emptyGuardStatus, proxied.emptyGuardStatus},
		{"force-combo exit code", classic.forceComboCode, proxied.forceComboCode},
		{"force-combo rejected", classic.forceComboRejected, proxied.forceComboRejected},
		{"force-combo assignee", classic.forceComboAssignee, proxied.forceComboAssignee},
		{"force-combo status", classic.forceComboStatus, proxied.forceComboStatus},
	} {
		if f.classic != f.proxied {
			t.Errorf("cross-mode divergence on %s: classic=%v proxied=%v", f.name, f.classic, f.proxied)
		}
	}
}
