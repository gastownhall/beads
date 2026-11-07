package dolt

// Deterministic fixture graph and frozen-predicate reference model for the
// migration body-plan tests (gastownhall/beads#6520, bd-f1s6sq).
//
// The report's restored clone carried 285 issues, 113 dependencies, 11,654
// wisps and 14,146 wisp dependencies; migration 0059's frozen recompute did
// not finish within two hours on it. This fixture reproduces the SHAPE of
// that graph at roughly 1/8 the wisp tier — 285 issues, 113 dependencies,
// 1,457 wisps, 1,770 wisp dependencies — with every branch of the frozen
// predicate exercised by construction, not by luck:
//
//   - blocks and conditional-blocks onto open, closed and pinned targets of
//     both target kinds, from waiters in both planes;
//   - waits-for with NULL, all-children and any-children gates onto hub
//     targets that have open children, closed children, both, or none;
//   - parent-child edges in both planes, including cross-plane targets, so
//     blockage propagates issue→wisp and wisp→issue;
//   - a three-deep issue chain and a three-deep wisp chain under a directly
//     blocked root, for recursive propagation;
//   - a mixed-kind cycle (issue → wisp → issue) for the recursive UNION
//     DISTINCT fixed point.
//
// Every waiter/child id is drawn without replacement from its status pool
// (each pool is larger than its draw count), so no two rows collide on the
// tables' natural keys. The generator is deterministic (fixed seed, fixed
// quotas): the same graph is built on every run and every lane, and the Go
// model below computes the blocked state the FROZEN predicate dictates.

import (
	"fmt"
	"testing"
)

const (
	planFixtureIssues    = 285
	planFixtureDeps      = 113
	planFixtureWisps     = 1457
	planFixtureWispDeps  = 1770
	planFixtureSeed      = 6520
	planFrozenMain59Hash = "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394"
	planFrozenIgnored15  = "e761d5c3ceb9610edbb2e1d153e82d235cc7d7f0b87f28a45bec43835e6a4e19"
)

// planFixtureEdge is one dependency row in either plane. from is the
// issue_id column: the waiter or child, an issue in the durable plane and a
// wisp in the clone-local plane. Exactly one of targetIssue/targetWisp is
// set unless typ is an external-target row.
type planFixtureEdge struct {
	id          string
	from        string
	targetIssue string
	targetWisp  string
	typ         string
	gate        string // "", "all-children" or "any-children"; "" is SQL NULL metadata
}

type planFixture struct {
	issueStatus map[string]string
	wispStatus  map[string]string
	issueIDs    []string
	wispIDs     []string
	issueEdges  []planFixtureEdge // dependencies
	wispEdges   []planFixtureEdge // wisp_dependencies
}

// splitmix64 is a small deterministic PRNG: fixed seed, no state outside the
// generator, stable across platforms (all uint64 arithmetic).
type splitmix64 struct{ state uint64 }

func (s *splitmix64) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *splitmix64) intn(n int) int { return int(s.next() % uint64(n)) }

func (s *splitmix64) pick(pool []string) string { return pool[s.intn(len(pool))] }

// idStream hands out a pool's ids in a deterministic shuffled order, without
// replacement. Exhausting one is a generator bug: every pool is larger than
// its draw count by construction.
type idStream struct {
	rng  *splitmix64
	perm []string
	next int
}

func newIDStream(rng *splitmix64, pool []string) *idStream {
	perm := append([]string(nil), pool...)
	for i := len(perm) - 1; i > 0; i-- {
		j := rng.intn(i + 1)
		perm[i], perm[j] = perm[j], perm[i]
	}
	return &idStream{rng: rng, perm: perm}
}

func (s *idStream) take() string {
	if s.next >= len(s.perm) {
		panic("plan fixture: id pool exhausted — quota/pool mismatch")
	}
	id := s.perm[s.next]
	s.next++
	return id
}

// Reserved ids (fixed roles; everything at or above the filler floor is
// drawn from status pools): issue hubs i-000..i-009 are waits-for and
// parent-child targets; i-010 is a waits-for target with no children; i-011
// is the root of the issue chain (i-012..i-014); i-015 closes the mixed-kind
// cycle with w-0015. Wisp hubs are w-0000..w-0019; w-0020 is the root of the
// wisp chain (w-0021, w-0022).
const (
	planIssueHubCount    = 10
	planWispHubCount     = 20
	planIssueFillerFloor = 16 // first non-reserved issue id
	planWispFillerFloor  = 23 // first non-reserved wisp id
)

func planIssueID(n int) string { return fmt.Sprintf("i-%03d", n) }
func planWispID(n int) string  { return fmt.Sprintf("w-%04d", n) }

// buildPlanFixture generates the #6520-shaped graph deterministically.
func buildPlanFixture() *planFixture {
	rng := &splitmix64{state: planFixtureSeed}
	f := &planFixture{issueStatus: map[string]string{}, wispStatus: map[string]string{}}

	for n := 0; n < planFixtureIssues; n++ {
		id := planIssueID(n)
		switch {
		case n%19 == 5:
			f.issueStatus[id] = "closed"
		case n%23 == 7:
			f.issueStatus[id] = "pinned"
		default:
			f.issueStatus[id] = "open"
		}
		f.issueIDs = append(f.issueIDs, id)
	}
	for n := 0; n < planFixtureWisps; n++ {
		id := planWispID(n)
		switch {
		case n%17 == 3:
			f.wispStatus[id] = "closed"
		case n%29 == 11:
			f.wispStatus[id] = "pinned"
		default:
			f.wispStatus[id] = "open"
		}
		f.wispIDs = append(f.wispIDs, id)
	}

	// Reserved-role ids are open regardless of the background pattern: the
	// chain roots must be directly blockable, the cycle and chain members
	// must propagate, and the hubs' own status is irrelevant to their role.
	for n := 0; n < planIssueFillerFloor; n++ {
		f.issueStatus[planIssueID(n)] = "open"
	}
	for n := 0; n < planWispFillerFloor; n++ {
		f.wispStatus[planWispID(n)] = "open"
	}

	var openIssues, closedIssues, pinnedIssues, openWisps, closedWisps, pinnedWisps []string
	for n := planIssueFillerFloor; n < planFixtureIssues; n++ {
		switch f.issueStatus[planIssueID(n)] {
		case "open":
			openIssues = append(openIssues, planIssueID(n))
		case "closed":
			closedIssues = append(closedIssues, planIssueID(n))
		case "pinned":
			pinnedIssues = append(pinnedIssues, planIssueID(n))
		}
	}
	for n := planWispFillerFloor; n < planFixtureWisps; n++ {
		switch f.wispStatus[planWispID(n)] {
		case "open":
			openWisps = append(openWisps, planWispID(n))
		case "closed":
			closedWisps = append(closedWisps, planWispID(n))
		case "pinned":
			pinnedWisps = append(pinnedWisps, planWispID(n))
		}
	}

	// Waiter/child ids come from without-replacement streams (distinct by
	// construction, so no two rows collide on the natural keys); targets are
	// free picks from status pools.
	openIssueSrc := newIDStream(rng, openIssues)
	closedIssueSrc := newIDStream(rng, closedIssues)
	openWispSrc := newIDStream(rng, openWisps)
	closedWispSrc := newIDStream(rng, closedWisps)

	// usedWispTargetKey guards the (issue_id, target) natural keys of
	// wisp_dependencies (the uk_* unique keys cover both target columns
	// regardless of type). Stream-drawn froms are distinct within their
	// roles, but the parent-child filler streams are fresh permutations of
	// the same pools, so a filler child can repeat a role-drawn child; its
	// hub target then rotates to a free one below.
	usedWispTargetKey := map[string]bool{}
	addIssueEdge := func(from, targetIssue, targetWisp, typ, gate string) {
		f.issueEdges = append(f.issueEdges, planFixtureEdge{
			id: fmt.Sprintf("d-%04d", len(f.issueEdges)+1), from: from,
			targetIssue: targetIssue, targetWisp: targetWisp, typ: typ, gate: gate,
		})
	}
	addWispEdge := func(from, targetIssue, targetWisp, typ, gate string) {
		usedWispTargetKey[from+"|"+targetIssue+"|"+targetWisp] = true
		f.wispEdges = append(f.wispEdges, planFixtureEdge{
			id: fmt.Sprintf("wd-%05d", len(f.wispEdges)+1), from: from,
			targetIssue: targetIssue, targetWisp: targetWisp, typ: typ, gate: gate,
		})
	}
	// freeWispHubIssueTarget rotates h until (from, issue hub h) is free.
	freeWispHubIssueTarget := func(from string, h int) int {
		for ; usedWispTargetKey[from+"|"+planIssueID(h)+"|"]; h = (h + 1) % planIssueHubCount {
		}
		return h
	}
	freeWispHubWispTarget := func(from string, h int) int {
		for ; usedWispTargetKey[from+"||"+planWispID(h)]; h = (h + 1) % planWispHubCount {
		}
		return h
	}
	gates := []string{"", "all-children", "any-children"}

	// ---- Durable dependencies (issues are the waiters/children): 113 rows.
	// 30 parent-child: every issue hub gets two open and one closed child.
	for h := 0; h < planIssueHubCount; h++ {
		hub := planIssueID(h)
		addIssueEdge(openIssueSrc.take(), hub, "", "parent-child", "")
		addIssueEdge(openIssueSrc.take(), hub, "", "parent-child", "")
		addIssueEdge(closedIssueSrc.take(), hub, "", "parent-child", "")
	}
	// 3 parent-child: the issue chain under directly blocked root i-011.
	addIssueEdge(planIssueID(12), planIssueID(11), "", "parent-child", "")
	addIssueEdge(planIssueID(13), planIssueID(12), "", "parent-child", "")
	addIssueEdge(planIssueID(14), planIssueID(13), "", "parent-child", "")
	addIssueEdge(planIssueID(11), planIssueID(0), "", "blocks", "")
	// 1 parent-child + 1 blocks: the issue half of the mixed-kind cycle.
	addIssueEdge(planIssueID(15), "", planWispID(15), "parent-child", "")
	addIssueEdge(planIssueID(15), planIssueID(1), "", "blocks", "")
	// 12 waits-for: onto issue hubs, wisp hubs and the childless target
	// i-010, with rotating gates.
	for k := 0; k < 5; k++ {
		addIssueEdge(openIssueSrc.take(), planIssueID(k), "", "waits-for", gates[k%3])
	}
	for k := 0; k < 5; k++ {
		addIssueEdge(openIssueSrc.take(), "", planWispID(k), "waits-for", gates[(k+1)%3])
	}
	addIssueEdge(openIssueSrc.take(), planIssueID(10), "", "waits-for", "")
	addIssueEdge(openIssueSrc.take(), planIssueID(10), "", "waits-for", "any-children")
	// 18 blocks onto open, closed and pinned targets of both kinds.
	for k := 0; k < 6; k++ {
		addIssueEdge(openIssueSrc.take(), rng.pick(openIssues), "", "blocks", "")
	}
	for k := 0; k < 3; k++ {
		addIssueEdge(openIssueSrc.take(), "", rng.pick(openWisps), "blocks", "")
	}
	for k := 0; k < 5; k++ {
		addIssueEdge(openIssueSrc.take(), rng.pick(closedIssues), "", "blocks", "")
	}
	for k := 0; k < 4; k++ {
		addIssueEdge(openIssueSrc.take(), "", rng.pick(closedWisps), "blocks", "")
	}
	// 5 conditional-blocks: 3 onto open wisp targets, 2 onto closed issues.
	for k := 0; k < 3; k++ {
		addIssueEdge(openIssueSrc.take(), "", rng.pick(openWisps), "conditional-blocks", "")
	}
	for k := 0; k < 2; k++ {
		addIssueEdge(openIssueSrc.take(), rng.pick(closedIssues), "", "conditional-blocks", "")
	}
	// 42 parent-child filler: distinct open children onto hubs of both kinds.
	for k := 0; k < 21; k++ {
		addIssueEdge(openIssueSrc.take(), planIssueID(rng.intn(planIssueHubCount)), "", "parent-child", "")
		addIssueEdge(openIssueSrc.take(), "", planWispID(rng.intn(planWispHubCount)), "parent-child", "")
	}

	// ---- Clone-local wisp dependencies (wisps are the waiters/children):
	// ---- 1,770 rows.
	// 80 parent-child: every wisp hub gets three open and one closed child.
	for h := 0; h < planWispHubCount; h++ {
		hub := planWispID(h)
		addWispEdge(openWispSrc.take(), "", hub, "parent-child", "")
		addWispEdge(openWispSrc.take(), "", hub, "parent-child", "")
		addWispEdge(openWispSrc.take(), "", hub, "parent-child", "")
		addWispEdge(closedWispSrc.take(), "", hub, "parent-child", "")
	}
	// 30 parent-child: every issue hub also gets two open and one closed
	// WISP child, so hub child sets span both planes.
	for h := 0; h < planIssueHubCount; h++ {
		hub := planIssueID(h)
		addWispEdge(openWispSrc.take(), hub, "", "parent-child", "")
		addWispEdge(openWispSrc.take(), hub, "", "parent-child", "")
		addWispEdge(closedWispSrc.take(), hub, "", "parent-child", "")
	}
	// 2 parent-child + 1 blocks: the wisp chain under directly blocked root
	// w-0020, which blocks an open issue.
	addWispEdge(planWispID(21), "", planWispID(20), "parent-child", "")
	addWispEdge(planWispID(22), "", planWispID(21), "parent-child", "")
	addWispEdge(planWispID(20), planIssueID(3), "", "blocks", "")
	// 1 parent-child: the wisp half of the mixed-kind cycle.
	addWispEdge(planWispID(15), planIssueID(15), "", "parent-child", "")
	// 40 waits-for onto hubs of both kinds with rotating gates.
	for k := 0; k < 20; k++ {
		addWispEdge(openWispSrc.take(), "", planWispID(5+k%15), "waits-for", gates[k%3])
	}
	for k := 0; k < 10; k++ {
		addWispEdge(openWispSrc.take(), planIssueID(k), "", "waits-for", gates[(k+1)%3])
		addWispEdge(openWispSrc.take(), planIssueID(k), "", "waits-for", gates[(k+2)%3])
	}
	// 299 blocks onto open, closed and pinned targets of both kinds (the
	// 300th is w-0020's root edge above).
	for k := 0; k < 60; k++ {
		addWispEdge(openWispSrc.take(), rng.pick(openIssues), "", "blocks", "")
	}
	for k := 0; k < 50; k++ {
		addWispEdge(openWispSrc.take(), rng.pick(closedIssues), "", "blocks", "")
	}
	for k := 0; k < 10; k++ {
		addWispEdge(openWispSrc.take(), rng.pick(pinnedIssues), "", "blocks", "")
	}
	for k := 0; k < 129; k++ {
		addWispEdge(openWispSrc.take(), "", rng.pick(openWisps), "blocks", "")
	}
	for k := 0; k < 20; k++ {
		addWispEdge(openWispSrc.take(), "", rng.pick(closedWisps), "blocks", "")
	}
	for k := 0; k < 30; k++ {
		addWispEdge(openWispSrc.take(), "", rng.pick(pinnedWisps), "blocks", "")
	}
	// 75 conditional-blocks.
	for k := 0; k < 40; k++ {
		addWispEdge(openWispSrc.take(), rng.pick(openIssues), "", "conditional-blocks", "")
	}
	for k := 0; k < 15; k++ {
		addWispEdge(openWispSrc.take(), "", rng.pick(openWisps), "conditional-blocks", "")
	}
	for k := 0; k < 20; k++ {
		addWispEdge(openWispSrc.take(), "", rng.pick(closedWisps), "conditional-blocks", "")
	}
	// 1,242 parent-child filler: two fresh full-pool permutations (621
	// distinct children each — fewer than the ~1,300-id open pool), onto
	// hubs of both kinds, rotating past any hub another role already
	// paired this child with.
	issueTargetChildren := newIDStream(rng, openWisps)
	wispTargetChildren := newIDStream(rng, openWisps)
	for k := 0; k < 621; k++ {
		from := issueTargetChildren.take()
		addWispEdge(from, planIssueID(freeWispHubIssueTarget(from, rng.intn(planIssueHubCount))), "", "parent-child", "")
	}
	for k := 0; k < 621; k++ {
		from := wispTargetChildren.take()
		addWispEdge(from, "", planWispID(freeWispHubWispTarget(from, rng.intn(planWispHubCount))), "parent-child", "")
	}

	return f
}

// checkCounts pins the generator's exact shape: the ACs are stated against
// these numbers, so silent quota drift must fail loudly, not reshape the
// workload.
func (f *planFixture) checkCounts(t *testing.T) {
	t.Helper()
	if len(f.issueIDs) != planFixtureIssues || len(f.issueStatus) != planFixtureIssues {
		t.Fatalf("fixture drifted: %d issues (want %d)", len(f.issueIDs), planFixtureIssues)
	}
	if len(f.wispIDs) != planFixtureWisps || len(f.wispStatus) != planFixtureWisps {
		t.Fatalf("fixture drifted: %d wisps (want %d)", len(f.wispIDs), planFixtureWisps)
	}
	if len(f.issueEdges) != planFixtureDeps {
		t.Fatalf("fixture drifted: %d dependencies (want %d)", len(f.issueEdges), planFixtureDeps)
	}
	if len(f.wispEdges) != planFixtureWispDeps {
		t.Fatalf("fixture drifted: %d wisp dependencies (want %d)", len(f.wispEdges), planFixtureWispDeps)
	}
}

// planModel is the frozen-predicate reference model: the blocked state the
// shipped 0059/ignored-15 CTEs dictate for the fixture, computed in Go.
type planModel struct {
	issueBlocked map[string]bool
	wispBlocked  map[string]bool
}

func planOpen(status string) bool { return status != "closed" && status != "pinned" }

// model computes the reference blocked state by mirroring the frozen CTEs:
// child sets per target kind, directly_blocked over blocks and the gated
// waits-for predicate, then the reachable fixpoint over parent-child edges
// in both planes.
func (f *planFixture) model() planModel {
	openChildIssue := map[string]bool{}
	openChildWisp := map[string]bool{}
	closedChildIssue := map[string]bool{}
	closedChildWisp := map[string]bool{}
	recordChild := func(e planFixtureEdge, childStatus string) {
		if e.typ != "parent-child" {
			return
		}
		open, closed := planOpen(childStatus), childStatus == "closed"
		if e.targetIssue != "" {
			if open {
				openChildIssue[e.targetIssue] = true
			} else if closed {
				closedChildIssue[e.targetIssue] = true
			}
			return
		}
		if open {
			openChildWisp[e.targetWisp] = true
		} else if closed {
			closedChildWisp[e.targetWisp] = true
		}
	}
	for _, e := range f.issueEdges {
		recordChild(e, f.issueStatus[e.from])
	}
	for _, e := range f.wispEdges {
		recordChild(e, f.wispStatus[e.from])
	}

	// directly_blocked: an open waiter blocked by a blocks/conditional-blocks
	// edge onto an open target of either kind, or by a waits-for edge whose
	// target has an open child and — only under the any-children gate — no
	// closed child yet.
	waiterBlocked := func(gate, targetIssue, targetWisp string) bool {
		hasOpenChild := (targetIssue != "" && openChildIssue[targetIssue]) ||
			(targetWisp != "" && openChildWisp[targetWisp])
		hasClosedChild := (targetIssue != "" && closedChildIssue[targetIssue]) ||
			(targetWisp != "" && closedChildWisp[targetWisp])
		return hasOpenChild && !(gate == "any-children" && hasClosedChild)
	}
	targetOpen := func(targetIssue, targetWisp string) bool {
		if targetIssue != "" {
			return planOpen(f.issueStatus[targetIssue])
		}
		return planOpen(f.wispStatus[targetWisp])
	}

	reachIssue := map[string]bool{}
	reachWisp := map[string]bool{}
	for id, status := range f.issueStatus {
		if !planOpen(status) {
			continue
		}
		for _, e := range f.issueEdges {
			if e.from != id {
				continue
			}
			switch e.typ {
			case "blocks", "conditional-blocks":
				if targetOpen(e.targetIssue, e.targetWisp) {
					reachIssue[id] = true
				}
			case "waits-for":
				if waiterBlocked(e.gate, e.targetIssue, e.targetWisp) {
					reachIssue[id] = true
				}
			}
		}
	}
	for id, status := range f.wispStatus {
		if !planOpen(status) {
			continue
		}
		for _, e := range f.wispEdges {
			if e.from != id {
				continue
			}
			switch e.typ {
			case "blocks", "conditional-blocks":
				if targetOpen(e.targetIssue, e.targetWisp) {
					reachWisp[id] = true
				}
			case "waits-for":
				if waiterBlocked(e.gate, e.targetIssue, e.targetWisp) {
					reachWisp[id] = true
				}
			}
		}
	}

	// reachable: the fixpoint over parent-child edges — an open child of a
	// blocked entity (either kind, either plane) is blocked.
	for changed := true; changed; {
		changed = false
		for _, e := range f.issueEdges {
			if e.typ != "parent-child" || reachIssue[e.from] || !planOpen(f.issueStatus[e.from]) {
				continue
			}
			if (e.targetIssue != "" && reachIssue[e.targetIssue]) || (e.targetWisp != "" && reachWisp[e.targetWisp]) {
				reachIssue[e.from] = true
				changed = true
			}
		}
		for _, e := range f.wispEdges {
			if e.typ != "parent-child" || reachWisp[e.from] || !planOpen(f.wispStatus[e.from]) {
				continue
			}
			if (e.targetIssue != "" && reachIssue[e.targetIssue]) || (e.targetWisp != "" && reachWisp[e.targetWisp]) {
				reachWisp[e.from] = true
				changed = true
			}
		}
	}

	return planModel{issueBlocked: reachIssue, wispBlocked: reachWisp}
}

// assertStructuralExpectations fails the test if the fixture stopped
// exercising the predicate branches the ACs depend on: recursive chains,
// the mixed-kind cycle, gated waiters and the childless waits-for target.
func (f *planFixture) assertStructuralExpectations(t *testing.T, m planModel) {
	t.Helper()
	for _, id := range []string{"i-011", "i-012", "i-013", "i-014", "i-015"} {
		if !m.issueBlocked[id] {
			t.Errorf("fixture: issue %s is not blocked in the model; the issue chain/cycle no longer exercises recursive propagation", id)
		}
	}
	for _, id := range []string{"w-0020", "w-0021", "w-0022", "w-0015"} {
		if !m.wispBlocked[id] {
			t.Errorf("fixture: wisp %s is not blocked in the model; the wisp chain/cycle no longer exercises recursive propagation", id)
		}
	}
	// Hubs have both open and closed children in both planes: an any-gate
	// waiter onto one is blocked by its open child despite the closed one —
	// the NOT(any-children AND closed-child) term with a true inner side.
	anyGated, allGated, nullGated := 0, 0, 0
	for _, edges := range [][]planFixtureEdge{f.issueEdges, f.wispEdges} {
		for _, e := range edges {
			if e.typ != "waits-for" {
				continue
			}
			switch {
			case e.gate == "any-children" && (m.issueBlocked[e.from] || m.wispBlocked[e.from]):
				anyGated++
			case e.gate == "all-children" && (m.issueBlocked[e.from] || m.wispBlocked[e.from]):
				allGated++
			case e.gate == "" && (m.issueBlocked[e.from] || m.wispBlocked[e.from]):
				nullGated++
			}
		}
	}
	if anyGated == 0 || allGated == 0 || nullGated == 0 {
		t.Errorf("fixture: blocked waiters no longer cover any/all/NULL gates (%d/%d/%d)", anyGated, allGated, nullGated)
	}
	// The childless target's waiters must stay unblocked.
	for _, e := range f.issueEdges {
		if e.targetIssue == planIssueID(10) && m.issueBlocked[e.from] {
			t.Errorf("fixture: waiter %s of childless target i-010 is blocked in the model", e.from)
		}
	}
}
