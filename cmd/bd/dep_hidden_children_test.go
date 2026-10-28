package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// hiddenChildrenRelations is a fake Relations that records the one request
// noteHiddenChildren makes and answers with a canned page.
type hiddenChildrenRelations struct {
	request issueops.RelatedRequest
	issues  []*issueops.RelatedIssue
	err     error
}

func (r *hiddenChildrenRelations) Related(_ context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	r.request = req
	if r.err != nil {
		return nil, r.err
	}
	return r.issues, nil
}

// hiddenChildrenPage builds n parent-child neighbors, the page the inbound
// parent-child read answers with for an anchor with n children.
func hiddenChildrenPage(n int) []*issueops.RelatedIssue {
	issues := make([]*issueops.RelatedIssue, n)
	for i := range issues {
		issues[i] = &issueops.RelatedIssue{
			Issue: types.Issue{
				ID:       fmt.Sprintf("hc-%04d", i),
				Title:    "hidden step",
				Priority: 2,
				Status:   types.StatusOpen,
			},
			DependencyType: types.DepParentChild,
		}
	}
	return issues
}

// TestNoteHiddenChildren pins the stderr-only note the default human down
// views of `bd dep list` and `bd dep tree` print for an anchor whose children
// reach it through inbound parent-child edges (GH#5151, bd-uuw3nk): the two
// exact lines per view, the nothing-printed cases, and the request the count
// comes from — the same inbound parent-child read `bd dep list <id>
// --direction=up --type=parent-child` makes, so the note's count always
// equals what its printed command lists.
func TestNoteHiddenChildren(t *testing.T) {
	const anchor = "hc-root0001"

	// The note's first line, shared by both views.
	firstLine := func(id string, n int) string {
		return fmt.Sprintf("note: %s has %d child issue(s) not shown here: this view lists what %s depends on, and its children depend on it (parent-child).\n", id, n, id)
	}

	// 26 children, list view: both lines, exactly.
	rel := &hiddenChildrenRelations{issues: hiddenChildrenPage(26)}
	stderr := captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, anchor, "list")
	})
	want := firstLine(anchor, 26) +
		fmt.Sprintf("To list them, run: bd dep list %s --direction=up --type=parent-child\n", anchor)
	if stderr != want {
		t.Errorf("list note:\ngot  %q\nwant %q", stderr, want)
	}

	// The request is the read the printed command makes.
	if rel.request.ID != anchor || rel.request.Direction != issueops.RelationIn {
		t.Errorf("request = %+v, want ID %s and direction %s", rel.request, anchor, issueops.RelationIn)
	}
	if len(rel.request.Types) != 1 || rel.request.Types[0] != types.DepParentChild {
		t.Errorf("request types = %v, want [%s]", rel.request.Types, types.DepParentChild)
	}

	// 26 children, tree view: same first line, the tree remedy.
	rel = &hiddenChildrenRelations{issues: hiddenChildrenPage(26)}
	stderr = captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, anchor, "tree")
	})
	want = firstLine(anchor, 26) +
		fmt.Sprintf("To see them, run: bd dep tree %s --direction=up\n", anchor)
	if stderr != want {
		t.Errorf("tree note:\ngot  %q\nwant %q", stderr, want)
	}

	// One child still reads as "1 child issue(s)".
	rel = &hiddenChildrenRelations{issues: hiddenChildrenPage(1)}
	stderr = captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, anchor, "list")
	})
	if !strings.Contains(stderr, "has 1 child issue(s) not shown here") {
		t.Errorf("one-child note should count 1, got:\n%s", stderr)
	}

	// No children: nothing at all, so a childless anchor's output is
	// indistinguishable from what it was before the note existed.
	rel = &hiddenChildrenRelations{}
	stderr = captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, anchor, "list")
	})
	if stderr != "" {
		t.Errorf("childless anchor should print nothing, got:\n%s", stderr)
	}

	// An id no shell bd prints a command for passes on as one word: the
	// command takes the placeholder and names the id escaped beside it
	// (remedy.CommandArg), and the note line names it the same way
	// (printedIssueID), so it cannot redraw the command.
	const weird = "hc-ba'd"
	// The exact spelling remedy.EscapedValue gives an apostrophe.
	const escapedWeird = `"hc-ba\x27d"`
	rel = &hiddenChildrenRelations{issues: hiddenChildrenPage(1)}
	stderr = captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, weird, "list")
	})
	want = firstLine(escapedWeird, 1) +
		fmt.Sprintf("To list them, run: bd dep list <issue-id> --direction=up --type=parent-child  (<issue-id>: %s)\n", escapedWeird)
	if stderr != want {
		t.Errorf("unprintable-id note:\ngot  %q\nwant %q", stderr, want)
	}

	// A Relations read that errors prints nothing and does not panic: the
	// command's actual answer was already produced, so the note is
	// best-effort and a failed read must not touch the view's output or
	// exit status.
	rel = &hiddenChildrenRelations{err: errors.New("boom")}
	stderr = captureStderr(t, func() {
		noteHiddenChildren(context.Background(), rel, anchor, "list")
	})
	if stderr != "" {
		t.Errorf("an erroring read should print nothing, got:\n%s", stderr)
	}
}
