package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.Commenter
// must satisfy. Each case asserts what issueops/commenter.go PROMISES, cited
// by line, rather than what any one backend happens to do today; a backend
// that disagrees is parked at its own wiring site with skipKnownDivergence so
// the case still runs on the ones that agree.
//
// There are three wirings — the server-backed store, the embedded store and
// the unit-of-work provider — but only TWO bodies. dolt and embeddeddolt share
// storageissueops.ValidateAddCommentRequest and ExecuteAddComment and differ
// only in the transaction wrapper and the engine underneath, so the third
// wiring catches wrapper and engine divergence, not body divergence. A case
// passing on all three is two independent votes plus a wrapper check, not
// three.
//
// What is deliberately NOT here, and where it lives instead:
//   - the commit-message spelling ("bd: comment <id>"), which is a persistence
//     detail single-sourced in storageissueops.AddCommentCommitMessage and
//     pinned once, at dolt, in dolt/commenter_persistence_test.go;
//   - the dolt_status pending-row sweep, which needs a planted dirty working
//     set and has no caller-visible meaning on the unit-of-work route.
//
// THE HISTORY COST OF A COMMENT IS THE SAME NUMBER ON ALL THREE WIRINGS, and
// it is asserted here as a number rather than as a bound: exactly one entry
// for a durable comment, exactly zero for an ephemeral one. Both were measured
// on all three legs, so neither is a divergence being papered over — see
// RunCommenterRecordsExactlyOneHistoryEntry for why a bound would have been
// worse than no assertion at all.

// CommenterFixture supplies adapter-specific storage access for the
// add-comment assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from, so a wiring is kit plus
// accessor plus prefix with no adapter in between.
type CommenterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Commenter   publicops.Commenter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp  func(context.Context, *types.Issue, string) error
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the cases
	// that need it SKIP with that reason rather than pass quietly.
	CountHistory func(context.Context) (int, error)
}

// RunCommenterStoresTextVerbatim pins commenter.go:31-34: blankness is decided
// on a trimmed copy and nothing trims the value that lands in the row. The
// text therefore begins with a newline and two spaces — content a trim would
// eat — and the assertion reads the stored column, not the returned struct,
// because a body that trimmed on the way in and echoed the caller's string
// back out would pass a result-only check.
func RunCommenterStoresTextVerbatim(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-verbatim"
	seedCommenterIssue(t, ctx, fixture, anchor)

	const text = "\n  still a comment"
	result, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: anchor,
		Text:    text,
	})
	if err != nil {
		t.Fatalf("AddComment with leading whitespace: %v", err)
	}
	requireCommenterResult(t, result)

	stored := readCommenterRow(t, ctx, fixture, "comments", result.Comment.ID)
	if stored.Text != text {
		t.Errorf("stored text = %q, want %q verbatim", stored.Text, text)
	}
	if result.Comment.Text != text {
		t.Errorf("returned text = %q, want %q verbatim", result.Comment.Text, text)
	}
}

// RunCommenterResultMirrorsTheStoredRow pins commenter.go:39-43: the result
// carries the id and created_at the row actually got, at the column's
// precision, so it is usable directly as a comment-page cursor.
//
// The precision half is the one a wall-clock implementation fails: created_at
// is DATETIME(0), so a returned CreatedAt carrying sub-second parts sorts
// after the same-second rows stored at the truncated second and a cursor walk
// resumed from it skips them.
//
// The author is checked against the REQUEST's literal as well as against the
// result (commenter.go:14-19): a comment is signed, and the name that lands in
// the row is the one the caller published under. Comparing stored against
// result alone would pass an implementation that canonicalized or defaulted
// the author consistently on both sides — the row would then be signed by
// somebody the caller never named.
func RunCommenterResultMirrorsTheStoredRow(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-mirror"
	seedCommenterIssue(t, ctx, fixture, anchor)

	const author = "mirror-author"
	result, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  author,
		IssueID: anchor,
		Text:    "the result describes the row",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	requireCommenterResult(t, result)
	assertCommenterRowCount(t, ctx, fixture, "comments", anchor, 1)

	stored := readCommenterRow(t, ctx, fixture, "comments", result.Comment.ID)
	if stored.IssueID != result.Comment.IssueID {
		t.Errorf("stored issue_id = %q, result says %q", stored.IssueID, result.Comment.IssueID)
	}
	if stored.Author != result.Comment.Author {
		t.Errorf("stored author = %q, result says %q", stored.Author, result.Comment.Author)
	}
	if stored.Author != author {
		t.Errorf("stored author = %q, want the request's %q: a comment is signed, and the name in the row is the one it was published under",
			stored.Author, author)
	}
	if stored.Text != result.Comment.Text {
		t.Errorf("stored text = %q, result says %q", stored.Text, result.Comment.Text)
	}
	if !stored.CreatedAt.Equal(result.Comment.CreatedAt) {
		t.Errorf("stored created_at = %s, result says %s: the result must carry the STORED value",
			stored.CreatedAt.UTC(), result.Comment.CreatedAt.UTC())
	}
	if truncated := result.Comment.CreatedAt.Truncate(time.Second); !result.Comment.CreatedAt.Equal(truncated) {
		t.Errorf("returned created_at = %s carries sub-second parts the DATETIME(0) column cannot hold, so it is not cursor-safe",
			result.Comment.CreatedAt.UTC())
	}
}

// RunCommenterCommentOnAWispLandsOnTheWispThread pins commenter.go:20-29: the
// issue-to-wisp fallback happens INSIDE, so a caller never has to know which
// plane the thread lives on. Pinning the durable table at zero is the half
// that catches a resolve that fell through to `comments` while the wisp read
// still passed, because a comments row keyed by an id the issues plane does
// not have is invisible to every thread read of the wisp.
//
// The history assertion is an EQUALITY at ZERO, where the durable path's is an
// equality at one: AddComment's ephemeral clause promises "NO durable history
// entry — none, not 'at most one'". A bound would let a regression that records
// exactly one entry pass on every backend, and that regression is the one that
// breaks federation — the wisp tables are dolt-ignored so ephemeral work never
// ships, and an entry naming a wisp thread ships.
//
// The anchor here lives on exactly one plane, and every anchor a CALLER can
// create does: no local write path reaches dual residency, which is why
// commenter.go and the GetRequest.ID doc it defers to state no plane order for
// the two bodies to disagree about. The refusals that close each local write
// path are enumerated there.
//
// Replication is the residual path and is deliberately not asserted here: a
// dual-resident id arriving by pull is a corrupt store — the merge-based
// lookups behind ready and search hard-error for all of it — so there is no
// well-defined answer for a case to pin, and staging it would mean writing
// rows this library refuses to write (bd-yby99.22).
func RunCommenterCommentOnAWispLandsOnTheWispThread(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	wisp := fixture.IssuePrefix + "-wisp"
	seedCommenterWisp(t, ctx, fixture, wisp)
	before := commenterHistoryCount(t, ctx, fixture)

	result, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: wisp,
		Text:    "on the ephemeral plane",
	})
	if err != nil {
		t.Fatalf("AddComment on a wisp: %v", err)
	}
	requireCommenterResult(t, result)

	assertCommenterRowCount(t, ctx, fixture, "wisp_comments", wisp, 1)
	assertCommenterRowCount(t, ctx, fixture, "comments", wisp, 0)
	stored := readCommenterRow(t, ctx, fixture, "wisp_comments", result.Comment.ID)
	if stored.Text != "on the ephemeral plane" {
		t.Errorf("stored wisp comment text = %q, want the comment that was written", stored.Text)
	}

	// No durable trace of an ephemeral comment. The events side is asserted
	// only on the DURABLE table, as a plane leak rather than as a promise about
	// events: no implementation records a comment event on either plane today
	// and no doc promises one either way, so pinning wisp_events at zero would
	// freeze an unpromised absence.
	assertCommenterRowCount(t, ctx, fixture, "events", wisp, 0)
	if after := commenterHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d for a comment on an ephemeral row, want no change: an entry naming a wisp thread is a sync artifact the dolt-ignored wisp tables exist to prevent", before, after)
	}
}

// RunCommenterRefusesAnIDOnNeitherPlane pins commenter.go:68-69: a NON-EMPTY
// id that names neither an issue nor a wisp is ErrNotFound, matchable with
// errors.Is rather than by reading prose, and nothing lands on either plane.
func RunCommenterRefusesAnIDOnNeitherPlane(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	absent := fixture.IssuePrefix + "-absent"
	before := commenterHistoryCount(t, ctx, fixture)

	_, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: absent,
		Text:    "into the void",
	})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("AddComment on an unknown id error = %v, want ErrNotFound", err)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", absent, 0)
	assertCommenterRowCount(t, ctx, fixture, "wisp_comments", absent, 0)
	if after := commenterHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d across a refused call, want no change", before, after)
	}
}

// RunCommenterRefusesAnEmptyIssueID pins commenter.go:20 and :67-68: IssueID
// must not be empty, and an unaddressed comment is ErrValidation rather than
// ErrNotFound. The two are not interchangeable to a caller — ErrNotFound reads
// as "that thread is gone", which invites a retry against a different id,
// where ErrValidation says the request itself was never addressed to anything
// and no id will fix it.
//
// The sentinel is named rather than left as "either of two" because the
// ErrNotFound clause is now scoped to a NON-EMPTY id (:62-63), so the empty
// string falls to validation and nowhere else. The refusal is also TYPED,
// matchable with errors.Is rather than by reading prose (:63-64), and nothing
// lands on either plane.
func RunCommenterRefusesAnEmptyIssueID(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	_, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: "",
		Text:    "unaddressed",
	})
	if err == nil {
		t.Fatal("AddComment with an empty issue id succeeded, want a typed refusal")
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("AddComment with an empty issue id error = %v, want ErrValidation: an unaddressed request is invalid, not a thread that went missing", err)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", "", 0)
	assertCommenterRowCount(t, ctx, fixture, "wisp_comments", "", 0)
}

// RunCommenterDoesNotResolvePrefixes pins the other half of commenter.go:20-29:
// IssueID is the EXACT canonical id and there is no fuzzy or prefix
// resolution here. A strict prefix of a real issue is therefore ErrNotFound,
// not a comment on the issue it nearly names — a comment is signed and
// published under someone's name, so landing it on the wrong thread is worse
// than refusing.
func RunCommenterDoesNotResolvePrefixes(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	full := fixture.IssuePrefix + "-exactid"
	partial := fixture.IssuePrefix + "-exact"
	seedCommenterIssue(t, ctx, fixture, full)

	_, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: partial,
		Text:    "addressed to a prefix",
	})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("AddComment on the strict prefix %q of %q: error = %v, want ErrNotFound", partial, full, err)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", full, 0)
	assertCommenterRowCount(t, ctx, fixture, "comments", partial, 0)
}

// RunCommenterRecordsExactlyOneHistoryEntry pins Commenter.AddComment's "ONE
// atomic mutation, with at most one history entry" at the number every wiring
// actually records: a durable comment costs EXACTLY one entry.
//
// The case is deliberately stricter than the leaf's bound, because the bound
// cannot fail in the direction that matters. "after <= before+1" holds whether
// the entry was written or not, so a body that quietly stopped committing
// keeps it green — and that is not hypothetical. The deleter role stopped
// versioning embedded deletes, the identically-shaped range in its contract
// absorbed it, and what noticed months later was a sibling-write test outside
// the contract suite (e9acfac6e).
//
// EXACTLY ONE IS MEASURED, NOT DEMANDED: all three wirings already record one
// entry for a durable comment — the server-backed store inside its write
// transaction, the embedded store on a second connection after the SQL commit,
// the unit-of-work provider from the message it hands RunTxResult — so this
// tightens the assertion and asks no backend to change. Ratifying the leaf's
// own wording to match is the owner's call; until then this case is the
// stricter of the two statements, and the honest one.
//
// What the strictness buys a caller: a comment row no entry carries is a row
// `bd dolt push` does not ship. The thread then reads back locally and exists
// nowhere else, and the author who published under their name cannot tell the
// difference.
//
// The ephemeral half of the promise is a different number and is pinned
// separately, at exactly zero, by
// RunCommenterCommentOnAWispLandsOnTheWispThread.
//
// The "one mutation" half is the exact-one row count, which catches a body that
// appended the comment twice while the entry count still held.
func RunCommenterRecordsExactlyOneHistoryEntry(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-history"
	seedCommenterIssue(t, ctx, fixture, anchor)
	before := commenterHistoryCount(t, ctx, fixture)

	if _, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: anchor,
		Text:    "one mutation",
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	after := commenterHistoryCount(t, ctx, fixture)
	if after != before+1 {
		t.Errorf("history entries went %d -> %d across one durable AddComment, want exactly one more: a comment no entry carries never leaves this workspace, and a comment carrying two is not one mutation",
			before, after)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", anchor, 1)
}

// RunCommenterLeavesTheAnchorIssueUntouched pins commenter.go:51-54, the clause
// that makes Commenter a role rather than a Lifecycle verb: a comment appends a
// row to a thread the issue owns and leaves every field of the issue untouched.
//
// It is the promise a caller leans on when it comments on work it is not
// otherwise touching. An implementation that bumped the anchor's updated_at
// would reorder every "recently updated" listing and re-dirty the row for every
// federation sync, and nothing else in this file would notice: the thread would
// still hold exactly one comment.
//
// updated_at is the load-bearing column, and not only because it is one field
// among many. The issues table declares it ON UPDATE CURRENT_TIMESTAMP
// (schema/migrations/0001_create_issues.up.sql), so any UPDATE that changes any
// column at all moves it. Reading it therefore stands in for a whole-row check
// that this seam has no way to spell exhaustively; the named columns beside it
// are the ones a comment path could plausibly reach on purpose.
func RunCommenterLeavesTheAnchorIssueUntouched(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-untouched"
	seedCommenterIssue(t, ctx, fixture, anchor)

	before := readCommenterAnchorRow(t, ctx, fixture, anchor)
	// A second comment on the same anchor, because "appends to the thread" is
	// what the issue row must survive, and the second append is the one that
	// would tempt a body into touching a comment count on the issue.
	for i, text := range []string{"first append", "second append"} {
		if _, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
			Author:  "author",
			IssueID: anchor,
			Text:    text,
		}); err != nil {
			t.Fatalf("AddComment %d on %s: %v", i, anchor, err)
		}
	}
	after := readCommenterAnchorRow(t, ctx, fixture, anchor)

	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("anchor %s updated_at moved %s -> %s across two comments: the column is ON UPDATE CURRENT_TIMESTAMP, so something wrote to the issue row",
			anchor, before.UpdatedAt.UTC(), after.UpdatedAt.UTC())
	}
	if after.Status != before.Status {
		t.Errorf("anchor %s status went %q -> %q across a comment", anchor, before.Status, after.Status)
	}
	if after.Assignee != before.Assignee {
		t.Errorf("anchor %s assignee went %q -> %q across a comment", anchor, before.Assignee, after.Assignee)
	}
	if after.Fingerprint != before.Fingerprint {
		t.Errorf("anchor %s changed across a comment:\nbefore %s\n after %s\na comment leaves every field of the issue untouched",
			anchor, before.Fingerprint, after.Fingerprint)
	}
	// The append itself has to have happened, or every equality above is a
	// statement about a call that did nothing.
	assertCommenterRowCount(t, ctx, fixture, "comments", anchor, 2)
}

// RunCommenterRefusesBlankText pins commenter.go:31-34 and :67 from the other
// side: a comment of nothing but whitespace carries no information, so it is
// ErrValidation, and per commenter.go:61-64 a deterministic request-validation
// failure leaves persistent state unchanged.
func RunCommenterRefusesBlankText(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-blank"
	seedCommenterIssue(t, ctx, fixture, anchor)
	before := commenterHistoryCount(t, ctx, fixture)

	_, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: anchor,
		Text:    " \t\n  ",
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("AddComment with whitespace-only text: error = %v, want ErrValidation", err)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", anchor, 0)
	if after := commenterHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d across a refused call, want no change", before, after)
	}
}

// RunCommenterRefusesAnEmptyAuthor pins commenter.go:14: a comment is signed,
// so the author must not be empty. It is a one-line tripwire on purpose —
// ValidateAddCommentRequest is shared by all three implementations, so what
// this guards against is a future implementation UNSHARING the validator, not
// three parallel validators drifting.
func RunCommenterRefusesAnEmptyAuthor(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-unsigned"
	seedCommenterIssue(t, ctx, fixture, anchor)
	before := commenterHistoryCount(t, ctx, fixture)

	_, err := fixture.Commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "",
		IssueID: anchor,
		Text:    "unsigned",
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("AddComment with an empty author: error = %v, want ErrValidation", err)
	}
	assertCommenterRowCount(t, ctx, fixture, "comments", anchor, 0)
	if after := commenterHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d across a refused call, want no change", before, after)
	}
}

// RunCommenterLeavesTheCallersRequestAlone pins commenter.go:61-64:
// implementations never mutate caller-owned request values, snapshot the
// request at method entry, and normalize only attempt-local clones.
//
// AddCommentRequest is three strings passed by value, so today no
// implementation CAN violate this. That is exactly why the tripwire is cheap
// and worth having: the day a field becomes a slice, a map or a pointer, the
// promise stops being free and this case is already in place to notice.
func RunCommenterLeavesTheCallersRequestAlone(t *testing.T, ctx context.Context, fixture CommenterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-snapshot"
	seedCommenterIssue(t, ctx, fixture, anchor)

	request := publicops.AddCommentRequest{
		Author:  "author",
		IssueID: anchor,
		Text:    "\n  the caller keeps this string",
	}
	snapshot := request
	if _, err := fixture.Commenter.AddComment(ctx, request); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if request != snapshot {
		t.Errorf("the caller's request became %#v, want it left as %#v", request, snapshot)
	}
}

func seedCommenterIssue(t *testing.T, ctx context.Context, fixture CommenterFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, commenterSeed(id, false), "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

func seedCommenterWisp(t *testing.T, ctx context.Context, fixture CommenterFixture, id string) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, commenterSeed(id, true), "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

func commenterSeed(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

// requireCommenterResult checks the shape every successful AddComment owes its
// caller before a case reads the rest of it, so a nil Comment fails as "the
// result was empty" rather than as a nil dereference three lines later.
func requireCommenterResult(t *testing.T, result publicops.AddCommentResult) {
	t.Helper()
	if result.Comment == nil || result.Comment.ID == "" || result.Comment.CreatedAt.IsZero() {
		t.Fatalf("Comment = %#v, want the stored row's id and created_at", result.Comment)
	}
}

// commenterStoredRow is one comment row as the database holds it.
type commenterStoredRow struct {
	IssueID   string
	Author    string
	Text      string
	CreatedAt time.Time
}

// readCommenterRow reads the row a result names, from the comment table of one
// plane. It fails the test when the row is absent, because every caller here
// has already been told the row exists.
func readCommenterRow(t *testing.T, ctx context.Context, fixture CommenterFixture, table, commentID string) commenterStoredRow {
	t.Helper()
	var row commenterStoredRow
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT issue_id, author, text, created_at FROM " + table + " WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{commentID},
		&row.IssueID, &row.Author, &row.Text, &row.CreatedAt); err != nil {
		t.Fatalf("read the %s row %s the result names: %v", table, commentID, err)
	}
	return row
}

// commenterAnchorRow is the issues row a comment must not disturb: three
// columns named so a failure reads as "the assignee moved" rather than as a
// diff of two long strings, plus the wide fingerprint that catches the rest.
type commenterAnchorRow struct {
	Status      string
	Assignee    string
	UpdatedAt   time.Time
	Fingerprint string
}

// readCommenterAnchorRow reads the anchor's row through the fixture's own SQL
// hook rather than through Reader.Get, because the subject is what the
// TRANSACTION wrote: a detail view composed in memory can report a field the
// row no longer carries.
//
// The fingerprint is CONCAT_WS rather than a column-by-column scan so the
// column list can be wide without the case growing a scan destination per
// column. CONCAT_WS skips NULL arguments, so a column going NULL -> ” still
// moves the string (the separator count changes) — the one shape that would
// otherwise slip past.
func readCommenterAnchorRow(t *testing.T, ctx context.Context, fixture CommenterFixture, issueID string) commenterAnchorRow {
	t.Helper()
	const query = `SELECT status, COALESCE(assignee, ''), updated_at,
		CONCAT_WS('|', content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, closed_by_session,
			external_ref, spec_id, sender, ephemeral, wisp_type, pinned, is_template,
			mol_type, work_type, source_system, source_repo, close_reason, is_blocked,
			due_at, defer_until)
		FROM issues WHERE id = ?`
	var row commenterAnchorRow
	if err := fixture.QueryScalar(ctx, query, []any{issueID},
		&row.Status, &row.Assignee, &row.UpdatedAt, &row.Fingerprint); err != nil {
		t.Fatalf("read the anchor row %s: %v", issueID, err)
	}
	return row
}

// assertCommenterRowCount counts one anchor's rows in one plane-specific
// table. Comment threads and event feeds are both keyed by issue_id, so the
// two kinds of assertion share it rather than differ by a copied query.
func assertCommenterRowCount(t *testing.T, ctx context.Context, fixture CommenterFixture, table, issueID string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's four hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{issueID}, &got); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, issueID, err)
	}
	if got != want {
		t.Errorf("%s rows for %s = %d, want %d", table, issueID, got, want)
	}
}

// commenterHistoryCount reads the branch's history depth, or SKIPS the case
// with the reason when the backend cannot observe history at all. A silent
// pass would be worse than no case: the entry-per-call clause would look
// pinned on a backend that never checked it.
func commenterHistoryCount(t *testing.T, ctx context.Context, fixture CommenterFixture) int {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil, so the entry-count clause is unpinned on this backend")
	}
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	return entries
}
