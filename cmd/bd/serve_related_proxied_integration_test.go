//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
)

// End-to-end for the neighbor read, against real Dolt through a real
// `bd serve` subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// parameter projection, the envelope, the path bound and the two refusals. What
// only this level can prove is what the ROWS are, and on this operation that is
// most of the contract:
//
//   - the two directions really are the INVERSE graph of each other, which is
//     the whole reason `direction` is required rather than defaulted;
//   - the read really SPANS BOTH PLANES, in both halves — the neighbors are
//     collected from `dependencies` AND `wisp_dependencies`, and they are
//     hydrated from the `issues` AND `wisps` tables. A read that touched one of
//     each would answer a shorter list that looks entirely plausible, and no
//     fake can be asked about two tables;
//   - a wisp is a legal ANCHOR, not just a legal neighbor;
//   - an edge whose far end this database holds no row for is not a neighbor,
//     while the same edge IS a row on `GET /v0/beads/dependencies` — the one
//     observation that separates the two operations;
//   - an id that names nothing is a 404 here, where the batched reads report it
//     in the body.
//
// Every case scopes itself to issues this test creates, so the lists are exact
// whatever else the workspace holds.

// relatedNeighbors asks the server for one anchor's neighbors and returns the
// status and the decoded elements.
func (sp *serveProcess) relatedNeighbors(t *testing.T, id, query string) (int, []map[string]any) {
	t.Helper()
	resp, err := sp.client.Get(sp.url("/v0/beads/issues/" + id + "/related" + query))
	if err != nil {
		t.Fatalf("GET issues/%s/related%s: %v\nstderr:\n%s", id, query, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read related body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode related body %q: %v", raw, err)
	}
	if body.Items == nil {
		t.Errorf("GET issues/%s/related%s returned a null `items`; the document promises an array", id, query)
	}
	return resp.StatusCode, body.Items
}

// relatedIDs is the neighbor ids in the order the server answered, which is the
// order the role pins.
func (sp *serveProcess) relatedIDs(t *testing.T, id, query string) []string {
	t.Helper()
	status, items := sp.relatedNeighbors(t, id, query)
	if status != http.StatusOK {
		t.Fatalf("related %s%s: status = %d, want 200", id, query, status)
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		got, ok := item["id"].(string)
		if !ok {
			t.Fatalf("related %s: item %d has no id: %#v", id, i, item)
		}
		out = append(out, got)
	}
	return out
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestProxiedServerServeRelatedIssues(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvrelated")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE TWO DIRECTIONS ARE THE INVERSE GRAPH, and this is the shape that shows
	// it: a hub that depends on two issues and is depended on by three, so out
	// and in are disjoint sets of different sizes and no handler that answered
	// the wrong one could pass by coincidence. A symmetric fixture would have
	// made the required `direction` parameter unfalsifiable.
	t.Run("out and in answer disjoint neighbor sets", func(t *testing.T) {
		hub := bdProxiedCreate(t, bd, p.dir, "the hub", "-p", "2")
		var dependsOn, dependents []string
		for range 2 {
			dependsOn = append(dependsOn, bdProxiedCreate(t, bd, p.dir, "a dependency", "-p", "2").ID)
		}
		for range 3 {
			dependents = append(dependents, bdProxiedCreate(t, bd, p.dir, "a dependent", "-p", "2").ID)
		}
		// THE EDGES ARE SEEDED IN DESCENDING NEIGHBOR ID, which is what makes
		// the order assertion below falsifiable. bd mints ids from a hash rather
		// than a counter, so creation order and id order are unrelated — an
		// implementation answering in insertion order would coincide with the
		// pinned one about half the time on a two-element fixture. Sorting the
		// expectation and inserting against it removes the coin flip.
		slices.Sort(dependsOn)
		slices.Sort(dependents)
		for _, target := range slices.Backward(dependsOn) {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", hub.ID, target); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", hub.ID, target, err, out)
			}
		}
		for _, source := range slices.Backward(dependents) {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", source, hub.ID); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", source, hub.ID, err, out)
			}
		}

		// ASCENDING BY NEIGHBOR ID, which is the order the role pins and the
		// reverse of the order these edges were written in.
		if got := sp.relatedIDs(t, hub.ID, "?direction=out"); !equalIDs(got, dependsOn) {
			t.Errorf("out = %v, want %v — the issues the anchor DEPENDS ON, ascending by id", got, dependsOn)
		}
		if got := sp.relatedIDs(t, hub.ID, "?direction=in"); !equalIDs(got, dependents) {
			t.Errorf("in = %v, want %v — the issues that DEPEND ON the anchor, ascending by id", got, dependents)
		}

		// EACH ROW CARRIES ITS EDGE TYPE, which is the one member this element
		// adds to a plain issue and the reason the role is not a filtered
		// listing.
		_, items := sp.relatedNeighbors(t, hub.ID, "?direction=out")
		for i, item := range items {
			if item["dependency_type"] != "blocks" {
				t.Errorf("out item %d dependency_type = %#v, want the stored edge type", i, item["dependency_type"])
			}
		}

		// The type filter narrows EDGES and never the anchor: the hub's edges
		// are all `blocks`, so a filter for a type it has none of leaves the
		// anchor found with an empty list rather than answering 404.
		status, filtered := sp.relatedNeighbors(t, hub.ID, "?direction=out&type=discovered-from")
		if status != http.StatusOK {
			t.Fatalf("a type filter matching nothing: status = %d, want 200 — the filter narrows edges, not anchors", status)
		}
		if len(filtered) != 0 {
			t.Errorf("a type filter matching nothing returned %d neighbors, want none", len(filtered))
		}
	})

	// THE TWO PLANES, in BOTH halves, and this is the case a one-plane
	// implementation fails. An edge is routed to `dependencies` or
	// `wisp_dependencies` by the plane its SOURCE sits on, so a durable issue
	// depended on by a wisp has that inbound edge in the OTHER table — and the
	// neighbor itself lives in the OTHER issue table, so answering it also
	// requires hydrating from `wisps`.
	//
	// The `ephemeral` member is what proves the second half over the wire. A read
	// that collected both edge tables but hydrated only `issues` would answer the
	// durable dependent and silently drop the wisp, which is a shorter list with
	// nothing wrong-looking in it.
	t.Run("the neighbors span both planes", func(t *testing.T) {
		durable := bdProxiedCreate(t, bd, p.dir, "a durable anchor", "-p", "2")
		durableDependent := bdProxiedCreate(t, bd, p.dir, "a durable dependent", "-p", "2")
		wisp := bdProxiedCreate(t, bd, p.dir, "an ephemeral dependent", "-p", "2",
			"--ephemeral", "--wisp-type", "heartbeat")

		for _, source := range []string{durableDependent.ID, wisp.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", source, durable.ID); err != nil {
				t.Fatalf("dep add %s -> %s: %v\n%s", source, durable.ID, err, out)
			}
		}

		status, items := sp.relatedNeighbors(t, durable.ID, "?direction=in")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if len(items) != 2 {
			t.Fatalf("%d inbound neighbors, want 2 — one from `dependencies` and one from `wisp_dependencies`: %v", len(items), items)
		}
		seen := map[string]bool{}
		for _, item := range items {
			id, _ := item["id"].(string)
			seen[id] = true
			if id == wisp.ID {
				// Hydrated from the WISPS table, which is the half that would
				// still be missing if only the edge read spanned both planes.
				if ephemeral, _ := item["ephemeral"].(bool); !ephemeral {
					t.Errorf("the wisp neighbor came back without `ephemeral`; it was not hydrated from the ephemeral plane: %#v", item)
				}
			}
		}
		if !seen[durableDependent.ID] || !seen[wisp.ID] {
			t.Errorf("inbound neighbors = %v, want both %s (durable) and %s (wisp)", items, durableDependent.ID, wisp.ID)
		}

		// A WISP IS A LEGAL ANCHOR TOO. The anchor probe reads both planes, so an
		// ephemeral id resolves rather than 404ing, and its own outbound edge
		// reaches the durable issue it depends on.
		if got := sp.relatedIDs(t, wisp.ID, "?direction=out"); !equalIDs(got, []string{durable.ID}) {
			t.Errorf("the wisp anchor's out = %v, want %v — an ephemeral id is an anchor, not a miss", got, []string{durable.ID})
		}
	})

	// AN EDGE WITH NO FAR END IS NOT A NEIGHBOR, and it IS a row next door.
	// This is the single observation that separates this operation from
	// `GET /v0/beads/dependencies`, and it is why the length of `items` is a
	// neighbor count rather than an edge count.
	t.Run("an external target is a row next door and no neighbor here", func(t *testing.T) {
		anchor := bdProxiedCreate(t, bd, p.dir, "an anchor with a dangling edge", "-p", "2")
		resolvable := bdProxiedCreate(t, bd, p.dir, "a real dependency", "-p", "2")
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", anchor.ID, resolvable.ID); err != nil {
			t.Fatalf("dep add: %v\n%s", err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", anchor.ID, "external:jira:JIRA-4141"); err != nil {
			t.Fatalf("dep add external: %v\n%s", err, out)
		}

		if got := sp.relatedIDs(t, anchor.ID, "?direction=out"); !equalIDs(got, []string{resolvable.ID}) {
			t.Errorf("out = %v, want only %s — an edge with no far end in this database is not a neighbor", got, resolvable.ID)
		}
		// The same graph, read as rows: both edges are there, so the difference
		// is this operation's and not the fixture's.
		if rows := len(sp.storedEdges(t, anchor.ID)); rows != 2 {
			t.Errorf("GET /v0/beads/dependencies returned %d rows, want 2 — the dangling edge is stored", rows)
		}
	})

	// AN ABSENT ANCHOR IS A 404, which is where this operation parts company with
	// every batched read on this surface: there is one anchor, so there is no
	// other answer to preserve by reporting the miss in the body — and an empty
	// neighbor list is the common case, so a typo answered with one would never
	// surface.
	t.Run("an absent anchor is refused and an empty one is not", func(t *testing.T) {
		lonely := bdProxiedCreate(t, bd, p.dir, "an issue with no neighbors", "-p", "2")

		status, items := sp.relatedNeighbors(t, lonely.ID, "?direction=in")
		if status != http.StatusOK {
			t.Fatalf("an issue with no neighbors: status = %d, want 200", status)
		}
		if len(items) != 0 {
			t.Errorf("an issue with no neighbors returned %d, want an empty list", len(items))
		}

		if status, _ := sp.relatedNeighbors(t, "bd-nosuchbead", "?direction=in"); status != http.StatusNotFound {
			t.Errorf("an id that names nothing: status = %d, want 404", status)
		}
	})

	// THE ROLE'S REFUSAL, over the wire, with the parameter named. `direction`
	// has no default and the role refuses its zero value, so an omitted one is a
	// 400 rather than a walk in some direction the caller did not choose.
	t.Run("a missing direction is refused by name", func(t *testing.T) {
		anchor := bdProxiedCreate(t, bd, p.dir, "an anchor", "-p", "2")

		resp, err := sp.client.Get(sp.url("/v0/beads/issues/" + anchor.ID + "/related"))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("no direction: status = %d, want 400: %s", resp.StatusCode, raw)
		}
		var problem map[string]any
		if err := json.Unmarshal(raw, &problem); err != nil {
			t.Fatalf("decode problem %q: %v", raw, err)
		}
		if problem["code"] != "invalid_argument" || problem["param"] != "direction" {
			t.Errorf("problem = %v, want invalid_argument on param direction", problem)
		}
	})
}
