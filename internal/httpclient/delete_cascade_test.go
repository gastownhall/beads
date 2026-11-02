package httpclient

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// The cascade member, behind issues.delete.containmentCascade (issue #6301).
// The three rows of version skew, same shape as the gate members:
//
//	new client, new server   a cascade dials and the result carries the
//	                         containment closure's ids in DeletedIDs.
//	new client, old server   the cached handshake lacks the token, so the
//	                         delete refuses BEFORE the dial with the typed
//	                         capability error naming it. Never a silent pass:
//	                         the old server cascades over EVERY dependency
//	                         edge, deleting bystanders joined by association
//	                         edges plus their own subtrees, and no reply
//	                         names them afterwards. A dry-run refuses too —
//	                         its preview would present the over-broad closure
//	                         as the right one.
//	old request, either      a delete naming no `cascade` is untouched by the
//	                         token, so it dials the same server either way.

// TestDeleteCascadeRefusesBeforeDialingWhenUnadvertised: the skew is not
// eatable on this member. A cascade against an older server is not a 400 to
// catch — it is rows already gone that this build's rule would have kept, and
// a dry-run against it is a preview that confidently names the wrong set.
func TestDeleteCascadeRefusesBeforeDialingWhenUnadvertised(t *testing.T) {
	guard := int64(41)
	for _, tc := range []struct {
		name string
		req  issueops.DeleteRequest
	}{
		{"a real cascade", issueops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true, Force: true}},
		{"a cascade dry-run", issueops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true, DryRun: true}},
		{"a cascade preview", issueops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true, DryRun: true, Force: true}},
		{"cascade with a guard", issueops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true, ExpectedVersion: &guard}},
		{"cascade with an actor", issueops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true, Force: true, Actor: "alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{deleted: &apigen.DeleteIssuesResult{}}
			deleter, err := stubStore(t, w).Deleter() // stubStore's server predates the token
			if err != nil {
				t.Fatal(err)
			}
			_, err = deleter.Delete(t.Context(), tc.req)
			requireCapabilityRefusal(t, err, wire.CapDeleteContainmentCascade, w)
		})
	}
}

// TestDeleteCascadeDialsWhenAdvertised: with the token in the cached handshake
// the same requests pass, and the wire body carries the member verbatim.
func TestDeleteCascadeDialsWhenAdvertised(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		w := &stubWire{deleted: &apigen.DeleteIssuesResult{Deleted: 2}}
		deleter, err := gateServer(t, w, wire.CapDeleteContainmentCascade).Deleter()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deleter.Delete(t.Context(), issueops.DeleteRequest{
			IDs: []string{"bd-root"}, Cascade: true, Force: true, DryRun: dryRun,
		}); err != nil {
			t.Fatalf("delete with the token advertised: %v", err)
		}
		if w.lastDelete.Cascade == nil || !*w.lastDelete.Cascade {
			t.Errorf("cascade = %v on the wire, want true", w.lastDelete.Cascade)
		}
		if dryRun && (w.lastDelete.DryRun == nil || !*w.lastDelete.DryRun) {
			t.Errorf("dry_run = %v on the wire, want true", w.lastDelete.DryRun)
		}
		if len(w.calls) != 1 {
			t.Errorf("dialed %v, want one deleteIssues call", w.calls)
		}
	}
}

// TestDeleteCascadeMapsTheClosureIDs: deleted_ids is the result member a
// cascade preview lives on — `deleted` counts the closure without naming it.
// It maps when the server sent it and stays absent when it did not, which is
// what keeps an older server answerable with no invented list.
func TestDeleteCascadeMapsTheClosureIDs(t *testing.T) {
	ids := []string{"bd-root", "bd-child", "bd-grandchild"}
	t.Run("a served list maps verbatim", func(t *testing.T) {
		w := &stubWire{deleted: &apigen.DeleteIssuesResult{Deleted: len(ids), DeletedIds: &ids}}
		deleter, err := gateServer(t, w, wire.CapDeleteContainmentCascade).Deleter()
		if err != nil {
			t.Fatal(err)
		}
		out, err := deleter.Delete(t.Context(), issueops.DeleteRequest{IDs: ids[:1], Cascade: true, Force: true})
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if !slices.Equal(out.DeletedIDs, ids) {
			t.Errorf("DeletedIDs = %v, want %v", out.DeletedIDs, ids)
		}
		if out.Deleted != len(ids) {
			t.Errorf("Deleted = %d, want %d", out.Deleted, len(ids))
		}
	})
	t.Run("an older server's answer stays empty", func(t *testing.T) {
		w := &stubWire{deleted: &apigen.DeleteIssuesResult{Deleted: 2}}
		deleter, err := stubStore(t, w).Deleter()
		if err != nil {
			t.Fatal(err)
		}
		// Non-cascade, so it dials past the refusal; the served result has no
		// member to fill DeletedIDs from.
		out, err := deleter.Delete(t.Context(), issueops.DeleteRequest{IDs: []string{"bd-1"}, Force: true})
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if out.DeletedIDs != nil {
			t.Errorf("DeletedIDs = %v over a member the server never sent, want nil", out.DeletedIDs)
		}
	})
}
