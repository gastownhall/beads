package workapi

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeCommentStreamer records what was asked of it and answers from a map, so
// a test can assert on the CALLS as well as on the result. The recorded plane
// is the half no rendered output would show.
type fakeCommentStreamer struct {
	byID  map[string][]*types.Comment
	err   error
	calls []streamCall
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
	return storage.NewSliceIter(f.byID[id]), nil
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

	if err := HydrateListComments(context.Background(), srcFunc(src), items, false); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}

	if len(src.calls) != 0 {
		t.Errorf("count-only mode issued %d comment reads, want 0: the marker is derived from the count already on the row", len(src.calls))
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

	if err := HydrateListComments(context.Background(), srcFunc(src), items, true); err != nil {
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
}

// TestHydrateListCommentsRoutesTheWispPlane pins the argument the unit-of-work
// detail source needs and the store-backed one ignores. Getting it wrong is a
// wrong-table read on exactly one of the two seams, so no CLI output would
// show it — only the recorded call does.
func TestHydrateListCommentsRoutesTheWispPlane(t *testing.T) {
	pinnedDurable := false
	cases := []struct {
		name   string
		issue  *types.Issue
		isWisp bool
	}{
		{"durable", &types.Issue{ID: "a-1"}, false},
		{"ephemeral", &types.Issue{ID: "a-1", Ephemeral: true}, true},
		{"no_history", &types.Issue{ID: "a-1", NoHistory: true}, true},
		// The override is the case the flags get wrong: a promoted no-history
		// row is a durable issues-table row that still carries the flag.
		{"override_wins_over_flags", &types.Issue{ID: "a-1", NoHistory: true, WispPlaneOverride: &pinnedDurable}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeCommentStreamer{byID: map[string][]*types.Comment{"a-1": {comment("c1", "x")}}}
			items := []*types.IssueWithCounts{{Issue: tc.issue, CommentCount: 1}}
			if err := HydrateListComments(context.Background(), srcFunc(src), items, true); err != nil {
				t.Fatalf("HydrateListComments: %v", err)
			}
			if len(src.calls) != 1 {
				t.Fatalf("issued %d comment reads, want 1", len(src.calls))
			}
			if src.calls[0].isWisp != tc.isWisp {
				t.Errorf("read the %v plane, want %v", planeName(src.calls[0].isWisp), planeName(tc.isWisp))
			}
		})
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

	err := HydrateListComments(context.Background(), srcFunc(src), items, true)
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
	if err := HydrateListComments(context.Background(), nil, items, false); err != nil {
		t.Fatalf("count-only mode with a nil source: %v", err)
	}
	if items[0].CommentsOmitted == nil || !*items[0].CommentsOmitted {
		t.Error("count-only mode with a nil source did not mark the row omitted")
	}
	if err := HydrateListComments(context.Background(), nil, items, true); err == nil {
		t.Error("hydrating with a nil source returned nil, want an error")
	}
}

// TestHydrateListCommentsSkipsNilRows keeps the shared epilogue from panicking
// on a page shape a caller can legally hand it.
func TestHydrateListCommentsSkipsNilRows(t *testing.T) {
	src := &fakeCommentStreamer{byID: map[string][]*types.Comment{}}
	items := []*types.IssueWithCounts{nil, {Issue: nil, CommentCount: 2}, row("a-1", 0)}
	for _, include := range []bool{false, true} {
		if err := HydrateListComments(context.Background(), srcFunc(src), items, include); err != nil {
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

	if err := HydrateListComments(context.Background(), newSrc, items, false); err != nil {
		t.Fatalf("HydrateListComments: %v", err)
	}
	if built != 0 {
		t.Errorf("constructed %d comment sources in count-only mode, want 0", built)
	}
	if items[0].CommentsOmitted == nil || !*items[0].CommentsOmitted {
		t.Error("count-only mode did not mark the row omitted")
	}
}
