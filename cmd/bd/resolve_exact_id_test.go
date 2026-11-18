package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// resolveCallStore is a store that records every call id resolution makes, in
// order. It holds issues by exact id and answers a miss the way the Dolt stores
// do (storage.ErrNotFound, wrapped). Any other store method panics through the
// nil embedded interface, so a resolution that starts reading something else
// fails loudly.
type resolveCallStore struct {
	storage.DoltStorage

	issues map[string]*types.Issue
	// getErr, when set, is GetIssue's answer for every id.
	getErr error
	calls  []string
}

func newResolveCallStore(ids ...string) *resolveCallStore {
	s := &resolveCallStore{issues: map[string]*types.Issue{}}
	for _, id := range ids {
		s.issues[id] = &types.Issue{ID: id, Title: "title of " + id}
	}
	return s
}

func (s *resolveCallStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	s.calls = append(s.calls, "GetIssue "+id)
	if s.getErr != nil {
		return nil, s.getErr
	}
	issue, ok := s.issues[id]
	if !ok {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	copied := *issue
	return &copied, nil
}

func (s *resolveCallStore) SearchIssues(_ context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.calls = append(s.calls, fmt.Sprintf("SearchIssues %q %v", query, filter.IDs))
	var out []*types.Issue
	for _, id := range filter.IDs {
		if issue, ok := s.issues[id]; ok {
			copied := *issue
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (s *resolveCallStore) SearchIssueIDs(_ context.Context, query string, _ types.IssueFilter) ([]string, error) {
	s.calls = append(s.calls, "SearchIssueIDs "+query)
	var ids []string
	for id := range s.issues {
		if strings.Contains(id, query) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *resolveCallStore) GetConfig(_ context.Context, key string) (string, error) {
	s.calls = append(s.calls, "GetConfig "+key)
	if key == "issue_prefix" {
		return "bd", nil
	}
	return "", nil
}

func TestLooksLikeFullIssueID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
		why  string
	}{
		{"bd-abc1", true, "a full id"},
		{"bd-ab", true, "an abbreviation passes and pays the miss"},
		{"hacker-news-ko4", true, "multi-hyphen prefix"},
		{"bd-abc1.2", true, "hierarchical child"},
		{"bd-wisp-abc1", true, "wisp"},
		{"a-b", true, "one character on each side is enough"},
		{"external:proj-x:cap", true, "syntactic only: it misses and falls back"},
		{"abc1", false, "bare hash"},
		{"bdabc1", false, "prefix without its hyphen"},
		{"-abc1", false, "nothing before the hyphen"},
		{"abc1-", false, "nothing after the hyphen"},
		{"-", false, "a lone hyphen"},
		{"", false, "empty"},
		{"null", false, "tooling sentinels keep the resolver's refusal"},
		{"bd-abc1 ", false, "trailing space"},
		{"bd -abc1", false, "inner space"},
		{"bd-ab\tc1", false, "tab"},
		{"bd-abc1\n", false, "newline"},
	} {
		if got := looksLikeFullIssueID(tc.id); got != tc.want {
			t.Errorf("looksLikeFullIssueID(%q) = %v, want %v (%s)", tc.id, got, tc.want, tc.why)
		}
	}
}

// TestResolveAndGetFromStoreFullIDReadsOnlyTheIssue pins the saving: an id
// that exists as typed is answered by the GetIssue the command needs anyway,
// without the resolver's exact search or any configuration read.
func TestResolveAndGetFromStoreFullIDReadsOnlyTheIssue(t *testing.T) {
	s := newResolveCallStore("bd-abc1", "bd-abc12")
	result, err := resolveAndGetFromStore(context.Background(), s, "bd-abc1", false)
	if err != nil {
		t.Fatalf("resolveAndGetFromStore: %v", err)
	}
	if result.ResolvedID != "bd-abc1" || result.Issue == nil || result.Issue.ID != "bd-abc1" {
		t.Fatalf("result = %+v, want bd-abc1", result)
	}
	if result.Store != storage.DoltStorage(s) || result.Routed {
		t.Fatalf("result store/routed = %v/%v, want the local store, unrouted", result.Store, result.Routed)
	}
	if want := []string{"GetIssue bd-abc1"}; !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls = %q, want %q", s.calls, want)
	}

	// The routed flag is carried through on the direct hit too, as a routed
	// tier's destination store relies on it.
	routed := newResolveCallStore("gt-xyz9")
	result, err = resolveAndGetFromStore(context.Background(), routed, "gt-xyz9", true)
	if err != nil || !result.Routed || result.ResolvedID != "gt-xyz9" {
		t.Fatalf("routed resolveAndGetFromStore = %+v, %v; want gt-xyz9, routed", result, err)
	}

	// The resolved id is the stored issue's, not the text typed. They differ
	// only where the id column's collation matches other spellings (a
	// case-insensitive or PAD SPACE collation; bd creates neither), and then the
	// command must act on the row's own id.
	folding := newResolveCallStore()
	folding.issues["BD-ABC1"] = &types.Issue{ID: "bd-abc1", Title: "stored lower-case"}
	result, err = resolveAndGetFromStore(context.Background(), folding, "BD-ABC1", false)
	if err != nil || result.ResolvedID != "bd-abc1" || result.Issue.ID != "bd-abc1" {
		t.Fatalf("resolveAndGetFromStore(BD-ABC1) = %+v, %v; want the stored id bd-abc1", result, err)
	}
}

// TestResolveAndGetFromStoreAbbreviatedIDFallsBack pins the cost: a prefixed
// abbreviation passes the gate, misses the direct read, and then runs the
// resolver's sequence exactly as before, ending in the same GetIssue.
func TestResolveAndGetFromStoreAbbreviatedIDFallsBack(t *testing.T) {
	t.Setenv("BD_NO_PARTIAL_ID_NOTICE", "1")
	s := newResolveCallStore("bd-abc1")
	result, err := resolveAndGetFromStore(context.Background(), s, "bd-ab", false)
	if err != nil {
		t.Fatalf("resolveAndGetFromStore: %v", err)
	}
	if result.ResolvedID != "bd-abc1" || result.Issue.ID != "bd-abc1" {
		t.Fatalf("result = %+v, want bd-abc1", result)
	}
	want := []string{
		"GetIssue bd-ab", // the direct read, which misses
		`SearchIssues "" [bd-ab]`,
		"GetConfig issue_prefix",
		"GetConfig allowed_prefixes",
		"SearchIssueIDs ab",
		"GetIssue bd-abc1",
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls = %q, want %q", s.calls, want)
	}
}

// TestResolveAndGetFromStoreBareHashKeepsTheResolver pins that an id with no
// hyphen never takes the direct read: the resolver runs first, as before.
func TestResolveAndGetFromStoreBareHashKeepsTheResolver(t *testing.T) {
	s := newResolveCallStore("bd-abc1")
	result, err := resolveAndGetFromStore(context.Background(), s, "abc1", false)
	if err != nil {
		t.Fatalf("resolveAndGetFromStore: %v", err)
	}
	if result.ResolvedID != "bd-abc1" {
		t.Fatalf("resolved %q, want bd-abc1", result.ResolvedID)
	}
	want := []string{
		`SearchIssues "" [abc1]`,
		"GetConfig issue_prefix",
		"GetConfig allowed_prefixes",
		`SearchIssues "" [bd-abc1]`,
		"GetIssue bd-abc1",
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls = %q, want %q", s.calls, want)
	}
}

// TestResolveAndGetFromStoreMissStillReportsNotFound pins that an id found
// nowhere still fails the way the routed tiers and the commands read as a miss.
func TestResolveAndGetFromStoreMissStillReportsNotFound(t *testing.T) {
	s := newResolveCallStore("bd-abc1")
	_, err := resolveAndGetFromStore(context.Background(), s, "bd-zzz9", false)
	if err == nil || !isNotFoundErr(err) {
		t.Fatalf("err = %v, want a not-found", err)
	}
	if s.calls[0] != "GetIssue bd-zzz9" || s.calls[1] != `SearchIssues "" [bd-zzz9]` {
		t.Fatalf("calls = %q, want the direct read and then the resolver", s.calls)
	}
}

// TestResolveAndGetFromStoreReturnsOtherGetIssueErrors pins that only a
// not-found falls back. Any other GetIssue failure is returned as it is today,
// where the same GetIssue ends the sequence, and the resolver is not run.
func TestResolveAndGetFromStoreReturnsOtherGetIssueErrors(t *testing.T) {
	broken := errors.New("connection reset by peer")
	s := newResolveCallStore("bd-abc1")
	s.getErr = broken
	_, err := resolveAndGetFromStore(context.Background(), s, "bd-abc1", false)
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want %v", err, broken)
	}
	if want := []string{"GetIssue bd-abc1"}; !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls = %q, want %q", s.calls, want)
	}
}

// resolveCountingWire is the http transport, counting each request it serves.
// It answers getIssue from rows and refuses every other operation; the nil
// embedded WireClient panics on anything else, such as a handshake.
type resolveCountingWire struct {
	httpclient.WireClient

	rows     map[string]types.Issue
	requests []string
}

func (w *resolveCountingWire) Preflight(context.Context, string) error { return nil }

func (w *resolveCountingWire) Do(_ context.Context, req wire.Request, out any) error {
	w.requests = append(w.requests, req.Op+" "+req.IssueID)
	if req.Op != wire.OpGetIssue {
		return fmt.Errorf("resolveCountingWire: unexpected %s", req.Op)
	}
	row, ok := w.rows[req.IssueID]
	if !ok {
		return fmt.Errorf("%w: issue %s", issueops.ErrNotFound, req.IssueID)
	}
	details, ok := out.(*types.IssueDetails)
	if !ok {
		return fmt.Errorf("resolveCountingWire: getIssue decoded into %T", out)
	}
	*details = types.IssueDetails{Issue: row, Revision: types.RevisionToken(1)}
	return nil
}

// TestResolveAndGetFromStoreHTTPRequestCountsUnchanged pins that the http
// backend, reached through a decorator as the CLI reaches it, keeps today's
// requests. Its resolver's exact search already is a getIssue, and a miss is
// not memoized, so the direct read would add a request on every miss.
func TestResolveAndGetFromStoreHTTPRequestCountsUnchanged(t *testing.T) {
	base, err := url.Parse("http://127.0.0.1:7777")
	if err != nil {
		t.Fatal(err)
	}
	transport := &resolveCountingWire{rows: map[string]types.Issue{
		"bd-abc1": {ID: "bd-abc1", Title: "served"},
	}}
	var s storage.DoltStorage = storage.NewHookFiringStore(httpclient.New(httpclient.Target{BaseURL: base}, transport, nil), nil)

	t.Run("hit", func(t *testing.T) {
		transport.requests = nil
		result, err := resolveAndGetFromStore(context.Background(), s, "bd-abc1", false)
		if err != nil {
			t.Fatalf("resolveAndGetFromStore: %v", err)
		}
		if result.ResolvedID != "bd-abc1" || result.Issue.Title != "served" {
			t.Fatalf("result = %+v, want bd-abc1", result)
		}
		// The resolver's exact lookup, then the command's GetIssue. Over a
		// real client the second is answered by the read memo.
		if want := []string{"getIssue bd-abc1", "getIssue bd-abc1"}; !reflect.DeepEqual(transport.requests, want) {
			t.Fatalf("requests = %q, want %q", transport.requests, want)
		}
	})

	t.Run("miss", func(t *testing.T) {
		transport.requests = nil
		_, err := resolveAndGetFromStore(context.Background(), s, "bd-zzz9", false)
		if err == nil {
			t.Fatal("resolveAndGetFromStore found bd-zzz9")
		}
		if !errors.Is(err, storage.ErrNotFound) || !isNotFoundErr(err) {
			t.Fatalf("err = %v, want a not-found", err)
		}
		const wantMsg = `no issue found matching "bd-zzz9" (this backend resolves exact ids only; partial-id search is not available)`
		if err.Error() != wantMsg {
			t.Fatalf("err = %q, want %q", err.Error(), wantMsg)
		}
		if want := []string{"getIssue bd-zzz9"}; !reflect.DeepEqual(transport.requests, want) {
			t.Fatalf("requests = %q, want %q", transport.requests, want)
		}
	})
}
