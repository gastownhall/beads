package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeChildRelations is an issueops.Relations that records the request it was
// asked and answers with n children or err.
type fakeChildRelations struct {
	n   int
	err error
	got []issueops.RelatedRequest
}

func (f *fakeChildRelations) Related(_ context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*issueops.RelatedIssue, 0, f.n)
	for i := 0; i < f.n; i++ {
		iss := &issueops.RelatedIssue{DependencyType: types.DepParentChild}
		iss.ID = fmt.Sprintf("hc-step%d", i)
		out = append(out, iss)
	}
	return out, nil
}

// runNoteHiddenChildren runs the note helper with stdout and stderr captured.
func runNoteHiddenChildren(t *testing.T, rel issueops.Relations, anchorID, view string) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	return captureStdio(t, func() {
		noteHiddenChildren(context.Background(), rel, anchorID, view)
	})
}

// TestNoteHiddenChildren pins the GH#5151 stderr note the default down views of
// bd dep list / bd dep tree print for an anchor with parent-child children.
func TestNoteHiddenChildren(t *testing.T) {
	const firstLine = "note: hc-root has %d child issue(s) not shown here: this view lists what hc-root depends on, and its children depend on it (parent-child).\n"

	t.Run("list_26", func(t *testing.T) {
		rel := &fakeChildRelations{n: 26}
		stdout, stderr := runNoteHiddenChildren(t, rel, "hc-root", "list")
		want := fmt.Sprintf(firstLine, 26) + "To list them, run: bd dep list hc-root --direction=up --type=parent-child\n"
		if stderr != want {
			t.Errorf("stderr:\n got %q\nwant %q", stderr, want)
		}
		if stdout != "" {
			t.Errorf("stdout must stay empty, got %q", stdout)
		}
		wantReq := []issueops.RelatedRequest{{
			ID:        "hc-root",
			Direction: issueops.RelationIn,
			Types:     []types.DependencyType{types.DepParentChild},
		}}
		if !reflect.DeepEqual(rel.got, wantReq) {
			t.Errorf("request:\n got %#v\nwant %#v", rel.got, wantReq)
		}
	})

	t.Run("tree_26", func(t *testing.T) {
		stdout, stderr := runNoteHiddenChildren(t, &fakeChildRelations{n: 26}, "hc-root", "tree")
		want := fmt.Sprintf(firstLine, 26) + "To see them, run: bd dep tree hc-root --direction=up\n"
		if stderr != want {
			t.Errorf("stderr:\n got %q\nwant %q", stderr, want)
		}
		if stdout != "" {
			t.Errorf("stdout must stay empty, got %q", stdout)
		}
	})

	t.Run("one_child", func(t *testing.T) {
		_, stderr := runNoteHiddenChildren(t, &fakeChildRelations{n: 1}, "hc-root", "list")
		if !strings.HasPrefix(stderr, fmt.Sprintf(firstLine, 1)) {
			t.Errorf("stderr = %q, want the 1-child note", stderr)
		}
	})

	t.Run("no_children", func(t *testing.T) {
		stdout, stderr := runNoteHiddenChildren(t, &fakeChildRelations{}, "hc-root", "list")
		if stdout != "" || stderr != "" {
			t.Errorf("no children must print nothing, got stdout %q stderr %q", stdout, stderr)
		}
	})

	// AC-8: a failing inbound read prints nothing and does not panic.
	t.Run("read_error", func(t *testing.T) {
		for _, view := range []string{"list", "tree"} {
			stdout, stderr := runNoteHiddenChildren(t, &fakeChildRelations{n: 3, err: errors.New("boom")}, "hc-root", view)
			if stdout != "" || stderr != "" {
				t.Errorf("%s: a read error must print nothing, got stdout %q stderr %q", view, stdout, stderr)
			}
		}
	})

	t.Run("unsafe_id_uses_placeholder", func(t *testing.T) {
		// A control character no shell quoting can carry onto a pasted line.
		id := "hc-\x1b[2K"
		_, stderr := runNoteHiddenChildren(t, &fakeChildRelations{n: 2}, id, "list")
		if strings.Contains(stderr, "\x1b") {
			t.Errorf("unsafe id printed raw:\n%q", stderr)
		}
		if !strings.Contains(stderr, "To list them, run: bd dep list <issue-id> --direction=up --type=parent-child  (<issue-id>: ") {
			t.Errorf("want the placeholder command and its note, got:\n%s", stderr)
		}
	})
}
