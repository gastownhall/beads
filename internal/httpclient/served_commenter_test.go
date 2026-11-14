//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_commenter_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Commenter contract, run through the http client against a real bd serve.
//
// NOTHING IS PARKED HERE: the request has three members, one of which is the
// path, and the answer has one, so there is no filter to drop and no shape for a
// refusal to hide in — the whole thirteen-case tier runs.
//
// THAT IS NOT THE SAME AS "NO DIVERGENCE", and one lives on the AUTHOR. The
// operation applies the `actor` rules to it — trim, then refuse a control rune —
// and passes the trimmed value on, while the role itself only checks the field
// is non-empty and stores what it was given. So the same request stores a
// different value on the two legs, and a value one leg stores the other refuses.
// No contract case reaches it, which is exactly why it needs a pin of its own:
// L-comment-author, asserted below by
// TestServedCommentAuthorTakesTheServersActorRule.
//
// TWO OF THE THIRTEEN ARE WHY THIS TIER EXISTS RATHER THAN A UNIT TEST.
//
//   - THE WISP PLANE. `CommentOnAWispLandsOnTheWispThread` writes through the
//     client to an EPHEMERAL anchor and reads the row back out of wisp_comments
//     raw. Nothing above the storage seam picks that plane: the operation
//     resolves the anchor inside the transaction it writes in, so the only way to
//     know the comment landed on the ephemeral thread is to look at the ephemeral
//     table — which a client cannot do and this harness can.
//   - THE ABSENT DURABLE TRACE. `RecordsExactlyOneHistoryEntry` measures the
//     version history around the call: exactly one entry for a durable comment
//     and exactly ZERO for an ephemeral one, because the wisp tables are
//     dolt-ignored. That is a fact about what the server did NOT write, and no
//     answer this client receives carries it.
//
// THE 1 MiB BOUND IS ONE NUMBER ON BOTH PLANES ONLY BECAUSE OF MIGRATION 0065,
// which widened wisp_comments.text from TEXT's 65535 bytes to match the durable
// column. Before it, this operation — which resolves its anchor across both
// planes — put a caller under a bound that depended on which plane its id
// happened to name, with nothing above the storage seam able to say which. There
// is no client-side length check to add; the fix was the column, and the cases
// below exercise both planes through the same body.
//
// SEEDING STAYS ON THE REFERENCE STORE even though this client now HAS a
// Commenter. That is the harness rule and it bites hardest exactly here: a
// Commenter contract whose preconditions were seeded through the Commenter under
// test would prove only that the client agrees with itself.

func newServedCommenterFixture(t *testing.T, prefix string) conformance.CommenterFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	commenter, err := env.subject.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	return conformance.CommenterFixture{
		IssuePrefix:   env.prefix,
		Commenter:     commenter,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		SeedCommentAt: env.seedCommentAt,
	}
}

func TestServedCommenterContract(t *testing.T) {
	fixture := newServedCommenterFixture(t, "hcm")

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.CommenterFixture)
	}{
		{"StoresTextVerbatim", conformance.RunCommenterStoresTextVerbatim},
		{"ResultMirrorsTheStoredRow", conformance.RunCommenterResultMirrorsTheStoredRow},
		{"AdvancesALiveStampPastTheThreadsNewestComment", conformance.RunCommenterAdvancesALiveStampPastTheThreadsNewestComment},
		{"TakesTheClockWhenTheThreadIsBehindIt", conformance.RunCommenterTakesTheClockWhenTheThreadIsBehindIt},
		{"CommentOnAWispLandsOnTheWispThread", conformance.RunCommenterCommentOnAWispLandsOnTheWispThread},
		{"RefusesAnIDOnNeitherPlane", conformance.RunCommenterRefusesAnIDOnNeitherPlane},
		{"RefusesAnEmptyIssueID", conformance.RunCommenterRefusesAnEmptyIssueID},
		{"DoesNotResolvePrefixes", conformance.RunCommenterDoesNotResolvePrefixes},
		{"RecordsExactlyOneHistoryEntry", conformance.RunCommenterRecordsExactlyOneHistoryEntry},
		{"LeavesTheAnchorIssueUntouched", conformance.RunCommenterLeavesTheAnchorIssueUntouched},
		{"RefusesBlankText", conformance.RunCommenterRefusesBlankText},
		{"RefusesAnEmptyAuthor", conformance.RunCommenterRefusesAnEmptyAuthor},
		{"LeavesTheCallersRequestAlone", conformance.RunCommenterLeavesTheCallersRequestAlone},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}

// TestServedCommentAuthorTakesTheServersActorRule is L-comment-author's pin, and
// it RUNS.
//
// BOTH HALVES, because they are one rule with two consequences and a pin that
// checked one would let the other rot. The stored half is read RAW out of the
// comments table rather than off the result: a body that trimmed on the way in
// and echoed the caller's own string back would pass a result-only check, which
// is the same trap the contract's own verbatim-text case is written around.
//
// The premise is asserted before the divergence is: the reference store is asked
// the SAME question through its own Commenter, so "the two legs differ" is a
// measurement rather than an assumption. Without it a server that stopped
// trimming and a role that started would both read as this test passing.
func TestServedCommentAuthorTakesTheServersActorRule(t *testing.T) {
	env := newServedEnv(t, "hcma")
	commenter, err := env.subject.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	reference, err := env.reference.Commenter()
	if err != nil {
		t.Fatalf("reference Commenter(): %v", err)
	}
	ctx := t.Context()

	served, local := env.prefix+"-served", env.prefix+"-local"
	for _, id := range []string{served, local} {
		if err := env.createIssue(ctx, &types.Issue{
			ID: id, Title: "author rule", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// ── The stored value ───────────────────────────────────────────────────
	const padded = "  Ada Lovelace  "
	const trimmed = "Ada Lovelace"

	if _, err := commenter.AddComment(ctx, issueops.AddCommentRequest{
		Author: padded, IssueID: served, Text: "over the wire",
	}); err != nil {
		t.Fatalf("AddComment over http: %v", err)
	}
	if got := servedCommentAuthor(t, ctx, env, served); got != trimmed {
		t.Errorf("the http leg stored author %q, want %q — the operation applies the actor rule's trim (L-comment-author)", got, trimmed)
	}

	if _, err := reference.AddComment(ctx, issueops.AddCommentRequest{
		Author: padded, IssueID: local, Text: "through the reference role",
	}); err != nil {
		t.Fatalf("AddComment through the reference store: %v", err)
	}
	if got := servedCommentAuthor(t, ctx, env, local); got != padded {
		t.Errorf("the local leg stored author %q, want %q verbatim; if this leg started trimming too, "+
			"L-comment-author has retired and the row should go rather than this test being relaxed", got, padded)
	}

	// ── The refusal ────────────────────────────────────────────────────────
	//
	// A C1 introducer, which is the value the server's rule exists for: it lands
	// in a column every renderer of the thread prints.
	const escaping = "ada\x1b[31m"

	err = func() error {
		_, err := commenter.AddComment(ctx, issueops.AddCommentRequest{
			Author: escaping, IssueID: served, Text: "refused",
		})
		return err
	}()
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("AddComment with a control rune over http = %v, want ErrValidation", err)
	}

	if _, err := reference.AddComment(ctx, issueops.AddCommentRequest{
		Author: escaping, IssueID: local, Text: "stored",
	}); err != nil {
		t.Fatalf("AddComment with a control rune through the reference store = %v, want it STORED; "+
			"if the role started refusing this too, L-comment-author has retired", err)
	}
}

// servedCommentAuthor reads the newest comment's author column raw, which is the
// only place the stored value can be observed: the role's own result echoes what
// the write produced, and this case is about what LANDED.
func servedCommentAuthor(t *testing.T, ctx context.Context, env *servedEnv, issueID string) string {
	t.Helper()
	var author string
	if err := env.queryScalar(ctx,
		"SELECT author FROM comments WHERE issue_id = ? ORDER BY id DESC LIMIT 1", []any{issueID},
		&author); err != nil {
		t.Fatalf("read the stored author for %s: %v", issueID, err)
	}
	return author
}
