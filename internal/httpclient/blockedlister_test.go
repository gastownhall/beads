package httpclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of the BlockedLister client leg: the preflight, the encoding
// and the projection, over the real wire client against a doctored server.
// The contract itself runs against a real bd serve in
// served_blocked_lister_test.go.

type blockedStub struct {
	mu      sync.Mutex
	queries []url.Values
	reply   string
}

func (b *blockedStub) store(t *testing.T, caps ...string) *Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wire.PathContext {
			ctx := v0Context("proj-blk")
			ctx.Capabilities = caps
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ctx)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != wire.PathBlocked {
			http.NotFound(w, r)
			return
		}
		b.mu.Lock()
		b.queries = append(b.queries, r.URL.Query())
		reply := b.reply
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := wire.New(base, nil, wire.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return New(Target{BaseURL: base}, client, nil)
}

func (b *blockedStub) dials() []url.Values {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]url.Values(nil), b.queries...)
}

func blockedListerOf(t *testing.T, s *Store) issueops.BlockedLister {
	t.Helper()
	role, err := s.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister(): %v", err)
	}
	return role
}

// TestBlockedAgainstAServerWithoutTheTokenRefusesBeforeTheDial is the skew
// row for a new client on an old server: no blocked.list in the handshake
// means a case-2 refusal NAMING the token, with nothing sent, and never a
// listing composed from other reads.
func TestBlockedAgainstAServerWithoutTheTokenRefusesBeforeTheDial(t *testing.T) {
	stub := &blockedStub{reply: `{"items":[],"has_more":false}`}
	s := stub.store(t, "issues.list", "ready.list", "dependencies.blocking", "dependencies.list")

	_, err := blockedListerOf(t, s).ListBlocked(t.Context(), issueops.BlockedRequest{})
	var capErr *wire.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("ListBlocked on an old server = %v, want *wire.CapabilityError", err)
	}
	if capErr.Capability != "blocked.list" {
		t.Errorf("CapabilityError.Capability = %q, want blocked.list", capErr.Capability)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) || unsupported.Capability != "blocked.list" {
		t.Errorf("ListBlocked on an old server = %v, want it to unwrap to *storage.ErrUnsupported{Capability: blocked.list}", err)
	}
	if !strings.Contains(err.Error(), "blocked.list") {
		t.Errorf("the refusal %q does not name the token", err)
	}
	if n := len(stub.dials()); n != 0 {
		t.Errorf("%d blocked GETs reached an old server, want 0", n)
	}
	// The legacy door refuses the same way.
	if _, err := s.GetBlockedIssues(t.Context(), types.WorkFilter{}); !errors.As(err, &capErr) {
		t.Errorf("GetBlockedIssues on an old server = %v, want *wire.CapabilityError", err)
	}
}

// TestBlockedEncodesTheRequestNormalized: every field reaches its parameter,
// labels repeated and normalized, nothing for an empty field.
func TestBlockedEncodesTheRequestNormalized(t *testing.T) {
	stub := &blockedStub{reply: `{"items":[],"has_more":false}`}
	s := stub.store(t, "blocked.list")
	lister := blockedListerOf(t, s)

	if _, err := lister.ListBlocked(t.Context(), issueops.BlockedRequest{
		ParentID:      "bd-p",
		Labels:        []string{" a ", "", "b", "a"},
		LabelsAny:     []string{"c"},
		ExcludeLabels: []string{" d"},
	}); err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if _, err := lister.ListBlocked(t.Context(), issueops.BlockedRequest{}); err != nil {
		t.Fatalf("ListBlocked(empty): %v", err)
	}
	dials := stub.dials()
	if len(dials) != 2 {
		t.Fatalf("dials = %d, want 2", len(dials))
	}
	want := url.Values{"parent": {"bd-p"}, "label": {"a", "b"}, "label_any": {"c"}, "exclude_label": {"d"}}
	if !reflect.DeepEqual(dials[0], want) {
		t.Errorf("query = %v, want %v", dials[0], want)
	}
	if len(dials[1]) != 0 {
		t.Errorf("an empty request sent %v, want no parameters", dials[1])
	}
}

// TestBlockedProjectsTheAnswer: rows come back as the server sent them, in
// its order, never nil; an empty answer is an empty, non-nil slice.
func TestBlockedProjectsTheAnswer(t *testing.T) {
	stub := &blockedStub{reply: `{"items":[` +
		`{"id":"bd-2","title":"two","priority":0,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["bd-9"]},` +
		`{"id":"bd-1","title":"one","priority":1,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["bd-2"]}` +
		`],"has_more":false}`}
	s := stub.store(t, "blocked.list")

	result, err := blockedListerOf(t, s).ListBlocked(t.Context(), issueops.BlockedRequest{})
	if err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "bd-2" || result.Items[1].ID != "bd-1" {
		t.Fatalf("items = %v, want bd-2 then bd-1", blockedRowIDs(result.Items))
	}
	if !reflect.DeepEqual(result.Items[0].BlockedBy, []string{"bd-9"}) || result.Items[0].BlockedByCount != 1 {
		t.Errorf("bd-2 = %+v, want blocked by bd-9", result.Items[0])
	}

	stub.reply = `{"items":[],"has_more":false}`
	result, err = blockedListerOf(t, s).ListBlocked(t.Context(), issueops.BlockedRequest{})
	if err != nil {
		t.Fatalf("ListBlocked(empty): %v", err)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Errorf("empty answer Items = %#v, want an empty non-nil slice", result.Items)
	}
}

// TestLegacyGetBlockedIssuesRidesTheRoleAndPages: the raw door dials the
// role's operation and applies the filter's page to its answer.
func TestLegacyGetBlockedIssuesRidesTheRoleAndPages(t *testing.T) {
	stub := &blockedStub{reply: `{"items":[` +
		`{"id":"bd-1","title":"a","priority":0,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["x"]},` +
		`{"id":"bd-2","title":"b","priority":1,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["x"]},` +
		`{"id":"bd-3","title":"c","priority":2,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["x"]}` +
		`],"has_more":false}`}
	s := stub.store(t, "blocked.list")
	parent := "bd-p"

	got, err := s.GetBlockedIssues(t.Context(), types.WorkFilter{ParentID: &parent, Labels: []string{"l"}, Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if ids := blockedRowIDs(got); !reflect.DeepEqual(ids, []string{"bd-2"}) {
		t.Errorf("page = %v, want [bd-2]", ids)
	}
	dials := stub.dials()
	if len(dials) != 1 || dials[0].Get("parent") != "bd-p" || dials[0].Get("label") != "l" {
		t.Errorf("dials = %v, want one carrying parent and label", dials)
	}
	if _, err := s.GetBlockedIssues(t.Context(), types.WorkFilter{MaxRows: 2, MaxRowsSource: "--max-rows"}); err == nil {
		t.Errorf("GetBlockedIssues over its row cap answered, want the cap's refusal")
	}
}

func blockedRowIDs(items []*types.BlockedIssue) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// TestBlockedRefusesAServerThatReportsMore: the listing has no cursor, so a
// server that answers part of it with has_more is refused with a typed error,
// never returned as a short listing a caller would read as complete.
func TestBlockedRefusesAServerThatReportsMore(t *testing.T) {
	stub := &blockedStub{reply: `{"items":[` +
		`{"id":"bd-1","title":"a","priority":0,"created_at":"2026-10-16T01:00:00Z","updated_at":"2026-10-16T01:00:00Z","blocked_by_count":1,"blocked_by":["x"]}` +
		`],"has_more":true}`}
	s := stub.store(t, "blocked.list")

	result, err := blockedListerOf(t, s).ListBlocked(t.Context(), issueops.BlockedRequest{})
	var truncated *TruncatedListingError
	if !errors.As(err, &truncated) {
		t.Fatalf("ListBlocked with has_more = %d items, err %v; want *TruncatedListingError", len(result.Items), err)
	}
	if truncated.Found != 1 || truncated.Op != wire.OpListBlockedIssues {
		t.Errorf("TruncatedListingError = %+v, want Op %s and Found 1", truncated, wire.OpListBlockedIssues)
	}
	if result.Items != nil {
		t.Errorf("a refused listing returned %d rows", len(result.Items))
	}
	if _, err := s.GetBlockedIssues(t.Context(), types.WorkFilter{}); !errors.As(err, &truncated) {
		t.Errorf("GetBlockedIssues with has_more: err %v, want *TruncatedListingError", err)
	}
}
