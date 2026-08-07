//go:build cgo

package main

import (
	"strings"
	"testing"
)

// cascadeRunner drives one storage mode. It is bdRunner plus the one read these
// tests need — "does this id still exist?" — which must NOT fatal when the
// answer is no, because that answer is the whole measurement.
type cascadeRunner struct {
	mode string
	// run fatals on nonzero exit; use for setup steps that must succeed.
	run func(t *testing.T, args ...string) string
	// exists reports whether id resolves, with no opinion about it.
	exists func(t *testing.T, id string) bool
}

func proxiedCascadeRunner(t *testing.T, bd string, p proxiedProject) cascadeRunner {
	return cascadeRunner{
		mode: "proxied",
		run: func(t *testing.T, args ...string) string {
			t.Helper()
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
			if err != nil {
				t.Fatalf("proxied bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
					strings.Join(args, " "), err, stdout, stderr)
			}
			return stdout
		},
		exists: func(t *testing.T, id string) bool {
			t.Helper()
			_, _, err := bdProxiedRunBuffers(t, bd, p.dir, "show", id)
			return err == nil
		},
	}
}

func classicCascadeRunner(t *testing.T, bd, dir string) cascadeRunner {
	return cascadeRunner{
		mode: "classic",
		run: func(t *testing.T, args ...string) string {
			t.Helper()
			return runClassic(t, bd, dir, args...)
		},
		exists: func(t *testing.T, id string) bool {
			t.Helper()
			_, err := bdRunWithFlockRetry(t, bd, dir, "show", id)
			return err == nil
		},
	}
}

// gateDeliveryLedgerOutcome is what wh-gate-sweep's purge step is allowed to
// leave behind: the gate gone, and NOTHING else.
type gateDeliveryLedgerOutcome struct {
	gateGone        bool
	targetSurvives  bool
	siblingSurvives bool
}

// runGateDeliveryLedger replays wh-gate-sweep's third step — `bd delete <gate>
// --force` used as a delivery ledger (the gate row IS the record that the wake
// has not been sent yet, so purging it is how the sweep becomes idempotent).
//
// The fixture is the shape that makes this dangerous: `bd gate create --blocks
// <target>` records the edge as target DEPENDS-ON gate, so the gated bead is a
// DEPENDENT of the row being deleted. A delete that cascades to dependents
// therefore deletes the very bead the sweep just unblocked, plus anything
// downstream of it — which is why the sibling below hangs off the target.
func runGateDeliveryLedger(t *testing.T, r cascadeRunner) gateDeliveryLedgerOutcome {
	t.Helper()

	target := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Gated work")))
	sibling := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Downstream of the gated work",
		"--deps", "blocked-by:"+target.ID)))

	gateOut := r.run(t, "gate", "create", "--type=human", "--blocks", target.ID,
		"--reason", "Need design review")
	gateID := parseCreatedGateID(t, gateOut)

	r.run(t, "gate", "resolve", gateID, "--reason", "reviewed")
	r.run(t, "delete", gateID, "--force")

	return gateDeliveryLedgerOutcome{
		gateGone:        !r.exists(t, gateID),
		targetSurvives:  r.exists(t, target.ID),
		siblingSurvives: r.exists(t, sibling.ID),
	}
}

// wispDecayOutcome is the observable result of the wisp-decay pair the
// wheelhouse runs to bound a flooding lane's trail: `bd close <wisp>` then
// `bd purge --pattern <p> --force`.
type wispDecayOutcome struct {
	wispGone              bool
	durableClosedSurvives bool
	openWispSurvives      bool
}

// runWispDecay replays that pair. The two survivors are the load-bearing half:
// purge is EPHEMERAL-only and CLOSED-only, so a durable closed bead and an open
// wisp must both come through untouched. A purge that swept either would be
// deleting durable work, or reclaiming a wisp a lane is still using.
func runWispDecay(t *testing.T, r cascadeRunner) wispDecayOutcome {
	t.Helper()

	durable := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Durable closed bead")))
	r.run(t, "close", durable.ID, "--reason", "done")

	wisp := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Decaying heartbeat",
		"--ephemeral", "--wisp-type", "heartbeat")))
	openWisp := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Live heartbeat",
		"--ephemeral", "--wisp-type", "heartbeat")))
	r.run(t, "close", wisp.ID, "--reason", "decayed")

	r.run(t, "purge", "--pattern", "*", "--force")

	return wispDecayOutcome{
		wispGone:              !r.exists(t, wisp.ID),
		durableClosedSurvives: r.exists(t, durable.ID),
		openWispSurvives:      r.exists(t, openWisp.ID),
	}
}

// pruneSweepOutcome is the observable result of `bd prune --pattern '*'
// --force`, the durable-plane sibling of the wisp purge.
type pruneSweepOutcome struct {
	closedGone         bool
	openSurvives       bool
	referencedSurvives bool
}

// runPruneSweep replays a durable prune. The referenced bead is the interesting
// one: prune is reference-aware and must protect a closed bead an OPEN bead
// still cites by id, which is a property no cascade rule can supply and which
// therefore has to be checked separately in each mode.
func runPruneSweep(t *testing.T, r cascadeRunner) pruneSweepOutcome {
	t.Helper()

	open := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Still open")))
	closed := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Now closed")))
	referenced := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Cited decision")))
	r.run(t, "create", "--json", "Open citer", "--description", "per "+referenced.ID+" we decided X")
	r.run(t, "close", closed.ID, referenced.ID, "--reason", "done")

	r.run(t, "prune", "--pattern", "*", "--force")

	return pruneSweepOutcome{
		closedGone:         !r.exists(t, closed.ID),
		openSurvives:       r.exists(t, open.ID),
		referencedSurvives: r.exists(t, referenced.ID),
	}
}

// TestProxiedServerCascadeParity is the delete/prune/purge half of bd-04vav:
// since bd-paurh, proxied `bd delete` honors the classic cascade policy
// (default refuse, --cascade sweep, --force orphan), so the question the
// wheelhouse needs answered is "does a delete reach anything classic mode
// would have left alone" — on the two shapes it actually runs:
// wh-gate-sweep's gate purge, and the wisp-decay close+purge pair.
//
// It was written while the answer was YES: proxied `bd delete` passed
// Cascade:true unconditionally and refused the --cascade flag outright.
// bd-x82so put both routes on issueops.Deleter with the flag the caller typed,
// so every scenario here now pins ONE outcome for both modes.
//
// Both scenarios are still replayed against a classic embedded workspace and a
// proxied one and the outcomes compared, so a re-divergence is reported as a
// divergence rather than as one mode's test failing alone.
func TestProxiedServerCascadeParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// This subtest is the acceptance test for bd-paurh: wh-gate-sweep's third
	// step is `bd delete <gate> --force`, and `bd gate create --blocks <target>`
	// makes the gated bead a DEPENDENT of the gate. Under the old proxied
	// unconditional Cascade:true, that step destroyed the work it had just
	// unblocked (plus everything downstream). With embedded parity, --force
	// without --cascade orphans dependents: the gate goes, the gated target and
	// its sibling stay — identical to classic.
	t.Run("gate_delivery_ledger", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cgp")
		proxied := runGateDeliveryLedger(t, proxiedCascadeRunner(t, bd, p))

		classicDir, _, _ := bdInit(t, bd, "--prefix", "cgc")
		classic := runGateDeliveryLedger(t, classicCascadeRunner(t, bd, classicDir))

		// One expectation, both modes: an unforced-cascade delete of the gate
		// removes the gate and leaves everything it was gating alive.
		wantClassic := gateDeliveryLedgerOutcome{
			gateGone: true, targetSurvives: true, siblingSurvives: true,
		}
		if classic != wantClassic {
			t.Errorf("classic gate delivery-ledger delete: got %#v, want %#v", classic, wantClassic)
		}

		wantProxied := wantClassic
		if proxied != wantProxied {
			t.Errorf("proxied gate delivery-ledger delete: got %#v, want %#v "+
				"(bd-paurh parity: `delete <gate> --force` must orphan the gated bead, "+
				"never cascade into it)", proxied, wantProxied)
		}
		if proxied != classic {
			t.Errorf("gate delivery ledger differs across modes:\n  proxied: %#v\n  classic: %#v",
				proxied, classic)
		}
	})

	t.Run("wisp_decay", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cwp")
		proxied := runWispDecay(t, proxiedCascadeRunner(t, bd, p))

		classicDir, _, _ := bdInit(t, bd, "--prefix", "cwc")
		classic := runWispDecay(t, classicCascadeRunner(t, bd, classicDir))

		if proxied != classic {
			t.Errorf("wisp decay differs across modes:\n  proxied: %#v\n  classic: %#v",
				proxied, classic)
		}
		if !proxied.wispGone {
			t.Errorf("proxied: closed wisp survived purge --pattern '*' --force")
		}
		if !proxied.durableClosedSurvives {
			t.Errorf("proxied: purge deleted a DURABLE closed bead — purge is ephemeral-only")
		}
		if !proxied.openWispSurvives {
			t.Errorf("proxied: purge deleted an OPEN wisp — purge is closed-only")
		}
	})

	t.Run("prune_sweep", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "cpp")
		proxied := runPruneSweep(t, proxiedCascadeRunner(t, bd, p))

		classicDir, _, _ := bdInit(t, bd, "--prefix", "cpc")
		classic := runPruneSweep(t, classicCascadeRunner(t, bd, classicDir))

		if proxied != classic {
			t.Errorf("prune sweep differs across modes:\n  proxied: %#v\n  classic: %#v",
				proxied, classic)
		}
		want := pruneSweepOutcome{closedGone: true, openSurvives: true, referencedSurvives: true}
		if proxied != want {
			t.Errorf("proxied prune sweep: got %#v, want %#v", proxied, want)
		}
	})
}
