package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// WHAT THIS FILE STILL OWNS, now that Claimer HAS a conformance contract
// (backend/conformance/claimer_contract.go, wired here by
// claimer_contract_test.go): everything below runs against FAKES, and each case
// is here because the property it pins is not reachable from a real database.
//
//   - the UNIT-OF-WORK ACCOUNTING. How many units of work were opened and how
//     many times they committed is invisible across the role seam; the contract
//     can only see the history the commits leave, and only when there is one.
//   - the REFUSAL THAT SURVIVES AN UNREADABLE ROW. The leaf qualifies the typed
//     conflict with "when the refusing state was readable in the same
//     transaction"; a black-box case cannot make that read fail, so the
//     negative half of the clause lives here and nowhere else.
//   - the ERROR SHAPES THE SEAM NORMALIZES. A bare wrapped sql.ErrNoRows
//     arriving from the repository is a shape only a fake can produce.
//   - VALIDATION BEFORE A CONNECTION. The contract pins that an incomplete
//     request is refused and writes nothing; that it costs no database
//     round-trip to discover is this seam's own promise.
//   - the NIL-PROVIDER accessor refusal, which has no role-level surface.
//
// The overlap that remains — the won CAS, the idempotent re-claim, the two
// refusal sentinels — is deliberate: these cases assert them against a fake
// whose answers are fixed, which is what makes the accounting assertions beside
// them mean anything.

// claimerIssues answers the two calls the claim makes: the CAS and the
// same-transaction read-back. Everything else panics rather than returning a
// zero value.
type claimerIssues struct {
	domain.IssueUseCase
	actors []string
	result domain.ClaimResult
	err    error
	issue  *types.Issue
	getErr error
}

func (f *claimerIssues) ClaimIssue(_ context.Context, _, actor string) (domain.ClaimResult, error) {
	f.actors = append(f.actors, actor)
	return f.result, f.err
}

func (f *claimerIssues) GetIssue(context.Context, string) (*types.Issue, error) {
	return f.issue, f.getErr
}

func newClaimer(t *testing.T, issues *claimerIssues) (publicops.Claimer, *mockUnitOfWork, *mockUnitOfWorkProvider) {
	t.Helper()
	uw := &mockUnitOfWork{issueUseCase: issues}
	provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}
	claimer, err := NewIssueClaimer(provider)
	if err != nil {
		t.Fatalf("NewIssueClaimer: %v", err)
	}
	return claimer, uw, provider
}

// TestClaimerCommitsAWonCASForAnyActor is the happy path and the ruling that
// goes with it: eligibility is decided by the issue's state alone, so an actor
// with no prior relationship to the issue — a guest — wins the CAS like any
// other. The actor is caller-asserted provenance, not authenticated identity.
func TestClaimerCommitsAWonCASForAnyActor(t *testing.T) {
	issues := &claimerIssues{issue: &types.Issue{ID: "bd-1", Assignee: "guest-42", Status: types.StatusInProgress}}
	claimer, uw, provider := newClaimer(t, issues)

	result, err := claimer.Claim(context.Background(), publicops.ClaimRequest{Actor: "guest-42", IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !result.Changed {
		t.Error("a won CAS reported no persisted mutation")
	}
	if result.Issue == nil || result.Issue.Assignee != "guest-42" {
		t.Errorf("result issue = %+v, want the row the CAS wrote", result.Issue)
	}
	if len(issues.actors) != 1 || issues.actors[0] != "guest-42" {
		t.Errorf("CAS actors = %v, want one attempt by guest-42", issues.actors)
	}
	if uw.commitCount != 1 {
		t.Errorf("commits = %d, want exactly one", uw.commitCount)
	}
	if provider.newUOWCalls != 1 {
		t.Errorf("opened %d units of work, want 1", provider.newUOWCalls)
	}
}

// TestClaimerIdempotentReclaimCommitsNothing: the holder re-claiming its own
// in-progress issue changed nothing, so nothing may be committed — a polling
// client must not be able to fill the storage history with empty commits.
func TestClaimerIdempotentReclaimCommitsNothing(t *testing.T) {
	issues := &claimerIssues{
		result: domain.ClaimResult{AlreadyClaimed: true, PriorAssignee: "alice"},
		issue:  &types.Issue{ID: "bd-1", Assignee: "alice", Status: types.StatusInProgress},
	}
	claimer, uw, _ := newClaimer(t, issues)

	result, err := claimer.Claim(context.Background(), publicops.ClaimRequest{Actor: "alice", IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if result.Changed {
		t.Error("a re-claim by the holder reported a persisted mutation")
	}
	if uw.commitCount != 0 {
		t.Errorf("a no-op re-claim committed %d times", uw.commitCount)
	}
}

// TestClaimerReportsTheStateThatLostTheCAS: the refusal keeps its sentinel and
// its prose and gains the state read inside the same transaction, so a caller
// classifies the conflict from typed fields rather than by matching substrings.
func TestClaimerReportsTheStateThatLostTheCAS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
		issue    *types.Issue
	}{
		{
			name: "held by another actor", sentinel: storage.ErrAlreadyClaimed,
			issue: &types.Issue{ID: "bd-1", Assignee: "bob", Status: types.StatusInProgress},
		},
		{
			name: "not in a claimable state", sentinel: storage.ErrNotClaimable,
			issue: &types.Issue{ID: "bd-1", Assignee: "bob", Status: types.StatusClosed},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := fmt.Errorf("claim bd-1: %w: refused", tc.sentinel)
			issues := &claimerIssues{err: refusal, issue: tc.issue}
			claimer, uw, _ := newClaimer(t, issues)

			_, err := claimer.Claim(context.Background(), publicops.ClaimRequest{Actor: "alice", IssueID: "bd-1"})
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want it to match %v", err, tc.sentinel)
			}
			var conflict *publicops.ClaimConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want a *ClaimConflictError", err)
			}
			if conflict.IssueID != "bd-1" || conflict.Assignee != tc.issue.Assignee || conflict.Status != tc.issue.Status {
				t.Errorf("conflict = %#v, want the state the transaction read", conflict)
			}
			if got := conflict.Error(); got != refusal.Error() {
				t.Errorf("Error() = %q, want the refusal unedited (%q)", got, refusal.Error())
			}
			if uw.commitCount != 0 {
				t.Errorf("a refused claim committed %d times", uw.commitCount)
			}
		})
	}
}

// TestClaimerRefusalSurvivesAnUnreadableRow: the state is a courtesy, the
// refusal is not. A failed re-read must leave the refusal exactly as it was
// rather than replacing a precise conflict with the read's error.
//
// The fake hands back a row AND an error, which is the case a nil-check alone
// gets wrong: publishing that row would publish state the transaction never
// successfully read.
func TestClaimerRefusalSurvivesAnUnreadableRow(t *testing.T) {
	refusal := fmt.Errorf("claim bd-1: %w", storage.ErrAlreadyClaimed)
	issues := &claimerIssues{
		err:    refusal,
		issue:  &types.Issue{ID: "bd-1", Assignee: "bob", Status: types.StatusInProgress},
		getErr: errors.New("read failed"),
	}
	claimer, _, _ := newClaimer(t, issues)

	_, err := claimer.Claim(context.Background(), publicops.ClaimRequest{Actor: "alice", IssueID: "bd-1"})
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("err = %v, want the refusal to stand", err)
	}
	var conflict *publicops.ClaimConflictError
	if errors.As(err, &conflict) {
		t.Errorf("err = %#v, want the bare refusal when the state could not be read", conflict)
	}
}

// TestClaimerNormalizesAMissingRow: the CAS reports a miss as a wrapped
// sql.ErrNoRows, but the role promises ErrNotFound — and its store-backed
// sibling answers with exactly that, so a caller must not have to know which
// implementation it holds. A wisp id arrives here the same way: the CAS only
// ever addresses the issues table.
func TestClaimerNormalizesAMissingRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"wrapped ErrNoRows", fmt.Errorf("db: Claim bd-404: read old issue: %w", sql.ErrNoRows)},
		{"already normalized", fmt.Errorf("claim bd-404: %w", storage.ErrNotFound)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &claimerIssues{err: tc.err}
			claimer, _, _ := newClaimer(t, issues)

			_, err := claimer.Claim(context.Background(), publicops.ClaimRequest{Actor: "alice", IssueID: "bd-404"})
			if !errors.Is(err, publicops.ErrNotFound) {
				t.Fatalf("err = %v, want it to match ErrNotFound", err)
			}
		})
	}
}

// TestClaimerRefusesIncompleteRequestsBeforeOpeningAUOW: a request missing an
// actor or an id is a deterministic validation failure and must not cost a
// database connection to discover.
func TestClaimerRefusesIncompleteRequestsBeforeOpeningAUOW(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.ClaimRequest
	}{
		{"no actor", publicops.ClaimRequest{IssueID: "bd-1"}},
		{"no issue", publicops.ClaimRequest{Actor: "alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockUnitOfWorkProvider{newUOWErr: errors.New("unexpected unit-of-work open")}
			claimer, err := NewIssueClaimer(provider)
			if err != nil {
				t.Fatalf("NewIssueClaimer: %v", err)
			}
			if _, err := claimer.Claim(context.Background(), tc.request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
			if provider.newUOWCalls != 0 {
				t.Errorf("an invalid request opened %d units of work", provider.newUOWCalls)
			}
		})
	}
}

// TestNewIssueClaimerRefusesANilProvider: a claimer over no provider would
// panic on first use, so the accessor says so instead.
func TestNewIssueClaimerRefusesANilProvider(t *testing.T) {
	if _, err := NewIssueClaimer(nil); err == nil {
		t.Fatal("NewIssueClaimer(nil) returned no error")
	}
	var typed *doltSQLProvider
	if _, err := NewIssueClaimer(typed); err == nil {
		t.Fatal("NewIssueClaimer of a typed-nil provider returned no error")
	}
}
