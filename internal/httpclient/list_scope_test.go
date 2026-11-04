package httpclient

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// listScopeCases are S15b's three ListRequest members, each with the
// listIssues parameter it rides and the behavior token that gates it:
// `bd list --no-assignee` (gc's on_boot recovery), `--title` (the PR-intake
// formula) and `--id` (the pr-review skill).
var listScopeCases = []struct {
	name, param, token string
	set                func(*issueops.ListRequest)
	want               []string
}{
	{"NoAssignee", "no_assignee", wire.CapListNoAssignee,
		func(r *issueops.ListRequest) { r.NoAssignee = true }, []string{"true"}},
	{"TitleSearch", "title", wire.CapListTitle,
		func(r *issueops.ListRequest) { r.TitleSearch = "Routed open task" }, []string{"Routed open task"}},
	// `--title-contains` compiles to the same predicate as `--title`, so it
	// folds onto `title` behind the same token.
	{"TitleContains", "title", wire.CapListTitle,
		func(r *issueops.ListRequest) { r.TitleContains = "Routed" }, []string{"Routed"}},
	{"IDFilter", "id", wire.CapListIDs,
		// Split the way the role splits it, one `id` value per member.
		func(r *issueops.ListRequest) { r.IDFilter = "e2e-r1, e2e-a1,e2e-r1" }, []string{"e2e-r1", "e2e-a1"}},
}

// TestListScopeFiltersRideTheirParametersBehindTheirTokens is S15b's client
// half: each member reaches listIssues as its own parameter once the
// handshake advertises its token.
func TestListScopeFiltersRideTheirParametersBehindTheirTokens(t *testing.T) {
	for _, tc := range listScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			served := &apigen.ContextResponse{Capabilities: []string{"issues.list", tc.token}}
			w := newCountingWire(`{"items":[],"has_more":false}`)
			limit := 10
			req := issueops.ListRequest{Limit: &limit}
			tc.set(&req)
			if _, err := listRoleWithSnapshot(t, w, served).List(t.Context(), req); err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := w.query(t)[tc.param]; !slices.Equal(got, tc.want) {
				t.Errorf("query[%s] = %v, want %v (full query: %v)", tc.param, got, tc.want, w.query(t))
			}
		})
	}
}

// TestListScopeFiltersRefuseLocallyWhenTheServerLacksTheToken is the skew
// side: against a server that does not advertise a member's token (the
// deployed reference server, for one), a request carrying it refuses BEFORE
// dialing with the typed capability error naming that token. Sending it would
// cost a guaranteed 400 unknown_parameter; dropping it would answer a wider
// listing than asked for — for gc's on_boot recovery, every in-progress bead,
// assigned or not.
func TestListScopeFiltersRefuseLocallyWhenTheServerLacksTheToken(t *testing.T) {
	for _, tc := range listScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			// Every token but this one: each member is gated by its own.
			var caps []string
			for _, other := range listScopeCases {
				if other.token != tc.token {
					caps = append(caps, other.token)
				}
			}
			masked := &apigen.ContextResponse{Capabilities: append([]string{"issues.list", wire.CapListSort}, caps...)}
			w := newCountingWire(`{"items":[],"has_more":false}`)
			req := issueops.ListRequest{}
			tc.set(&req)
			_, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), req)
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("List error = %v, want *storage.ErrUnsupported", err)
			}
			if unsup.Capability != tc.token || unsup.Op != "Reader.List" {
				t.Errorf("refusal = {Op: %q, Capability: %q}, want {Reader.List, %s}", unsup.Op, unsup.Capability, tc.token)
			}
			if !strings.Contains(err.Error(), tc.token) {
				t.Errorf("refusal text %q does not name %s; a user cannot tell which filter the server lacks", err, tc.token)
			}
			if len(w.dialed) != 0 {
				t.Errorf("dialed %d times, want 0: a pre-dial refusal must never reach the wire", len(w.dialed))
			}

			// A listing without the member is unaffected by the missing token.
			limit := 10
			if _, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), issueops.ListRequest{Limit: &limit}); err != nil {
				t.Fatalf("List without %s against the same server: %v", tc.name, err)
			}
			if q := w.query(t); q.Has(tc.param) {
				t.Errorf("a listing without %s sent %s: %v", tc.name, tc.param, q)
			}
		})
	}
}

// TestListTitleContainsThatDiffersFromTitleRefusesBeforeDialing: a `--title`
// and `--title-contains` that differ cannot both ride `title`, so the leftover
// TitleContains rides `title_contains`, behind issues.list.filters. Against a
// server that serves the title token but not that one, the request refuses,
// naming the token, without a round trip and without dropping either half.
func TestListTitleContainsThatDiffersFromTitleRefusesBeforeDialing(t *testing.T) {
	served := &apigen.ContextResponse{Capabilities: []string{"issues.list", wire.CapListTitle}}
	w := newCountingWire(`{"items":[],"has_more":false}`)
	req := issueops.ListRequest{TitleSearch: "Routed", TitleContains: "task"}
	_, err := listRoleWithSnapshot(t, w, served).List(t.Context(), req)
	if err == nil {
		t.Fatal("List with a differing --title/--title-contains pair succeeded; one half was dropped")
	}
	if !strings.Contains(err.Error(), wire.CapListFilters) {
		t.Errorf("refusal %q does not name %s", err, wire.CapListFilters)
	}
	if len(w.dialed) != 0 {
		t.Errorf("dialed %d times, want 0", len(w.dialed))
	}
}
