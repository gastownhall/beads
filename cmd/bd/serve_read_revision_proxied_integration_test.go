//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
)

// End-to-end for the READ-SIDE revision token, against real Dolt through a real
// `bd serve` subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role: that
// the row's token arrives under `revision`, that a legacy zero is emitted
// rather than omitted, and that the list rows carry no token at all. What only
// this level can prove is the thing the member exists FOR — that the token a
// read hands out is the same token a guard is checked against, in the same
// database, through the same engine that mints it.
//
// A fake cannot state that. It answers whatever it was seeded with, so a
// handler that published a plausible-but-wrong number — the wisp plane's token
// for a durable row, a token read outside the transaction that hydrated the
// row, a value narrowed through a float64 somewhere in the middle — would
// satisfy every unit test in the package and refuse every guard composed from
// it on a live server.
//
// So the shape here is the LOOP, not the member: read, guard with what the read
// said, watch it land; let another actor move the row, guard with the same
// stale token, watch it refuse. Both halves are needed. A token that always
// matched would pass the first half alone, and a token that never matched would
// pass the second.

// getIssueRaw fetches one detail view and returns the status and the UNDECODED
// body, so a caller can read `revision` as the 64-bit integer the document
// declares rather than through encoding/json's float64 default for `any`. See
// revisionOf, which does exactly that and says why.
func (sp *serveProcess) getIssueRaw(t *testing.T, id string) (int, []byte) {
	t.Helper()
	resp, err := sp.client.Get(sp.url("/v0/beads/issues/" + id))
	if err != nil {
		t.Fatalf("GET issues/%s: %v\nstderr:\n%s", id, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read detail body: %v", err)
	}
	return resp.StatusCode, raw
}

func TestProxiedServerServeReadRevision(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvrev")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE LOOP THE MEMBER EXISTS FOR. Before this token was published, the
	// first guarded write of any read-modify-write chain had to be preceded by
	// a write the caller did not want to make, because only writes answered
	// with a token. This is that chain with a READ at the front of it.
	t.Run("a token read off the detail view guards the next write", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "guarded from a read", "-p", "2")

		status, raw := sp.getIssueRaw(t, issue.ID)
		if status != http.StatusOK {
			t.Fatalf("GET: status = %d, want 200: %s", status, raw)
		}
		read := revisionOf(t, raw)
		// A created row is stamped with a fresh token, so a zero here would
		// mean the handler published a field it never filled in rather than
		// the row's value — the one failure that a legacy-zero row makes
		// otherwise indistinguishable from success.
		if read == 0 {
			t.Fatalf("revision = 0 on a row this test just created; the token was not read off the row: %s", raw)
		}

		// The guard the read sourced. It lands, which is half the claim.
		writeStatus, written := sp.updateIssueRaw(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(read, 10)+`,"patch":{"title":"guarded by a read"}}`)
		if writeStatus != http.StatusOK {
			t.Fatalf("guarded write: status = %d, want 200: %s", writeStatus, written)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title != "guarded by a read" {
			t.Errorf("title = %q, want the guarded write to have landed", shown.Title)
		}

		// AND THE READ AGREES WITH THE WRITE. The document says this member is
		// the one the write responses' tokens agree with; a read that answered
		// from a different source — a stale cache, the other plane's row —
		// would still have passed the guard above with its FIRST value and
		// diverged from here on.
		wrote := revisionOf(t, written)
		if wrote == read {
			t.Fatalf("revision = %d after a write; a write that does not move the token makes every guard vacuous", wrote)
		}
		reStatus, reRaw := sp.getIssueRaw(t, issue.ID)
		if reStatus != http.StatusOK {
			t.Fatalf("re-read: status = %d, want 200: %s", reStatus, reRaw)
		}
		if reRead := revisionOf(t, reRaw); reRead != wrote {
			t.Errorf("the read answers %d and the write answered %d; the two must be one token", reRead, wrote)
		}

		// THE OTHER HALF: a concurrent actor moves the row, and the token the
		// caller is still holding is now genuinely stale. This is the only way
		// to make a stale one — the token is opaque and compared for equality
		// alone, so there is no arithmetic that produces a wrong value on
		// purpose.
		otherStatus, other := sp.updateIssueRaw(t, issue.ID,
			`{"actor":"other-agent","patch":{"notes":"a concurrent edit"}}`)
		if otherStatus != http.StatusOK {
			t.Fatalf("the concurrent write: status = %d, want 200: %s", otherStatus, other)
		}

		conflictStatus, problem := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(wrote, 10)+`,"patch":{"title":"never"}}`)
		if conflictStatus != http.StatusConflict {
			t.Fatalf("stale guard: status = %d, want 409: %v", conflictStatus, problem)
		}
		if problem["code"] != "precondition_failed" {
			t.Errorf("code = %v, want precondition_failed", problem["code"])
		}
		if problem["param"] != "expected_version" {
			t.Errorf("param = %v, want expected_version", problem["param"])
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title == "never" {
			t.Error("a refused guard wrote to the row")
		}

		// And the loop closes the way the document says it does: re-read, and
		// the same guard that just refused now lands.
		freshStatus, freshRaw := sp.getIssueRaw(t, issue.ID)
		if freshStatus != http.StatusOK {
			t.Fatalf("re-read after the conflict: status = %d, want 200: %s", freshStatus, freshRaw)
		}
		fresh := revisionOf(t, freshRaw)
		retryStatus, retry := sp.updateIssue(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(fresh, 10)+`,"patch":{"title":"retried"}}`)
		if retryStatus != http.StatusOK {
			t.Fatalf("the retry: status = %d, want 200: %v", retryStatus, retry)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); shown.Title != "retried" {
			t.Errorf("title = %q, want the re-read guard to have landed", shown.Title)
		}
	})

	// THE OTHER GUARDED VERBS read from the same source, and this is where that
	// is checked rather than assumed. `close` and `delete` each carry their own
	// `expected_version` against their own precondition path, and the document
	// now points all of them at this one read; a token that was right for the
	// patch path and wrong for the close path would be a member that works
	// until the first caller composes the chain the spec describes.
	t.Run("the read's token guards a close", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "closed under a read guard", "-p", "2")

		status, raw := sp.getIssueRaw(t, issue.ID)
		if status != http.StatusOK {
			t.Fatalf("GET: status = %d, want 200: %s", status, raw)
		}
		read := revisionOf(t, raw)

		closeStatus, closed := sp.closeIssueRaw(t, issue.ID,
			`{"actor":"http-agent","expected_version":`+strconv.FormatInt(read, 10)+`}`)
		if closeStatus != http.StatusOK {
			t.Fatalf("guarded close: status = %d, want 200: %s", closeStatus, closed)
		}
		if shown := bdProxiedShow(t, bd, p.dir, issue.ID); string(shown.Status) != "closed" {
			t.Errorf("status = %q, want the guarded close to have landed", shown.Status)
		}

		// The token the close answered with is what the read now says too,
		// which is the whole of "the read and the writes are one token" on the
		// lifecycle path.
		afterStatus, afterRaw := sp.getIssueRaw(t, issue.ID)
		if afterStatus != http.StatusOK {
			t.Fatalf("read after close: status = %d, want 200: %s", afterStatus, afterRaw)
		}
		if got, want := revisionOf(t, afterRaw), revisionOf(t, closed); got != want {
			t.Errorf("the read answers %d and the close answered %d; the two must be one token", got, want)
		}
	})

	// THE WISP PLANE. The detail read falls back to the wisps table when no
	// issue matches, and the token there is a DIFFERENT column on a DIFFERENT
	// table. The operation promises the member on every 200, so the fallback
	// path has to carry it too — and the way this goes wrong is not an absent
	// member but a zero one, because the wisps row_lock was added by the same
	// migration and a lookup that read the issues column would find nothing and
	// leave the field at its zero.
	t.Run("an ephemeral row carries its own token", func(t *testing.T) {
		id := bdProxiedCreateSilent(t, bd, p.dir, "an ephemeral row", "-p", "2", "--ephemeral")

		status, raw := sp.getIssueRaw(t, id)
		if status != http.StatusOK {
			t.Fatalf("GET wisp: status = %d, want 200: %s", status, raw)
		}
		var body struct {
			ID        string `json:"id"`
			Ephemeral bool   `json:"ephemeral"`
			Revision  *int64 `json:"revision"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode wisp detail %q: %v", raw, err)
		}
		if !body.Ephemeral {
			t.Fatalf("the fallback lookup did not answer with the wisp: %s", raw)
		}
		if body.Revision == nil {
			t.Fatalf("the wisp detail carries no `revision`: %s", raw)
		}
		if *body.Revision == 0 {
			t.Errorf("revision = 0 on a wisp this test just created; the fallback read the wrong plane's token: %s", raw)
		}
	})
}
