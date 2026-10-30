//go:build cgo

package bdhttp_test

import (
	"strings"
	"testing"
)

// TestE2E_PerCommandRequestBudget pins how many sequential gateway requests
// each everyday bd command makes over the http backend (bts-xpw.3.14.6,
// beads-team-server docs/v0-gateway-latency.md option F). On the hosted /v0
// path every request costs ~0.33 s warm, and a command makes them strictly one
// after another, so the request count IS the command's latency floor.
//
// The budgets are ceilings, not exact counts: a change that makes a command
// cheaper passes, a change that adds a round trip fails here first. Both the
// current server's handshake and the MC server's (fewer capabilities, no
// wire_revision) are held to the same ceilings.
func TestE2E_PerCommandRequestBudget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	for _, tc := range []struct {
		name string
		mask func(map[string]any)
		// over raises a step's budget for this handshake shape, where the
		// server lacks a capability the step otherwise relies on.
		over map[string]int
	}{
		// The in-process server composes no external-dependency policy, so
		// its handshake lacks policy.external_dependencies and the client
		// applies the policy itself with extra reads. A deployed bd serve
		// (hosted included) enforces it and says so; advertise it here so
		// the budget measures that shape.
		{"current server", advertiseExternalPolicy, nil},
		// The MC server predates issues.close.autoCloseMolecule, so a close
		// there also runs the molecule auto-close check client side (an edge
		// read and a re-read of the closed row).
		{"MC server mask", mcContextMask, map[string]int{"close": 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyAddr, counter := startMaskingProxy(t, addr, tc.mask)
			f := newHTTPFixture(t, bin, proxyAddr)
			target := f.create("budget target", "-t", "task")
			other := f.create("budget other", "-t", "task")
			closing := f.create("budget closing", "-t", "task")
			f.mustRun(nil, "comments", "add", target, "seed comment")

			for _, step := range []struct {
				name   string
				budget int
				args   []string
			}{
				{"ready", 2, []string{"ready", "--json"}},
				// The limit hides rows, and without BD_JSON_ENVELOPE nothing
				// prints their count: one listing, no ready:count after it.
				{"ready --limit 1", 2, []string{"ready", "--json", "--limit", "1"}},
				{"list", 4, []string{"list", "--json"}},
				{"show", 1, []string{"show", target, "--json"}},
				// The first ready learned the server's external-dependency
				// policy from the handshake; a later process reads it from the
				// policy cache instead of fetching the context again.
				{"ready, policy cached", 1, []string{"ready", "--json"}},
				{"create", 2, []string{"create", "budget created", "-t", "task", "--json"}},
				{"update --append-notes", 3, []string{"update", target, "--append-notes", "more", "--json"}},
				{"update --status", 3, []string{"update", target, "--status", "in_progress", "--json"}},
				{"close", 3, []string{"close", closing, "--json"}},
				{"comments add", 3, []string{"comments", "add", target, "a budget comment"}},
				{"dep add", 5, []string{"dep", "add", target, other}},
			} {
				counter.reset()
				f.mustRun(nil, step.args...)
				paths := counter.snapshot()
				t.Logf("bd %s: %d requests: %s", step.name, len(paths), strings.Join(paths, ", "))
				budget := step.budget
				if over, ok := tc.over[step.name]; ok {
					budget = over
				}
				if len(paths) > budget {
					t.Errorf("bd %s made %d gateway requests, budget %d: %v", step.name, len(paths), budget, paths)
				}
			}
		})
	}
}

// advertiseExternalPolicy adds policy.external_dependencies to the handshake's
// capabilities, the shape of a deployed bd serve that enforces the policy.
func advertiseExternalPolicy(body map[string]any) {
	caps, _ := body["capabilities"].([]any)
	for _, c := range caps {
		if c == "policy.external_dependencies" {
			return
		}
	}
	body["capabilities"] = append(caps, "policy.external_dependencies")
}
