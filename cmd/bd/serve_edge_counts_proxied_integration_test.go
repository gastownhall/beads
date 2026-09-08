//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// End-to-end for the edge count, against real Dolt through a real `bd serve`
// subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// parameter projection, the per-anchor envelope, the four refusals and the
// anchor bound. What only this level can prove is what the NUMBERS mean, and on
// this operation that is most of the contract:
//
//   - the two directions really are DIFFERENT edge sets, which is the whole
//     reason the parameter is required rather than defaulted;
//   - the count really SPANS BOTH DEPENDENCY PLANES — an edge lives in
//     `dependencies` or in `wisp_dependencies` by which plane its SOURCE sits
//     on, and a durable issue's dependent count includes the wisps that depend
//     on it. That is a fact about two tables and a fake cannot be asked it;
//   - a missing anchor really is a per-anchor `missing: true` beside answers
//     that were found, rather than a 404 that throws them away;
//   - the numbers AGREE with the rows the stored-edge read returns, which is
//     the property that makes this operation a count of that graph rather than
//     of some other one.
//
// Every case scopes itself to issues this test creates, so the numbers are
// exact whatever else the workspace holds.

// anchorCount is one decoded entry of the response. Every member is a POINTER:
// an omitted `count` or `missing` is the failure mode worth catching, and a
// value decode would read it as the zero that is correct most of the time.
type anchorCount struct {
	ID      *string `json:"id"`
	Count   *int64  `json:"count"`
	Missing *bool   `json:"missing"`
}

// countEdges asks the server for the edge counts and returns the status and the
// decoded anchors.
//
// The count decodes through an int64 rather than through `any`, because the
// member is `format: int64` and reading a cardinality through a float64 answers
// a number NEAR the count — which on a number is worse than an error, since
// nothing downstream can tell.
func (sp *serveProcess) countEdges(t *testing.T, query string) (int, []anchorCount) {
	t.Helper()
	resp, err := sp.client.Get(sp.url("/v0/beads/dependencies:count" + query))
	if err != nil {
		t.Fatalf("GET dependencies:count%s: %v\nstderr:\n%s", query, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read count body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var body struct {
		Anchors []anchorCount `json:"anchors"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode count body %q: %v", raw, err)
	}
	if body.Anchors == nil {
		t.Errorf("GET dependencies:count%s returned a null `anchors`; the document promises an array", query)
	}
	return resp.StatusCode, body.Anchors
}

// one asks about a single anchor and returns its entry.
func (sp *serveProcess) countEdgesOf(t *testing.T, id, query string) anchorCount {
	t.Helper()
	status, anchors := sp.countEdges(t, "?issue_id="+id+query)
	if status != http.StatusOK {
		t.Fatalf("count edges of %s%s: status = %d", id, query, status)
	}
	if len(anchors) != 1 {
		t.Fatalf("count edges of %s: %d anchors, want 1", id, len(anchors))
	}
	a := anchors[0]
	if a.ID == nil || a.Count == nil || a.Missing == nil {
		t.Fatalf("count edges of %s: an anchor member is missing: %+v", id, a)
	}
	if *a.ID != id {
		t.Fatalf("count edges of %s answered about %q; the id is echoed as spelled", id, *a.ID)
	}
	return a
}

func TestProxiedServerServeEdgeCounts(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvedgecnt")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE TWO DIRECTIONS ARE DIFFERENT EDGE SETS, and this is the shape that
	// shows it: a hub that depends on two issues and is depended on by three,
	// so out and in are 2 and 3 and no handler that answered the wrong one
	// could pass by coincidence. A symmetric fixture would have made the
	// required `direction` parameter unfalsifiable.
	t.Run("out and in count different edges", func(t *testing.T) {
		hub := bdProxiedCreate(t, bd, p.dir, "the hub", "-p", "2")
		var dependsOn, dependents []string
		for i := 0; i < 2; i++ {
			dependsOn = append(dependsOn, bdProxiedCreate(t, bd, p.dir, "a dependency", "-p", "2").ID)
		}
		for i := 0; i < 3; i++ {
			dependents = append(dependents, bdProxiedCreate(t, bd, p.dir, "a dependent", "-p", "2").ID)
		}
		for _, target := range dependsOn {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", hub.ID, target); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", hub.ID, target, err, out)
			}
		}
		for _, source := range dependents {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", source, hub.ID); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", source, hub.ID, err, out)
			}
		}

		if got := *sp.countEdgesOf(t, hub.ID, "&direction=out").Count; got != 2 {
			t.Errorf("out = %d, want 2 — the edges whose SOURCE is the anchor", got)
		}
		if got := *sp.countEdgesOf(t, hub.ID, "&direction=in").Count; got != 3 {
			t.Errorf("in = %d, want 3 — the edges whose TARGET is the anchor", got)
		}

		// THE COUNT AGREES WITH THE ROWS. The stored-edge read beside it is
		// outbound-only and returns the rows themselves; if the two disagreed,
		// one of them would be counting a different graph.
		if rows := len(sp.storedEdges(t, hub.ID)); int64(rows) != *sp.countEdgesOf(t, hub.ID, "&direction=out").Count {
			t.Errorf("the outbound count and GET /v0/beads/dependencies disagree: %d rows", rows)
		}

		// The type filter narrows EDGES and never anchors: the hub's outbound
		// edges are all `blocks`, so a filter for a type it has none of leaves
		// it PRESENT at 0 rather than missing.
		filtered := sp.countEdgesOf(t, hub.ID, "&direction=out&type=discovered-from")
		if *filtered.Count != 0 || *filtered.Missing {
			t.Errorf("a type filter matching nothing = {count %d missing %v}, want a present anchor at 0",
				*filtered.Count, *filtered.Missing)
		}
	})

	// THE WISP PLANE, which is the fact this operation's contract states most
	// loudly and the one no fake can be asked. An edge is routed to
	// `dependencies` or `wisp_dependencies` by the plane its SOURCE sits on, so
	// a durable issue depended on by a wisp has an inbound edge in the OTHER
	// table — and the count is the sum across both.
	t.Run("the count spans both dependency planes", func(t *testing.T) {
		durable := bdProxiedCreate(t, bd, p.dir, "a durable anchor", "-p", "2")
		durableDependent := bdProxiedCreate(t, bd, p.dir, "a durable dependent", "-p", "2")
		wisp := bdProxiedCreate(t, bd, p.dir, "an ephemeral dependent", "-p", "2",
			"--ephemeral", "--wisp-type", "heartbeat")

		for _, source := range []string{durableDependent.ID, wisp.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", source, durable.ID); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", source, durable.ID, err, out)
			}
		}

		// One edge in `dependencies`, one in `wisp_dependencies`, and the
		// answer is 2. A count that read one table would answer 1 — and would
		// look perfectly plausible.
		if got := *sp.countEdgesOf(t, durable.ID, "&direction=in").Count; got != 2 {
			t.Errorf("in = %d, want 2 — the sum across `dependencies` and `wisp_dependencies`", got)
		}

		// And from the wisp's own side: an ephemeral anchor's outbound count
		// reaches the durable issue it depends on.
		if got := *sp.countEdgesOf(t, wisp.ID, "&direction=out").Count; got != 1 {
			t.Errorf("the wisp's out = %d, want 1 — an ephemeral anchor counts its own edges", got)
		}
	})

	// A MISSING ANCHOR IS REPORTED, NOT REFUSED, and it is reported BESIDE the
	// answers that were found. This is the case the `missing` member exists
	// for: both entries here carry a count of 0, and only the flag tells the
	// typo from the issue that genuinely has no inbound edges.
	t.Run("a missing anchor rides beside the answers that were found", func(t *testing.T) {
		found := bdProxiedCreate(t, bd, p.dir, "found, with no dependents", "-p", "2")
		withEdges := bdProxiedCreate(t, bd, p.dir, "found, with a dependent", "-p", "2")
		dependent := bdProxiedCreate(t, bd, p.dir, "the dependent", "-p", "2")
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", dependent.ID, withEdges.ID); err != nil {
			t.Fatalf("dep add: %v\n%s", err, out)
		}

		status, anchors := sp.countEdges(t,
			"?issue_id="+withEdges.ID+"&issue_id=bd-nosuchbead&issue_id="+found.ID+"&direction=in")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 — an absent id is not a 404 on this operation", status)
		}
		if len(anchors) != 3 {
			t.Fatalf("%d anchors, want 3 in the order the request named them", len(anchors))
		}
		for i, want := range []struct {
			id      string
			count   int64
			missing bool
		}{
			{withEdges.ID, 1, false},
			{"bd-nosuchbead", 0, true},
			{found.ID, 0, false},
		} {
			got := anchors[i]
			if got.ID == nil || got.Count == nil || got.Missing == nil {
				t.Fatalf("anchor %d is missing a member: %+v", i, got)
			}
			if *got.ID != want.id || *got.Count != want.count || *got.Missing != want.missing {
				t.Errorf("anchor %d = {%q %d %v}, want {%q %d %v}",
					i, *got.ID, *got.Count, *got.Missing, want.id, want.count, want.missing)
			}
		}
	})

	// THE STATUS FILTER AND ITS ASYMMETRY, both halves against real rows. The
	// filter narrows by the DEPENDENT's stored status, read from the
	// dependent's own plane; and it is refused beside `direction=out`, because
	// an outbound edge's far end may be a row this database does not hold.
	t.Run("status narrows inbound edges and is refused outbound", func(t *testing.T) {
		anchor := bdProxiedCreate(t, bd, p.dir, "status-filtered anchor", "-p", "2")
		open1 := bdProxiedCreate(t, bd, p.dir, "an open dependent", "-p", "2")
		open2 := bdProxiedCreate(t, bd, p.dir, "another open dependent", "-p", "2")
		closed := bdProxiedCreate(t, bd, p.dir, "a dependent to close", "-p", "2")
		for _, source := range []string{open1.ID, open2.ID, closed.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", source, anchor.ID); err != nil {
				t.Fatalf("dep add: %v\n%s", err, out)
			}
		}
		// --force because the dependent is BLOCKED by the very anchor it
		// depends on, and close policy refuses that. This case is about the
		// count's status filter, not about the close gate: what it needs is a
		// dependent in a different stored status, and forcing is the shortest
		// honest way to get one without giving the edge a second meaning.
		if out, err := bdProxiedRun(t, bd, p.dir, "close", closed.ID, "--reason", "done", "--force"); err != nil {
			t.Fatalf("close: %v\n%s", err, out)
		}

		if got := *sp.countEdgesOf(t, anchor.ID, "&direction=in").Count; got != 3 {
			t.Errorf("unnarrowed in = %d, want 3", got)
		}
		if got := *sp.countEdgesOf(t, anchor.ID, "&direction=in&status=open").Count; got != 2 {
			t.Errorf("status=open in = %d, want 2 — the closed dependent's edge is narrowed out", got)
		}
		if got := *sp.countEdgesOf(t, anchor.ID, "&direction=in&status=closed").Count; got != 1 {
			t.Errorf("status=closed in = %d, want 1", got)
		}
		// An unrecognized status is not a refusal: it matches nothing and
		// counts 0, so a scripted caller counting a status its workspace has
		// since dropped keeps reading 0.
		if got := *sp.countEdgesOf(t, anchor.ID, "&direction=in&status=nosuchstatus").Count; got != 0 {
			t.Errorf("an unrecognized status counted %d, want 0 rather than a refusal", got)
		}

		// The asymmetry, over the wire: the ROLE raises it and the handler
		// names the member the caller has to move.
		resp, err := sp.client.Get(sp.url("/v0/beads/dependencies:count?issue_id=" + anchor.ID + "&direction=out&status=open"))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status beside direction=out: status = %d, want 400: %s", resp.StatusCode, raw)
		}
		var problem map[string]any
		if err := json.Unmarshal(raw, &problem); err != nil {
			t.Fatalf("decode problem %q: %v", raw, err)
		}
		if problem["code"] != "invalid_argument" || problem["param"] != "status" {
			t.Errorf("problem = %v, want invalid_argument on param status", problem)
		}
	})

	// A REPEATED ANCHOR COLLAPSES, against a real read rather than a fake's
	// bookkeeping. The role promises the collapse and the handler deliberately
	// does not do it, so this is the case that shows the promise is kept by
	// something.
	t.Run("a repeated anchor is answered once", func(t *testing.T) {
		anchor := bdProxiedCreate(t, bd, p.dir, "named twice", "-p", "2")
		dependent := bdProxiedCreate(t, bd, p.dir, "its dependent", "-p", "2")
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", dependent.ID, anchor.ID); err != nil {
			t.Fatalf("dep add: %v\n%s", err, out)
		}

		status, anchors := sp.countEdges(t, "?issue_id="+anchor.ID+"&issue_id="+anchor.ID+"&direction=in")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if len(anchors) != 1 {
			t.Fatalf("%d anchors for one id named twice, want 1", len(anchors))
		}
		if *anchors[0].Count != 1 {
			t.Errorf("count = %d, want 1 — a repeat must not count the same edges twice", *anchors[0].Count)
		}
	})
}
