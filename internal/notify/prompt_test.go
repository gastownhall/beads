package notify

import (
	"strings"
	"testing"
)

// bannedSteering is the copy that made agents over-apply notifications:
// command bodies that ordered work, stamping, captain isolation, and idling
// (captain order 2026-11-01: reminders must never steer).
var bannedSteering = []string{
	"Go idle",
	"Do the work yourself",
	"Stamp progress",
	"stamp progress",
	"Do not ask the captain",
	"Do not only reply in chat",
	"Read it, act,",
}

// TestDefaultPrompt_RemindsWithoutSteering is the core contract: every kind
// names what happened, points at the thread, disclaims priority changes, and
// carries none of the old command copy.
func TestDefaultPrompt_RemindsWithoutSteering(t *testing.T) {
	for _, kind := range []Kind{KindDue, KindComment, KindStaleClaim, KindProgress, KindEscalate, KindDefer, KindManual} {
		rec := Record{Kind: kind, Seat: "wiseman", IssueID: "fm-abc", Title: "Do thing"}
		p := DefaultPrompt(rec)
		for _, want := range []string{
			"BEADS NOTIFY (" + string(kind) + ")",
			"fm-abc",
			"bd comments fm-abc",
			"does not\nchange your priorities",
			"never a reason to\nstop or go idle",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", kind, want)
			}
		}
		for _, ban := range bannedSteering {
			if strings.Contains(p, ban) {
				t.Errorf("%s prompt carries banned steering copy %q", kind, ban)
			}
		}
	}
}

func TestDefaultPrompt_KindLines(t *testing.T) {
	cases := map[Kind]string{
		KindComment:    "New comment on a bead your seat owns.",
		KindStaleClaim: "still open under your seat",
		KindProgress:   "wants a delta comment",
		KindEscalate:   "escalated after repeated misses",
		KindDue:        "due date arrived",
	}
	for kind, want := range cases {
		if p := DefaultPrompt(Record{Kind: kind, IssueID: "x"}); !strings.Contains(p, want) {
			t.Errorf("%s line missing %q in:\n%s", kind, want, p)
		}
	}
}
