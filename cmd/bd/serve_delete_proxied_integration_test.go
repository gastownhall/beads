//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// End-to-end for the DELETE's row-version guard, against real Dolt through a
// real `bd serve` subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// arity rule, the member projection, the 409's shape. What only this level can
// prove is the part that matters most on the one operation whose mistakes are
// irreversible: that a stale token really refuses against a store and the rows
// are STILL THERE afterwards, and that neither `force` nor `cascade` walks past
// it. A fake can report a refusal; only a real row can show nothing was erased.
//
// The role-level guard is owned against real backends by the deleter contract
// (backend/conformance/deleter_contract.go); this test owns the wire-to-role
// seam and cites that one rather than duplicating it.

// deleteIssues posts a delete and returns the status and the UNDECODED body.
func (sp *serveProcess) deleteIssues(t *testing.T, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, sp.url("/v0/beads/issues:delete"), strings.NewReader(body))
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("POST issues:delete: %v\nstderr:\n%s", err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read delete body: %v", err)
	}
	return resp.StatusCode, raw
}

// deleteProblem decodes a refusal body.
func deleteProblem(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode delete problem %q: %v", raw, err)
	}
	return m
}

// stillThere fails unless the bead is readable, which is the assertion every
// refusal case here ends on. A delete that refused and erased anyway is the
// failure this whole slice exists to make impossible.
func stillThere(t *testing.T, bd, dir, id string) {
	t.Helper()
	if shown := bdProxiedShow(t, bd, dir, id); shown.ID != id {
		t.Fatalf("bd show %s answered with %q; the refused delete erased it", id, shown.ID)
	}
}

func TestProxiedServerServeDelete(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvdel")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// The guard's whole loop on the operation where being wrong cannot be
	// undone: a token minted by a write, invalidated by another actor, refused
	// while the bead survives, and then honored.
	t.Run("a stale guard refuses and the bead survives", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "guarded delete", "-p", "2")

		status, raw := sp.updateIssueRaw(t, issue.ID, `{"actor":"http-agent","patch":{"notes":"seeded"}}`)
		if status != http.StatusOK {
			t.Fatalf("the seeding write: status = %d, want 200: %s", status, raw)
		}
		first := revisionOf(t, raw)

		status, raw = sp.updateIssueRaw(t, issue.ID, `{"actor":"other-agent","patch":{"notes":"moved"}}`)
		if status != http.StatusOK {
			t.Fatalf("the concurrent write: status = %d, want 200: %s", status, raw)
		}
		second := revisionOf(t, raw)
		if second == first {
			t.Fatalf("revision = %d after a second write; a token that does not move makes the guard vacuous", second)
		}

		status, raw = sp.deleteIssues(t, `{"ids":["`+issue.ID+`"],"actor":"http-agent","expected_version":`+strconv.FormatInt(first, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("stale delete: status = %d, want 409: %s", status, raw)
		}
		problem := deleteProblem(t, raw)
		if problem["code"] != "precondition_failed" || problem["param"] != "expected_version" {
			t.Errorf("stale delete: code = %v param = %v, want precondition_failed / expected_version",
				problem["code"], problem["param"])
		}
		stillThere(t, bd, p.dir, issue.ID)

		// FORCE AND CASCADE BYPASS POLICY, NEVER THE PRECONDITION. Both say what
		// to do about OTHER beads; neither says the bead named is still the one
		// the caller read.
		for _, flags := range []string{`"force":true`, `"cascade":true`, `"force":true,"cascade":true`} {
			status, raw = sp.deleteIssues(t, `{"ids":["`+issue.ID+`"],`+flags+`,"expected_version":`+strconv.FormatInt(first, 10)+`}`)
			if status != http.StatusConflict {
				t.Fatalf("stale delete with %s: status = %d, want 409: %s", flags, status, raw)
			}
			stillThere(t, bd, p.dir, issue.ID)
		}

		// A DRY RUN REFUSES WHERE THE REAL RUN WOULD, which is the whole value
		// of previewing a guarded delete: a clean preview means the real request
		// will not stop half-explained.
		status, raw = sp.deleteIssues(t, `{"ids":["`+issue.ID+`"],"dry_run":true,"expected_version":`+strconv.FormatInt(first, 10)+`}`)
		if status != http.StatusConflict {
			t.Fatalf("stale dry run: status = %d, want 409: %s", status, raw)
		}
		stillThere(t, bd, p.dir, issue.ID)

		// And the fresh token lands.
		status, raw = sp.deleteIssues(t, `{"ids":["`+issue.ID+`"],"actor":"http-agent","expected_version":`+strconv.FormatInt(second, 10)+`}`)
		if status != http.StatusOK {
			t.Fatalf("guarded delete: status = %d, want 200: %s", status, raw)
		}
		var result struct {
			Deleted int `json:"deleted"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode delete result %q: %v", raw, err)
		}
		if result.Deleted != 1 {
			t.Errorf("deleted = %d, want 1: %s", result.Deleted, raw)
		}
		bdProxiedShowFail(t, bd, p.dir, issue.ID, "--json")
	})

	// The arity rule, over the wire and against a store: refused before
	// anything is read, so BOTH beads are still there. The role refuses the
	// same pair, but with no `param` — the member name is what this refusal
	// adds, and it is the whole recovery.
	t.Run("a guard beside two beads is refused and neither is erased", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "arity a", "-p", "2")
		b := bdProxiedCreate(t, bd, p.dir, "arity b", "-p", "2")

		status, raw := sp.deleteIssues(t, `{"ids":["`+a.ID+`","`+b.ID+`"],"expected_version":41}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", status, raw)
		}
		problem := deleteProblem(t, raw)
		if problem["code"] != "invalid_argument" || problem["param"] != "expected_version" {
			t.Errorf("code = %v param = %v, want invalid_argument / expected_version", problem["code"], problem["param"])
		}
		stillThere(t, bd, p.dir, a.ID)
		stillThere(t, bd, p.dir, b.ID)

		// The same two ids WITHOUT the guard are an ordinary delete, which is
		// what makes the refusal above a statement about the guard rather than
		// about the list.
		status, raw = sp.deleteIssues(t, `{"ids":["`+a.ID+`","`+b.ID+`"],"actor":"http-agent"}`)
		if status != http.StatusOK {
			t.Fatalf("unguarded two-id delete: status = %d, want 200: %s", status, raw)
		}
	})

	// DUPLICATES COLLAPSE FIRST, so repeating one id beside a guard names one
	// bead and is legal — the rule the handler respells because the transport
	// boundary keeps the library's normalizer out of it. This is the case that
	// would go red if the wire's counting ever drifted from the library's.
	t.Run("a repeated id beside a guard is one bead", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "repeated id", "-p", "2")

		status, raw := sp.updateIssueRaw(t, issue.ID, `{"actor":"http-agent","patch":{"notes":"seeded"}}`)
		if status != http.StatusOK {
			t.Fatalf("the seeding write: status = %d, want 200: %s", status, raw)
		}
		token := revisionOf(t, raw)

		status, raw = sp.deleteIssues(t,
			`{"ids":["`+issue.ID+`","`+issue.ID+`"],"actor":"http-agent","expected_version":`+strconv.FormatInt(token, 10)+`}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 — duplicates collapse to one bead: %s", status, raw)
		}
		bdProxiedShowFail(t, bd, p.dir, issue.ID, "--json")
	})

	// The 404 outranks the guard: a request that named no bead has nothing to
	// be stale about, so the caller is told about the typo rather than about a
	// version it cannot check.
	t.Run("an absent id outranks the guard", func(t *testing.T) {
		status, raw := sp.deleteIssues(t, `{"ids":["bd-nosuchbead"],"expected_version":41}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", status, raw)
		}
		if problem := deleteProblem(t, raw); problem["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", problem["code"])
		}
	})

	sp.shutdown(t)
}
