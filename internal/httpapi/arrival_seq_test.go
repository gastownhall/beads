package httpapi

import (
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// arrival_seq on the wire (issues.arrivalSeq): the commit-ordered create
// sequence storage stamps on a wisp-plane row. These cases hold the handlers'
// half — the member travels out on every Issue-shaped answer when the role's
// row carries one and is absent when it does not. Whether a row is stamped,
// and that a supplied value is never stored, are the store's (the Journal
// role's arrival cases in backend/conformance, run on every leg).

// TestCreateAnswersTheStoredArrivalSeq pins that the create response carries
// the stamp of the row the role stored, so a caller has it without a second
// read, and omits it for a row that has none.
func TestCreateAnswersTheStoredArrivalSeq(t *testing.T) {
	stamped := createdIssue("bd-wisp")
	stamped.NoHistory = true
	stamped.ArrivalSeq = 77
	for _, tc := range []struct {
		name  string
		issue *types.Issue
		want  any
	}{
		{"a stamped wisp-plane row answers its stamp", stamped, float64(77)},
		{"an unstamped row answers none", createdIssue("bd-durable"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newCreateServer(t, &roleLifecycle{createResult: issueops.CreateResult{Issue: tc.issue}})
			resp := ts.createIssue(t, `{"actor":"alice","title":"t"}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got, present := body["arrival_seq"]; got != tc.want || (tc.want == nil && present) {
				t.Errorf("arrival_seq = %v (present %v), want %v", got, present, tc.want)
			}
		})
	}
}
