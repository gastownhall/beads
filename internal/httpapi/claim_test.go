package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// These are pure: the claim path runs end to end over a real listener against a
// fake use case, so the wire edge — path split, media type, body rules, actor
// rules, response and problem shapes — is covered on every pull request by the
// PR workflow's unconditional Go test job. What
// a fake cannot prove is the CAS itself; the concurrent-claim race, the storage
// commit, and the full-item parity oracle against `bd update --claim --json`
// live in cmd/bd's proxied-server integration test, against real Dolt.

// fakeIssues embeds the interface so any method the claim path does not call
// panics instead of quietly returning a zero value.
type fakeIssues struct {
	domain.IssueUseCase

	mu sync.Mutex
	// claims records (id, actor) per CAS attempt. An empty list is how the
	// tests assert that a refusal happened before any database work.
	claims []claimCall
	// claim answers the CAS. Default: the caller wins it.
	claim func(id, actor string) (domain.ClaimResult, error)
	// wispCAS records every CAS attempt against the WISP plane. It must stay
	// empty: v0 claims issues only.
	wispCAS []claimCall
	// issue is what the same-transaction read returns; get overrides it.
	issue *types.Issue
	get   func(id string) (*types.Issue, error)
}

type claimCall struct{ id, actor string }

func (f *fakeIssues) ClaimIssue(_ context.Context, id, actor string) (domain.ClaimResult, error) {
	f.mu.Lock()
	f.claims = append(f.claims, claimCall{id, actor})
	f.mu.Unlock()
	if f.claim == nil {
		return domain.ClaimResult{}, nil
	}
	return f.claim(id, actor)
}

func (f *fakeIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if f.get != nil {
		return f.get(id)
	}
	return f.issue, nil
}

func (f *fakeIssues) ClaimWisp(_ context.Context, id, actor string) (domain.ClaimResult, error) {
	f.mu.Lock()
	f.wispCAS = append(f.wispCAS, claimCall{id, actor})
	f.mu.Unlock()
	return domain.ClaimResult{}, nil
}

func (f *fakeIssues) claimed() []claimCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.claims)
}

func (f *fakeIssues) wispClaims() []claimCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.wispCAS)
}

func seededIssue(id, assignee string, status types.Status) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "claim me",
		Status:    status,
		Priority:  1,
		Assignee:  assignee,
		IssueType: types.TypeTask,
		CreatedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}
}

// newClaimServer wires a server over a provider whose units of work all share
// one use case, so a retried attempt sees the state the previous one left.
func newClaimServer(t *testing.T, issues *fakeIssues) (*testServer, *fakeProvider) {
	t.Helper()
	provider := &fakeProvider{issues: issues}
	return newTestServer(t, Config{Provider: provider}), provider
}

const claimPath = "/v0/beads/issues/bd-1:claim"

// postBody posts a body with an explicit media type, because the media type is
// part of what every body-carrying endpoint checks. Shared by the operations
// that take one; the claim's own wrapper below fills in the ordinary type.
func (ts *testServer) postBody(t *testing.T, path, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (ts *testServer) claim(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

// TestClaimWritesOnceAndAnswersWithTheRowItWrote is the happy path and the two
// things a client depends on: the response carries the issue as it stands after
// the CAS, and exactly one storage commit is made, naming the id and the actor.
func TestClaimWritesOnceAndAnswersWithTheRowItWrote(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	// A provider that takes a measurable moment to hand out a unit of work, so
	// the request line's uow_ms is checkable rather than a rounded zero.
	provider := &fakeProvider{issues: issues, delay: 5 * time.Millisecond}
	ts := newTestServer(t, Config{Provider: provider})

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	body := decodeBody(t, resp)
	if body["already_claimed"] != false {
		t.Errorf("already_claimed = %v, want false on a fresh claim", body["already_claimed"])
	}
	issue, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue = %#v, want an object", body["issue"])
	}
	if issue["id"] != "bd-1" || issue["assignee"] != "alice" || issue["status"] != string(types.StatusInProgress) {
		t.Errorf("issue = %v, want the row the CAS just wrote", issue)
	}

	if got := issues.claimed(); len(got) != 1 || got[0] != (claimCall{"bd-1", "alice"}) {
		t.Errorf("CAS calls = %v, want one for bd-1 by alice", got)
	}

	// One commit, and the actor is in it: that line is the audit trail, which
	// is the whole reason the actor is validated at the wire edge.
	uows := provider.openedUOWs()
	if len(uows) != 1 {
		t.Fatalf("opened %d units of work, want 1", len(uows))
	}
	if got := uows[0].commitMessages(); len(got) != 1 || got[0] != "bd: claim bd-1 by alice" {
		t.Errorf("commit messages = %q, want exactly [bd: claim bd-1 by alice]", got)
	}

	// The observability floor holds for the write too: a claim that waited on
	// the database says so on its own request line.
	line := findLogLine(t, ts.stderr.String(), "op="+OpClaimIssue)
	for _, want := range []string{"method=POST", "path=" + claimPath, "status=200", "uow_ms="} {
		if !strings.Contains(line, want) {
			t.Errorf("claim request line is missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "uow_ms=0.000") {
		t.Errorf("claim request line reports no unit-of-work time though the provider took 5ms:\n%s", line)
	}
}

// TestClaimIsIdempotentForTheHolder: a re-claim by the current holder changes
// nothing, so it answers 200 with already_claimed rather than 409 — and it must
// not mint a storage commit, or a polling client would fill the history with
// empty ones.
func TestClaimIsIdempotentForTheHolder(t *testing.T) {
	issues := &fakeIssues{
		issue: seededIssue("bd-1", "alice", types.StatusInProgress),
		claim: func(_, actor string) (domain.ClaimResult, error) {
			return domain.ClaimResult{AlreadyClaimed: true, PriorAssignee: actor}, nil
		},
	}
	ts, provider := newClaimServer(t, issues)

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["already_claimed"] != true {
		t.Errorf("already_claimed = %v, want true for the current holder", body["already_claimed"])
	}
	uows := provider.openedUOWs()
	if len(uows) != 1 {
		t.Fatalf("opened %d units of work, want 1", len(uows))
	}
	if got := uows[0].commitMessages(); len(got) != 0 {
		t.Errorf("a no-op re-claim committed %q; nothing changed, so nothing should be committed", got)
	}
}

// TestClaimConflictsCarryTheirState is the point of the endpoint. The 409 says
// WHO holds the issue and WHAT state it is in, read inside the claim's own
// transaction — so a client classifies the refusal from `code`, `assignee` and
// `issue_status`, and deletes the substring matching on "already assigned" and
// "claimed by" that was the only way to do it through a CLI subprocess.
func TestClaimConflictsCarryTheirState(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		issue        *types.Issue
		wantCode     Code
		wantAssignee string
		wantStatus   string
	}{
		{
			name:         "held by another actor",
			err:          fmt.Errorf("claim bd-1: %w: already assigned to %q — coordinate with the holder", storage.ErrAlreadyClaimed, "bob"),
			issue:        seededIssue("bd-1", "bob", types.StatusInProgress),
			wantCode:     CodeAlreadyClaimed,
			wantAssignee: "bob",
			wantStatus:   string(types.StatusInProgress),
		},
		{
			// A closed issue may still carry the assignee who closed it. The
			// document publishes `assignee` with already_claimed only, so this
			// row must NOT report one: it would tell a client somebody holds
			// work nobody holds.
			name:       "not in a claimable state",
			err:        fmt.Errorf("claim bd-1: %w: status closed", storage.ErrNotClaimable),
			issue:      seededIssue("bd-1", "bob", types.StatusClosed),
			wantCode:   CodeNotClaimable,
			wantStatus: string(types.StatusClosed),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssues{
				issue: tc.issue,
				claim: func(string, string) (domain.ClaimResult, error) { return domain.ClaimResult{}, tc.err },
			}
			ts, _ := newClaimServer(t, issues)

			resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
			}
			if got := resp.Header.Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want problem+json", got)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(tc.wantCode) {
				t.Errorf("code = %v, want %s", body["code"], tc.wantCode)
			}
			if got, _ := body["assignee"].(string); got != tc.wantAssignee {
				t.Errorf("assignee = %#v, want %q", body["assignee"], tc.wantAssignee)
			}
			if got, _ := body["issue_status"].(string); got != tc.wantStatus {
				t.Errorf("issue_status = %#v, want %q", body["issue_status"], tc.wantStatus)
			}
			if body["request_id"] == nil {
				t.Error("no request_id on the problem body")
			}
		})
	}
}

// TestClaimConflictExtensionsSurviveAnUnreadableRow: the extensions are a
// courtesy, the refusal is not. If the same-transaction read fails, the client
// still gets the typed 409 — degrading it to a 500 would turn a precise answer
// into an unactionable one.
func TestClaimConflictExtensionsSurviveAnUnreadableRow(t *testing.T) {
	issues := &fakeIssues{
		claim: func(string, string) (domain.ClaimResult, error) {
			return domain.ClaimResult{}, fmt.Errorf("claim bd-1: %w", storage.ErrAlreadyClaimed)
		},
		get: func(string) (*types.Issue, error) { return nil, errors.New("read failed") },
	}
	ts, _ := newClaimServer(t, issues)

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 even with the state read failing", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeAlreadyClaimed) {
		t.Errorf("code = %v, want %s", body["code"], CodeAlreadyClaimed)
	}
	if _, present := body["assignee"]; present {
		t.Errorf("assignee is present without a successful read: %v", body)
	}
}

// TestClaimUnknownIDIs404: the miss shape this seam actually produces is a
// wrapped sql.ErrNoRows — the CAS reads the old row first — and a missing issue
// must never surface as a 500.
func TestClaimUnknownIDIs404(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"wrapped ErrNoRows", fmt.Errorf("claim bd-404: read old issue: %w", sql.ErrNoRows)},
		{"normalized not found", fmt.Errorf("claim bd-404: %w", storage.ErrNotFound)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssues{claim: func(string, string) (domain.ClaimResult, error) {
				return domain.ClaimResult{}, tc.err
			}}
			ts, _ := newClaimServer(t, issues)

			resp := ts.claim(t, "/v0/beads/issues/bd-404:claim", `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
				t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
			}
		})
	}
}

// TestClaimNullActorReadsAsATypeMismatch: `detail` is prose, but it is the part
// of a 400 a human reads to find their own mistake, so it has to describe the
// input that was actually sent. Unmarshaling JSON null into a string is a
// no-op, so a null used to fall through to the actor rules and come back as
// "empty after trimming" — which sends a client looking for whitespace in a
// value they never supplied.
func TestClaimNullActorReadsAsATypeMismatch(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	ts, _ := newClaimServer(t, issues)

	resp := ts.claim(t, claimPath, `{"actor":null}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := decodeBody(t, resp)["detail"]; got != "`"+claimActorMember+"` must be a string" {
		t.Errorf("detail = %v, want the type-mismatch text: null is not an empty string", got)
	}
}

// TestClaimRefusesUnrowableIDsBeforeAnyDatabaseWork: `issues.id` is
// VARCHAR(255) and the document calls the path parameter an exact canonical id,
// so these name no row that can exist. Answering them from the edge is what
// keeps an absurd id from buying a concurrency slot and two round trips — the
// same refuse-before-database-work rule the actor lives under, applied to the
// one input on this write path that had no edge check.
//
// The refusal is byte-for-byte the 404 a real miss gets, so a caller cannot map
// which ids the server considers well-formed.
func TestClaimRefusesUnrowableIDsBeforeAnyDatabaseWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"one character past the id column", strings.Repeat("x", types.MaxFieldLen+1)},
		{"absurd", strings.Repeat("x", 64<<10)},
		// A percent-escape in the path decodes before the handler sees it, so
		// this is how a control character reaches an id at all.
		{"percent-escaped newline", "bd-1%0Abd-2"},
		{"percent-escaped NUL", "bd-1%00"},
		{"percent-escaped C1 control sequence introducer", "bd-1%C2%9B31m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
			ts, provider := newClaimServer(t, issues)

			resp := ts.claim(t, "/v0/beads/issues/"+tc.id+":claim", `{"actor":"alice"}`)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeNotFound) {
				t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
			}
			if want := *NotFound().Problem.Detail; body["detail"] != want {
				t.Errorf("detail = %v, want the same text a real miss carries (%q)", body["detail"], want)
			}
			if got := issues.claimed(); len(got) != 0 {
				t.Errorf("the CAS ran on an id no row could hold: %v", got)
			}
			if got := provider.openedUOWs(); len(got) != 0 {
				t.Errorf("an unrowable id opened %d units of work; the bound precedes the database", len(got))
			}
		})
	}
}

// TestClaimRejectsActorsBeforeAnyDatabaseWork is the wire-edge rule. The domain
// layer refuses only actor == "", so without this a whitespace-only or megabyte
// actor would land in the assignee column and in the storage commit message —
// where a newline forges audit-trail lines. Every row asserts that no CAS was
// attempted: this is not a rollback, it is a request that never started.
func TestClaimRejectsActorsBeforeAnyDatabaseWork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor string
	}{
		{"empty", ""},
		{"spaces only", "   "},
		{"tab and newline only", "\t\n"},
		// The document's byte cap. maxLength counts characters; bytes bind.
		{"over the byte cap", strings.Repeat("x", maxActorBytes+1)},
		{"over the byte cap in multibyte", strings.Repeat("é", maxActorBytes)},
		// Within the document's byte cap but one character past what the
		// assignee column holds: a 400 rather than a 500 out of storage.
		{"over the storage character cap", strings.Repeat("x", types.MaxFieldLen+1)},
		{"embedded newline", "alice\nbd serve: claim bd-2 by mallory"},
		{"embedded carriage return", "alice\rmallory"},
		{"embedded NUL", "alice\x00mallory"},
		{"delete character", "alice\x7f"},
		// The C1 block. The schema's pattern excludes only C0 and DEL, but the
		// document's prose says "any control character", and these two are the
		// ones that bite: U+0085 is NEL, a line break on a VT-conformant
		// terminal, so this row is the same forged commit line as the newline
		// row above; U+009B is the one-byte CSI introducer, so the second is
		// escape-sequence injection into anything that prints an assignee.
		{"embedded C1 next line", "alice\u0085bd serve: claim bd-2 by mallory"},
		{"embedded C1 control sequence introducer", "alice\u009b31mmallory"},
		{"embedded C1 lower bound", "alice\u0080mallory"},
		{"embedded C1 upper bound", "alice\u009fmallory"},
		// Not Cc, but they end a line for anything that splits on Unicode
		// breaks — including a log viewer reading the storage commit message.
		{"embedded line separator", "alice\u2028mallory"},
		{"embedded paragraph separator", "alice\u2029mallory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
			ts, provider := newClaimServer(t, issues)

			resp := ts.claim(t, claimPath, jsonBody(t, map[string]any{"actor": tc.actor}))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			problem := decodeBody(t, resp)
			if problem["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", problem["code"], CodeInvalidArgument)
			}
			if problem["param"] != claimActorMember {
				t.Errorf("param = %v, want %q", problem["param"], claimActorMember)
			}
			// invalid_value, not unknown_parameter: the member is known, and
			// the recovery is to send a different value, never to retry.
			if problem["reason"] != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want %s", problem["reason"], ReasonInvalidValue)
			}
			if got := issues.claimed(); len(got) != 0 {
				t.Errorf("the CAS ran on a refused actor: %v", got)
			}
			if got := provider.openedUOWs(); len(got) != 0 {
				t.Errorf("a refused actor opened %d units of work; validation precedes the database", len(got))
			}
		})
	}
}

// TestClaimTrimsTheActor: trimming is documented, so surrounding whitespace is
// not an error — but what reaches storage, and the commit message, is trimmed.
func TestClaimTrimsTheActor(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	ts, provider := newClaimServer(t, issues)

	resp := ts.claim(t, claimPath, jsonBody(t, map[string]any{"actor": "  alice\t "}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := issues.claimed(); len(got) != 1 || got[0].actor != "alice" {
		t.Errorf("CAS actor = %v, want the trimmed value", got)
	}
	uows := provider.openedUOWs()
	if got := uows[0].commitMessages(); len(got) != 1 || got[0] != "bd: claim bd-1 by alice" {
		t.Errorf("commit messages = %q, want the trimmed actor", got)
	}
}

// TestClaimRejectsMalformedRequests covers everything between the socket and
// the actor: the media type, the body shape, and the schema's
// additionalProperties: false — each refused with the member NAMED, because a
// client that has to read prose to find its own mistake is what this surface
// exists to end.
func TestClaimRejectsMalformedRequests(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		contentType string
		body        string
		wantParam   any
		wantReason  Reason
	}{
		{
			// Not pedantry: a JSON content type is not CORS-"simple", so
			// requiring it means a cross-origin claim always triggers a
			// preflight this server never approves. text/plain would skip it.
			name: "form content type", contentType: "application/x-www-form-urlencoded",
			body: `{"actor":"alice"}`, wantParam: "Content-Type", wantReason: ReasonInvalidValue,
		},
		{
			name: "text content type", contentType: "text/plain",
			body: `{"actor":"alice"}`, wantParam: "Content-Type", wantReason: ReasonInvalidValue,
		},
		{
			name: "no content type", contentType: "",
			body: `{"actor":"alice"}`, wantParam: "Content-Type", wantReason: ReasonInvalidValue,
		},
		{
			// A parameterized spelling of the same media type is the same media
			// type; refusing it would break honest clients.
			name: "unknown body member", contentType: "application/json; charset=utf-8",
			body: `{"actor":"alice","force":true}`, wantParam: "force", wantReason: ReasonUnknownParameter,
		},
		{
			// Deterministic naming: two unknown members must not produce a
			// different `param` per request depending on map order.
			name: "several unknown body members", contentType: "application/json",
			body: `{"actor":"alice","zeta":1,"force":true}`, wantParam: "force", wantReason: ReasonUnknownParameter,
		},
		{
			name: "missing actor", contentType: "application/json",
			body: `{}`, wantParam: claimActorMember, wantReason: ReasonInvalidValue,
		},
		{
			name: "actor is not a string", contentType: "application/json",
			body: `{"actor":42}`, wantParam: claimActorMember, wantReason: ReasonInvalidValue,
		},
		{
			name: "actor is null", contentType: "application/json",
			body: `{"actor":null}`, wantParam: claimActorMember, wantReason: ReasonInvalidValue,
		},
		// A body with no nameable part carries no `param`: the document
		// promises the member on every other 400, and promises its absence
		// here.
		{
			name: "empty body", contentType: "application/json",
			body: ``, wantParam: nil, wantReason: ReasonInvalidValue,
		},
		{
			name: "not an object", contentType: "application/json",
			body: `["alice"]`, wantParam: nil, wantReason: ReasonInvalidValue,
		},
		{
			name: "json null", contentType: "application/json",
			body: `null`, wantParam: nil, wantReason: ReasonInvalidValue,
		},
		{
			name: "truncated json", contentType: "application/json",
			body: `{"actor":`, wantParam: nil, wantReason: ReasonInvalidValue,
		},
		{
			// Two documents in one body: the second could carry a different
			// actor, so "the first one wins" is not a rule worth having.
			name: "two objects", contentType: "application/json",
			body: `{"actor":"alice"}{"actor":"mallory"}`, wantParam: nil, wantReason: ReasonInvalidValue,
		},
		{
			// Every operation rejects every query key it does not declare, and
			// this one declares none.
			name: "query parameter", path: claimPath + "?force=1", contentType: "application/json",
			body: `{"actor":"alice"}`, wantParam: "force", wantReason: ReasonUnknownParameter,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
			ts, provider := newClaimServer(t, issues)

			path := tc.path
			if path == "" {
				path = claimPath
			}
			resp := ts.postBody(t, path, tc.contentType, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			problem := decodeBody(t, resp)
			if problem["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", problem["code"], CodeInvalidArgument)
			}
			if got := problem["param"]; got != tc.wantParam {
				t.Errorf("param = %#v, want %#v", got, tc.wantParam)
			}
			if problem["reason"] != string(tc.wantReason) {
				t.Errorf("reason = %v, want %s", problem["reason"], tc.wantReason)
			}
			if got := provider.openedUOWs(); len(got) != 0 {
				t.Errorf("a malformed request opened %d units of work", len(got))
			}
		})
	}
}

// TestClaimBodyCapIsEnforcedWhileReading. The cap is refused mid-read, before
// any member exists to name — which is also why it is driven at the decoder
// rather than over a socket: a megabyte in flight makes the assertion about
// timing instead of about the rule.
func TestClaimBodyCapIsEnforcedWhileReading(t *testing.T) {
	oversized := `{"actor":"` + strings.Repeat("x", maxJSONBodyBytes) + `"}`
	r := httptest.NewRequest(http.MethodPost, claimPath, strings.NewReader(oversized))

	members, res := decodeJSONObjectBody(httptest.NewRecorder(), r)
	if res == nil {
		t.Fatalf("a %d-byte body was accepted (%d members)", len(oversized), len(members))
	}
	if res.Problem.Status != http.StatusBadRequest || res.Problem.Code != string(CodeInvalidArgument) {
		t.Errorf("problem = %d/%s, want 400/%s", res.Problem.Status, res.Problem.Code, CodeInvalidArgument)
	}
	if res.Problem.Param != nil {
		t.Errorf("param = %q; a body refused while reading has no nameable part", *res.Problem.Param)
	}
	if res.Problem.Detail == nil || !strings.Contains(*res.Problem.Detail, "larger than") {
		t.Errorf("detail = %v, want it to say the body was too large", res.Problem.Detail)
	}
}

// TestCustomMethodsNarrowThePOSTSurface. ServeMux wildcards match a whole
// segment, so the single-resource custom methods share one pattern —
// POST /v0/beads/issues/{idop} — and every POST under that prefix reaches the
// dispatcher. The issue-detail path is documented GET-only, and a POST to it
// must NOT be read as an operation on the issue named there: it is an unrouted
// path, and it gets the unrouted path's answer.
//
// The generalization from the claim's own version is the point. A dispatcher
// that fell back to its first row for an unrecognized suffix would turn every
// probe of this prefix into a claim, and the suite would stay green because the
// claim's happy path still worked.
func TestCustomMethodsNarrowThePOSTSurface(t *testing.T) {
	for _, path := range []string{
		"/v0/beads/issues/bd-1",           // the GET-only detail path
		"/v0/beads/issues/:claim",         // a custom method with no id
		"/v0/beads/issues/:close",         // the same, on the newer verb
		"/v0/beads/issues/bd-1:CLAIM",     // the custom method is not a spelling
		"/v0/beads/issues/bd-1:Close",     // nor is this one
		"/v0/beads/issues/bd-1:claim-not", // a suffix that merely starts the same
		"/v0/beads/issues/bd-1:close-not",
		// `unclaim` is the CLI's spelling of the release and is deliberately not
		// a second name for it here: one verb per operation, and a client that
		// guessed from the command name gets the same 404 any other unrouted
		// suffix gets.
		"/v0/beads/issues/bd-1:unclaim",
		"/v0/beads/issues/:release",
		"/v0/beads/issues/bd-1:Release",
		"/v0/beads/issues/bd-1:released",
		"/v0/beads/issues/:reopen",
		"/v0/beads/issues/bd-1:Reopen",
		"/v0/beads/issues/bd-1:reopened",
		"/v0/beads/issues/bd-1:delete", // a verb this build does not serve
	} {
		t.Run(path, func(t *testing.T) {
			issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
			ts, provider := newClaimServer(t, issues)

			resp := ts.claim(t, path, `{"actor":"mallory"}`)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("POST %s: status = %d, want 404: %s", path, resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
				t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
			}
			if got := issues.claimed(); len(got) != 0 {
				t.Errorf("POST %s reached the CAS: %v", path, got)
			}
			if got := provider.openedUOWs(); len(got) != 0 {
				t.Errorf("POST %s opened a unit of work", path)
			}
		})
	}
}

// TestClaimRetriesOnWriteContention pins that the handler inherits
// uow.RunTxResult's behavior rather than carrying a retry loop of its own: a
// serialization failure redoes the WHOLE attempt in a FRESH unit of work,
// because re-committing a session the server already rolled back is the
// lost-write bug that behavior exists to avoid.
func TestClaimRetriesOnWriteContention(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	issues.claim = func(string, string) (domain.ClaimResult, error) {
		if len(issues.claimed()) == 1 {
			// The shape the driver actually reports a deadlock in — the retry
			// decision is made with errors.As on this type, never on text.
			return domain.ClaimResult{}, fmt.Errorf("claim bd-1: %w",
				&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"})
		}
		return domain.ClaimResult{}, nil
	}
	ts, provider := newClaimServer(t, issues)

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the retry: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := issues.claimed(); len(got) != 2 {
		t.Errorf("CAS attempts = %d, want 2", len(got))
	}
	if got := provider.openedUOWs(); len(got) != 2 {
		t.Errorf("opened %d units of work, want one per attempt", len(got))
	}
}

// TestClaimTakesADatabaseSlot: the claim is not exempt from the in-flight
// limit. An exempt write would keep opening connections while every reader is
// already queued — the saturation case the semaphore exists for. Each later
// write carries the same assertion against its own row.
func TestClaimTakesADatabaseSlot(t *testing.T) {
	for _, rt := range routeTable {
		if rt.op != OpClaimIssue {
			continue
		}
		if rt.bypassSemaphore {
			t.Error("the claim route bypasses the database semaphore; only handlers that touch no database may")
		}
		if !rt.implemented {
			t.Error("the claim route is still marked unimplemented, so capabilities will not advertise it")
		}
		return
	}
	t.Fatalf("no %s row in the route table", OpClaimIssue)
}

// TestClaimNeverReachesTheWispPlane pins as a test what was prose until the
// claim became a role: this surface addresses the issues table only, so a wisp
// id names no row it can see and answers 404 rather than claiming the wisp. The
// fake's ClaimWisp records rather than panicking, so the assertion is "it was
// never called" instead of "the test crashed".
func TestClaimNeverReachesTheWispPlane(t *testing.T) {
	issues := &fakeIssues{claim: func(string, string) (domain.ClaimResult, error) {
		// What the issues-plane CAS reports for an id whose row lives in the
		// wisp tables: the pre-image read finds nothing.
		return domain.ClaimResult{}, fmt.Errorf("db: Claim bd-w1: read old issue: %w", sql.ErrNoRows)
	}}
	ts, _ := newClaimServer(t, issues)

	resp := ts.claim(t, "/v0/beads/issues/bd-w1:claim", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
	if got := issues.wispClaims(); len(got) != 0 {
		t.Errorf("the claim fell back to the wisp plane: %v", got)
	}
}

// TestAClaimTimesTheUnitsOfWorkItsClaimerOpens is the write-side twin of
// TestAReadRouteTimesTheUnitsOfWorkItsReaderOpens, and it exists for the same
// tempting edit: `p.inner.IssueClaimer()` — "add the layer by recursion, like
// every other decorator". This decorator's layer is on NewUOW, which only a
// claimer holding THIS wrapper can reach, so recursion hands back a claimer
// bound to the untimed provider. It compiles, and every claim reports
// uow_ms=0.000 forever.
func TestAClaimTimesTheUnitsOfWorkItsClaimerOpens(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	provider := &fakeProvider{issues: issues, delay: 5 * time.Millisecond}
	ts := newTestServer(t, Config{Provider: provider})

	if resp := ts.claim(t, claimPath, `{"actor":"alice"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(provider.openedUOWs()); n != 1 {
		t.Fatalf("opened %d units of work, want 1", n)
	}

	line := findLogLine(t, ts.stderr.String(), "op="+OpClaimIssue)
	if !strings.Contains(line, "uow_ms=") {
		t.Fatalf("claim request line has no uow_ms field:\n%s", line)
	}
	if strings.Contains(line, "uow_ms=0.000") {
		t.Errorf("claim request line reports no unit-of-work time though the provider took 5ms; the claimer is bound to the untimed provider:\n%s", line)
	}
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %v: %v", v, err)
	}
	return string(b)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
