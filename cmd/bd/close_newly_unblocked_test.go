package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// unblockedSections parses printNewlyUnblocked's output back into the two
// buckets it claims to separate, keyed by the heading each bullet appeared
// under. It asserts on IDs rather than rendered lines because
// formatFeedbackID suppresses titles when output.title-length is unset, which
// is exactly the state a unit test runs in.
//
// A bullet printed before any heading is a failure, not an empty bucket: the
// whole point of the split is that every listed issue carries the label that
// applies to it.
func unblockedSections(t *testing.T, out string) (ready, stillBlocked []string) {
	t.Helper()
	section := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Newly unblocked:"):
			section = "ready"
			continue
		case strings.HasPrefix(line, "Still status=blocked"):
			section = "blocked"
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "•") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(trimmed, "•"))
		if len(fields) == 0 {
			t.Fatalf("bullet with no id: %q", line)
		}
		switch section {
		case "ready":
			ready = append(ready, fields[0])
		case "blocked":
			stillBlocked = append(stillBlocked, fields[0])
		default:
			t.Fatalf("bullet %q printed before any heading", trimmed)
		}
	}
	return ready, stillBlocked
}

func issueWithStatus(id string, status types.Status) *types.Issue {
	return &types.Issue{ID: id, Title: "t " + id, Status: status, Priority: 2, IssueType: types.TypeTask}
}

// TestPrintNewlyUnblocked is the user-visible half of be-ntbxt. bd close
// reports GetNewlyUnblockedByClose's result, and is_blocked really does clear
// for every issue in it — but the manually-set status='blocked' does not, and
// nothing in bd clears it. An issue still at that status is NOT in 'bd ready',
// so listing it under "Newly unblocked" asserts an outcome that did not
// happen: the message that made 29 beads look handled while they stayed
// stranded. The split must key on the status, print neither heading when its
// bucket is empty, and tell the still-blocked ones how to actually get fixed.
func TestPrintNewlyUnblocked(t *testing.T) {
	const fixHint = "bd recompute-blocked --status --fix"

	t.Run("genuinely ready issues keep the unblocked heading", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printNewlyUnblocked([]*types.Issue{
				issueWithStatus("nu-open", types.StatusOpen),
				issueWithStatus("nu-wip", types.StatusInProgress),
			})
			return nil
		})
		ready, blocked := unblockedSections(t, out)
		if want := []string{"nu-open", "nu-wip"}; !sameIDOrder(ready, want) {
			t.Errorf("ready bucket = %v, want %v\nraw:\n%s", ready, want, out)
		}
		if len(blocked) != 0 {
			t.Errorf("still-blocked bucket = %v, want empty\nraw:\n%s", blocked, out)
		}
		if strings.Contains(out, "Still status=blocked") {
			t.Errorf("printed an empty still-blocked heading:\n%s", out)
		}
	})

	t.Run("status=blocked issues are never called newly unblocked", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printNewlyUnblocked([]*types.Issue{issueWithStatus("nu-stuck", types.StatusBlocked)})
			return nil
		})
		ready, blocked := unblockedSections(t, out)
		if len(ready) != 0 {
			t.Errorf("ready bucket = %v, want empty — this is the be-ntbxt lie\nraw:\n%s", ready, out)
		}
		if want := []string{"nu-stuck"}; !sameIDOrder(blocked, want) {
			t.Errorf("still-blocked bucket = %v, want %v\nraw:\n%s", blocked, want, out)
		}
		if strings.Contains(out, "Newly unblocked:") {
			t.Errorf("printed an empty newly-unblocked heading:\n%s", out)
		}
		// The whole reason to name the state is to hand over the repair.
		if !strings.Contains(out, fixHint) {
			t.Errorf("still-blocked section must name %q:\n%s", fixHint, out)
		}
	})

	t.Run("a mixed batch splits by status", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printNewlyUnblocked([]*types.Issue{
				issueWithStatus("nu-a", types.StatusOpen),
				issueWithStatus("nu-b", types.StatusBlocked),
				issueWithStatus("nu-c", types.StatusInProgress),
				issueWithStatus("nu-d", types.StatusBlocked),
			})
			return nil
		})
		ready, blocked := unblockedSections(t, out)
		if want := []string{"nu-a", "nu-c"}; !sameIDOrder(ready, want) {
			t.Errorf("ready bucket = %v, want %v\nraw:\n%s", ready, want, out)
		}
		if want := []string{"nu-b", "nu-d"}; !sameIDOrder(blocked, want) {
			t.Errorf("still-blocked bucket = %v, want %v\nraw:\n%s", blocked, want, out)
		}
	})

	t.Run("an empty list prints nothing at all", func(t *testing.T) {
		out := captureStdout(t, func() error {
			printNewlyUnblocked(nil)
			return nil
		})
		if strings.TrimSpace(out) != "" {
			t.Errorf("want no output for an empty list, got:\n%s", out)
		}
	})
}

// sameIDOrder compares the parsed bucket against the expected one, order
// included — the bullets are printed in input order and a reordering would be
// a real change in what bd close shows. Deliberately not named equalIDs: an
// identical helper already exists in this package behind //go:build cgo, and
// this file must also compile in the CGO_ENABLED=0 pure-Go build that CI
// checks, so the two cannot share a name.
func sameIDOrder(got, want []string) bool {
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
