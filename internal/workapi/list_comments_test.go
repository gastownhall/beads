package workapi

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeCommentStreamer records what was asked of it and answers from a map, so
// a test can assert on the CALLS as well as on the result. The recorded plane
// is the half no rendered output would show.
//
// It answers PER PLANE, like the storage it stands in for: comments put in byID
// are on the durable plane and comments put in wispByID on the wisp one, so a
// read of the wrong table comes back empty here exactly as it does against a
// database. That is what lets a test assert the comment TEXT rather than only
// the recorded plane flag — the defect this file's plane coverage exists for
// produced an empty array, not a wrong one.
type fakeCommentStreamer struct {
	byID     map[string][]*types.Comment
	wispByID map[string][]*types.Comment
	err      error
	calls    []streamCall
}

type streamCall struct {
	id     string
	isWisp bool
}

func (f *fakeCommentStreamer) IterComments(_ context.Context, id string, isWisp bool) (storage.Iter[types.Comment], error) {
	f.calls = append(f.calls, streamCall{id: id, isWisp: isWisp})
	if f.err != nil {
		return nil, f.err
	}
	if isWisp {
		return storage.NewSliceIter(f.wispByID[id]), nil
	}
	return storage.NewSliceIter(f.byID[id]), nil
}

// fakePlanes is the seam that knows where a row lives, which on the production
// unit-of-work path is a query against the wisps table. It records the ids it
// was asked about so a test can pin that the lookup is bounded to the rows a
// comment read is actually issued for — and that a page needing no read pays
// for no lookup.
type fakePlanes struct {
	wisps map[string]bool
	err   error
	asked [][]string
}

// planesFor builds the answer for a page whose wisp-table residents are the
// given ids. Called with none, it is a page of durable rows.
func planesFor(wispIDs ...string) *fakePlanes {
	wisps := make(map[string]bool, len(wispIDs))
	for _, id := range wispIDs {
		wisps[id] = true
	}
	return &fakePlanes{wisps: wisps}
}

func (f *fakePlanes) resolve(_ context.Context, ids []string) (map[string]bool, error) {
	f.asked = append(f.asked, ids)
	if f.err != nil {
		return nil, f.err
	}
	return f.wisps, nil
}

// noPlaneLookup fails the test if the plane is resolved at all, which is the
// same assertion the failing source constructor makes one layer up: the routing
// query is part of the READ, so a page that reads nothing must not pay for it.
func noPlaneLookup(t *testing.T) CommentPlanes {
	t.Helper()
	return func(_ context.Context, ids []string) (map[string]bool, error) {
		t.Errorf("resolved comment planes for %v on a page that issues no comment read", ids)
		return nil, nil
	}
}

// srcFunc adapts a fake to the lazy constructor HydrateListComments takes.
// The laziness is itself under test in TestHydrateListCommentsCountOnlyBuildsNoSource.
func srcFunc(src CommentStreamer) func() CommentStreamer {
	return func() CommentStreamer { return src }
}

func row(id string, commentCount int) *types.IssueWithCounts {
	return &types.IssueWithCounts{
		Issue:        &types.Issue{ID: id},
		CommentCount: commentCount,
	}
}

func comment(id, text string) *types.Comment {
	return &types.Comment{ID: id, Text: text}
}

// TestHydrateListCommentsMarksOmittedWithoutFetching pins the DEFAULT half of
// the contract, which is the half be-73x is actually about: a page nobody
// asked to hydrate must still say that its comment text is missing, and it
// must say so without paying for a single read.
func TestHydrateListCommentsMarksOmittedWithoutFetching(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{
		"a-1": {comment("c1", "root cause")},
	}}
	items := []*types.IssueWithCounts{row("a-1", 1), row("a-2", 0)}
	planes := planesFor()

	if err := HydrateListComments(context.Background(), srcFunc(src), planes.resolve, items, false, true); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}

	if len(src.calls) != 0 {
		t.Errorf("count-only mode issued %d comment reads, want 0: the marker is derived from the count already on the row", len(src.calls))
	}
	// The plane lookup is part of the read, so a page that reads nothing must
	// not pay for it either: the marker path routes no query and so asks no
	// table where anything lives.
	if len(planes.asked) != 0 {
		t.Errorf("count-only mode resolved comment planes %d times, want 0: nothing is being read, so nothing needs routing", len(planes.asked))
	}
	if items[0].CommentsOmitted == nil || !*items[0].CommentsOmitted {
		t.Errorf("CommentsOmitted = %v on a row with comment_count 1, want true: without it an absent comments field reads as none", items[0].CommentsOmitted)
	}
	if items[0].Comments != nil {
		t.Errorf("Comments = %v in count-only mode, want nil", items[0].Comments)
	}
	// The zero-count row is the control. A marker on it would make the flag
	// meaningless: every row would carry it and none would be informative.
	if items[1].CommentsOmitted != nil {
		t.Errorf("CommentsOmitted = %v on a row with no comments, want unset: a true empty stays plain omission", *items[1].CommentsOmitted)
	}
}

// TestHydrateListCommentsPopulatesBodies pins the opt-in half, and asserts on
// the COMMENT TEXT rather than on a length: the defect this fixes is that the
// text is unreachable, and a count of rows cannot tell a populated slice from
// a slice of empty ones.
func TestHydrateListCommentsPopulatesBodies(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{
		"a-1": {comment("c1", "zzzuniquephrase"), comment("c2", "second")},
	}}
	items := []*types.IssueWithCounts{row("a-1", 2), row("a-2", 0)}
	planes := planesFor()

	if err := HydrateListComments(context.Background(), srcFunc(src), planes.resolve, items, true, true); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}

	if got := len(items[0].Comments); got != 2 {
		t.Fatalf("hydrated %d comments, want 2", got)
	}
	if got := items[0].Comments[0].Text; got != "zzzuniquephrase" {
		t.Errorf("comment body = %q, want %q: the bodies are the whole point", got, "zzzuniquephrase")
	}
	if items[0].CommentsOmitted != nil {
		t.Errorf("CommentsOmitted = %v beside a populated slice, want unset: the two fields must read as one unambiguous answer", *items[0].CommentsOmitted)
	}
	// A row with no comments is not queried at all, which is what keeps the
	// per-row cost proportional to the rows that actually have text.
	for _, call := range src.calls {
		if call.id == "a-2" {
			t.Errorf("issued a comment read for a row with comment_count 0")
		}
	}
	// And the plane lookup covers that same narrowed set, not the whole page.
	if len(planes.asked) != 1 || !slices.Equal(planes.asked[0], []string{"a-1"}) {
		t.Errorf("resolved planes for %v, want one lookup for [a-1]: routing is asked about the rows being read", planes.asked)
	}
}

// TestHydrateListCommentsRoutesByResidenceNotFlags pins where the comment plane
// comes from, and the two rows in the middle of the table are the reason it is
// not the row's flags.
//
// Both of those rows are states the tree maintains on purpose. `bd import` pins
// a no_history record to the DURABLE table and preserves the flag on the row,
// because clearing it would change the content hash and break
// export→import→export byte-identity — so a promoted no-history bead that has
// been through a JSONL round-trip (which is how a clone materializes its
// database) is a durable row whose flags say wisp. The mirror case is an import
// record whose wisp_plane key pins it to the WISPS table with neither flag set.
// An earlier version of this body read Ephemeral || NoHistory, so it queried
// wisp_comments for the first and comments for the second, found nothing either
// way, and returned the row with its nonzero comment_count, no comments, no
// marker and no error — the silent hole be-73x exists to close, under the flag
// that promises the bodies or an error.
//
// The fixture puts each row's comments only on the plane it actually lives on,
// so a wrong-table read shows up as MISSING TEXT and not merely as a wrong flag
// in a recorded call.
func TestHydrateListCommentsRoutesByResidenceNotFlags(t *testing.T) {
	cases := []struct {
		name    string
		issue   *types.Issue
		inWisps bool
	}{
		{"durable row, no flags", &types.Issue{ID: "a-1"}, false},
		// The flags say wisp and the table says durable: the import-pinned
		// promoted bead. Residence wins.
		{"durable row carrying no_history", &types.Issue{ID: "a-1", NoHistory: true}, false},
		// The mirror: the flags say durable and the row is in the wisps table.
		{"wisp-table row carrying no flags", &types.Issue{ID: "a-1"}, true},
		{"wisp-table row carrying ephemeral", &types.Issue{ID: "a-1", Ephemeral: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeCommentStreamer{}
			planes := planesFor()
			if tc.inWisps {
				src.wispByID = map[string][]*types.Comment{"a-1": {comment("c1", "zzzuniquephrase")}}
				planes = planesFor("a-1")
			} else {
				src.byID = map[string][]*types.Comment{"a-1": {comment("c1", "zzzuniquephrase")}}
			}
			items := []*types.IssueWithCounts{{Issue: tc.issue, CommentCount: 1}}

			if err := HydrateListComments(context.Background(), srcFunc(src), planes.resolve, items, true, true); err != nil {
				t.Fatalf("HydrateListComments: %v", err)
			}

			if len(src.calls) != 1 {
				t.Fatalf("issued %d comment reads, want 1", len(src.calls))
			}
			if src.calls[0].isWisp != tc.inWisps {
				t.Errorf("read the %v plane for a row living on the %v plane", planeName(src.calls[0].isWisp), planeName(tc.inWisps))
			}
			if got := len(items[0].Comments); got != 1 {
				t.Fatalf("hydrated %d comments, want 1: a read of the other plane comes back empty, with a nonzero comment_count still on the row", got)
			}
			if got := items[0].Comments[0].Text; got != "zzzuniquephrase" {
				t.Errorf("comment body = %q, want %q", got, "zzzuniquephrase")
			}
			if items[0].CommentsOmitted != nil {
				t.Errorf("CommentsOmitted = %v on a hydrated row, want unset", *items[0].CommentsOmitted)
			}
		})
	}
}

// TestHydrateListCommentsRequiresAPlaneAnswer pins that the routing question has
// to be ANSWERED rather than defaulted.
//
// A nil CommentPlanes would read every row on the durable plane, which is the
// wrong-table read of the test above wearing a default instead of an inference,
// and the seam that gets it wrong is the one whose source cannot detect it. A
// caller whose source routes ids itself says so by name
// (SourceRoutesCommentPlanes), so the two cases are distinguishable at the call
// site and a third seam has to make the choice rather than inherit it.
func TestHydrateListCommentsRequiresAPlaneAnswer(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{"a-1": {comment("c1", "x")}}}
	items := []*types.IssueWithCounts{row("a-1", 1)}

	if err := HydrateListComments(context.Background(), srcFunc(src), nil, items, true, true); err == nil {
		t.Error("hydrating with no plane resolution returned nil, want an error: an unanswered plane defaults to a wrong-table read no caller can see")
	}
	if len(src.calls) != 0 {
		t.Errorf("issued %d comment reads without knowing the plane, want 0", len(src.calls))
	}
	// The marker path routes nothing, so it needs no answer and must not
	// demand one: a caller can pass the same arguments either way.
	if err := HydrateListComments(context.Background(), srcFunc(src), nil, items, false, true); err != nil {
		t.Errorf("count-only mode with no plane resolution: %v", err)
	}
	// SourceRoutesCommentPlanes is the explicit form of "durable for everyone,
	// and the source is the one that knows better".
	if err := HydrateListComments(context.Background(), srcFunc(src), SourceRoutesCommentPlanes, items, true, true); err != nil {
		t.Fatalf("HydrateListComments with SourceRoutesCommentPlanes: %v", err)
	}
	if len(src.calls) != 1 || src.calls[0].isWisp {
		t.Errorf("calls = %v, want one durable-plane read: the source is left to route the id", src.calls)
	}
}

// TestHydrateListCommentsFailsWhenThePlaneIsUnknown extends hydrate-or-error to
// the routing lookup. A page that cannot find out where its rows live must not
// fall back to a guess: guessing is what returned empty comment arrays beside
// nonzero counts, and it is worse here because the failure would be invisible in
// the answer rather than reported in it.
func TestHydrateListCommentsFailsWhenThePlaneIsUnknown(t *testing.T) {
	sentinel := errors.New("wisps table unreachable")
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{"a-1": {comment("c1", "x")}}}
	planes := planesFor()
	planes.err = sentinel
	items := []*types.IssueWithCounts{row("a-1", 1)}

	err := HydrateListComments(context.Background(), srcFunc(src), planes.resolve, items, true, true)
	if err == nil {
		t.Fatal("HydrateListComments returned nil when the comment plane could not be resolved, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap %v", err, sentinel)
	}
	if len(src.calls) != 0 {
		t.Errorf("issued %d comment reads after the plane lookup failed, want 0: a read on a guessed plane answers with the wrong table", len(src.calls))
	}
}

func planeName(isWisp bool) string {
	if isWisp {
		return "wisp"
	}
	return "durable"
}

// TestHydrateListCommentsFailsRatherThanShortening pins the promise that makes
// the opt-in half trustworthy. A caller that asked for the bodies and silently
// received fewer than exist is back in the failure this whole change removes,
// with no way to detect it.
func TestHydrateListCommentsFailsRatherThanShortening(t *testing.T) {
	sentinel := errors.New("comment table unreachable")
	src := &fakeCommentStreamer{err: sentinel}
	items := []*types.IssueWithCounts{row("a-1", 1)}

	err := HydrateListComments(context.Background(), srcFunc(src), planesFor().resolve, items, true, true)
	if err == nil {
		t.Fatal("HydrateListComments returned nil on a failing read, want an error: a short list a caller cannot detect is the defect, not a degraded success")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap %v", err, sentinel)
	}
}

// TestHydrateListCommentsCountOnlyToleratesNoSource pins that the default path
// needs no comment source at all, which is what lets a caller pass one
// unconditionally without deciding whether it will be used.
func TestHydrateListCommentsCountOnlyToleratesNoSource(t *testing.T) {
	items := []*types.IssueWithCounts{row("a-1", 3)}
	if err := HydrateListComments(context.Background(), nil, planesFor().resolve, items, false, true); err != nil {
		t.Fatalf("count-only mode with a nil source: %v", err)
	}
	if items[0].CommentsOmitted == nil || !*items[0].CommentsOmitted {
		t.Error("count-only mode with a nil source did not mark the row omitted")
	}
	if err := HydrateListComments(context.Background(), nil, planesFor().resolve, items, true, true); err == nil {
		t.Error("hydrating with a nil source returned nil, want an error")
	}
}

// TestHydrateListCommentsSkipsNilRows keeps the shared epilogue from panicking
// on a page shape a caller can legally hand it.
func TestHydrateListCommentsSkipsNilRows(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{}}
	items := []*types.IssueWithCounts{nil, {Issue: nil, CommentCount: 2}, row("a-1", 0)}
	for _, include := range []bool{false, true} {
		if err := HydrateListComments(context.Background(), srcFunc(src), planesFor().resolve, items, include, true); err != nil {
			t.Fatalf("include=%v: %v", include, err)
		}
	}
}

// TestHydrateListCommentsCountOnlyBuildsNoSource pins the laziness as a
// behaviour rather than an implementation detail.
//
// It is here because the eager version shipped and broke something: building
// the unit-of-work comment source unconditionally panicked for a provider that
// has no comment use case, on list requests that had asked for no comments.
// A count-only listing must not so much as CONSTRUCT a comment reader, so the
// constructor here fails the test if it is ever called.
func TestHydrateListCommentsCountOnlyBuildsNoSource(t *testing.T) {
	built := 0
	newSrc := func() CommentStreamer {
		built++
		t.Error("count-only hydration constructed a comment source; the dependency must be as opt-in as the feature")
		return &fakeCommentStreamer{}
	}
	items := []*types.IssueWithCounts{row("a-1", 4), row("a-2", 0)}

	if err := HydrateListComments(context.Background(), newSrc, noPlaneLookup(t), items, false, true); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}
	if built != 0 {
		t.Errorf("constructed %d comment sources in count-only mode, want 0", built)
	}
	if items[0].CommentsOmitted == nil || !*items[0].CommentsOmitted {
		t.Error("count-only mode did not mark the row omitted")
	}
}

// TestHydrateListCommentsHonorsIncludeWhenCountsAreSkipped is the regression
// for the combination that made the count-based skip unsound.
//
// issueops.ListRequest.SkipCounts zeroes CommentCount and says in its own doc
// that a zero then means UNKNOWN. A body that reads the count as authoritative
// therefore answers IncludeComments with an entirely empty page — no error, no
// short-list marker, nothing. That is the same silent-omission failure this
// whole change exists to remove, reintroduced one layer down and reachable by
// any caller of a PUBLIC request type.
func TestHydrateListCommentsHonorsIncludeWhenCountsAreSkipped(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{
		"a-1": {comment("c1", "zzzuniquephrase")},
	}}
	// Counts skipped: every row reports zero whether or not it has comments.
	items := []*types.IssueWithCounts{row("a-1", 0), row("a-2", 0)}
	planes := planesFor()

	if err := HydrateListComments(context.Background(), srcFunc(src), planes.resolve, items, true, false); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}

	if len(src.calls) != 2 {
		t.Fatalf("issued %d comment reads, want 2: with no trustworthy count, every row must be queried", len(src.calls))
	}
	// The plane lookup widens with them: a page whose counts prove nothing
	// cannot narrow the set whose residence has to be resolved either.
	if len(planes.asked) != 1 || !slices.Equal(planes.asked[0], []string{"a-1", "a-2"}) {
		t.Errorf("resolved planes for %v, want one lookup for [a-1 a-2]", planes.asked)
	}
	if got := len(items[0].Comments); got != 1 {
		t.Fatalf("hydrated %d comments for the row that has one, want 1", got)
	}
	if got := items[0].Comments[0].Text; got != "zzzuniquephrase" {
		t.Errorf("comment body = %q, want %q", got, "zzzuniquephrase")
	}
	if items[1].Comments != nil {
		t.Errorf("row with genuinely no comments got %v, want nil", items[1].Comments)
	}
}

// TestHydrateListCommentsMarksNothingWhenCountsAreSkipped is the other side of
// the same unknown. CommentsOmitted asserts "this row HAS comments and they are
// not here"; a page whose counts were skipped cannot support that claim about
// any row, so it must make it about none rather than deriving it from a zero
// that means nothing.
func TestHydrateListCommentsMarksNothingWhenCountsAreSkipped(t *testing.T) {
	items := []*types.IssueWithCounts{row("a-1", 0), row("a-2", 0)}
	if err := HydrateListComments(context.Background(), nil, nil, items, false, false); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}
	for _, item := range items {
		if item.CommentsOmitted != nil {
			t.Errorf("%s carries comments_omitted on a page with no trustworthy counts, want unset: the marker would be a claim the page cannot support", item.ID)
		}
	}
}
