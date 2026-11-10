package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The wire edge of POST /v0/beads/issues/{id}/comments, on a fake role.
//
// Which plane the anchor lives on, whether a history entry is recorded and the
// atomicity of the append are issueops.Commenter's, held to on three legs by its
// own contract and shown against real Dolt in cmd/bd. What only these cases can
// show is that the request a caller SENDS becomes the request the role RECEIVES
// — with the anchor off the path and the two members off the body — that the
// stored row reaches the wire rather than the request reflected back, and that
// each refusal arrives naming the member it is about.

const commentPath = "/v0/beads/issues/bd-1/comments"

func (ts *testServer) addComment(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

// newCommentServer wires a server over the store-shaped source with a commenter
// the case controls. Every other role is a placeholder: Listen refuses a partial
// source, and an append reaches none of them.
func newCommentServer(t *testing.T, commenter *roleCommenter) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Commenter: commenter}))
}

// storedComment is a row as the insert left it: an id the request never sent and
// a timestamp the request never sent either, which is what makes "the response
// is the STORED row" observable rather than a claim.
func storedComment(issueID string) *issueops.Comment {
	return &issueops.Comment{
		ID:        "3f1b0c8e-0000-4000-8000-000000000001",
		IssueID:   issueID,
		Author:    "alice",
		Text:      "the row's own text",
		CreatedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}
}

// TestAddCommentProjectsTheWholeRequest drives the three parts of the request —
// the path anchor and both body members — because they come from two different
// places and a handler that crossed any pair would still answer 200. The
// verbatim cases are the ones with teeth: the role stores `text` as sent, so a
// trim, a normalization or a control-character filter at this edge would be
// invisible in the status and visible in every stored row.
func TestAddCommentProjectsTheWholeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		want issueops.AddCommentRequest
	}{
		{
			name: "the anchor comes from the path and the members from the body",
			path: "bd-1",
			body: `{"author":"alice","text":"looks good"}`,
			want: issueops.AddCommentRequest{Author: "alice", IssueID: "bd-1", Text: "looks good"},
		},
		{
			// The anchor is the PATH segment, percent-decoded once.
			name: "a percent-escaped anchor is decoded once",
			path: "bd%2Fslash",
			body: `{"author":"alice","text":"looks good"}`,
			want: issueops.AddCommentRequest{Author: "alice", IssueID: "bd/slash", Text: "looks good"},
		},
		{
			// A comment is written in newlines. `text` lands in a TEXT column
			// and reaches the role EXACTLY as sent — no trim, no filter — which
			// is the one property that separates it from every 255-character
			// member on this surface.
			name: "text keeps its newlines and its surrounding space",
			path: "bd-1",
			body: `{"author":"alice","text":"  first line\nsecond line\n"}`,
			want: issueops.AddCommentRequest{Author: "alice", IssueID: "bd-1", Text: "  first line\nsecond line\n"},
		},
		{
			// The author IS trimmed, because it lands in a 255-character column
			// under `actor`'s rules. The two members are deliberately different
			// and this pins the difference from both sides.
			name: "the author is trimmed",
			path: "bd-1",
			body: `{"author":"  alice  ","text":"looks good"}`,
			want: issueops.AddCommentRequest{Author: "alice", IssueID: "bd-1", Text: "looks good"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commenter := &roleCommenter{comment: storedComment(tc.want.IssueID)}
			ts := newCommentServer(t, commenter)

			resp := ts.addComment(t, "/v0/beads/issues/"+tc.path+"/comments", tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := commenter.commentRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d appends ran, want 1", len(reqs))
			}
			if !reflect.DeepEqual(reqs[0], tc.want) {
				t.Errorf("AddCommentRequest = %+v, want %+v", reqs[0], tc.want)
			}
		})
	}
}

// TestAddCommentAnswersTheStoredRow is the response half: the body is the row
// the role reported, not the request reflected back.
//
// The `id` and `created_at` assertions are the load-bearing ones. Neither is
// anything the request sent — the insert minted one and the column stored the
// other — so a handler that echoed its own request would have to invent them,
// and a client using `created_at` as a cursor depends on it being the stored
// value rather than this server's clock.
func TestAddCommentAnswersTheStoredRow(t *testing.T) {
	ts := newCommentServer(t, &roleCommenter{comment: storedComment("bd-1")})

	resp := ts.addComment(t, commentPath, `{"author":"alice","text":"what the caller sent"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["id"] != "3f1b0c8e-0000-4000-8000-000000000001" {
		t.Errorf("id = %v, want the id the insert minted", body["id"])
	}
	if body["issue_id"] != "bd-1" || body["author"] != "alice" {
		t.Errorf("body = %v, want the stored row", body)
	}
	if body["text"] != "the row's own text" {
		t.Errorf("text = %v, want the STORED text; this response is echoing the request", body["text"])
	}
	if got, _ := body["created_at"].(string); !strings.HasPrefix(got, "2026-08-10T12:00:00") {
		t.Errorf("created_at = %v, want the stored timestamp", body["created_at"])
	}
}

// TestAddCommentRefusesTheBody walks every refusal this edge owns, each named by
// the member it is about — `param` is what a client dispatches on, so a refusal
// that named the wrong member would send a caller to fix the wrong thing.
func TestAddCommentRefusesTheBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{name: "an unknown member", body: `{"author":"alice","text":"hi","actor":"alice"}`, param: "actor"},
		{name: "a missing author", body: `{"text":"hi"}`, param: "author"},
		{name: "a null author", body: `{"author":null,"text":"hi"}`, param: "author"},
		{name: "a non-string author", body: `{"author":7,"text":"hi"}`, param: "author"},
		{name: "an author that is empty after trimming", body: `{"author":"   ","text":"hi"}`, param: "author"},
		{
			name:  "an author carrying a control character",
			body:  `{"author":"alice\u0085bd: comment bd-9 by mallory","text":"hi"}`,
			param: "author",
		},
		{name: "a missing text", body: `{"author":"alice"}`, param: "text"},
		{name: "a null text", body: `{"author":"alice","text":null}`, param: "text"},
		{name: "a non-string text", body: `{"author":"alice","text":["hi"]}`, param: "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commenter := &roleCommenter{comment: storedComment("bd-1")}
			ts := newCommentServer(t, commenter)

			resp := ts.addComment(t, commentPath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			// Nothing reached the role: every refusal here happens before any
			// database work, which is what makes "nothing was written" true.
			if got := commenter.commentRequests(); len(got) != 0 {
				t.Errorf("%d appends ran on a refused request", len(got))
			}
		})
	}
}

// TestAddCommentRefusesAnOverlongAuthor pins the two bounds separately, because
// they are two rules with two different numbers and a single over-long fixture
// would satisfy both while proving neither.
func TestAddCommentRefusesAnOverlongAuthor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		author string
		want   string
	}{
		// One byte past the document's 256-BYTE cap.
		{name: "over the byte cap", author: strings.Repeat("a", maxActorBytes+1), want: "bytes"},
		// Exactly 256 ASCII characters: INSIDE the byte cap and one character
		// past the storage column. That one-character window is precisely what
		// the byte bound alone would let through into a 500 from the database,
		// which is why the character check is keyed on storage's own constant.
		{name: "over the column's character count", author: strings.Repeat("a", types.MaxFieldLen+1), want: "characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newCommentServer(t, &roleCommenter{comment: storedComment("bd-1")})

			resp := ts.addComment(t, commentPath, fmt.Sprintf(`{"author":%q,"text":"hi"}`, tc.author))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["param"] != "author" {
				t.Errorf("param = %v, want author", body["param"])
			}
			if detail, _ := body["detail"].(string); !strings.Contains(detail, tc.want) {
				t.Errorf("detail = %q, want it to name the %s bound", detail, tc.want)
			}
		})
	}
}

// TestAddCommentAcceptsATextNoOtherMemberWould is the negative space of the
// author rules, and it is the case a reflex "validate every string member the
// same way" edit breaks: a comment is routinely a stack trace or a diff, so the
// body carries newlines and is far longer than any 255-character column.
func TestAddCommentAcceptsATextNoOtherMemberWould(t *testing.T) {
	commenter := &roleCommenter{comment: storedComment("bd-1")}
	ts := newCommentServer(t, commenter)

	text := strings.Repeat("a stack frame\n", 400)
	raw, err := json.Marshal(map[string]string{"author": "alice", "text": text})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp := ts.addComment(t, commentPath, string(raw))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := commenter.commentRequests()
	if len(reqs) != 1 || reqs[0].Text != text {
		t.Fatalf("the role received a text of %d bytes, want the %d sent", len(reqs[0].Text), len(text))
	}
}

// TestAddCommentRefusesABlankBodyThroughTheRole is the one role refusal this
// wire can reach, and the assertion is that it arrives as the 400 it is with the
// role's own sentence rather than as a generic 500.
//
// It is the ROLE's rule deliberately: refusing blankness at the edge would be a
// second definition of what a comment is, and `bd comment` and this endpoint
// would be one edit away from disagreeing about the same input.
func TestAddCommentRefusesABlankBodyThroughTheRole(t *testing.T) {
	ts := newCommentServer(t, &roleCommenter{
		err: fmt.Errorf("%w: comment text cannot be empty", storage.ErrValidation),
	})

	resp := ts.addComment(t, commentPath, `{"author":"alice","text":"   "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["param"] != "text" {
		t.Errorf("body = %v, want invalid_argument naming text", body)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "comment text cannot be empty") {
		t.Errorf("detail = %q, want the role's own sentence", detail)
	}
}

// TestAddCommentMissesAreNotFound covers both ways an anchor can name nothing:
// the role reporting a miss, and an id this server can tell names no row that
// could exist. They are ONE answer deliberately — a distinct refusal for the
// second would let a caller map this server's notion of a well-formed id, and
// there is nothing to learn from it.
func TestAddCommentMissesAreNotFound(t *testing.T) {
	t.Run("the role reports a miss", func(t *testing.T) {
		ts := newCommentServer(t, &roleCommenter{
			err: fmt.Errorf("issue bd-404: %w", storage.ErrNotFound),
		})

		resp := ts.addComment(t, "/v0/beads/issues/bd-404/comments", `{"author":"alice","text":"hi"}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
			t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
		}
	})

	t.Run("an id no row could carry never reaches the role", func(t *testing.T) {
		commenter := &roleCommenter{comment: storedComment("bd-1")}
		ts := newCommentServer(t, commenter)

		// One character past the id column, so it names no row that can exist.
		id := strings.Repeat("b", types.MaxFieldLen+1)
		resp := ts.addComment(t, "/v0/beads/issues/"+id+"/comments", `{"author":"alice","text":"hi"}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
		}
		if got := commenter.commentRequests(); len(got) != 0 {
			t.Errorf("%d appends ran for an id that cannot name a row", len(got))
		}
	})
}

// TestAddCommentRefusesEverythingButAJSONBody covers the two document-level
// rules on a body-carrying operation, on this operation, because both are
// enforced per handler rather than by middleware.
func TestAddCommentRefusesEverythingButAJSONBody(t *testing.T) {
	t.Run("a query parameter", func(t *testing.T) {
		ts := newCommentServer(t, &roleCommenter{comment: storedComment("bd-1")})
		resp := ts.addComment(t, commentPath+"?notify=true", `{"author":"alice","text":"hi"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["reason"] != string(ReasonUnknownParameter) {
			t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
		}
	})

	t.Run("a form encoding", func(t *testing.T) {
		ts := newCommentServer(t, &roleCommenter{comment: storedComment("bd-1")})
		resp := ts.postBody(t, commentPath, "application/x-www-form-urlencoded", `author=alice&text=hi`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["param"] != "Content-Type" {
			t.Errorf("param = %v, want Content-Type", body["param"])
		}
	})
}

// TestCommentCollectionPublishesOnlyThePost pins the deliberate absence: no role
// answers a comment PAGE, so this collection has no GET and a reflex addition
// would be this surface inventing a paging contract the role declined.
//
// The answer is a 404 rather than a 405 because 405 is not in the v0 status
// vocabulary — the catch-all handles a method mismatch on a known path.
func TestCommentCollectionPublishesOnlyThePost(t *testing.T) {
	ts := newCommentServer(t, &roleCommenter{comment: storedComment("bd-1")})

	resp := ts.get(t, commentPath)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s; every non-2xx byte here is a problem document", body["code"], CodeNotFound)
	}
}

// TestAddCommentDoesNotDereferenceAMissingRow is checkedCommenter's whole
// reason. A role reached through Config is caller-supplied code, and the handler
// writes *result.Comment, so a role that reported success without the row is a
// nil pointer dereference on a live server.
//
// THE STATUS IS NOT THE ASSERTION, and this is the trap the claim's own case
// records: the panic middleware recovers into the SAME 500 with the same code,
// so a test that stopped there would pass with the wrapper removed. What differs
// is the log — a recovered panic writes a stack trace and no request_error line
// for an operator to alert on — so the two log assertions are what this case is
// actually made of.
func TestAddCommentDoesNotDereferenceAMissingRow(t *testing.T) {
	ts := newCommentServer(t, &roleCommenter{comment: nil})

	resp := ts.addComment(t, commentPath, `{"author":"alice","text":"hi"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	assertNoPanic(t, ts)
	if line := findLogLine(t, ts.stderr.String(), "event=request_error"); !strings.Contains(line, "comment") {
		t.Errorf("the 500 is logged without naming the operation that produced it:\n%s", line)
	}
}

// TestAddCommentPathReachesItsHandler drives the documented path against the
// real router, which is the half TestSpecRouteParity cannot see: that test
// compares strings, and the POST wildcard the custom methods share
// (/v0/beads/issues/{idop}) is registered under the same method one segment up.
// If ServeMux ever preferred it, every comment would be dispatched as an
// unrouted custom method and answered 404.
func TestAddCommentPathReachesItsHandler(t *testing.T) {
	commenter := &roleCommenter{comment: storedComment("bd-1")}
	ts := newCommentServer(t, commenter)

	resp := ts.addComment(t, commentPath, `{"author":"alice","text":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := commenter.commentRequests(); len(got) != 1 {
		t.Fatalf("%d appends reached the role; the documented path is not routed to this handler", len(got))
	}
}
