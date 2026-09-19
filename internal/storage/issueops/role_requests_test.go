package issueops

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestValidateClaimNextRequestRejectsPagingKnobs pins the rule that makes the
// claim's filter honest. The scan clears Limit and Offset itself, so accepting
// them would mean silently dropping a field a caller spelled — and a caller
// who wrote --limit 1 expecting a bounded claim would get an unbounded one
// with no way to find out.
func TestValidateClaimNextRequestRejectsPagingKnobs(t *testing.T) {
	limit := 1
	for _, tc := range []struct {
		name    string
		request publicops.ClaimNextRequest
		wantErr bool
	}{
		{
			name:    "actor and no paging is valid",
			request: publicops.ClaimNextRequest{Actor: "worker"},
		},
		{
			name:    "missing actor",
			request: publicops.ClaimNextRequest{},
			wantErr: true,
		},
		{
			name:    "limit set, even to zero",
			request: publicops.ClaimNextRequest{Actor: "worker", Filter: publicops.ReadyRequest{Limit: new(int)}},
			wantErr: true,
		},
		{
			name:    "limit set to a real page size",
			request: publicops.ClaimNextRequest{Actor: "worker", Filter: publicops.ReadyRequest{Limit: &limit}},
			wantErr: true,
		},
		{
			name:    "offset set",
			request: publicops.ClaimNextRequest{Actor: "worker", Filter: publicops.ReadyRequest{Offset: 3}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClaimNextRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateClaimNextRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateClaimNextRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

func TestValidateCloseBatchRequest(t *testing.T) {
	item := publicops.BatchCloseItem{IssueID: "bd-1"}
	limit := 1
	for _, tc := range []struct {
		name    string
		request publicops.CloseBatchRequest
		wantErr bool
	}{
		{
			name:    "one item is valid",
			request: publicops.CloseBatchRequest{Actor: "worker", Items: []publicops.BatchCloseItem{item}},
		},
		{
			name:    "missing actor",
			request: publicops.CloseBatchRequest{Items: []publicops.BatchCloseItem{item}},
			wantErr: true,
		},
		{
			name:    "no items",
			request: publicops.CloseBatchRequest{Actor: "worker"},
			wantErr: true,
		},
		{
			name:    "item without an id",
			request: publicops.CloseBatchRequest{Actor: "worker", Items: []publicops.BatchCloseItem{item, {}}},
			wantErr: true,
		},
		{
			// The embedded claim answers to the claim's own rules, by
			// reference: restating them here is how the two would drift.
			name: "claim next carrying a limit",
			request: publicops.CloseBatchRequest{
				Actor:     "worker",
				Items:     []publicops.BatchCloseItem{item},
				ClaimNext: &publicops.ReadyRequest{Limit: &limit},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCloseBatchRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateCloseBatchRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateCloseBatchRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

// TestCloseBatchCommitMessageNamesWhatLanded pins that a skipped id stays out
// of the history entry. Composing the message from the REQUEST would put an id
// in `bd dolt log` that the commit never touched — and so would composing it
// from every nil-Err outcome, because an idempotent re-close is a per-item
// success that touched nothing (issueops/batchcloser.go:60-64, 119-122).
func TestCloseBatchCommitMessageNamesWhatLanded(t *testing.T) {
	refused := publicops.CloseOutcome{IssueID: "bd-2", Err: errors.New("refused")}
	landed := func(id string) publicops.CloseOutcome {
		return publicops.CloseOutcome{IssueID: id, Changed: true, Issue: &types.Issue{ID: id}}
	}
	landedWisp := func(id string) publicops.CloseOutcome {
		return publicops.CloseOutcome{IssueID: id, Changed: true, Issue: &types.Issue{ID: id, Ephemeral: true}}
	}
	for _, tc := range []struct {
		name   string
		result publicops.CloseBatchResult
		want   string
	}{
		{
			name: "every id landed",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				landed("bd-1"), landed("bd-2"), landed("bd-3"),
			}},
			want: "bd: close bd-1, bd-2, bd-3",
		},
		{
			name: "a refused id is not named",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				landed("bd-1"), refused, landed("bd-3"),
			}},
			want: "bd: close bd-1, bd-3",
		},
		{
			name: "an idempotent re-close is not named",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				landed("bd-1"), {IssueID: "bd-3"},
			}},
			want: "bd: close bd-1",
		},
		{
			name:   "nothing landed records no entry",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{refused}},
			want:   "",
		},
		{
			name: "an all-idempotent batch records no entry",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				{IssueID: "bd-1"}, {IssueID: "bd-3"},
			}},
			want: "",
		},
		{
			name: "a wisp landing is not named",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				landed("bd-1"), landedWisp("bd-wisp-2"),
			}},
			want: "bd: close bd-1",
		},
		{
			name: "an ephemeral-only batch is counted, never named",
			result: publicops.CloseBatchResult{Outcomes: []publicops.CloseOutcome{
				landedWisp("bd-wisp-1"), landedWisp("bd-wisp-2"),
			}},
			want: "bd: close 2 ephemeral items",
		},
		{
			name: "an ephemeral-only batch that claimed names the claim",
			result: publicops.CloseBatchResult{
				Outcomes:    []publicops.CloseOutcome{landedWisp("bd-wisp-1")},
				ClaimedNext: &publicops.IssueWithCounts{Issue: &types.Issue{ID: "bd-9"}},
			},
			want: "bd: close 1 ephemeral item; claim bd-9",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CloseBatchCommitMessage(tc.result); got != tc.want {
				t.Fatalf("CloseBatchCommitMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
