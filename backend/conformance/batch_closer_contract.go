package conformance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.BatchCloser
// must satisfy. There are three of them — the direct store, the embedded store
// and the unit-of-work backend — and the first two share one validate/execute
// body (internal/storage/issueops.ValidateCloseBatchRequest and
// ExecuteCloseBatch) that the third does not, so wiring all three catches
// wrapper and engine divergence on those two and body divergence only on uow.
//
// THE LEAF DOC COMMENT IS THE SPEC. Every case below asserts what
// issueops/batchcloser.go promises, not what an implementation currently does.
// A backend that disagrees is parked at its own WIRING site with
// skipKnownDivergence so the agreeing backends stay pinned; the shared Run
// functions here never bend to accommodate one implementation.
//
// What it pins, clause by clause:
//   - Outcomes mirror Items index for index in request order, refusals
//     included, each echoing its own IssueID (batchcloser.go:96-99, 142-143,
//     75-77).
//   - A per-item refusal is a RESULT of the batch, not an error of it, and the
//     survivors commit (batchcloser.go:87-91, 111-117); Issue is nil exactly when
//     Err is set (batchcloser.go:78-80); the per-item Reason and the
//     request-wide Session land on the rows they were asked for
//     (batchcloser.go:5-8, 41-42).
//   - A non-nil error and populated Outcomes are mutually exclusive
//     (batchcloser.go:119-126).
//   - An idempotent re-close is a per-item success with Changed false, and
//     still reports OpenChildren (batchcloser.go:81-86); a duplicated id is one
//     of those at its own index, and the first occurrence's reason is what the
//     row keeps (batchcloser.go:28-33).
//   - Force is request-wide and bypasses only blocker and open-child policy;
//     the per-item precondition its clause also names is a category the request
//     type keeps EMPTY (batchcloser.go:43-53).
//   - ClaimNext runs after the closes, inside the same transaction, and only
//     when at least one item closed (batchcloser.go:55-70, 100-103, 143-146).
//   - LANDED means CHANGED: an all-idempotent batch earns no claim and records
//     no history entry (batchcloser.go:60-64, 119-122).
//   - A wisp is an admissible item and its close counts toward the claim
//     coupling (batchcloser.go:35-39), but the durable history entry a mixed
//     batch records never names it (batchcloser.go:128-135).
//   - What LANDS is atomic: one transaction, at most one history entry, none
//     when nothing landed (batchcloser.go:119-122).
//   - The method's own error is reserved for validation, cancellation and
//     infrastructure — the failures that mean the batch NEVER RAN, so no
//     outcomes and no durable trace (batchcloser.go:119-126).
//   - The caller's request is never mutated (batchcloser.go:137-140).

// BatchCloserFixture supplies adapter-specific storage access for the
// batch-close assertions.
type BatchCloserFixture struct {
	// IssuePrefix namespaces the ids and labels each assertion seeds, so
	// several of them can share one database. The claim cases lean on it
	// harder than the others: they select their candidate through a label
	// derived from this prefix, because a ready front shared with a sibling
	// case would otherwise decide which row a claim wins.
	IssuePrefix string
	Closer      publicops.BatchCloser
	// CreateIssue seeds a durable issue in the issues plane, labels included.
	CreateIssue func(context.Context, *types.Issue, string) error
	// AddDependency seeds one edge. The contract uses it for the two refusal
	// shapes a close has: a live blocker and an open child.
	AddDependency func(context.Context, *types.Dependency, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane, for the three
	// ephemeral rules the leaf doc states: a wisp is an admissible item and
	// closing one counts toward "at least one item closed"
	// (batchcloser.go:35-39), and the durable history entry never names it
	// (batchcloser.go:128-135).
	CreateWisp func(context.Context, *types.Issue, string) error
	//
	// AddComment seeds one comment on an issue, for the single clause that
	// needs one: the outcome snapshot OMITS comments (batchcloser.go:78-80),
	// which cannot be told from an issue that simply has none.
	//
	// It is not part of the shared per-backend role fixture kit — that kit
	// exposes no comment hook — so each wiring supplies it locally. Folding it
	// into the kit is an S0 follow-up.
	AddComment  func(ctx context.Context, issueID, actor, text string) error
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports the branch's history-entry count, for the
	// entry-per-call clause. Nil means this backend cannot observe history,
	// and the two history cases then skip loudly rather than pass quietly.
	CountHistory func(context.Context) (int, error)
	// CountHistoryMatching is CountHistory narrowed to the entries whose
	// message matches a SQL LIKE pattern ("" = every entry), which is how the
	// wisp-naming case asks whether any entry NAMES an id. Nil means this
	// backend cannot observe history by message, and that case skips loudly.
	// See history_matching.go for the convention.
	CountHistoryMatching func(context.Context, string) (int, error)
}

// RunBatchCloserOutcomesMirrorItemsIndexForIndex pins the index correspondence
// (batchcloser.go:96-99, method doc 142-143) and the id echo (75-77).
//
// The request order is deliberately neither id order nor
// successes-then-refusals, and the refusal sits in the middle, so an
// implementation that sorted, appended, or dropped the refused item lands its
// outcomes at the wrong indices and fails here rather than in a caller that
// prints one issue's verdict under another issue's name.
func RunBatchCloserOutcomesMirrorItemsIndexForIndex(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	third := fixture.IssuePrefix + "-mirror-c"
	first := fixture.IssuePrefix + "-mirror-a"
	second := fixture.IssuePrefix + "-mirror-b"
	missing := fixture.IssuePrefix + "-mirror-absent"
	seedBatchCloserIssue(t, ctx, fixture, third)
	seedBatchCloserIssue(t, ctx, fixture, first)
	seedBatchCloserIssue(t, ctx, fixture, second)

	items := []publicops.BatchCloseItem{
		{IssueID: third},
		{IssueID: missing},
		{IssueID: first},
		{IssueID: second},
	}
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "closer", Items: items})
	if err != nil {
		t.Fatalf("CloseBatch with one refused item returned an error of the batch: %v", err)
	}
	if len(result.Outcomes) != len(items) {
		t.Fatalf("outcomes = %d for %d items (%v), want one per item in request order",
			len(result.Outcomes), len(items), batchCloserOutcomeIDs(result.Outcomes))
	}
	for i, item := range items {
		if result.Outcomes[i].IssueID != item.IssueID {
			t.Errorf("outcome %d echoes %q, want %q; whole echo was %v",
				i, result.Outcomes[i].IssueID, item.IssueID, batchCloserOutcomeIDs(result.Outcomes))
		}
	}
	if result.Outcomes[1].Err == nil {
		t.Errorf("outcome 1 (%s, never seeded) has no error — the refusal was not reported at its own index", missing)
	}
	for _, i := range []int{0, 2, 3} {
		if result.Outcomes[i].Err != nil {
			t.Errorf("outcome %d (%s) = %v, want a success beside the refusal", i, items[i].IssueID, result.Outcomes[i].Err)
		}
	}
	// The first of the three nil-ClaimedNext conditions (batchcloser.go:100-103):
	// this request asked for no claim at all.
	if result.ClaimedNext != nil {
		t.Errorf("ClaimedNext = %s for a request with a nil ClaimNext, want nil", result.ClaimedNext.ID)
	}
}

// RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit is the central case:
// a batch of [closeable, missing, blocked, open-children, closeable] returns a
// NIL error, one typed refusal per bad item, and durably closes both good ones
// (batchcloser.go:87-91, 111-117).
//
// It also pins the two per-item fields nothing else can (batchcloser.go:5-8,
// 41-42): the two survivors carry DISTINCT reasons and one shared session, and
// both are read back off the rows. An implementation that recorded Items[0]'s
// reason against every issue, or dropped reasons entirely, passes every other
// case in this file.
func RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	alpha := fixture.IssuePrefix + "-refusal-alpha"
	beta := fixture.IssuePrefix + "-refusal-beta"
	missing := fixture.IssuePrefix + "-refusal-absent"
	blocked := fixture.IssuePrefix + "-refusal-blocked"
	blocker := fixture.IssuePrefix + "-refusal-blocker"
	parent := fixture.IssuePrefix + "-refusal-parent"
	child := fixture.IssuePrefix + "-refusal-child"
	seedBatchCloserIssue(t, ctx, fixture, alpha)
	seedBatchCloserIssue(t, ctx, fixture, beta)
	seedBatchCloserIssue(t, ctx, fixture, blocked)
	seedBatchCloserIssue(t, ctx, fixture, blocker)
	seedBatchCloserIssue(t, ctx, fixture, parent)
	seedBatchCloserIssue(t, ctx, fixture, child)
	seedBatchCloserEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)
	seedBatchCloserEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	const session = "refusal-session"
	const alphaReason = "alpha finished"
	const betaReason = "beta finished"
	items := []publicops.BatchCloseItem{
		{IssueID: alpha, Reason: alphaReason},
		{IssueID: missing, Reason: "never seeded"},
		{IssueID: blocked, Reason: "still blocked"},
		{IssueID: parent, Reason: "still has a child"},
		{IssueID: beta, Reason: betaReason},
	}
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:   "closer",
		Items:   items,
		Session: session,
	})
	if err != nil {
		t.Fatalf("CloseBatch returned an error of the batch: %v — a per-item refusal is a result, not an error", err)
	}
	if len(result.Outcomes) != len(items) {
		t.Fatalf("outcomes = %d for %d items, want one per item", len(result.Outcomes), len(items))
	}
	for i, item := range items {
		if result.Outcomes[i].IssueID != item.IssueID {
			t.Fatalf("outcome %d echoes %q, want %q", i, result.Outcomes[i].IssueID, item.IssueID)
		}
	}

	for _, survivor := range []struct {
		index  int
		id     string
		reason string
	}{{0, alpha, alphaReason}, {4, beta, betaReason}} {
		outcome := result.Outcomes[survivor.index]
		if outcome.Err != nil {
			t.Errorf("outcome %d (%s) = %v, want a success: the survivors of a mixed batch commit", survivor.index, survivor.id, outcome.Err)
			continue
		}
		if outcome.Issue == nil {
			t.Errorf("outcome %d (%s) has a nil Issue with a nil Err — Issue is nil exactly when Err is set", survivor.index, survivor.id)
			continue
		}
		if !outcome.Changed {
			t.Errorf("outcome %d (%s) Changed = false on a first close, want true", survivor.index, survivor.id)
		}
		if outcome.Issue.Status != types.StatusClosed {
			t.Errorf("outcome %d (%s) snapshot status = %q, want %q", survivor.index, survivor.id, outcome.Issue.Status, types.StatusClosed)
		}
		if outcome.Issue.CloseReason != survivor.reason {
			t.Errorf("outcome %d (%s) snapshot CloseReason = %q, want %q — reasons are per item, not per request",
				survivor.index, survivor.id, outcome.Issue.CloseReason, survivor.reason)
		}
		if outcome.Issue.ClosedBySession != session {
			t.Errorf("outcome %d (%s) snapshot ClosedBySession = %q, want %q — the session is recorded against every item",
				survivor.index, survivor.id, outcome.Issue.ClosedBySession, session)
		}
		assertBatchCloserClosedRow(t, ctx, fixture, survivor.id, survivor.reason, session)
	}

	if !errors.Is(result.Outcomes[1].Err, publicops.ErrNotFound) {
		t.Errorf("outcome 1 (%s) = %v, want ErrNotFound", missing, result.Outcomes[1].Err)
	}
	if !errors.Is(result.Outcomes[2].Err, publicops.ErrCloseBlocked) {
		t.Errorf("outcome 2 (%s, blocked by open %s) = %v, want ErrCloseBlocked", blocked, blocker, result.Outcomes[2].Err)
	}
	var openChildren *publicops.CloseOpenChildrenError
	if !errors.As(result.Outcomes[3].Err, &openChildren) {
		t.Errorf("outcome 3 (%s, one open child) = %v, want *CloseOpenChildrenError", parent, result.Outcomes[3].Err)
	} else {
		if openChildren.OpenChildren != 1 {
			t.Errorf("open-children refusal for %s carries OpenChildren = %d, want 1", parent, openChildren.OpenChildren)
		}
		if openChildren.IssueID != parent {
			t.Errorf("open-children refusal names %q, want %q", openChildren.IssueID, parent)
		}
	}
	for _, refused := range []int{1, 2, 3} {
		if result.Outcomes[refused].Issue != nil {
			t.Errorf("outcome %d (%s) carries an Issue beside its Err — Issue is nil exactly when Err is set",
				refused, result.Outcomes[refused].IssueID)
		}
	}
	assertBatchCloserStatus(t, ctx, fixture, blocked, types.StatusOpen)
	assertBatchCloserStatus(t, ctx, fixture, parent, types.StatusOpen)
}

// RunBatchCloserOutcomeSnapshotIsTheDocumentedShape pins batchcloser.go:78-80,
// which the bead's case list did not cover: the post-close snapshot carries
// LABELS and DEPENDENCY RECORDS, and COMMENTS ARE OMITTED from it, exactly as
// they are for CloseResult.
//
// Both halves are load-bearing for a caller that prints the outcome instead of
// re-reading the issue. The omission half needs an issue that actually has a
// comment, or it is a claim about an empty thread rather than about the shape.
func RunBatchCloserOutcomeSnapshotIsTheDocumentedShape(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	if fixture.AddComment == nil {
		t.Fatal("fixture has no AddComment: the comments-omitted half of batchcloser.go:78-80 cannot be told apart from an empty thread")
	}
	closeable := fixture.IssuePrefix + "-shape-closeable"
	target := fixture.IssuePrefix + "-shape-target"
	label := batchCloserCaseLabel(fixture, "shape")
	seedBatchCloserIssue(t, ctx, fixture, closeable, label)
	seedBatchCloserIssue(t, ctx, fixture, target)
	// A related edge rather than a blocking one: the snapshot's dependency
	// records are the subject here, and a live blocker would refuse the close.
	seedBatchCloserEdge(t, ctx, fixture, closeable, target, types.DepRelated)
	if err := fixture.AddComment(ctx, closeable, "seed", "a comment the snapshot must omit"); err != nil {
		t.Fatalf("seed comment on %s: %v", closeable, err)
	}

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: closeable}},
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	outcome := result.Outcomes[0]
	if outcome.Err != nil {
		t.Fatalf("outcome 0 (%s) = %v, want a success", closeable, outcome.Err)
	}
	if outcome.Issue == nil {
		t.Fatalf("outcome 0 (%s) has a nil Issue with a nil Err", closeable)
	}
	if len(outcome.Issue.Labels) != 1 || outcome.Issue.Labels[0] != label {
		t.Errorf("snapshot labels = %v, want exactly [%s] — the snapshot carries labels", outcome.Issue.Labels, label)
	}
	if len(outcome.Issue.Dependencies) != 1 {
		t.Errorf("snapshot dependency records = %d, want 1 — the snapshot carries dependency records", len(outcome.Issue.Dependencies))
	} else if outcome.Issue.Dependencies[0].DependsOnID != target {
		t.Errorf("snapshot dependency record points at %q, want %q", outcome.Issue.Dependencies[0].DependsOnID, target)
	}
	if len(outcome.Issue.Comments) != 0 {
		t.Errorf("snapshot carries %d comments, want none: comments are omitted, as they are for CloseResult", len(outcome.Issue.Comments))
	}
}

// RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing pins the
// mutual exclusion of a non-nil error and populated Outcomes
// (batchcloser.go:119-126) across the whole validation vocabulary
// ValidateCloseBatchRequest single-sources
// (internal/storage/issueops/close_batch.go:30-34, which delegates the claim's
// unset-Limit-and-Offset rule to ValidateClaimNextRequest rather than
// restating it).
//
// Every request here names a real, closeable issue, so "no state change" is a
// claim with teeth: a backend that validated after running the closes would
// leave the survivor closed.
func RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	survivor := fixture.IssuePrefix + "-validation-survivor"
	seedBatchCloserIssue(t, ctx, fixture, survivor)
	limit := 5

	for _, test := range []struct {
		name    string
		request publicops.CloseBatchRequest
	}{
		{"empty items", publicops.CloseBatchRequest{Actor: "closer"}},
		{"empty actor", publicops.CloseBatchRequest{Items: []publicops.BatchCloseItem{{IssueID: survivor}}}},
		{"item with an empty issue id", publicops.CloseBatchRequest{
			Actor: "closer",
			Items: []publicops.BatchCloseItem{{IssueID: survivor}, {IssueID: ""}},
		}},
		{"claim carrying a limit", publicops.CloseBatchRequest{
			Actor:     "closer",
			Items:     []publicops.BatchCloseItem{{IssueID: survivor}},
			ClaimNext: &publicops.ReadyRequest{Limit: &limit},
		}},
		{"claim carrying an offset", publicops.CloseBatchRequest{
			Actor:     "closer",
			Items:     []publicops.BatchCloseItem{{IssueID: survivor}},
			ClaimNext: &publicops.ReadyRequest{Offset: 3},
		}},
	} {
		result, err := fixture.Closer.CloseBatch(ctx, test.request)
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("CloseBatch(%s) error = %v, want ErrValidation", test.name, err)
		}
		if result.Outcomes != nil {
			t.Errorf("CloseBatch(%s) returned %d outcomes beside an error — the two are mutually exclusive",
				test.name, len(result.Outcomes))
		}
		if result.ClaimedNext != nil {
			t.Errorf("CloseBatch(%s) returned a claim beside an error — the batch never ran", test.name)
		}
		assertBatchCloserStatus(t, ctx, fixture, survivor, types.StatusOpen)
	}
}

// RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure pins the
// clause ValidateCloseBatchRequest does NOT single-source: a ClaimNext whose
// filter carries a deterministically bad VALUE rather than a forbidden field.
//
// batchcloser.go:119-126 gives the method's own error exactly three meanings —
// request validation, cancellation and infrastructure — and an invalid sort
// policy is neither of the last two. batchcloser.go:137-140 then says what a
// deterministic request-validation failure looks like: it matches ErrValidation
// and leaves persistent state unchanged. The unset-Limit-and-Offset rule
// already reaches that shape through ValidateClaimNextRequest; this case asks
// the same of the filter's own vocabulary (batchcloser.go:66-69, by reference
// to readyclaimer.go:11-23 and reader.go:78-83, which name the three legal
// policies).
//
// The item is real and closeable, so "changes nothing" has teeth: a backend
// that translated the filter after running the closes would leave it closed.
func RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	closeable := fixture.IssuePrefix + "-badsort-closeable"
	candidate := fixture.IssuePrefix + "-badsort-candidate"
	label := batchCloserCaseLabel(fixture, "badsort")
	seedBatchCloserIssue(t, ctx, fixture, closeable)
	// A row the claim would have won had the filter been legal, so a backend
	// that ran the claim anyway is caught by the assignee read rather than only
	// by the nil ClaimedNext it also owes.
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: closeable}},
		ClaimNext: &publicops.ReadyRequest{
			Labels: []string{label},
			Sort:   "bogus",
		},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("CloseBatch with ClaimNext.Sort = %q error = %v, want ErrValidation: the method's own error is request validation, cancellation or infrastructure, and a bad sort policy is the first",
			"bogus", err)
	}
	if result.Outcomes != nil {
		t.Errorf("CloseBatch with an invalid sort returned %d outcomes beside its error — the two are mutually exclusive", len(result.Outcomes))
	}
	if result.ClaimedNext != nil {
		t.Errorf("CloseBatch with an invalid sort returned a claim beside its error — the batch never ran")
	}
	assertBatchCloserStatus(t, ctx, fixture, closeable, types.StatusOpen)
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "" {
		t.Errorf("durable assignee of the eligible %s = %q, want it untouched: a refused request changes nothing", candidate, got)
	}
}

// RunBatchCloserBackendFailureReturnsNoOutcomes pins the other half of
// batchcloser.go:119-126: a failure that means the batch NEVER RAN reports no
// outcomes and leaves no durable trace, so a caller that sees an error never
// has to wonder which prefix of its list got through.
//
// The failure is induced with an already-canceled context, the house
// precedent for "make the backend fail without destroying the fixture"
// (conformance/reader_contract.go:761-763): every backend has to begin a
// transaction before it can close anything, so the failure lands underneath the
// batch rather than inside one item. What is asserted is the RESULT shape and
// the untouched row — the error's spelling is each backend's own, and the
// in-transaction claim-failure path this stands in for cannot be reached from
// these fixtures at all (every deterministic claim failure is rejected before
// the transaction).
func RunBatchCloserBackendFailureReturnsNoOutcomes(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	closeable := fixture.IssuePrefix + "-deadctx-closeable"
	seedBatchCloserIssue(t, ctx, fixture, closeable)

	dead, cancel := context.WithCancel(ctx)
	cancel()
	result, err := fixture.Closer.CloseBatch(dead, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: closeable}},
	})
	if err == nil {
		t.Fatalf("CloseBatch on a dead context returned %d outcomes and no error — the failure was not induced, so this case proves nothing",
			len(result.Outcomes))
	}
	if result.Outcomes != nil {
		t.Errorf("CloseBatch that failed returned %d outcomes beside its error (%v) — a non-nil error and populated Outcomes are mutually exclusive",
			len(result.Outcomes), err)
	}
	if result.ClaimedNext != nil {
		t.Errorf("CloseBatch that failed returned a claim beside its error (%v)", err)
	}
	// Read back on the LIVE context: the assertion is about the row, not about
	// the dead context's reach.
	assertBatchCloserStatus(t, ctx, fixture, closeable, types.StatusOpen)
}

// RunBatchCloserIdempotentRecloseIsAPerItemSuccess pins batchcloser.go:81-86.
// A re-close is not a refusal — an agent that retries a batch after a partial
// failure must not have the already-done items reported as errors — and
// OpenChildren is still observed on the second pass, which is what lets a
// forced re-close report the same warning the first one did.
//
// The claim and history consequences of an ALL-idempotent batch are pinned by
// RunBatchCloserAllIdempotentBatchLandsNothing, which is where "landed means
// Changed" (batchcloser.go:55-64, 119-122) is asserted.
//
// This is the ACROSS-request re-close; the WITHIN-request one, [a, a], is
// RunBatchCloserDuplicateItemRecloseAtItsOwnIndex.
func RunBatchCloserIdempotentRecloseIsAPerItemSuccess(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	plain := fixture.IssuePrefix + "-reclose-plain"
	parent := fixture.IssuePrefix + "-reclose-parent"
	child := fixture.IssuePrefix + "-reclose-child"
	seedBatchCloserIssue(t, ctx, fixture, plain)
	seedBatchCloserIssue(t, ctx, fixture, parent)
	seedBatchCloserIssue(t, ctx, fixture, child)
	seedBatchCloserEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	// Force, because an open child refuses an unforced close of a CLOSED target
	// just as it refuses an open one; the reported count is the point here.
	request := publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: plain}, {IssueID: parent}},
		Force: true,
	}
	first, err := fixture.Closer.CloseBatch(ctx, request)
	if err != nil {
		t.Fatalf("first CloseBatch: %v", err)
	}
	for i, outcome := range first.Outcomes {
		if outcome.Err != nil {
			t.Fatalf("first close outcome %d (%s) = %v, want a success", i, outcome.IssueID, outcome.Err)
		}
		if !outcome.Changed {
			t.Errorf("first close outcome %d (%s) Changed = false, want true", i, outcome.IssueID)
		}
	}
	if first.Outcomes[1].OpenChildren != 1 {
		t.Errorf("forced close of %s reports OpenChildren = %d, want 1", parent, first.Outcomes[1].OpenChildren)
	}

	second, err := fixture.Closer.CloseBatch(ctx, request)
	if err != nil {
		t.Fatalf("re-close CloseBatch returned an error of the batch: %v — an idempotent re-close is a success", err)
	}
	for i, outcome := range second.Outcomes {
		if outcome.Err != nil {
			t.Errorf("re-close outcome %d (%s) = %v, want a per-item success", i, outcome.IssueID, outcome.Err)
			continue
		}
		if outcome.Issue == nil {
			t.Errorf("re-close outcome %d (%s) has a nil Issue with a nil Err", i, outcome.IssueID)
		}
		if outcome.Changed {
			t.Errorf("re-close outcome %d (%s) Changed = true, want false: nothing was mutated", i, outcome.IssueID)
		}
	}
	if second.Outcomes[1].OpenChildren != 1 {
		t.Errorf("forced re-close of %s reports OpenChildren = %d, want 1 — it is reported even for an idempotent re-close",
			parent, second.Outcomes[1].OpenChildren)
	}
}

// RunBatchCloserAllIdempotentBatchLandsNothing pins the two consequences of
// "CHANGED IS THE TEST" (batchcloser.go:55-64, 119-122): a batch whose items were
// all already closed mutated nothing, so it earns NO claim and records NO
// history entry.
//
// The claim half is the one with teeth. The candidate is ready, carries the
// case's label and would be won by an identical request that closed something —
// RunBatchCloserClaimNextHydratesWhenSomethingClosed is that request — so only
// the coupling keeps it unassigned, and an implementation that counted a nil
// per-item Err as a landing hands out a claim the caller never earned.
func RunBatchCloserAllIdempotentBatchLandsNothing(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserHistory(t, fixture)
	closeable := fixture.IssuePrefix + "-allidem-closeable"
	candidate := fixture.IssuePrefix + "-allidem-candidate"
	label := batchCloserCaseLabel(fixture, "allidem")
	seedBatchCloserIssue(t, ctx, fixture, closeable)
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)

	first, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: closeable}},
	})
	if err != nil {
		t.Fatalf("first CloseBatch: %v", err)
	}
	if first.Outcomes[0].Err != nil {
		t.Fatalf("first close of %s = %v, want it to land — the re-close below has nothing to be idempotent about otherwise",
			closeable, first.Outcomes[0].Err)
	}

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before the re-close: %v", err)
	}
	second, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: closeable}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("all-idempotent CloseBatch returned an error of the batch: %v — a re-close is a per-item success", err)
	}
	if second.Outcomes[0].Err != nil {
		t.Fatalf("re-close outcome 0 (%s) = %v, want a per-item success", closeable, second.Outcomes[0].Err)
	}
	if second.Outcomes[0].Changed {
		t.Fatalf("re-close outcome 0 (%s) Changed = true, want false — this case is about a batch that mutated nothing", closeable)
	}
	if second.ClaimedNext != nil {
		t.Errorf("ClaimedNext = %s after a batch whose only item was already closed, want nil: landed means Changed, and a claim handed out by a request that closed nothing is one the caller never earned",
			second.ClaimedNext.ID)
	}
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "" {
		t.Errorf("durable assignee of the eligible %s = %q, want it untouched", candidate, got)
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after the re-close: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across a batch whose items were all already closed, want no change: none at all when nothing landed",
			before, after)
	}
}

// RunBatchCloserDuplicateItemRecloseAtItsOwnIndex pins what the SECOND
// occurrence of a duplicated id reports (batchcloser.go:28-33): the index
// correspondence is promised for every item, so [a, a] gets two outcomes, and
// the second is the intra-batch re-close the first made — a per-item success
// with Changed false. `bd close a b a` is a plausible typo and it is not a
// refusal.
//
// The two occurrences carry DISTINCT reasons, which is the half a caller can
// observe on the row: the first occurrence's reason is what the close wrote,
// and the second changed nothing, so it wrote nothing.
func RunBatchCloserDuplicateItemRecloseAtItsOwnIndex(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserHistory(t, fixture)
	duplicated := fixture.IssuePrefix + "-dup-target"
	seedBatchCloserIssue(t, ctx, fixture, duplicated)

	const session = "dup-session"
	const firstReason = "the occurrence that closed it"
	const secondReason = "the occurrence that found it closed"
	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:   "closer",
		Session: session,
		Items: []publicops.BatchCloseItem{
			{IssueID: duplicated, Reason: firstReason},
			{IssueID: duplicated, Reason: secondReason},
		},
	})
	if err != nil {
		t.Fatalf("CloseBatch naming the same id twice returned an error of the batch: %v — a duplicate is not a request-validation failure", err)
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("outcomes = %d for 2 items (%v), want one per item: every item appears in Outcomes at the same index",
			len(result.Outcomes), batchCloserOutcomeIDs(result.Outcomes))
	}
	for i, outcome := range result.Outcomes {
		if outcome.IssueID != duplicated {
			t.Fatalf("outcome %d echoes %q, want %q", i, outcome.IssueID, duplicated)
		}
		if outcome.Err != nil {
			t.Fatalf("outcome %d (%s) = %v, want a per-item success at both indices", i, duplicated, outcome.Err)
		}
	}
	if !result.Outcomes[0].Changed {
		t.Errorf("outcome 0 (%s) Changed = false, want true — the first occurrence is the one that closed it", duplicated)
	}
	if result.Outcomes[1].Changed {
		t.Errorf("outcome 1 (%s) Changed = true, want false — the second occurrence is an idempotent re-close of the first", duplicated)
	}
	assertBatchCloserClosedRow(t, ctx, fixture, duplicated, firstReason, session)
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before+1 {
		t.Errorf("history entries went %d -> %d across a batch naming one id twice, want exactly one more", before, after)
	}
}

// RunBatchCloserWispItemClosesAndEarnsTheClaim pins the two admissibility
// halves of the ephemeral rules (batchcloser.go:35-39): a WISP id is an
// admissible batch item, and closing one is real work landing, so it counts
// toward "at least one item closed" and the request earns its claim.
//
// The batch closes NOTHING durable, which is what makes the claim half a claim
// about the wisp rather than about a durable sibling carrying it.
func RunBatchCloserWispItemClosesAndEarnsTheClaim(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserWisps(t, fixture)
	wisp := fixture.IssuePrefix + "-wispclaim-wisp"
	candidate := fixture.IssuePrefix + "-wispclaim-candidate"
	label := batchCloserCaseLabel(fixture, "wispclaim")
	seedBatchCloserWisp(t, ctx, fixture, wisp)
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: wisp}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("CloseBatch of a wisp returned an error of the batch: %v — a wisp id is an admissible item", err)
	}
	if result.Outcomes[0].Err != nil {
		t.Fatalf("outcome 0 (%s, a wisp) = %v, want a success", wisp, result.Outcomes[0].Err)
	}
	if !result.Outcomes[0].Changed {
		t.Errorf("outcome 0 (%s) Changed = false on a first close, want true", wisp)
	}
	assertBatchCloserWispStatus(t, ctx, fixture, wisp, types.StatusClosed)
	if result.ClaimedNext == nil {
		t.Fatalf("ClaimedNext is nil after a batch that closed the wisp %s with %s ready under label %q — closing a wisp is real work landing",
			wisp, candidate, label)
	}
	if result.ClaimedNext.ID != candidate {
		t.Errorf("ClaimedNext = %s, want the only row under label %q, %s", result.ClaimedNext.ID, label, candidate)
	}
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "claimer" {
		t.Errorf("durable assignee of %s = %q, want %q — the claim committed", candidate, got, "claimer")
	}
}

// RunBatchCloserDurableHistoryNeverNamesAWisp pins the third ephemeral rule
// (batchcloser.go:128-135), and it is the one with a federation consequence: a
// mixed batch records ONE durable entry, and that entry names the durable id
// alone.
//
// The wisp tables are dolt-ignored so ephemeral work never ships; an entry
// naming a wisp is exactly the sync artifact ignoring them exists to prevent.
// Asserting that the durable id IS newly named keeps the wisp assertion from
// passing vacuously against a batch that recorded nothing at all.
//
// Both id counts are DELTAS, because seeding an issue records an entry naming
// it too — the totals answer "who has ever been named", and the question here
// is what THIS batch shipped.
func RunBatchCloserDurableHistoryNeverNamesAWisp(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserWisps(t, fixture)
	requireBatchCloserHistory(t, fixture)
	requireBatchCloserHistoryMatching(t, fixture)
	durable := fixture.IssuePrefix + "-wisphistory-durable"
	wisp := fixture.IssuePrefix + "-wisphistory-wisp"
	seedBatchCloserIssue(t, ctx, fixture, durable)
	seedBatchCloserWisp(t, ctx, fixture, wisp)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	namingDurableBefore := batchCloserHistoryEntriesNaming(t, ctx, fixture, durable)
	namingWispBefore := batchCloserHistoryEntriesNaming(t, ctx, fixture, wisp)
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: durable}, {IssueID: wisp}},
	})
	if err != nil {
		t.Fatalf("mixed-plane CloseBatch: %v", err)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Err != nil {
			t.Fatalf("outcome %d (%s) = %v, want both planes to land", i, outcome.IssueID, outcome.Err)
		}
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before+1 {
		t.Fatalf("history entries went %d -> %d across a mixed-plane batch, want exactly one more", before, after)
	}
	if got := batchCloserHistoryEntriesNaming(t, ctx, fixture, durable); got != namingDurableBefore+1 {
		t.Errorf("entries naming the durable %s went %d -> %d, want exactly one more — the wisp assertion below is vacuous otherwise",
			durable, namingDurableBefore, got)
	}
	if got := batchCloserHistoryEntriesNaming(t, ctx, fixture, wisp); got != namingWispBefore {
		t.Errorf("entries naming the wisp %s went %d -> %d, want no change: the wisp tables are dolt-ignored so ephemeral work never ships, and an entry naming one is the sync artifact ignoring them exists to prevent",
			wisp, namingWispBefore, got)
	}
}

// RunBatchCloserForceBypassesOnlyClosePolicy pins batchcloser.go:43-46. Force
// is request-wide, so both policy refusals of the mixed batch land; it is not
// a blanket override, so the id that does not exist still refuses and an invalid
// request is still invalid.
//
// The same clause says force would not bypass a per-item LIFECYCLE
// PRECONDITION either, and the doc says that category is EMPTY today. That is
// the one half no request can exercise, so it is asserted against the request
// TYPE instead — see assertBatchCloserCarriesNoPrecondition below.
func RunBatchCloserForceBypassesOnlyClosePolicy(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	assertBatchCloserCarriesNoPrecondition(t)
	blocked := fixture.IssuePrefix + "-force-blocked"
	blocker := fixture.IssuePrefix + "-force-blocker"
	parent := fixture.IssuePrefix + "-force-parent"
	child := fixture.IssuePrefix + "-force-child"
	missing := fixture.IssuePrefix + "-force-absent"
	seedBatchCloserIssue(t, ctx, fixture, blocked)
	seedBatchCloserIssue(t, ctx, fixture, blocker)
	seedBatchCloserIssue(t, ctx, fixture, parent)
	seedBatchCloserIssue(t, ctx, fixture, child)
	seedBatchCloserEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)
	seedBatchCloserEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: blocked}, {IssueID: missing}, {IssueID: parent}},
		Force: true,
	})
	if err != nil {
		t.Fatalf("forced CloseBatch: %v", err)
	}
	if len(result.Outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(result.Outcomes))
	}
	if result.Outcomes[0].Err != nil {
		t.Errorf("forced close of the blocked %s = %v, want it to land: force bypasses blocker policy", blocked, result.Outcomes[0].Err)
	}
	if !errors.Is(result.Outcomes[1].Err, publicops.ErrNotFound) {
		t.Errorf("forced close of the absent %s = %v, want ErrNotFound: force bypasses policy, not existence", missing, result.Outcomes[1].Err)
	}
	if result.Outcomes[2].Err != nil {
		t.Errorf("forced close of the parent %s = %v, want it to land: force bypasses open-child policy", parent, result.Outcomes[2].Err)
	} else if result.Outcomes[2].OpenChildren != 1 {
		t.Errorf("forced close of %s reports OpenChildren = %d, want 1", parent, result.Outcomes[2].OpenChildren)
	}
	assertBatchCloserStatus(t, ctx, fixture, blocked, types.StatusClosed)
	assertBatchCloserStatus(t, ctx, fixture, parent, types.StatusClosed)
	assertBatchCloserStatus(t, ctx, fixture, child, types.StatusOpen)

	// The other half of the clause: force never bypasses validation.
	invalid, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: blocker}, {IssueID: ""}},
		Force: true,
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("forced CloseBatch with an empty item id = %v, want ErrValidation", err)
	}
	if invalid.Outcomes != nil {
		t.Errorf("forced CloseBatch with an empty item id returned %d outcomes beside its error", len(invalid.Outcomes))
	}
	assertBatchCloserStatus(t, ctx, fixture, blocker, types.StatusOpen)
}

// RunBatchCloserClaimNextHydratesWhenSomethingClosed is case (a) of the claim
// coupling (batchcloser.go:55-58, 100-103): a request that closed something and
// asked for a claim gets one.
//
// It does not re-pin what a claim DOES to the row it wins beyond the
// assignment — that is ReadyClaimer's own contract, referenced rather than
// restated at batchcloser.go:66-69, and restating it here is how the two come
// apart.
func RunBatchCloserClaimNextHydratesWhenSomethingClosed(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	closeable := fixture.IssuePrefix + "-claimhit-closeable"
	candidate := fixture.IssuePrefix + "-claimhit-candidate"
	dependent := fixture.IssuePrefix + "-claimhit-dependent"
	label := batchCloserCaseLabel(fixture, "claimhit")
	seedBatchCloserIssue(t, ctx, fixture, closeable)
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)
	// One issue blocked BY the candidate, so "hydrated with its cardinalities"
	// is a claim about a nonzero number rather than about a zero value. The
	// dependent carries no label and is blocked, so it is never a rival for
	// the claim.
	seedBatchCloserIssue(t, ctx, fixture, dependent)
	seedBatchCloserEdge(t, ctx, fixture, dependent, candidate, types.DepBlocks)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: closeable}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("CloseBatch with a claim: %v", err)
	}
	if result.Outcomes[0].Err != nil {
		t.Fatalf("the close the claim is earned by refused: %v", result.Outcomes[0].Err)
	}
	if result.ClaimedNext == nil {
		t.Fatalf("ClaimedNext is nil after a batch that closed %s with %s ready under label %q", closeable, candidate, label)
	}
	if result.ClaimedNext.Issue == nil {
		t.Fatalf("ClaimedNext carries a nil Issue")
	}
	if result.ClaimedNext.ID != candidate {
		t.Errorf("ClaimedNext = %s, want the only row under label %q, %s", result.ClaimedNext.ID, label, candidate)
	}
	if result.ClaimedNext.Assignee != "claimer" {
		t.Errorf("ClaimedNext assignee = %q, want the batch's actor %q", result.ClaimedNext.Assignee, "claimer")
	}
	if result.ClaimedNext.DependentCount != 1 {
		t.Errorf("ClaimedNext DependentCount = %d, want 1 — the row is hydrated with its cardinalities, not returned bare",
			result.ClaimedNext.DependentCount)
	}
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "claimer" {
		t.Errorf("durable assignee of %s = %q, want %q — the claim committed with the closes", candidate, got, "claimer")
	}
}

// RunBatchCloserClaimNextIsNilWhenNothingClosed is case (b), and it is the one
// the doc argues for in prose (batchcloser.go:55-58): a claim handed out by a
// request that closed nothing would be a claim the caller never earned. The
// candidate is ready and matches the filter, so only the coupling keeps it
// unclaimed.
func RunBatchCloserClaimNextIsNilWhenNothingClosed(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	missing := fixture.IssuePrefix + "-claimnone-absent"
	candidate := fixture.IssuePrefix + "-claimnone-candidate"
	label := batchCloserCaseLabel(fixture, "claimnone")
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: missing}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("CloseBatch whose only item refused returned an error of the batch: %v", err)
	}
	if !errors.Is(result.Outcomes[0].Err, publicops.ErrNotFound) {
		t.Fatalf("outcome 0 (%s) = %v, want ErrNotFound", missing, result.Outcomes[0].Err)
	}
	if result.ClaimedNext != nil {
		t.Errorf("ClaimedNext = %s after a batch that closed nothing, want nil", result.ClaimedNext.ID)
	}
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "" {
		t.Errorf("durable assignee of the eligible %s = %q, want it untouched", candidate, got)
	}
}

// RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty is case (c)
// (batchcloser.go:100-103, by reference to readyclaimer.go:32-36): an empty
// front is a normal outcome, not an error, so the closes still land and the
// batch still succeeds. Nothing carries the case's label, so the front the
// claim looks at is empty by construction rather than by luck.
func RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	closeable := fixture.IssuePrefix + "-claimempty-closeable"
	label := batchCloserCaseLabel(fixture, "claimempty")
	seedBatchCloserIssue(t, ctx, fixture, closeable)

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: closeable}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("CloseBatch whose claim found nothing returned an error: %v — an empty front is a normal outcome", err)
	}
	if result.Outcomes[0].Err != nil {
		t.Errorf("outcome 0 (%s) = %v, want the close to land regardless of the claim", closeable, result.Outcomes[0].Err)
	}
	if !result.Outcomes[0].Changed {
		t.Errorf("outcome 0 (%s) Changed = false, want true", closeable)
	}
	if result.ClaimedNext != nil {
		t.Errorf("ClaimedNext = %s with nothing eligible under label %q, want nil", result.ClaimedNext.ID, label)
	}
	assertBatchCloserStatus(t, ctx, fixture, closeable, types.StatusClosed)
}

// RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch is case (d), and the
// only case that can tell "runs after the closes inside the same transaction"
// (batchcloser.go:143-146) from "runs in the same call": the candidate's ONLY
// blocker is an item of the batch, so it is claimable by this request's claim
// and by nothing that ran before it.
//
// The control batch is what makes that a proof rather than a coincidence. It
// closes an unrelated issue with the identical claim, and must come back
// empty; only when the blocker itself is in the batch does the same claim win
// the row. An implementation that ran its claim against a snapshot taken
// before the closes passes the control and fails the real one.
func RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	blocker := fixture.IssuePrefix + "-claimunblock-blocker"
	candidate := fixture.IssuePrefix + "-claimunblock-candidate"
	unrelated := fixture.IssuePrefix + "-claimunblock-unrelated"
	label := batchCloserCaseLabel(fixture, "claimunblock")
	seedBatchCloserIssue(t, ctx, fixture, blocker)
	seedBatchCloserIssue(t, ctx, fixture, unrelated)
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)
	seedBatchCloserEdge(t, ctx, fixture, candidate, blocker, types.DepBlocks)

	if blocked := batchCloserIsBlocked(t, ctx, fixture, candidate); blocked != 1 {
		t.Fatalf("is_blocked of %s = %d before the batch, want 1 — the seed did not block the candidate, so this case would pass vacuously",
			candidate, blocked)
	}

	control, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: unrelated}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("control CloseBatch: %v", err)
	}
	if control.ClaimedNext != nil {
		t.Fatalf("control batch claimed %s while its blocker %s was still open — the candidate was not actually blocked",
			control.ClaimedNext.ID, blocker)
	}

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "claimer",
		Items:     []publicops.BatchCloseItem{{IssueID: blocker}},
		ClaimNext: batchCloserClaimFor(label),
	})
	if err != nil {
		t.Fatalf("CloseBatch closing the blocker with a claim: %v", err)
	}
	if result.Outcomes[0].Err != nil {
		t.Fatalf("closing the blocker %s refused: %v", blocker, result.Outcomes[0].Err)
	}
	if result.ClaimedNext == nil {
		t.Fatalf("ClaimedNext is nil after the same request closed %s, the candidate's only blocker — the claim did not run after the closes",
			blocker)
	}
	if result.ClaimedNext.ID != candidate {
		t.Errorf("ClaimedNext = %s, want %s", result.ClaimedNext.ID, candidate)
	}
	if got := batchCloserAssignee(t, ctx, fixture, candidate); got != "claimer" {
		t.Errorf("durable assignee of %s = %q, want %q — the unblocking and the claim committed together", candidate, got, "claimer")
	}
}

// RunBatchCloserRecordsOneHistoryEntryForWhatLanded pins the first half of
// batchcloser.go:119-122. N closes are ONE transaction and ONE entry, which is
// the entire reason a batch exists rather than a loop over Lifecycle.Close.
func RunBatchCloserRecordsOneHistoryEntryForWhatLanded(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserHistory(t, fixture)
	first := fixture.IssuePrefix + "-history-a"
	second := fixture.IssuePrefix + "-history-b"
	seedBatchCloserIssue(t, ctx, fixture, first)
	seedBatchCloserIssue(t, ctx, fixture, second)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: first}, {IssueID: second}},
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Err != nil {
			t.Fatalf("outcome %d (%s) = %v, want both to land", i, outcome.IssueID, outcome.Err)
		}
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before+1 {
		t.Errorf("history entries went %d -> %d across a batch closing two issues, want exactly one more: what lands is one transaction and at most one entry",
			before, after)
	}
}

// RunBatchCloserAllRefusedBatchRecordsNoHistory pins the second half of
// batchcloser.go:119-122: none at all when nothing landed. A batch of typos must
// not leave a commit behind.
func RunBatchCloserAllRefusedBatchRecordsNoHistory(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	requireBatchCloserHistory(t, fixture)
	missing := fixture.IssuePrefix + "-nohistory-absent"
	blocked := fixture.IssuePrefix + "-nohistory-blocked"
	blocker := fixture.IssuePrefix + "-nohistory-blocker"
	seedBatchCloserIssue(t, ctx, fixture, blocked)
	seedBatchCloserIssue(t, ctx, fixture, blocker)
	seedBatchCloserEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: missing}, {IssueID: blocked}},
	})
	if err != nil {
		t.Fatalf("all-refused CloseBatch returned an error of the batch: %v", err)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Err == nil {
			t.Fatalf("outcome %d (%s) landed, want every item refused for this case to mean anything", i, outcome.IssueID)
		}
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across a batch that landed nothing, want no change", before, after)
	}
	assertBatchCloserStatus(t, ctx, fixture, blocked, types.StatusOpen)
}

// RunBatchCloserDoesNotMutateTheCallerRequest pins batchcloser.go:137-140.
// Validation and normalization apply to attempt-local clones, so the caller's
// struct — its Items slice and the ReadyRequest its ClaimNext points at, both
// carrying values normalization would visibly rewrite — is byte-identical
// afterwards, and the pointer itself is not swapped.
func RunBatchCloserDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	closeable := fixture.IssuePrefix + "-snapshot-closeable"
	candidate := fixture.IssuePrefix + "-snapshot-candidate"
	label := batchCloserCaseLabel(fixture, "snapshot")
	seedBatchCloserIssue(t, ctx, fixture, closeable)
	seedBatchCloserIssue(t, ctx, fixture, candidate, label)

	// Two independently built copies of the same request: the slices and the
	// ClaimNext pointee are distinct allocations, so an in-place normalization
	// shows up as a difference rather than mutating both sides of the compare.
	// The label sets are deliberately padded and duplicated, which is exactly
	// what workapi.BuildReadyFilter's normalization rewrites.
	build := func() publicops.CloseBatchRequest {
		priority := 2
		return publicops.CloseBatchRequest{
			Actor:   "closer",
			Session: "snapshot-session",
			Items: []publicops.BatchCloseItem{
				{IssueID: closeable, Reason: "snapshot reason"},
			},
			ClaimNext: &publicops.ReadyRequest{
				Labels:        []string{"  " + label + "  ", label},
				ExcludeLabels: []string{" " + label + "-excluded "},
				ExcludeTypes:  []string{" chore , chore "},
				Priority:      &priority,
				Sort:          "priority",
			},
		}
	}
	request := build()
	want := build()
	claim := request.ClaimNext

	result, err := fixture.Closer.CloseBatch(ctx, request)
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	// The close has to LAND, or the claim never runs and the normalization
	// this case is watching for never happens.
	if result.Outcomes[0].Err != nil {
		t.Fatalf("outcome 0 (%s) = %v; the claim path only runs when something closed", closeable, result.Outcomes[0].Err)
	}
	if request.ClaimNext != claim {
		t.Errorf("CloseBatch replaced the caller's ClaimNext pointer")
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("CloseBatch mutated the caller's request:\n got %+v\nwant %+v\n got claim %+v\nwant claim %+v",
			request, want, request.ClaimNext, want.ClaimNext)
	}
}

// assertBatchCloserCarriesNoPrecondition holds the emptiness the force clause
// depends on (batchcloser.go:43-53): neither the request nor an item carries a
// per-item precondition, so "force never bypasses a lifecycle precondition"
// governs nothing a caller can send.
//
// A promise with no reachable member is one a future reader will try to test
// and cannot, so the claim is made falsifiable HERE rather than left as prose.
// Expected* is the house spelling for a precondition — Lifecycle's
// ExpectedVersion, Update's ExpectedAssignee and ExpectedStatus — so the day a
// batch grows one, this fails and the doc's "the category is empty" has to be
// replaced by a case that forces one and watches it hold.
func assertBatchCloserCarriesNoPrecondition(t *testing.T) {
	t.Helper()
	for _, subject := range []struct {
		name string
		typ  reflect.Type
	}{
		{"CloseBatchRequest", reflect.TypeOf(publicops.CloseBatchRequest{})},
		{"BatchCloseItem", reflect.TypeOf(publicops.BatchCloseItem{})},
	} {
		for i := 0; i < subject.typ.NumField(); i++ {
			if field := subject.typ.Field(i).Name; strings.HasPrefix(field, "Expected") {
				t.Errorf("%s carries the precondition field %s, but batchcloser.go says a batch has none — force's lifecycle clause now governs something, and needs a case that exercises it",
					subject.name, field)
			}
		}
	}
}

// RunBatchCloserSettlesTheDependersOfWhatItClosed pins the blocked-state
// clause the leaf states for this role: what LANDS leaves its dependers
// settled in the SAME transaction the batch commits.
//
// The existing claim case observes an unblocking from inside a batch only
// THROUGH THE CLAIM — a row the batch unblocked turns up as the winner. That
// is a role answer, and a role answer to "is this ready" can be computed from
// the live edge set by a backend that never denormalized. This one reads the
// raw column, which is the read that cannot be satisfied that way.
//
// The batch closes a blocker AND an unrelated item, so a body that collapsed
// the batch to its first item is still visibly wrong; the control depender
// hangs off a blocker the batch never names.
func RunBatchCloserSettlesTheDependersOfWhatItClosed(t *testing.T, ctx context.Context, fixture BatchCloserFixture) {
	t.Helper()
	blocker := fixture.IssuePrefix + "-bsbatch-blocker"
	other := fixture.IssuePrefix + "-bsbatch-other"
	depender := fixture.IssuePrefix + "-bsbatch-depender"
	child := fixture.IssuePrefix + "-bsbatch-child"
	wispDepender := fixture.IssuePrefix + "-bsbatch-wispdep"
	controlBlocker := fixture.IssuePrefix + "-bsbatch-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bsbatch-ctldepender"
	for _, id := range []string{blocker, other, depender, child, controlBlocker, controlDepender} {
		seedBatchCloserIssue(t, ctx, fixture, id)
	}
	seedBatchCloserWisp(t, ctx, fixture, wispDepender)
	seedBatchCloserEdge(t, ctx, fixture, depender, blocker, types.DepBlocks)
	seedBatchCloserEdge(t, ctx, fixture, child, depender, types.DepParentChild)
	seedBatchCloserEdge(t, ctx, fixture, wispDepender, blocker, types.DepBlocks)
	seedBatchCloserEdge(t, ctx, fixture, controlDepender, controlBlocker, types.DepBlocks)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requirePlaneResidency(t, blockedWisp(wispDepender))
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the direct depender of a batch item")
	probe.requireBlockedByOpenBlocker(t, blockedWisp(wispDepender), blockedIssue(blocker), "the cross-plane depender of a batch item")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child), "the child's block is inherited from the depender")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker), "the control's blocker is in no batch")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(depender), blockedIssue(child), blockedWisp(wispDepender)},
		[]blockedStateRow{blockedIssue(controlDepender)})

	result, err := fixture.Closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "closer",
		Items: []publicops.BatchCloseItem{{IssueID: blocker}, {IssueID: other}},
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Err != nil || !outcome.Changed {
			t.Fatalf("outcome[%d] = %#v, want a landed close: the postcondition only applies to what LANDED", i, outcome)
		}
	}

	flip.requireFlippedTo(t, 0, "a batch settles the dependers of every item that landed, in the transaction the batch commits")
}

func seedBatchCloserIssue(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string, labels ...string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedBatchCloserWisp(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string, labels ...string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
		Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func seedBatchCloserEdge(t *testing.T, ctx context.Context, fixture BatchCloserFixture, source, target string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID:     source,
		DependsOnID: target,
		Type:        depType,
	}, "seed"); err != nil {
		t.Fatalf("seed %s edge %s -> %s: %v", depType, source, target, err)
	}
}

// batchCloserCaseLabel is how a claim case names its own candidate set. One
// database serves a whole role suite on at least one backend, so a claim
// filtered by anything less specific would race the ready front against a
// sibling case's seeds.
func batchCloserCaseLabel(fixture BatchCloserFixture, name string) string {
	return strings.ToLower(fixture.IssuePrefix + "-" + name)
}

// batchCloserClaimFor builds the claim a coupling case carries. Sort is
// explicit because reader.go:79-83 says no surface may rely on the empty-sort
// fallback.
func batchCloserClaimFor(label string) *publicops.ReadyRequest {
	return &publicops.ReadyRequest{Labels: []string{label}, Sort: "priority"}
}

func assertBatchCloserStatus(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string, want types.Status) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("read status of %s: %v", id, err)
	}
	if types.Status(got) != want {
		t.Errorf("durable status of %s = %q, want %q", id, got, want)
	}
}

// assertBatchCloserClosedRow reads the close back off the row rather than out
// of the result, because a result snapshot composed in memory can report a
// reason the transaction never wrote.
func assertBatchCloserClosedRow(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id, wantReason, wantSession string) {
	t.Helper()
	var status, reason, session string
	if err := fixture.QueryScalar(ctx,
		"SELECT status, COALESCE(close_reason, ''), COALESCE(closed_by_session, '') FROM issues WHERE id = ?",
		[]any{id}, &status, &reason, &session); err != nil {
		t.Fatalf("read the closed row %s: %v", id, err)
	}
	if types.Status(status) != types.StatusClosed {
		t.Errorf("durable status of %s = %q, want %q", id, status, types.StatusClosed)
	}
	if reason != wantReason {
		t.Errorf("durable close_reason of %s = %q, want %q — the reason is recorded against that issue alone", id, reason, wantReason)
	}
	if session != wantSession {
		t.Errorf("durable closed_by_session of %s = %q, want %q", id, session, wantSession)
	}
}

// assertBatchCloserWispStatus is assertBatchCloserStatus against the ephemeral
// plane. A wisp close that wrote nothing anywhere would still report a per-item
// success, so the row is read back off the table that actually holds it.
func assertBatchCloserWispStatus(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string, want types.Status) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM wisps WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("read status of the wisp %s: %v", id, err)
	}
	if types.Status(got) != want {
		t.Errorf("ephemeral status of %s = %q, want %q", id, got, want)
	}
}

// batchCloserHistoryEntriesNaming counts the branch's history entries whose
// message mentions id anywhere. The contract asserts on the MESSAGE rather than
// on a count alone because "an entry naming a wisp" is a claim about what
// shipped, and only the message carries an id at all.
//
// The ids this is called with are the fixture's own seeded ones, which carry no
// LIKE wildcard, so the surrounding %s are the only wildcards in the pattern.
func batchCloserHistoryEntriesNaming(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string) int {
	t.Helper()
	entries, err := fixture.CountHistoryMatching(ctx, "%"+historyPatternForExactMessage(t, id)+"%")
	if err != nil {
		t.Fatalf("count history entries naming %s: %v", id, err)
	}
	return entries
}

func batchCloserAssignee(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string) string {
	t.Helper()
	var assignee string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, '') FROM issues WHERE id = ?", []any{id}, &assignee); err != nil {
		t.Fatalf("read assignee of %s: %v", id, err)
	}
	return assignee
}

func batchCloserIsBlocked(t *testing.T, ctx context.Context, fixture BatchCloserFixture, id string) int {
	t.Helper()
	var blocked int
	if err := fixture.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{id}, &blocked); err != nil {
		t.Fatalf("read is_blocked of %s: %v", id, err)
	}
	return blocked
}

func batchCloserOutcomeIDs(outcomes []publicops.CloseOutcome) []string {
	ids := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		ids = append(ids, outcome.IssueID)
	}
	return ids
}

// requireBatchCloserHistory skips LOUDLY rather than passing quietly when a
// backend's fixture cannot observe history at all, which is the difference
// between "this clause is unpinned here" and "this clause holds here".
func requireBatchCloserHistory(t *testing.T, fixture BatchCloserFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skipf("fixture has no CountHistory: this backend cannot observe history, so batchcloser.go:119-122 is UNPINNED here")
	}
}

// requireBatchCloserHistoryMatching is requireBatchCloserHistory for the
// message-scoped count: a backend that can say how LONG its history is but not
// what an entry READS leaves the wisp-naming clause unpinned, and says so.
func requireBatchCloserHistoryMatching(t *testing.T, fixture BatchCloserFixture) {
	t.Helper()
	if fixture.CountHistoryMatching == nil {
		t.Skipf("fixture has no CountHistoryMatching: this backend cannot observe history BY MESSAGE, so batchcloser.go:128-135 is UNPINNED here")
	}
}

// requireBatchCloserWisps is requireBatchCloserHistory for the ephemeral plane:
// a backend that cannot seed a wisp leaves the three ephemeral rules unpinned,
// and says so rather than passing.
func requireBatchCloserWisps(t *testing.T, fixture BatchCloserFixture) {
	t.Helper()
	if fixture.CreateWisp == nil {
		t.Skipf("fixture has no CreateWisp: this backend cannot seed the ephemeral plane, so the ephemeral rules are UNPINNED here")
	}
}
