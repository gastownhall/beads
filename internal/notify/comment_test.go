package notify

import (
	"strings"
	"testing"
)

func TestCommentAssigneeSeat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		assignee, author string
		ok               bool
		seat             string
	}{
		{assignee: "wiseman", author: "firstmate", ok: true, seat: "wiseman"},
		{assignee: "wiseman", author: "wiseman", ok: false},
		{assignee: "WiseMan", author: "wiseman", ok: false},
		{assignee: "", author: "firstmate", ok: false},
		{assignee: "unassigned", author: "firstmate", ok: false},
		{assignee: "  portal-ops ", author: "firstmate", ok: true, seat: "portal-ops"},
	}
	for _, tc := range cases {
		got, ok := CommentAssigneeSeat(tc.assignee, tc.author)
		if ok != tc.ok || (ok && got != tc.seat) {
			t.Fatalf("CommentAssigneeSeat(%q, %q) = %q, %v want %q, %v",
				tc.assignee, tc.author, got, ok, tc.seat, tc.ok)
		}
	}
}

func TestCommentPingTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		title, author, text, want string
	}{
		{"Do thing", "firstmate", "please look at this", "Do thing — firstmate: please look at this"},
		{"Do thing", "firstmate", "   ", "Do thing — comment from firstmate"},
		{"Do thing", "", "hello", "Do thing — hello"},
		{"", "firstmate", "hello", "firstmate: hello"},
		{"Do thing", "", "", "Do thing"},
	}
	for _, tc := range cases {
		got := CommentPingTitle(tc.title, tc.author, tc.text)
		if got != tc.want {
			t.Fatalf("CommentPingTitle(%q, %q, %q) = %q want %q",
				tc.title, tc.author, tc.text, got, tc.want)
		}
	}
	got := CommentPingTitle("T", "a", strings.Repeat("x", 90))
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview not truncated: %q", got)
	}
}

func TestDefaultPrompt_CommentPlaybook(t *testing.T) {
	t.Parallel()
	p := DefaultPrompt(Record{Kind: KindComment, Seat: "wiseman", IssueID: "fm-c", Title: "hello from firstmate"})
	for _, want := range []string{"BEADS NOTIFY (comment)", "fm-c", "bd comments fm-c", "New comment on a bead your seat owns"} {
		if !strings.Contains(p, want) {
			t.Fatalf("comment prompt missing %q:\n%s", want, p)
		}
	}
}
