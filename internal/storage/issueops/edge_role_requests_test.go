package issueops

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func edge(from, to string, depType types.DependencyType) publicops.DependencyEdge {
	return publicops.DependencyEdge{IssueID: from, DependsOnID: to, Type: depType}
}

func TestValidateAddDependenciesRequest(t *testing.T) {
	valid := edge("bd-1", "bd-2", publicops.DepBlocks)
	for _, tc := range []struct {
		name    string
		request publicops.AddDependenciesRequest
		wantErr bool
	}{
		{
			name:    "one edge is valid",
			request: publicops.AddDependenciesRequest{Actor: "worker", Edges: []publicops.DependencyEdge{valid}},
		},
		{
			name:    "missing actor",
			request: publicops.AddDependenciesRequest{Edges: []publicops.DependencyEdge{valid}},
			wantErr: true,
		},
		{
			name:    "no edges",
			request: publicops.AddDependenciesRequest{Actor: "worker"},
			wantErr: true,
		},
		{
			name:    "edge without a source",
			request: publicops.AddDependenciesRequest{Actor: "worker", Edges: []publicops.DependencyEdge{edge("", "bd-2", publicops.DepBlocks)}},
			wantErr: true,
		},
		{
			name:    "edge without a target",
			request: publicops.AddDependenciesRequest{Actor: "worker", Edges: []publicops.DependencyEdge{edge("bd-1", "", publicops.DepBlocks)}},
			wantErr: true,
		},
		{
			name:    "edge without a type",
			request: publicops.AddDependenciesRequest{Actor: "worker", Edges: []publicops.DependencyEdge{edge("bd-1", "bd-2", "")}},
			wantErr: true,
		},
		{
			name:    "edge with a type one character longer than the column",
			request: publicops.AddDependenciesRequest{Actor: "worker", Edges: []publicops.DependencyEdge{edge("bd-1", "bd-2", types.DependencyType(strings.Repeat("x", types.MaxDependencyTypeLen+1)))}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAddDependenciesRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateAddDependenciesRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateAddDependenciesRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

// TestValidateAddDependenciesRequestAcceptsUnlistedTypes is the OWNER RULING
// as a test. The Dep* constants document an open, workspace-configurable set;
// they are not a whitelist. A validator that rejected a type merely because it
// is not one of them would refuse every workspace that configured its own, and
// it would do it at the contract boundary where nothing can route around it.
//
// The counterpart is the type check that DOES fire — that there is a type at
// all — pinned by the two cases above it. Membership is never asked about.
func TestValidateAddDependenciesRequestAcceptsUnlistedTypes(t *testing.T) {
	for _, depType := range []types.DependencyType{
		"blocks-ish",                // nothing like a built-in
		"caused-by",                 // built-in, but deliberately absent from the leaf constants
		types.DependencyType("私の型"), // a workspace that does not spell its vocabulary in ASCII
	} {
		request := publicops.AddDependenciesRequest{
			Actor: "worker",
			Edges: []publicops.DependencyEdge{edge("bd-1", "bd-2", depType)},
		}
		if err := ValidateAddDependenciesRequest(request); err != nil {
			t.Fatalf("ValidateAddDependenciesRequest() with type %q error = %v, want the open set to accept it", depType, err)
		}
	}
}

// TestValidateAddDependenciesRequestRefusesSelfEdges pins that the refusal is
// typed and that it fires ahead of any cycle probe. A blocking self-edge
// otherwise trips the cycle check and reports the wrong refusal, and
// SkipPerEdgeCycleCheck would skip the probe entirely — so this check cannot
// live inside it.
func TestValidateAddDependenciesRequestRefusesSelfEdges(t *testing.T) {
	for _, skip := range []bool{false, true} {
		request := publicops.AddDependenciesRequest{
			Actor:                 "worker",
			Edges:                 []publicops.DependencyEdge{edge("bd-1", "bd-1", publicops.DepBlocks)},
			SkipPerEdgeCycleCheck: skip,
		}
		err := ValidateAddDependenciesRequest(request)
		if !errors.Is(err, domain.ErrSelfDependency) {
			t.Fatalf("ValidateAddDependenciesRequest(SkipPerEdgeCycleCheck=%v) error = %v, want ErrSelfDependency", skip, err)
		}
	}
}

func TestValidateRemoveDependencyRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.RemoveDependencyRequest
		wantErr bool
	}{
		{
			name:    "both endpoints and an actor is valid",
			request: publicops.RemoveDependencyRequest{Actor: "worker", IssueID: "bd-1", DependsOnID: "bd-2"},
		},
		{
			name:    "missing actor",
			request: publicops.RemoveDependencyRequest{IssueID: "bd-1", DependsOnID: "bd-2"},
			wantErr: true,
		},
		{
			name:    "missing source",
			request: publicops.RemoveDependencyRequest{Actor: "worker", DependsOnID: "bd-2"},
			wantErr: true,
		},
		{
			name:    "missing target",
			request: publicops.RemoveDependencyRequest{Actor: "worker", IssueID: "bd-1"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRemoveDependencyRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateRemoveDependencyRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateRemoveDependencyRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

// TestAddDependenciesCommitMessageNamesTheRequest pins the two spellings the
// CLI already wrote, and the reason this message is composed from the REQUEST
// where the batch close's is composed from its result: the request is
// all-or-nothing, so what was asked for is exactly what landed.
func TestAddDependenciesCommitMessageNamesTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.AddDependenciesRequest
		want    string
	}{
		{
			name:    "one edge names both endpoints",
			request: publicops.AddDependenciesRequest{Edges: []publicops.DependencyEdge{edge("bd-1", "bd-2", publicops.DepBlocks)}},
			want:    "bd: dep add bd-1 bd-2",
		},
		{
			name: "several edges name their count",
			request: publicops.AddDependenciesRequest{Edges: []publicops.DependencyEdge{
				edge("bd-1", "bd-2", publicops.DepBlocks), edge("bd-3", "bd-4", publicops.DepParentChild),
			}},
			want: "dependency: add 2 edges",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AddDependenciesCommitMessage(tc.request); got != tc.want {
				t.Fatalf("AddDependenciesCommitMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAddCommentRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.AddCommentRequest
		wantErr bool
	}{
		{
			name:    "author, issue and text is valid",
			request: publicops.AddCommentRequest{Author: "worker", IssueID: "bd-1", Text: "hi"},
		},
		{
			name:    "missing author",
			request: publicops.AddCommentRequest{IssueID: "bd-1", Text: "hi"},
			wantErr: true,
		},
		{
			name:    "missing issue",
			request: publicops.AddCommentRequest{Author: "worker", Text: "hi"},
			wantErr: true,
		},
		{
			name:    "empty text",
			request: publicops.AddCommentRequest{Author: "worker", IssueID: "bd-1"},
			wantErr: true,
		},
		{
			name:    "whitespace-only text",
			request: publicops.AddCommentRequest{Author: "worker", IssueID: "bd-1", Text: " \n\t "},
			wantErr: true,
		},
		{
			// Blankness is decided on a trimmed copy; the value that lands in
			// the row is not trimmed, so leading whitespace is content.
			name:    "text that merely starts blank is content",
			request: publicops.AddCommentRequest{Author: "worker", IssueID: "bd-1", Text: "\n  still a comment"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAddCommentRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateAddCommentRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateAddCommentRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

// TestValidateRelatedRequestRefusesTheZeroDirection is the rule that makes the
// neighbor query honest. "out" and "in" answer inverse questions with
// identical shapes, so a defaulted direction hands a caller the wrong graph
// with nothing to notice.
func TestValidateRelatedRequestRefusesTheZeroDirection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.RelatedRequest
		wantErr bool
	}{
		{
			name:    "out is valid",
			request: publicops.RelatedRequest{ID: "bd-1", Direction: publicops.RelationOut},
		},
		{
			name:    "in is valid",
			request: publicops.RelatedRequest{ID: "bd-1", Direction: publicops.RelationIn},
		},
		{
			name:    "the zero direction is refused, not defaulted",
			request: publicops.RelatedRequest{ID: "bd-1"},
			wantErr: true,
		},
		{
			name:    "an unknown direction is refused",
			request: publicops.RelatedRequest{ID: "bd-1", Direction: publicops.RelationDirection("both")},
			wantErr: true,
		},
		{
			name:    "missing id",
			request: publicops.RelatedRequest{Direction: publicops.RelationOut},
			wantErr: true,
		},
		{
			name:    "an unusable type entry is refused",
			request: publicops.RelatedRequest{ID: "bd-1", Direction: publicops.RelationOut, Types: []types.DependencyType{""}},
			wantErr: true,
		},
		{
			// The open set again: a filter has to be able to name a type this
			// build has never heard of, or a workspace cannot filter by its own.
			name: "an unlisted type entry filters",
			request: publicops.RelatedRequest{ID: "bd-1", Direction: publicops.RelationOut,
				Types: []types.DependencyType{"blocks-ish"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRelatedRequest(tc.request)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateRelatedRequest() error = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateRelatedRequest() error = %v, want it to match ErrValidation", err)
			}
		})
	}
}

// TestFinishRelatedPagePinsTheOrder covers the ordering both Relations
// implementations answer in. The underlying reads walk two dependency tables
// in sequence, so without this the order tracks which plane a neighbor
// happens to live on.
func TestFinishRelatedPagePinsTheOrder(t *testing.T) {
	related := func(id string, depType types.DependencyType) *types.IssueWithDependencyMetadata {
		return &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: id},
			DependencyType: depType,
		}
	}
	items := []*types.IssueWithDependencyMetadata{
		related("bd-9", publicops.DepBlocks),
		related("bd-1", publicops.DepRelated),
		related("bd-1", publicops.DepBlocks),
		related("bd-5", publicops.DepParentChild),
	}

	got := FinishRelatedPage(items, nil)
	want := []string{"bd-1/blocks", "bd-1/related", "bd-5/parent-child", "bd-9/blocks"}
	if keys := relatedKeys(got); !equalStrings(keys, want) {
		t.Fatalf("FinishRelatedPage() order = %v, want %v", keys, want)
	}

	filtered := FinishRelatedPage(items, []types.DependencyType{publicops.DepBlocks})
	want = []string{"bd-1/blocks", "bd-9/blocks"}
	if keys := relatedKeys(filtered); !equalStrings(keys, want) {
		t.Fatalf("FinishRelatedPage(blocks) = %v, want %v", keys, want)
	}

	if empty := FinishRelatedPage(nil, nil); empty == nil {
		t.Fatal("FinishRelatedPage(nil) = nil, want an empty slice so a marshalled answer is [] and not null")
	}
}

func relatedKeys(items []*types.IssueWithDependencyMetadata) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID+"/"+string(item.DependencyType))
	}
	return keys
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
