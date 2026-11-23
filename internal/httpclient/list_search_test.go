package httpclient

import (
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

func listRoleWithSnapshot(t *testing.T, w *countingWire, snap *apigen.ContextResponse) issueops.Reader {
	t.Helper()
	reader, err := New(testTarget(t), w, snap).IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	return reader
}

// TestListQueryRidesQBehindTheSearchCapability is S26's client half: a
// ListRequest carrying Query reaches listIssues as `q` once the handshake
// advertises issues.list.search.
func TestListQueryRidesQBehindTheSearchCapability(t *testing.T) {
	served := &apigen.ContextResponse{Capabilities: []string{"issues.list", wire.CapListSearch}}
	w := newCountingWire(`{"items":[],"has_more":false}`)
	limit := 10
	if _, err := listRoleWithSnapshot(t, w, served).List(t.Context(), issueops.ListRequest{Query: "login bug", Limit: &limit}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := w.query(t)["q"]; !slices.Equal(got, []string{"login bug"}) {
		t.Errorf("query[q] = %v, want [login bug] (full query: %v)", got, w.query(t))
	}
}

// TestListQueryRefusesLocallyWhenTheServerLacksTheSearchCapability is the skew
// side: against a server that does not advertise issues.list.search (the
// deployed reference server, for one), a search refuses BEFORE dialing with
// the typed capability error. Sending it would cost a guaranteed 400
// unknown_parameter; dropping `q` would answer the unsearched listing as the
// search.
func TestListQueryRefusesLocallyWhenTheServerLacksTheSearchCapability(t *testing.T) {
	masked := &apigen.ContextResponse{Capabilities: []string{"issues.list", wire.CapListSort}}
	w := newCountingWire(`{"items":[],"has_more":false}`)
	_, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), issueops.ListRequest{Query: "login bug"})
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("List error = %v, want *storage.ErrUnsupported", err)
	}
	if unsup.Capability != wire.CapListSearch || unsup.Op != "Reader.List" {
		t.Errorf("refusal = {Op: %q, Capability: %q}, want {Reader.List, %s}", unsup.Op, unsup.Capability, wire.CapListSearch)
	}
	if len(w.dialed) != 0 {
		t.Errorf("dialed %d times, want 0: a pre-dial refusal must never reach the wire", len(w.dialed))
	}

	// A listing without Query is unaffected by the missing token.
	limit := 10
	if _, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), issueops.ListRequest{Limit: &limit}); err != nil {
		t.Fatalf("List without Query against the same server: %v", err)
	}
	if q := w.query(t); q.Has("q") {
		t.Errorf("a listing without Query sent q: %v", q)
	}
}
