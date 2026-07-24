package beads

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestClaimSentinelsWrapPreservation proves that errors produced the way the
// engine's claim/circuit paths produce them still satisfy errors.Is against the
// ROOT package's re-exported sentinels — i.e. the alias re-export preserves the
// wrap chain across the package boundary. The wrapped forms mirror
// issueops/claim.go and storage/dolt/circuit.go exactly.
func TestClaimSentinelsWrapPreservation(t *testing.T) {
	t.Parallel()

	// Mirrors issueops.ClaimIssueInTx: fmt.Errorf("%w by %s", ErrAlreadyClaimed, assignee),
	// then an outer context wrap as a real caller would add.
	alreadyClaimed := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, "alice"))
	if !errors.Is(alreadyClaimed, ErrAlreadyClaimed) {
		t.Errorf("wrapped already-claimed error does not match root beads.ErrAlreadyClaimed")
	}

	notClaimable := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w: status %s", storage.ErrNotClaimable, "closed"))
	if !errors.Is(notClaimable, ErrNotClaimable) {
		t.Errorf("wrapped not-claimable error does not match root beads.ErrNotClaimable")
	}

	circuit := fmt.Errorf("read issue: %w", dolt.ErrCircuitOpen)
	if !errors.Is(circuit, ErrCircuitOpen) {
		t.Errorf("wrapped circuit error does not match root beads.ErrCircuitOpen")
	}

	// A conflict must not cross-match the wrong sentinel.
	if errors.Is(alreadyClaimed, ErrNotClaimable) {
		t.Errorf("already-claimed error unexpectedly matched ErrNotClaimable")
	}
}

func TestParseClaimConflict(t *testing.T) {
	t.Parallel()

	t.Run("already claimed recovers assignee", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, "alice"))
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for an ErrAlreadyClaimed error")
		}
		if got.CurrentAssignee != "alice" {
			t.Errorf("CurrentAssignee = %q, want %q", got.CurrentAssignee, "alice")
		}
		if got.CurrentStatus != "" {
			t.Errorf("CurrentStatus = %q, want empty", got.CurrentStatus)
		}
	})

	t.Run("open pre-assigned conflict classifies, assignee best-effort", func(t *testing.T) {
		t.Parallel()
		// Mirrors issueops.ClaimIssueInTx's open-but-assigned branch: the
		// sentinel is wrapped (so errors.Is classification holds) but the
		// holder-focused message does not end in the " by <assignee>" tail, so
		// the assignee comes back empty — best-effort — while ok stays true.
		err := fmt.Errorf("claim cl-fa1: %w", fmt.Errorf("%w: already assigned to %q — coordinate with the holder; if their claim is abandoned (crashed agent), lease expiry will surface it for bd reclaim", storage.ErrAlreadyClaimed, "alice"))
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for a wrapped open-assigned ErrAlreadyClaimed")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty (holder-focused message has no parseable tail)", got.CurrentAssignee)
		}
		if got.CurrentStatus != "" {
			t.Errorf("CurrentStatus = %q, want empty", got.CurrentStatus)
		}
	})

	t.Run("not claimable recovers status", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("%w: status %s", storage.ErrNotClaimable, "in_progress")
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for an ErrNotClaimable error")
		}
		if got.CurrentStatus != "in_progress" {
			t.Errorf("CurrentStatus = %q, want %q", got.CurrentStatus, "in_progress")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty", got.CurrentAssignee)
		}
	})

	t.Run("unrelated error returns false", func(t *testing.T) {
		t.Parallel()
		if got, ok := ParseClaimConflict(errors.New("boom")); ok {
			t.Errorf("ParseClaimConflict(unrelated) = (%+v, true), want ok=false", got)
		}
		if got, ok := ParseClaimConflict(nil); ok {
			t.Errorf("ParseClaimConflict(nil) = (%+v, true), want ok=false", got)
		}
	})

	t.Run("is-match with unparseable message still ok", func(t *testing.T) {
		t.Parallel()
		// Wraps the sentinel without the " by <assignee>" tail.
		err := fmt.Errorf("weird wrap: %w", storage.ErrAlreadyClaimed)
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for a wrapped ErrAlreadyClaimed")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty for unparseable message", got.CurrentAssignee)
		}
	})

	t.Run("appended wrap yields empty field but still ok", func(t *testing.T) {
		t.Parallel()
		// A wrap APPENDED after the assignee (not the repo-convention prepend) is
		// ambiguous, so the field comes back empty — best-effort — while ok stays
		// true because it is still an ErrAlreadyClaimed match.
		err := fmt.Errorf("%w (context appended)", fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, "alice"))
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for an appended-wrap ErrAlreadyClaimed")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty on an appended-wrap message", got.CurrentAssignee)
		}
	})
}

// TestTailAfterBoundsTheToken pins the token-recovery boundary directly: a clean
// token and a prepended wrap recover the bare token; an appended suffix (space or
// "(...)" wrap) is rejected to "" rather than leaking a corrupted token.
func TestTailAfterBoundsTheToken(t *testing.T) {
	t.Parallel()
	m := alreadyClaimedMarker // "issue already claimed by "
	cases := []struct {
		name, in, want string
	}{
		{"clean token", m + "alice", "alice"},
		{"prepended wrap", "claim dr-1: " + m + "alice", "alice"},
		{"appended paren wrap", m + "alice (from ctx)", ""},
		{"appended space suffix", m + "alice then boom", ""},
		{"marker absent", "unrelated error", ""},
		{"empty token", m, ""},
	}
	for _, c := range cases {
		if got := tailAfter(c.in, m); got != c.want {
			t.Errorf("%s: tailAfter(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
