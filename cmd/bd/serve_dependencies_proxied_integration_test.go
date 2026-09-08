//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the dependency-graph writes, against real Dolt through a real
// `bd serve` subprocess. The pure tests in internal/httpapi cover the wire edge
// against a fake role; what only this level can prove is what the STORAGE
// TRANSACTION did — that a removal really removed, and that a refused batch
// left the graph exactly as it found it.

// postJSON posts body to path with the documented media type and returns the
// status and decoded body.
func (sp *serveProcess) postJSON(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url(path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %s body %q: %v", path, raw, err)
		}
	}
	return resp.StatusCode, m
}

// storedEdges reads the edges leaving id back out of the database through the
// documented stored-edge read. It is the read-back every assertion below is
// made against: what the graph holds after a write, not what the write said.
func (sp *serveProcess) storedEdges(t *testing.T, id string) []map[string]any {
	t.Helper()
	status, body, _ := sp.get(t, "/v0/beads/dependencies?issue_id="+id)
	if status != http.StatusOK {
		t.Fatalf("GET the stored edges of %s: status = %d: %v", id, status, body)
	}
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("stored edges of %s: items = %#v, want an array", id, body["items"])
	}
	edges := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		edge, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("stored edges of %s: item = %#v, want an object", id, item)
		}
		// The read is per-source, so an edge that names another source is a
		// read this test cannot reason about.
		if edge["issue_id"] == id {
			edges = append(edges, edge)
		}
	}
	return edges
}

func (sp *serveProcess) removeDependency(t *testing.T, issueID, dependsOnID, actor string) (int, map[string]any) {
	t.Helper()
	return sp.postJSON(t, "/v0/beads/dependencies:remove",
		fmt.Sprintf(`{"actor":%q,"issue_id":%q,"depends_on_id":%q}`, actor, issueID, dependsOnID))
}

func (sp *serveProcess) addDependencies(t *testing.T, actor string, edges ...string) (int, map[string]any) {
	t.Helper()
	return sp.postJSON(t, "/v0/beads/dependencies:add",
		fmt.Sprintf(`{"actor":%q,"edges":[%s]}`, actor, strings.Join(edges, ",")))
}

func edgeJSON(issueID, dependsOnID, edgeType string) string {
	return fmt.Sprintf(`{"issue_id":%q,"depends_on_id":%q,"type":%q}`, issueID, dependsOnID, edgeType)
}

func TestProxiedServerServeAddDependencies(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvdepadd")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE ATOMICITY PROOF, and the thing a fake role structurally cannot own.
	// A batch whose LATER edge closes a cycle is refused, and the earlier edges
	// — which were individually fine and really were written inside the
	// transaction — must be gone when the graph is read back. A handler that
	// forwarded the batch to a role committing per edge would pass every pure
	// test in internal/httpapi and leave half a graph here.
	t.Run("a mid-batch refusal leaves zero edges", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "atomicity a", "-p", "1")
		b := bdProxiedCreate(t, bd, p.dir, "atomicity b", "-p", "1")
		c := bdProxiedCreate(t, bd, p.dir, "atomicity c", "-p", "1")

		status, body := sp.addDependencies(t, "http-agent",
			edgeJSON(a.ID, b.ID, "blocks"),
			edgeJSON(b.ID, c.ID, "blocks"),
			// The closing edge. Nothing before it is wrong; the SET is.
			edgeJSON(c.ID, a.ID, "blocks"),
		)
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, body)
		}
		if body["code"] != "dependency_cycle" {
			t.Fatalf("code = %v, want dependency_cycle", body["code"])
		}
		if body["request_id"] == nil {
			t.Error("no request_id on the problem body")
		}

		for _, id := range []string{a.ID, b.ID, c.ID} {
			if edges := sp.storedEdges(t, id); len(edges) != 0 {
				t.Errorf("the refused batch left %d edge(s) on %s: %v — the request is all-or-nothing", len(edges), id, edges)
			}
		}
		// And the CLI reads the same empty graph through its own path.
		if out := bdProxiedDep(t, bd, p.dir, "list", a.ID); strings.Contains(out, b.ID) {
			t.Errorf("`bd dep list` shows an edge from the refused batch:\n%s", out)
		}
	})

	t.Run("every edge of an accepted batch lands", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "batch source", "-p", "1")
		b := bdProxiedCreate(t, bd, p.dir, "batch target", "-p", "1")
		c := bdProxiedCreate(t, bd, p.dir, "batch other target", "-p", "1")

		status, body := sp.addDependencies(t, "http-agent",
			edgeJSON(a.ID, b.ID, "blocks"),
			edgeJSON(a.ID, c.ID, "related"),
		)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		added, ok := body["added"].([]any)
		if !ok || len(added) != 2 {
			t.Fatalf("added = %#v, want the two requested edges", body["added"])
		}

		stored := map[string]string{}
		for _, edge := range sp.storedEdges(t, a.ID) {
			target, _ := edge["depends_on_id"].(string)
			edgeType, _ := edge["type"].(string)
			stored[target] = edgeType
		}
		if stored[b.ID] != "blocks" || stored[c.ID] != "related" {
			t.Errorf("the graph holds %v, want %s->blocks and %s->related", stored, b.ID, c.ID)
		}

		// A same-type re-add refuses nothing and writes nothing new.
		status, body = sp.addDependencies(t, "http-agent", edgeJSON(a.ID, b.ID, "blocks"))
		if status != http.StatusOK {
			t.Fatalf("idempotent re-add: status = %d, want 200: %v", status, body)
		}
		if edges := sp.storedEdges(t, a.ID); len(edges) != 2 {
			t.Errorf("the re-add changed the edge count: %v", edges)
		}
	})

	// The typed 409 the wire tests drive with a fabricated error, produced by
	// the real role against real Dolt: the members must arrive from the
	// transaction that refused, not from a fake that was told what to say.
	t.Run("a retype is dependency_exists carrying both types", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "retype source", "-p", "1")
		b := bdProxiedCreate(t, bd, p.dir, "retype target", "-p", "1")
		if status, body := sp.addDependencies(t, "http-agent", edgeJSON(a.ID, b.ID, "related")); status != http.StatusOK {
			t.Fatalf("seed: status = %d, want 200: %v", status, body)
		}

		status, body := sp.addDependencies(t, "http-agent", edgeJSON(a.ID, b.ID, "blocks"))
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, body)
		}
		if body["code"] != "dependency_exists" {
			t.Fatalf("code = %v, want dependency_exists", body["code"])
		}
		if body["existing_type"] != "related" || body["requested_type"] != "blocks" {
			t.Errorf("existing/requested = %v/%v, want related/blocks — read from the typed error, not the prose",
				body["existing_type"], body["requested_type"])
		}
		// The stored edge is untouched.
		edges := sp.storedEdges(t, a.ID)
		if len(edges) != 1 || edges[0]["type"] != "related" {
			t.Errorf("the refused retype changed the graph: %v", edges)
		}
	})

	// The hierarchy refusal, folded into dependency_cycle and distinguished by
	// the PRESENCE of its three members. Only a real store can build the
	// parent-child hierarchy the role walks.
	t.Run("a blocker that is an ancestor carries the hierarchy members", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "hierarchy parent", "-p", "1")
		child := bdProxiedCreate(t, bd, p.dir, "hierarchy child", "-p", "1")
		bdProxiedDep(t, bd, p.dir, "add", child.ID, parent.ID, "--type", "parent-child")

		status, body := sp.addDependencies(t, "http-agent", edgeJSON(child.ID, parent.ID, "blocks"))
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %v", status, body)
		}
		if body["code"] != "dependency_cycle" {
			t.Fatalf("code = %v, want dependency_cycle", body["code"])
		}
		if body["issue_id"] != child.ID || body["blocker_id"] != parent.ID {
			t.Errorf("issue_id/blocker_id = %v/%v, want %s/%s", body["issue_id"], body["blocker_id"], child.ID, parent.ID)
		}
		if body["blocker_is_ancestor"] != true {
			t.Errorf("blocker_is_ancestor = %v, want true — the blocker is the parent", body["blocker_is_ancestor"])
		}
	})

	// The endpoint refusal is NARROW, and only a real database can show where
	// the line falls: a target this database WOULD have held and does not is a
	// 400, while an id whose prefix belongs to another repository is a
	// legitimate external target and is accepted. A blanket unknown-target rule
	// would break every cross-repo edge, so both halves are pinned together.
	t.Run("a locally-absent target is a 400 and writes nothing", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "ghost endpoint source", "-p", "2")
		b := bdProxiedCreate(t, bd, p.dir, "ghost endpoint target", "-p", "2")

		status, body := sp.addDependencies(t, "http-agent",
			edgeJSON(a.ID, b.ID, "blocks"),
			edgeJSON(a.ID, p.prefix+"-nosuchissue", "blocks"),
		)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_argument" {
			t.Errorf("code = %v, want invalid_argument", body["code"])
		}
		// The refusal names the edge it is about, found by BOTH endpoints.
		if body["param"] != "edges[1].depends_on_id" {
			t.Errorf("param = %v, want edges[1].depends_on_id", body["param"])
		}
		if edges := sp.storedEdges(t, a.ID); len(edges) != 0 {
			t.Errorf("the refused batch left edges behind: %v", edges)
		}
	})

	t.Run("a ghost source is a 400 whatever its prefix", func(t *testing.T) {
		b := bdProxiedCreate(t, bd, p.dir, "ghost source target", "-p", "2")

		status, body := sp.addDependencies(t, "http-agent", edgeJSON(p.prefix+"-nosuchsource", b.ID, "blocks"))
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["param"] != "edges[0].issue_id" {
			t.Errorf("param = %v, want edges[0].issue_id — an edge follows its source", body["param"])
		}
	})

	t.Run("an external or foreign target is accepted", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "external target source", "-p", "2")

		status, body := sp.addDependencies(t, "http-agent",
			edgeJSON(a.ID, "external:JIRA-9", "blocks"),
			// Another repository's namespace: this database is not the one that
			// would have held it, so its absence here is not an absence.
			edgeJSON(a.ID, "otherrepo-9", "related"),
		)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if edges := sp.storedEdges(t, a.ID); len(edges) != 2 {
			t.Errorf("the open-set targets did not land: %v", edges)
		}
	})

	sp.shutdown(t)
}

func TestProxiedServerServeRemoveDependency(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvdeprm")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// The retained proof for this operation: the idempotent re-remove, read
	// back out of the database between the calls. A fake role can report
	// `removed: false` for any reason at all; only a real store proves that the
	// second call found nothing because the first call had already taken it.
	t.Run("the second removal finds nothing and changes nothing", func(t *testing.T) {
		source := bdProxiedCreate(t, bd, p.dir, "removal source", "-p", "1")
		target := bdProxiedCreate(t, bd, p.dir, "removal target", "-p", "1")
		bdProxiedDep(t, bd, p.dir, "add", source.ID, target.ID)

		if edges := sp.storedEdges(t, source.ID); len(edges) != 1 {
			t.Fatalf("the CLI-written edge is not in the graph: %v", edges)
		}

		status, body := sp.removeDependency(t, source.ID, target.ID, "http-agent")
		if status != http.StatusOK {
			t.Fatalf("first removal: status = %d, want 200: %v", status, body)
		}
		if body["removed"] != true {
			t.Errorf("first removal: removed = %v, want true", body["removed"])
		}
		if edges := sp.storedEdges(t, source.ID); len(edges) != 0 {
			t.Fatalf("the edge survived a removal that reported success: %v", edges)
		}
		// And the CLI reads the same graph through its own path.
		if out := bdProxiedDep(t, bd, p.dir, "list", source.ID); strings.Contains(out, target.ID) {
			t.Errorf("`bd dep list` still shows the removed edge:\n%s", out)
		}

		status, body = sp.removeDependency(t, source.ID, target.ID, "http-agent")
		if status != http.StatusOK {
			t.Fatalf("re-removal: status = %d, want 200 — a missing edge is not a refusal: %v", status, body)
		}
		if body["removed"] != false {
			t.Errorf("re-removal: removed = %v, want false", body["removed"])
		}
		if edges := sp.storedEdges(t, source.ID); len(edges) != 0 {
			t.Errorf("the re-removal changed the graph: %v", edges)
		}
	})

	// An endpoint id that names nothing is a 200 with `removed: false`, not a
	// 404. This is the operation's absent-code row proved against a real
	// database rather than against a fake that could not have looked.
	t.Run("an endpoint that names nothing is not a 404", func(t *testing.T) {
		source := bdProxiedCreate(t, bd, p.dir, "no edges at all", "-p", "2")

		status, body := sp.removeDependency(t, source.ID, "bd-nosuchissue", "http-agent")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["removed"] != false {
			t.Errorf("removed = %v, want false", body["removed"])
		}

		status, body = sp.removeDependency(t, "bd-nosuchissue", source.ID, "http-agent")
		if status != http.StatusOK {
			t.Fatalf("ghost source: status = %d, want 200: %v", status, body)
		}
		if body["removed"] != false {
			t.Errorf("ghost source: removed = %v, want false", body["removed"])
		}
	})

	t.Run("a refused request writes nothing", func(t *testing.T) {
		source := bdProxiedCreate(t, bd, p.dir, "refusals keep the edge", "-p", "2")
		target := bdProxiedCreate(t, bd, p.dir, "refusals keep the target", "-p", "2")
		bdProxiedDep(t, bd, p.dir, "add", source.ID, target.ID)

		for _, body := range []string{
			fmt.Sprintf(`{"actor":"   ","issue_id":%q,"depends_on_id":%q}`, source.ID, target.ID),
			fmt.Sprintf(`{"actor":"agent\nbd serve: forged","issue_id":%q,"depends_on_id":%q}`, source.ID, target.ID),
			fmt.Sprintf(`{"actor":"agent","issue_id":%q}`, source.ID),
			fmt.Sprintf(`{"actor":"agent","issue_id":%q,"depends_on_id":%q,"force":true}`, source.ID, target.ID),
		} {
			status, problem := sp.postJSON(t, "/v0/beads/dependencies:remove", body)
			if status != http.StatusBadRequest {
				t.Fatalf("body %.50q: status = %d, want 400: %v", body, status, problem)
			}
			if problem["code"] != "invalid_argument" {
				t.Errorf("body %.50q: code = %v, want invalid_argument", body, problem["code"])
			}
		}

		if edges := sp.storedEdges(t, source.ID); len(edges) != 1 {
			t.Errorf("a refused removal changed the graph: %v", edges)
		}
	})

	sp.shutdown(t)
}
