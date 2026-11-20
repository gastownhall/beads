package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Guard for the stderr twin of the ga-clgh contract: `bd show --json` without
// --include-comments carries comment_count + comments_omitted in the payload,
// but a consumer that does not know the schema reads the absent `comments`
// key as "no comments" (#5078). One line on stderr must name the id, the
// count, and the flag that fixes it — stdout stays byte-identical. These
// tests pin the helper; comments_omitted_hint_cli_test.go pins the command.

func hintOmitted(v bool) *bool { return &v }
func hintCount(n int64) *int64 { return &n }

func hintDetails(id string, count *int64, omitted *bool) *issueops.IssueDetails {
	return &issueops.IssueDetails{
		Issue:           types.Issue{ID: id},
		CommentCount:    count,
		CommentsOmitted: omitted,
	}
}

func TestCommentsOmittedHint_NamesIDCountAndFlag(t *testing.T) {
	got := commentsOmittedHint(hintDetails("bd-42", hintCount(3), hintOmitted(true)))
	if !strings.HasPrefix(got, "Hint: ") {
		t.Errorf("hint must use the neighbouring Hint: form (showNotFoundHint), got: %q", got)
	}
	if !strings.Contains(got, "bd-42") || !strings.Contains(got, "3 comments") ||
		!strings.Contains(got, "--include-comments") {
		t.Errorf("hint must name id, count and the flag, got: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("hint must be one terminated line, got: %q", got)
	}
	if strings.Contains(got, "stdout") {
		t.Errorf("hint line leaked stdout advice: %q", got)
	}
}

func TestCommentsOmittedHint_SingularForOneComment(t *testing.T) {
	got := commentsOmittedHint(hintDetails("bd-42", hintCount(1), hintOmitted(true)))
	if !strings.Contains(got, "1 comment not shown") {
		t.Errorf("one withheld comment must read \"1 comment\", got: %q", got)
	}
	if strings.Contains(got, "1 comments") {
		t.Errorf("hint rendered plural for a single comment: %q", got)
	}
}

func TestCommentsOmittedHint_SilentWhenNotWithheld(t *testing.T) {
	cases := map[string]interface{}{
		"hydrated row (no marker)":  hintDetails("bd-1", hintCount(3), nil),
		"marker false":              hintDetails("bd-2", hintCount(3), hintOmitted(false)),
		"zero count with marker":    hintDetails("bd-3", hintCount(0), hintOmitted(true)),
		"missing count with marker": hintDetails("bd-4", nil, hintOmitted(true)),
		"nil details":               (*issueops.IssueDetails)(nil),
		"not a detail view":         "bd-5",
	}
	for name, v := range cases {
		if h := commentsOmittedHint(v); h != "" {
			t.Errorf("%s: expected silence, got %q", name, h)
		}
	}
}

func TestCommentsOmittedHint_IsIDSpecific(t *testing.T) {
	a := commentsOmittedHint(hintDetails("bd-aaa", hintCount(1), hintOmitted(true)))
	b := commentsOmittedHint(hintDetails("bd-bbb", hintCount(2), hintOmitted(true)))
	if !strings.Contains(a, "bd-aaa") || strings.Contains(a, "bd-bbb") {
		t.Errorf("hint must reference only its own issue: %q / %q", a, b)
	}
	if !strings.Contains(b, "bd-bbb") || strings.Contains(b, "bd-aaa") {
		t.Errorf("hint must reference only its own issue: %q / %q", a, b)
	}
}

// TestWriteCommentsOmittedHints_QuietGate pins the --quiet contract at the
// emit point, for both show routes (embedded show.go and proxied
// show_proxied_server.go call this same function): --quiet is documented as
// "Suppress non-essential output (errors only)" and is the escape hatch for
// consumers merging streams, so it must actually silence the hint.
func TestWriteCommentsOmittedHints_QuietGate(t *testing.T) {
	oldQuiet := quietFlag
	t.Cleanup(func() { quietFlag = oldQuiet })

	capture := func(t *testing.T, quiet bool) string {
		t.Helper()
		quietFlag = quiet
		oldStderr := os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = w
		writeCommentsOmittedHints([]interface{}{
			hintDetails("bd-quiet", hintCount(4), hintOmitted(true)),
			hintDetails("bd-hydrated", hintCount(4), nil),
		})
		w.Close()
		os.Stderr = oldStderr
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	if got := capture(t, false); got != "Hint: bd-quiet has 4 comments not shown; add --include-comments\n" {
		t.Errorf("without --quiet the withheld issue must emit exactly its hint line, got: %q", got)
	}
	if got := capture(t, true); got != "" {
		t.Errorf("--quiet must suppress the hint, got: %q", got)
	}
}
