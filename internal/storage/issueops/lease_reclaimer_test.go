package issueops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	publicops "github.com/steveyegge/beads/issueops"
)

// TestValidateReclaimRequestRefusesInTheDocumentedOrder pins the database-free
// tier of the role's refusals: each rule on both sides, and the cap checked
// before the blank scan, against the request as sent.
func TestValidateReclaimRequestRefusesInTheDocumentedOrder(t *testing.T) {
	overCap := make([]string, publicops.MaxReclaimIDs+1)
	atCap := make([]string, publicops.MaxReclaimIDs)
	for i := range atCap {
		atCap[i] = "bd-x"
	}
	for _, test := range []struct {
		name    string
		request publicops.ReclaimRequest
		wantErr bool
		wantCap bool
	}{
		{"a bare valid request", publicops.ReclaimRequest{Actor: "reaper"}, false, false},
		{"a zero grace window", publicops.ReclaimRequest{Actor: "reaper", OlderThan: 0}, false, false},
		{"exactly the cap", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{IDs: atCap}}, false, false},
		{"no actor", publicops.ReclaimRequest{}, true, false},
		{"a blank actor", publicops.ReclaimRequest{Actor: "  "}, true, false},
		{"a negative grace window", publicops.ReclaimRequest{Actor: "reaper", OlderThan: -time.Nanosecond}, true, false},
		{"a blank id", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{IDs: []string{"bd-1", ""}}}, true, false},
		{"a blank assignee", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{Assignees: []string{"w", " "}}}, true, false},
		{"a blank label", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{Labels: []string{""}}}, true, false},
		{"a blank label-any", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{LabelsAny: []string{"\t"}}}, true, false},
		{"a blank exclude", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{ExcludeLabels: []string{"x", ""}}}, true, false},
		// Every entry blank AND one past the cap: the cap answers, so a caller
		// that sent 1001 ids learns about the cap rather than about an entry.
		{"over the cap of blanks", publicops.ReclaimRequest{Actor: "reaper", Filter: publicops.ReclaimFilter{IDs: overCap}}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateReclaimRequest(test.request)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateReclaimRequest = %v, want error %v", err, test.wantErr)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("ValidateReclaimRequest = %v, want ErrValidation", err)
			}
			var capErr *publicops.TooManyReclaimIDsError
			if got := errors.As(err, &capErr); got != test.wantCap {
				t.Fatalf("errors.As(*TooManyReclaimIDsError) = %v, want %v (err %v)", got, test.wantCap, err)
			}
			var fieldErr *publicops.ReclaimFieldError
			if !test.wantCap && (!errors.As(err, &fieldErr) || fieldErr.Field == "") {
				t.Fatalf("refusal %v names no field; every refusal but the cap is a *ReclaimFieldError", err)
			}
		})
	}
}

// TestExecuteReclaimInTxRefusesBeforeAnyQuery pins "a request that fails
// validation never reaches storage": the mock expects nothing, so any query
// would fail the test.
func TestExecuteReclaimInTxRefusesBeforeAnyQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	_, err = ExecuteReclaimInTx(context.Background(), db, publicops.ReclaimRequest{})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("ExecuteReclaimInTx with no actor = %v, want ErrValidation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestReclaimVersionCommitComposesNothingForANoOp pins the shared commit
// composition: no entry for a sweep that reverted nothing, and one entry
// naming the count, staging issues and events, for one that did.
func TestReclaimVersionCommitComposesNothingForANoOp(t *testing.T) {
	if tables, msg := ReclaimVersionCommit(publicops.ReclaimResult{Reclaimed: []publicops.ReclaimedLease{}}); len(tables) != 0 || msg != "" {
		t.Fatalf("a no-op sweep composed tables=%v msg=%q, want neither", tables, msg)
	}
	tables, msg := ReclaimVersionCommit(publicops.ReclaimResult{Reclaimed: []publicops.ReclaimedLease{{ID: "a"}, {ID: "b"}}})
	if !tables["issues"] || !tables["events"] || len(tables) != 2 {
		t.Fatalf("tables = %v, want issues and events", tables)
	}
	if !strings.Contains(msg, "reclaim 2 expired lease(s)") {
		t.Fatalf("message = %q, want it to name the two reverted leases", msg)
	}
}
