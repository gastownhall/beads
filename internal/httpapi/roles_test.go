package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These tests cover the OTHER database source: a backend whose facade is a
// store rather than a unit-of-work provider, served by handing Listen the two
// issue roles directly.
//
// The fakes below implement issueops.Reader and issueops.Claimer and NOTHING
// else — deliberately not uow.UnitOfWorkProvider, because "a backend that
// cannot produce a unit of work is still servable" is the property this whole
// seam exists for. If either of them ever grows a NewUOW method these tests
// stop proving it.

type roleReader struct {
	page    issueops.IssuePage
	details *issueops.IssueDetails
	err     error

	mu    sync.Mutex
	ready []issueops.ReadyRequest
	list  []issueops.ListRequest
	get   []issueops.GetRequest
}

func (r *roleReader) Ready(_ context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	r.mu.Lock()
	r.ready = append(r.ready, req)
	r.mu.Unlock()
	if r.err != nil {
		return issueops.IssuePage{}, r.err
	}
	return r.page, nil
}

func (r *roleReader) List(_ context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	r.mu.Lock()
	r.list = append(r.list, req)
	r.mu.Unlock()
	if r.err != nil {
		return issueops.IssuePage{}, r.err
	}
	return r.page, nil
}

func (r *roleReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	r.mu.Lock()
	r.get = append(r.get, req)
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return r.details, nil
}

func (r *roleReader) readyRequests() []issueops.ReadyRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]issueops.ReadyRequest(nil), r.ready...)
}

func (r *roleReader) getRequests() []issueops.GetRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]issueops.GetRequest(nil), r.get...)
}

type roleClaimer struct {
	result issueops.ClaimResult
	err    error

	mu     sync.Mutex
	claims []issueops.ClaimRequest
}

func (c *roleClaimer) Claim(_ context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	c.mu.Lock()
	c.claims = append(c.claims, req)
	c.mu.Unlock()
	if c.err != nil {
		return issueops.ClaimResult{}, c.err
	}
	return c.result, nil
}

func (c *roleClaimer) claimRequests() []issueops.ClaimRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]issueops.ClaimRequest(nil), c.claims...)
}

// countedPage is the fixture both database sources answer with, so a body
// difference between them can only come from the construction.
func countedPage() []*types.IssueWithCounts {
	return []*types.IssueWithCounts{
		{Issue: seededIssue("bd-1", "alice", types.StatusOpen), DependencyCount: 1, DependentCount: 2, CommentCount: 3},
		{Issue: seededIssue("bd-2", "", types.StatusOpen)},
	}
}

// TestListenRequiresExactlyOneDatabaseSource pins the precondition that
// replaced the old nil-provider check.
//
// The two refusals are different mistakes and must stay distinguishable. A
// HALF-SET pair is the dangerous one: a Config carrying a reader and no claimer
// would bind, answer every read, and fail the one write on this surface with a
// nil dereference — at claim time, in a handler, on a live server.
func TestListenRequiresExactlyOneDatabaseSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "neither source",
			cfg:     Config{},
			wantErr: "no database source",
		},
		{
			name: "a provider alone is a complete source",
			cfg:  Config{Provider: &fakeProvider{}},
		},
		{
			name: "both roles together are a complete source",
			cfg:  Config{Reader: &roleReader{}, Claimer: &roleClaimer{}},
		},
		{
			name:    "a reader without a claimer",
			cfg:     Config{Reader: &roleReader{}},
			wantErr: "no database source",
		},
		{
			name:    "a claimer without a reader",
			cfg:     Config{Claimer: &roleClaimer{}},
			wantErr: "no database source",
		},
		{
			name:    "a provider and a reader",
			cfg:     Config{Provider: &fakeProvider{}, Reader: &roleReader{}},
			wantErr: "exactly one database source",
		},
		{
			name:    "a provider and a claimer",
			cfg:     Config{Provider: &fakeProvider{}, Claimer: &roleClaimer{}},
			wantErr: "exactly one database source",
		},
		{
			name:    "a provider and both roles",
			cfg:     Config{Provider: &fakeProvider{}, Reader: &roleReader{}, Claimer: &roleClaimer{}},
			wantErr: "exactly one database source",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Addr = "127.0.0.1:0"
			cfg.Stdout = io.Discard
			cfg.Stderr = io.Discard

			srv, err := Listen(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Listen: %v, want a bound server", err)
				}
				t.Cleanup(func() { _ = srv.http.Close() })
				return
			}
			if err == nil {
				t.Fatalf("Listen bound a server for %s; want a refusal mentioning %q", tc.name, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestConfiguredRolesServeTheSameReadyBytesAsAProvider is the "construction
// only" proof: two servers built from the two database sources, over fakes that
// answer with the same page, produce the same response byte for byte.
func TestConfiguredRolesServeTheSameReadyBytesAsAProvider(t *testing.T) {
	items := countedPage()

	viaProvider := newTestServer(t, Config{Provider: &fakeProvider{
		issues:     &fakeIssues{},
		readIssues: &recordingIssues{items: items},
		readConfig: emptyConfig{},
	}})
	viaRoles := newTestServer(t, Config{
		Reader:  &roleReader{page: issueops.IssuePage{Items: items}},
		Claimer: &roleClaimer{},
	})

	for _, path := range []string{"/v0/beads/ready", "/v0/beads/issues"} {
		fromProvider := viaProvider.get(t, path)
		fromRoles := viaRoles.get(t, path)

		if fromProvider.StatusCode != http.StatusOK || fromRoles.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: provider status %d, roles status %d, want 200 from both",
				path, fromProvider.StatusCode, fromRoles.StatusCode)
		}
		if got, want := fromRoles.Header.Get("Content-Type"), fromProvider.Header.Get("Content-Type"); got != want {
			t.Errorf("GET %s: roles Content-Type %q, provider %q", path, got, want)
		}
		if got, want := readAll(t, fromRoles), readAll(t, fromProvider); got != want {
			t.Errorf("GET %s: the two database sources answer differently\nroles:    %s\nprovider: %s", path, got, want)
		}
	}
}

// TestConfiguredRolesAnswerEveryDatabaseRoute drives all four
// database-touching operations against a store-shaped source, which is the
// whole point: none of them can reach a unit of work here, because there is no
// provider to open one.
func TestConfiguredRolesAnswerEveryDatabaseRoute(t *testing.T) {
	details := &issueops.IssueDetails{Issue: *seededIssue("bd-1", "alice", types.StatusOpen)}
	reader := &roleReader{page: issueops.IssuePage{Items: countedPage(), HasMore: true}, details: details}
	claimer := &roleClaimer{result: issueops.ClaimResult{
		Issue:   seededIssue("bd-1", "alice", types.StatusInProgress),
		Changed: true,
	}}
	ts := newTestServer(t, Config{Reader: reader, Claimer: claimer})

	t.Run("ready", func(t *testing.T) {
		resp := ts.get(t, "/v0/beads/ready?sort=oldest")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		body := decodeBody(t, resp)
		if body["has_more"] != true {
			t.Errorf("has_more = %v, want the value the role reported", body["has_more"])
		}
		if got, ok := body["items"].([]any); !ok || len(got) != 2 {
			t.Errorf("items = %v, want the role's two rows", body["items"])
		}
		// The role is handed the request the wire named, not a rewritten one.
		reqs := reader.readyRequests()
		if len(reqs) != 1 || reqs[0].Sort != "oldest" {
			t.Errorf("ready requests = %+v, want one carrying sort=oldest", reqs)
		}
	})

	t.Run("list", func(t *testing.T) {
		resp := ts.get(t, "/v0/beads/issues")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		body := decodeBody(t, resp)
		// has_more and next_cursor are a biconditional on this surface, and the
		// cursor is minted from the page the role returned.
		if body["has_more"] != true {
			t.Errorf("has_more = %v, want true", body["has_more"])
		}
		if cursor, _ := body["next_cursor"].(string); cursor == "" {
			t.Errorf("no next_cursor beside has_more: %v", body)
		}
	})

	t.Run("get", func(t *testing.T) {
		resp := ts.get(t, "/v0/beads/issues/bd-1")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["id"] != "bd-1" {
			t.Errorf("body = %v, want the detail view the role returned", body)
		}
		if reqs := reader.getRequests(); len(reqs) != 1 || reqs[0].ID != "bd-1" {
			t.Errorf("get requests = %+v, want one for bd-1", reqs)
		}
	})

	t.Run("claim", func(t *testing.T) {
		resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		body := decodeBody(t, resp)
		if body["already_claimed"] != false {
			t.Errorf("already_claimed = %v, want false for a claim that changed the row", body["already_claimed"])
		}
		issue, _ := body["issue"].(map[string]any)
		if issue["id"] != "bd-1" || issue["status"] != string(types.StatusInProgress) {
			t.Errorf("issue = %v, want the row the role reported", body["issue"])
		}
		if reqs := claimer.claimRequests(); len(reqs) != 1 || reqs[0] != (issueops.ClaimRequest{IssueID: "bd-1", Actor: "alice"}) {
			t.Errorf("claim requests = %+v, want one for bd-1 by alice", reqs)
		}
	})
}

// TestConfiguredRolesKeepTheDocumentedRefusals: the error vocabulary belongs to
// the handlers, so it cannot depend on which database source produced the role.
// Every case here is the roles-path twin of a provider-path test in this
// package.
func TestConfiguredRolesKeepTheDocumentedRefusals(t *testing.T) {
	t.Run("a missing issue is 404", func(t *testing.T) {
		ts := newTestServer(t, Config{
			Reader:  &roleReader{err: fmt.Errorf("get bd-404: %w", storage.ErrNotFound)},
			Claimer: &roleClaimer{},
		})
		resp := ts.get(t, "/v0/beads/issues/bd-404")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeNotFound) {
			t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
		}
	})

	t.Run("a filter refusal is the documented 400", func(t *testing.T) {
		// The builders run INSIDE the role on this path, so their refusal
		// arrives at the handler exactly as it does from a unit-of-work reader —
		// and must still be mapped to its parameter rather than to a 500.
		ts := newTestServer(t, Config{
			Reader:  &roleReader{err: errors.New("invalid status bogus")},
			Claimer: &roleClaimer{},
		})
		resp := ts.get(t, "/v0/beads/issues?status=bogus")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		body := decodeBody(t, resp)
		if body["code"] != string(CodeInvalidArgument) || body["param"] != "status" {
			t.Errorf("body = %v, want invalid_argument on param status", body)
		}
	})

	t.Run("a foreign holder is 409 with its state", func(t *testing.T) {
		ts := newTestServer(t, Config{
			Reader: &roleReader{},
			Claimer: &roleClaimer{err: &issueops.ClaimConflictError{
				IssueID:  "bd-1",
				Assignee: "bob",
				Status:   types.StatusInProgress,
				Err:      fmt.Errorf("claim bd-1: %w", storage.ErrAlreadyClaimed),
			}},
		})
		resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
		}
		body := decodeBody(t, resp)
		if body["code"] != string(CodeAlreadyClaimed) {
			t.Errorf("code = %v, want %s", body["code"], CodeAlreadyClaimed)
		}
		if body["assignee"] != "bob" || body["issue_status"] != string(types.StatusInProgress) {
			t.Errorf("body = %v, want the holder and status the role reported", body)
		}
	})

	t.Run("a role failure is the generic 500", func(t *testing.T) {
		ts := newTestServer(t, Config{
			Reader:  &roleReader{err: errors.New("backend is unreachable")},
			Claimer: &roleClaimer{},
		})
		resp := ts.get(t, "/v0/beads/ready")
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := readAll(t, resp); strings.Contains(body, "backend is unreachable") {
			t.Errorf("the 5xx body republished the backend's error text: %s", body)
		}
	})
}

// TestStartupNamesTheDatabaseSource: uow_ms is 0.000 for every request a
// roles-backed server answers, because it opens no units of work. That is the
// true value, and the startup line is what makes it attributable instead of
// looking like lost instrumentation.
func TestStartupNamesTheDatabaseSource(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		ts := newTestServer(t, Config{Provider: &tunableProvider{}})
		startup := findLogLine(t, ts.stderr.String(), "event=startup")
		if !strings.Contains(startup, "db=provider") {
			t.Errorf("startup line does not name the database source:\n%s", startup)
		}
		limits := findLogLine(t, ts.stderr.String(), "event=limits")
		if !strings.Contains(limits, "pool_max_open=") {
			t.Errorf("limits line omits the pool bounds a provider-backed server applies:\n%s", limits)
		}
	})

	t.Run("roles", func(t *testing.T) {
		ts := newTestServer(t, Config{Reader: &roleReader{}, Claimer: &roleClaimer{}})
		startup := findLogLine(t, ts.stderr.String(), "event=startup")
		if !strings.Contains(startup, "db=roles") {
			t.Errorf("startup line does not name the database source:\n%s", startup)
		}
		// The pool belongs to whatever the backend is; this server neither owns
		// it nor tuned it, so publishing bounds it did not set would be a lie.
		limits := findLogLine(t, ts.stderr.String(), "event=limits")
		if strings.Contains(limits, "pool_") {
			t.Errorf("limits line publishes pool bounds this server never applied:\n%s", limits)
		}
		if strings.Contains(ts.stderr.String(), "event=pool_limits_unavailable") {
			t.Errorf("a roles-backed server announced a missing provider knob:\n%s", ts.stderr.String())
		}
	})
}

// TestARolesRequestReportsNoUnitOfWorkTime states the other half out loud: the
// number really is zero, and it is zero because nothing on this path opens a
// unit of work — not because the timing wrapper was dropped.
func TestARolesRequestReportsNoUnitOfWorkTime(t *testing.T) {
	ts := newTestServer(t, Config{Reader: &roleReader{}, Claimer: &roleClaimer{}})

	if resp := ts.get(t, "/v0/beads/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "op="+OpListReadyWork)
	if !strings.Contains(line, "uow_ms=0.000") {
		t.Errorf("a roles-backed request reported unit-of-work time it cannot have spent:\n%s", line)
	}
}

// TestWithUOWRefusesWithoutAProvider: the helper's whole job is to open a unit
// of work, and a roles-backed server has nothing to open one from. An error is
// the answer; a nil dereference inside a handler is not.
func TestWithUOWRefusesWithoutAProvider(t *testing.T) {
	ts := newTestServer(t, Config{Reader: &roleReader{}, Claimer: &roleClaimer{}})

	ran := false
	err := ts.WithUOW(context.Background(), &reqInfo{}, func(uow.UnitOfWork) error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("WithUOW succeeded on a server with no unit-of-work provider")
	}
	if ran {
		t.Error("the callback ran without a unit of work")
	}
}

// TestARoleThatAnswersWithNothingIsNotDereferenced covers the one guarantee a
// configured role cannot be asked for: that a call which reports no error
// carries the value the handler is about to dereference.
//
// It used to hold BY CONSTRUCTION. s.reader() could only return
// uow.NewIssueReader(...), whose Get routes through workapi.GetIssueOrWisp —
// a function whose whole reason to exist is folding both miss shapes into
// ErrNotFound so that no caller can write `if err != nil || issue == nil` and
// report a dropped connection as "not found". A caller-supplied role is
// ordinary code and carries no such guarantee, and both handlers that hold a
// pointer from a role dereference it unconditionally.
func TestARoleThatAnswersWithNothingIsNotDereferenced(t *testing.T) {
	// The answer is the SAME 404 a real miss produces, byte for byte: the
	// document states one not-found body, and a client must not be able to
	// tell a broken role from an absent issue.
	t.Run("a reader with no detail view is the documented miss", func(t *testing.T) {
		silent := newTestServer(t, Config{Reader: &roleReader{}, Claimer: &roleClaimer{}})
		missed := newTestServer(t, Config{
			Reader:  &roleReader{err: fmt.Errorf("get bd-1: %w", storage.ErrNotFound)},
			Claimer: &roleClaimer{},
		})

		got := silent.get(t, "/v0/beads/issues/bd-1")
		want := missed.get(t, "/v0/beads/issues/bd-1")
		if got.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", got.StatusCode, readAll(t, got))
		}
		gotBody, wantBody := decodeBody(t, got), decodeBody(t, want)
		// request_id is per request and is the only member that may differ.
		delete(gotBody, "request_id")
		delete(wantBody, "request_id")
		if !reflect.DeepEqual(gotBody, wantBody) {
			t.Errorf("body = %v, want the body a real miss produces: %v", gotBody, wantBody)
		}
		assertNoPanic(t, silent)
	})

	// A claim that reports success without a row is not a documented outcome —
	// there is no wire code for it — so it is the generic 500. What it must not
	// be is a panic: the response is recovered into the same status, but the
	// fault reaches the log as a stack trace instead of as an error, and the
	// panic path writes no request_error line for an operator to alert on.
	t.Run("a claimer with no issue is the generic failure", func(t *testing.T) {
		ts := newTestServer(t, Config{
			Reader:  &roleReader{},
			Claimer: &roleClaimer{result: issueops.ClaimResult{Changed: true}},
		})

		resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
			t.Errorf("code = %v, want %s", body["code"], CodeInternal)
		}
		assertNoPanic(t, ts)
		if line := findLogLine(t, ts.stderr.String(), "event=request_error"); !strings.Contains(line, "claim") {
			t.Errorf("the 500 is logged without naming the operation that produced it:\n%s", line)
		}
	})
}

// assertNoPanic fails when the server recovered a panic. Every case in this
// file is one a handler could reach by trusting a role's return value, so the
// status alone does not distinguish a refusal from a recovered dereference.
func assertNoPanic(t *testing.T, ts *testServer) {
	t.Helper()
	if log := ts.stderr.String(); strings.Contains(log, "event=panic") {
		t.Errorf("a handler dereferenced what the role did not return:\n%s", log)
	}
}

// hookableStore is the smallest thing storage.NewHookFiringStore will decorate:
// the DoltStorage surface is embedded nil because IssueClaimer is the only
// method this test ever reaches through it.
type hookableStore struct {
	storage.DoltStorage
	claimer issueops.Claimer
}

func (s hookableStore) IssueClaimer() (issueops.Claimer, error) { return s.claimer, nil }

// TestListenRefusesARoleThatFiresTheWorkspaceHooks.
//
// `bd serve` documents that hooks do not fire, and until roles became
// configuration nothing could make them: the provider seam builds its claimer
// from a unit of work, which carries no hook layer at all. A store is the
// opposite — its accessors hand out its decorators, deliberately, so that a CLI
// claim keeps its on_update — and bd's own chain is
// caller -> HookFiringStore -> InstrumentedStorage -> raw. So the one line a
// caller with a store would obviously write, store.IssueClaimer(), returns
// exactly the claimer this server may not serve.
//
// The refusal is at Listen because the alternative is silent: a server built
// that way answers every request correctly and runs a user's subprocess per
// landed claim for as long as it is up.
func TestListenRefusesARoleThatFiresTheWorkspaceHooks(t *testing.T) {
	// A nil runner, which is what a HookFiringStore built without one carries.
	// The refusal must not depend on that: the type's job is to fire hooks, and
	// a server that admitted this one would be a config change away from
	// breaking its own contract.
	hooked := storage.NewHookFiringStore(hookableStore{claimer: &roleClaimer{}}, nil)

	fromTheStore, err := hooked.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer: %v", err)
	}
	if !storage.RoleFiresHooks(fromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing claimer; this test proves nothing")
	}

	listen := func(cl issueops.Claimer) (*Server, error) {
		return Listen(Config{
			Addr:    "127.0.0.1:0",
			Stdout:  io.Discard,
			Stderr:  io.Discard,
			Reader:  &roleReader{},
			Claimer: cl,
		})
	}

	if _, err := listen(fromTheStore); err == nil {
		t.Error("Listen bound a server whose claim route runs the workspace's hook scripts")
	} else if !strings.Contains(err.Error(), "hooks") {
		t.Errorf("refusal %q does not say what is wrong with the role", err)
	}

	// And the store BENEATH the decorator is the value the doc sends a caller
	// to, so it has to be servable. Without this the guard could be a blanket
	// refusal of every store-backed claimer and still pass.
	fromBeneath, err := hooked.Unwrap().IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer on the undecorated store: %v", err)
	}
	srv, err := listen(fromBeneath)
	if err != nil {
		t.Fatalf("Listen: %v, want a bound server for the claimer beneath the hook layer", err)
	}
	t.Cleanup(func() { _ = srv.http.Close() })
}
