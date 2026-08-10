//go:build cgo

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the comment append, against real Dolt through a real
// `bd serve` subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// request projection, the response shape, the path bound and every refusal this
// edge owns. What only this level can prove is what the STORAGE TRANSACTION did,
// and on this operation that is most of the contract:
//
//   - the comment really lands on the thread and reads back from it, with the
//     text stored VERBATIM — newlines and surrounding space survive, which no
//     fake can be asked about because nothing in between is a column;
//   - A WISP IS A LEGAL TARGET. The comment lands on the ephemeral thread and
//     reads back from it, which requires the role to have resolved the plane and
//     written `wisp_comments`. An implementation that resolved only `issues`
//     answers 404 for an id the CLI comments on happily;
//   - the append is NOT idempotent: two identical requests are two rows, which
//     is a claim about the table rather than about the handler;
//   - an id that names neither plane is a 404 and nothing is written.
//
// Every case scopes itself to issues this test creates, so the threads are exact
// whatever else the workspace holds.

// comments reads one issue's thread back out of the database through the
// documented detail read. It is the read-back every assertion below is made
// against: what the thread holds after a write, not what the write said.
func (sp *serveProcess) comments(t *testing.T, id string) []map[string]any {
	t.Helper()
	status, body, _ := sp.get(t, "/v0/beads/issues/"+id+"?include_comments=true")
	if status != http.StatusOK {
		t.Fatalf("GET the thread of %s: status = %d: %v", id, status, body)
	}
	raw, ok := body["comments"].([]any)
	if !ok {
		// An issue with no comments omits the member, which is what
		// `comments_omitted` reports about. Callers here always expect rows.
		t.Fatalf("thread of %s: comments = %#v, want an array", id, body["comments"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("thread of %s: comment %d is %T, want an object", id, i, item)
		}
		out = append(out, row)
	}
	return out
}

func TestProxiedServerServeAddComment(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcomment")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE ROW IS WHAT THE RESPONSE SAID, and the read-back is what proves it.
	// The response's `id` and `created_at` are values the request never sent, so
	// this is the case that separates "the server answered plausibly" from "the
	// database holds this".
	t.Run("the comment lands and reads back verbatim", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "an issue with a thread", "-p", "2")

		// Newlines and surrounding space, because that is what a real comment
		// carries and because `text` is the one member on this surface that is
		// neither trimmed nor character-filtered.
		text := "  first line\nsecond line\n"
		raw, err := json.Marshal(map[string]string{"author": "alice", "text": text})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		status, body := sp.postJSON(t, "/v0/beads/issues/"+issue.ID+"/comments", string(raw))
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		id, _ := body["id"].(string)
		if id == "" {
			t.Fatalf("the response carries no id: %v", body)
		}
		if body["created_at"] == "" || body["created_at"] == nil {
			t.Errorf("the response carries no created_at: %v", body)
		}

		rows := sp.comments(t, issue.ID)
		if len(rows) != 1 {
			t.Fatalf("%d comments on the thread, want 1: %v", len(rows), rows)
		}
		if rows[0]["id"] != id {
			t.Errorf("the stored comment is %v, the response reported %q", rows[0]["id"], id)
		}
		if rows[0]["author"] != "alice" {
			t.Errorf("author = %v, want alice", rows[0]["author"])
		}
		if rows[0]["text"] != text {
			t.Errorf("text = %q, want %q — the column stores what was sent, untrimmed", rows[0]["text"], text)
		}
		if rows[0]["created_at"] != body["created_at"] {
			t.Errorf("created_at = %v, the response reported %v; the response must carry the STORED value",
				rows[0]["created_at"], body["created_at"])
		}
	})

	// A WISP IS A LEGAL TARGET, and this is the case a one-plane implementation
	// fails. The anchor resolves through the issue-then-wisp fallback and the row
	// lands in `wisp_comments`; a server that probed only `issues` would answer
	// 404 for an id `bd comment` writes to happily.
	t.Run("an ephemeral issue is a legal target", func(t *testing.T) {
		wisp := bdProxiedCreate(t, bd, p.dir, "an ephemeral thread", "-p", "2",
			"--ephemeral", "--wisp-type", "heartbeat")

		status, body := sp.postJSON(t, "/v0/beads/issues/"+wisp.ID+"/comments",
			`{"author":"alice","text":"a note on ephemeral work"}`)
		if status != http.StatusOK {
			t.Fatalf("commenting on a wisp: status = %d, want 200 — the anchor resolves across both planes: %v", status, body)
		}
		if body["issue_id"] != wisp.ID {
			t.Errorf("issue_id = %v, want the wisp's own id %q", body["issue_id"], wisp.ID)
		}

		// And it reads back from the EPHEMERAL thread, which is the half that
		// would still be missing if the anchor probe spanned both planes and the
		// insert wrote `comments`.
		rows := sp.comments(t, wisp.ID)
		if len(rows) != 1 || rows[0]["text"] != "a note on ephemeral work" {
			t.Fatalf("the wisp's thread = %v, want the one comment just written", rows)
		}
	})

	// NOT IDEMPOTENT, and the document says so rather than leaving it to be
	// discovered: two identical comments are a legitimate thread, so nothing here
	// can tell that pair from a retry. This is a claim about the table, which is
	// why it is asserted here and not on a fake.
	t.Run("a repeated request appends a second row", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "an issue commented on twice", "-p", "2")

		for range 2 {
			status, body := sp.postJSON(t, "/v0/beads/issues/"+issue.ID+"/comments",
				`{"author":"alice","text":"the same words"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %v", status, body)
			}
		}
		if rows := sp.comments(t, issue.ID); len(rows) != 2 {
			t.Errorf("%d comments after two identical appends, want 2", len(rows))
		}
	})

	// AN ABSENT ANCHOR IS A 404 and nothing is written — one anchor, so there is
	// no other answer to preserve by reporting the miss in the body.
	t.Run("an id that names nothing is refused", func(t *testing.T) {
		status, body := sp.postJSON(t, "/v0/beads/issues/bd-nosuchbead/comments",
			`{"author":"alice","text":"into the void"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}
	})

	// THE ROLE'S REFUSAL, over the wire, with the parameter named. Blankness is
	// judged on a trimmed copy by issueops.Commenter, not at this edge, so this
	// is the one case that shows the role's sentence reaching a client as the 400
	// it is rather than as a generic 500.
	t.Run("a blank body is refused by name and writes nothing", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "an issue nobody managed to comment on", "-p", "2")

		status, body := sp.postJSON(t, "/v0/beads/issues/"+issue.ID+"/comments",
			`{"author":"alice","text":"   \n  "}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_argument" || body["param"] != "text" {
			t.Errorf("problem = %v, want invalid_argument on param text", body)
		}
		if detail, _ := body["detail"].(string); !strings.Contains(detail, "empty") {
			t.Errorf("detail = %q, want the role's own sentence about empty text", detail)
		}

		// Nothing was written: the detail read omits `comments` entirely for a
		// thread with no rows, which is what `comments_omitted` reports about.
		status, detail, _ := sp.get(t, "/v0/beads/issues/"+issue.ID+"?include_comments=true")
		if status != http.StatusOK {
			t.Fatalf("read back: status = %d", status)
		}
		if rows, present := detail["comments"]; present && len(rows.([]any)) != 0 {
			t.Errorf("the refused comment was written: %v", rows)
		}
	})
}
