package httpclient

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// listFilterRequest populates exactly one encode.ListFiltersMembers member of a
// ListRequest with a value that constrains something, by reflection over the
// member list itself so a member added there is swept here without a new row.
func listFilterRequest(t *testing.T, member string) issueops.ListRequest {
	t.Helper()
	var req issueops.ListRequest
	f := reflect.ValueOf(&req).Elem().FieldByName(member)
	if !f.IsValid() {
		t.Fatalf("ListRequest has no member %s", member)
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString("x")
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Slice:
		f.Set(reflect.ValueOf([]string{"epic"}))
	case reflect.Pointer:
		switch f.Type().Elem() {
		case reflect.TypeOf(time.Time{}):
			at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
			f.Set(reflect.ValueOf(&at))
		case reflect.TypeOf(0):
			zero := 0 // priority 0 is a real priority: a set pointer is set
			f.Set(reflect.ValueOf(&zero))
		case reflect.TypeOf(issueops.MolType("")):
			mt := issueops.MolType("swarm")
			f.Set(reflect.ValueOf(&mt))
		case reflect.TypeOf(issueops.WispType("")):
			wt := issueops.WispType("patrol")
			f.Set(reflect.ValueOf(&wt))
		default:
			t.Fatalf("member %s: no probe value for %s", member, f.Type())
		}
	default:
		t.Fatalf("member %s: no probe value for kind %s", member, f.Kind())
	}
	if member == "TitleContains" {
		// A lone TitleContains folds onto `title` (encode.FoldTitleContains)
		// and asks only for issues.list.title; it rides title_contains only
		// beside a TitleSearch that differs from it.
		req.TitleSearch = "y"
	}
	return req
}

// TestListFiltersRideListIssuesBehindTheToken is the client half of
// issues.list.filters: against a server that advertises it, each member
// reaches listIssues as its parameter (listTable's mapping), never dropped.
func TestListFiltersRideListIssuesBehindTheToken(t *testing.T) {
	table, ok := encode.TableFor(encode.OpListIssues, "")
	if !ok {
		t.Fatal("no listIssues table")
	}
	paramOf := map[string]string{}
	for _, e := range table.Fields {
		paramOf[e.Name] = e.Param
	}
	served := &apigen.ContextResponse{Capabilities: []string{"issues.list", wire.CapListTitle, wire.CapListFilters}}
	for _, member := range encode.ListFiltersMembers {
		t.Run(member, func(t *testing.T) {
			w := newCountingWire(`{"items":[],"has_more":false}`)
			limit := 10
			req := listFilterRequest(t, member)
			req.Limit = &limit
			if _, err := listRoleWithSnapshot(t, w, served).List(t.Context(), req); err != nil {
				t.Fatalf("List: %v", err)
			}
			param := paramOf[member]
			if param == "" {
				t.Fatalf("listTable maps %s to no parameter", member)
			}
			if !w.query(t).Has(param) {
				t.Errorf("%s did not reach listIssues as %s (query %v)", member, param, w.query(t))
			}
		})
	}
}

// TestListFiltersRefuseLocallyWhenTheServerLacksTheToken is the skew side, for
// every member: against a server that advertises every OTHER list token but
// not issues.list.filters (the deployed reference server's shape and newer),
// a request carrying one refuses BEFORE dialing with the typed capability
// error naming the token. Sending it would earn a 400 unknown_parameter;
// dropping it would answer a wider listing than asked for.
func TestListFiltersRefuseLocallyWhenTheServerLacksTheToken(t *testing.T) {
	masked := &apigen.ContextResponse{Capabilities: []string{
		"issues.list", wire.CapListSort, wire.CapListSearch, wire.CapListNoAssignee, wire.CapListTitle, wire.CapListIDs,
	}}
	for _, member := range encode.ListFiltersMembers {
		t.Run(member, func(t *testing.T) {
			w := newCountingWire(`{"items":[],"has_more":false}`)
			_, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), listFilterRequest(t, member))
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("List error = %v, want *storage.ErrUnsupported", err)
			}
			if unsup.Capability != wire.CapListFilters || unsup.Op != "Reader.List" {
				t.Errorf("refusal = {Op: %q, Capability: %q}, want {Reader.List, %s}", unsup.Op, unsup.Capability, wire.CapListFilters)
			}
			if !strings.Contains(err.Error(), wire.CapListFilters) {
				t.Errorf("refusal text %q does not name %s", err, wire.CapListFilters)
			}
			if len(w.dialed) != 0 {
				t.Errorf("dialed %d times, want 0: a pre-dial refusal must never reach the wire", len(w.dialed))
			}
		})
	}

	// A listing with none of the members is unaffected by the missing token,
	// and an EMPTY exclusion list constrains nothing, so it asks for nothing.
	w := newCountingWire(`{"items":[],"has_more":false}`)
	limit := 10
	if _, err := listRoleWithSnapshot(t, w, masked).List(t.Context(), issueops.ListRequest{Limit: &limit, ExcludeTypes: []string{}}); err != nil {
		t.Fatalf("List without any filter member against the same server: %v", err)
	}
}
