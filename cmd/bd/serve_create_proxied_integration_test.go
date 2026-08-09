//go:build cgo

package main

import (
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the single create, against real Dolt through a real `bd serve`
// subprocess. The pure tests in internal/httpapi cover the wire edge on a fake
// role and assert the projection onto issueops.CreateRequest member by member;
// what only this level can prove is that the projection LANDS — that the whole
// published vocabulary reaches the ROW, that the edges reach the GRAPH, and
// that the two members no fake can exercise at all do what the document says.
//
// Those two are the point of this file. `ephemeral` decides which TABLE the row
// is written to, so a fake role reporting "I was handed Ephemeral: true" proves
// nothing about where the write went — and `ephemeral` together with `metadata`
// and `sender` in ONE transaction is the shape a real programmatic caller
// composes. The occupied-id `409` is the other: `ErrAlreadyExists` comes from
// the create-only guard inside the transaction, so only a stored row can show
// the refusal is reachable rather than merely mapped.

// EVERY BODY BELOW SPELLS `issue_type`, and that is this workspace's rule
// rather than tidiness. The schema makes the member optional and the ROLE
// refuses an empty type — types.Issue.ValidateWithCustom rejects "" because it
// is neither built-in nor configured — so an absent one is a 400 carrying this
// workspace's own validation refusal. It is not this operation's rule either:
// every `issues:batchCreate` call in this repo's e2e tests spells
// `"issue_type":"task"` for the same reason. The document says so on the member.

// createIssue posts a create and returns the status and decoded body.
func (sp *serveProcess) createIssue(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return sp.postJSON(t, "/v0/beads/issues", body)
}

// storedEdgeTypes indexes the edges leaving id by their target, so an assertion
// names the pair it cares about rather than an index into a list whose order is
// the graph read's business.
func storedEdgeTypes(edges []map[string]any) map[string]string {
	byTarget := make(map[string]string, len(edges))
	for _, edge := range edges {
		target, _ := edge["depends_on_id"].(string)
		edgeType, _ := edge["type"].(string)
		byTarget[target] = edgeType
	}
	return byTarget
}

func TestProxiedServerServeCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcrt")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE WHOLE PUBLISHED VOCABULARY IN ONE REQUEST, read back out of the row
	// and out of the graph. A create is atomic, so a member that did not land
	// means the issue should not exist either — which is why the edges are read
	// through the documented stored-edge endpoint rather than trusted from the
	// response.
	t.Run("every published member lands in one transaction", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "the parent", "-p", "2")
		blocker := bdProxiedCreate(t, bd, p.dir, "the blocker", "-p", "2")

		const id = "srvcrt-wire1"
		status, body := sp.createIssue(t, `{
			"actor":"http-agent",
			"id":"`+id+`",
			"title":"the wire row",
			"description":"body",
			"design":"how",
			"acceptance_criteria":"done when",
			"notes":"scratch",
			"issue_type":"bug",
			"status":"in_progress",
			"priority":1,
			"assignee":"http-agent",
			"owner":"carol",
			"labels":["api","wire"],
			"estimated_minutes":30,
			"external_ref":"gh-9",
			"sender":"planner",
			"metadata":{"lane":"wire"},
			"parent_id":"`+parent.ID+`",
			"dependencies":[{"target_id":"`+blocker.ID+`","type":"blocks"}]
		}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}

		// The response is the STORED row: the document promises the id, the
		// status and the persisted timestamps, none of which the request
		// carried in the form the row holds them.
		if body["id"] != id {
			t.Errorf("response id = %v, want %s", body["id"], id)
		}
		if body["status"] != "in_progress" {
			t.Errorf("response status = %v, want in_progress", body["status"])
		}
		if body["created_at"] == nil || body["updated_at"] == nil {
			t.Errorf("the response omitted the persisted timestamps: %v", body)
		}

		stored := bdProxiedShow(t, bd, p.dir, id)
		for _, tc := range []struct {
			member    string
			got, want any
		}{
			{"title", stored.Title, "the wire row"},
			{"description", stored.Description, "body"},
			{"design", stored.Design, "how"},
			{"acceptance_criteria", stored.AcceptanceCriteria, "done when"},
			{"notes", stored.Notes, "scratch"},
			{"issue_type", string(stored.IssueType), "bug"},
			{"status", string(stored.Status), "in_progress"},
			{"priority", stored.Priority, 1},
			{"assignee", stored.Assignee, "http-agent"},
			{"owner", stored.Owner, "carol"},
			// `sender` is one of the five members issues:batchCreate cannot
			// spell, and the reason a message-shaped row cannot be created
			// through that operation at all.
			{"sender", stored.Sender, "planner"},
		} {
			if tc.got != tc.want {
				t.Errorf("stored %s = %v, want %v", tc.member, tc.got, tc.want)
			}
		}
		if stored.EstimatedMinutes == nil || *stored.EstimatedMinutes != 30 {
			t.Errorf("stored estimated_minutes = %v, want 30", stored.EstimatedMinutes)
		}
		if stored.ExternalRef == nil || *stored.ExternalRef != "gh-9" {
			t.Errorf("stored external_ref = %v, want gh-9", stored.ExternalRef)
		}
		if !strings.Contains(string(stored.Metadata), `"lane"`) {
			t.Errorf("stored metadata = %s, want the document the request sent", stored.Metadata)
		}
		if len(stored.Labels) != 2 {
			t.Errorf("stored labels = %v, want the authoritative two-element set", stored.Labels)
		}

		// The edges, out of the graph rather than out of the response. Both
		// were written in the create's own transaction: `parent_id` as a typed
		// parent-child edge and `dependencies` as the edge it names.
		edges := storedEdgeTypes(sp.storedEdges(t, id))
		if got := edges[parent.ID]; got != "parent-child" {
			t.Errorf("edge to the parent = %q, want parent-child (all edges: %v)", got, edges)
		}
		if got := edges[blocker.ID]; got != "blocks" {
			t.Errorf("edge to the blocker = %q, want blocks (all edges: %v)", got, edges)
		}
	})

	// THE CLAIM A PROGRAMMATIC CALLER HANGS OFF, and the one a fake role can
	// never make: `ephemeral` chooses the TABLE, so "the role was handed
	// Ephemeral: true" says nothing about where the row went. Sent together
	// with `metadata` and `sender` because that is the row such a caller
	// actually composes, and because all three land in one transaction or none
	// of them do.
	t.Run("an ephemeral create lands on the wisp plane carrying its metadata and sender", func(t *testing.T) {
		status, body := sp.createIssue(t, `{
			"actor":"http-agent",
			"title":"a wisp",
			"issue_type":"task",
			"ephemeral":true,
			"sender":"planner",
			"metadata":{"lane":"wisp"}
		}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["ephemeral"] != true {
			t.Errorf("response ephemeral = %v, want true", body["ephemeral"])
		}
		id, ok := body["id"].(string)
		if !ok || id == "" {
			t.Fatalf("the response carries no minted id: %v", body)
		}

		stored := bdProxiedShow(t, bd, p.dir, id)
		if !stored.Ephemeral {
			t.Errorf("stored ephemeral = false; the row did not land on the wisp plane")
		}
		if stored.NoHistory {
			t.Errorf("stored no_history = true; `ephemeral` and `no_history` are different retention modes")
		}
		if stored.Sender != "planner" {
			t.Errorf("stored sender = %q, want planner", stored.Sender)
		}
		if !strings.Contains(string(stored.Metadata), `"lane"`) {
			t.Errorf("stored metadata = %s, want the document the request sent", stored.Metadata)
		}
	})

	// THE 409, against a row that really exists. ErrAlreadyExists comes from
	// the create-only guard inside the transaction, so this is the one arm a
	// fake role can only assert is MAPPED — never that it is reachable.
	t.Run("an occupied explicit id is a 409 and overwrites nothing", func(t *testing.T) {
		const id = "srvcrt-taken"
		status, body := sp.createIssue(t,
			`{"actor":"http-agent","id":"`+id+`","issue_type":"task","title":"the first row","description":"untouched"}`)
		if status != http.StatusOK {
			t.Fatalf("first create: status = %d, want 200: %v", status, body)
		}

		conflictStatus, problem := sp.createIssue(t,
			`{"actor":"http-agent","id":"`+id+`","issue_type":"task","title":"the second row"}`)
		if conflictStatus != http.StatusConflict {
			t.Fatalf("second create: status = %d, want 409: %v", conflictStatus, problem)
		}
		if problem["code"] != "already_exists" {
			t.Errorf("code = %v, want already_exists", problem["code"])
		}
		if problem["param"] != "id" {
			t.Errorf("param = %v, want id", problem["param"])
		}

		// NEVER AN ADOPTION AND NEVER AN OVERWRITE: the refusal must leave the
		// stored row exactly as the first create left it.
		stored := bdProxiedShow(t, bd, p.dir, id)
		if stored.Title != "the first row" || stored.Description != "untouched" {
			t.Errorf("the refused create wrote to the stored row: %+v", stored)
		}
	})

	// A dependency target this workspace holds no row for is a refusal of the
	// BODY, not of a resource this request addressed — there is no id in the
	// path to have missed. Nothing is created, which is the half only a real
	// store can show.
	t.Run("a dependency target that names nothing is a 400 and creates nothing", func(t *testing.T) {
		const id = "srvcrt-dangling"
		status, problem := sp.createIssue(t,
			`{"actor":"http-agent","id":"`+id+`","issue_type":"task","title":"never created","dependencies":[{"target_id":"srvcrt-nosuchissue","type":"blocks"}]}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, problem)
		}
		if problem["code"] != "invalid_argument" {
			t.Errorf("code = %v, want invalid_argument", problem["code"])
		}
		if problem["param"] != "dependencies" {
			t.Errorf("param = %v, want dependencies", problem["param"])
		}
		if getStatus, _, _ := sp.get(t, "/v0/beads/issues/"+id); getStatus != http.StatusNotFound {
			t.Errorf("GET the refused id: status = %d, want 404 — a refused create must write no row", getStatus)
		}
	})
}
