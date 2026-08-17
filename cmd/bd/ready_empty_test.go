package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestPrintReadyEmptyHuman(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		hasOpen          bool
		hasStoredBlocked bool
		want             string
		reject           string
	}{
		{
			name:    "open issues still blocked",
			hasOpen: true,
			want:    "all issues have blocking dependencies",
			reject:  "No open issues",
		},
		{
			name:             "only stored blocked leftovers",
			hasStoredBlocked: true,
			want:             "stored status blocked",
			reject:           "No open issues",
		},
		{
			name: "truly empty",
			want: "No open issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() error {
				printReadyEmptyHuman(tt.hasOpen, tt.hasStoredBlocked)
				return nil
			})
			if !strings.Contains(out, tt.want) {
				t.Fatalf("output %q does not contain %q", out, tt.want)
			}
			if tt.reject != "" && strings.Contains(out, tt.reject) {
				t.Fatalf("output %q should not contain %q", out, tt.reject)
			}
			if outputContainsDisallowedEmoji(out) {
				t.Fatalf("output %q uses disallowed emoji", out)
			}
		})
	}
}

func TestPrintBlockedHumanStoredHold(t *testing.T) {
	t.Parallel()

	empty := captureStdout(t, func() error {
		printBlockedHuman(nil)
		return nil
	})
	if !strings.Contains(empty, "No blocked issues") {
		t.Fatalf("empty list output = %q", empty)
	}

	held := captureStdout(t, func() error {
		printBlockedHuman([]*types.BlockedIssue{{
			Issue:          types.Issue{ID: "bd-hold", Title: "Parked", Priority: 2},
			BlockedByCount: 0,
		}})
		return nil
	})
	if !strings.Contains(held, "Stored status blocked") {
		t.Fatalf("stored-blocked output = %q", held)
	}
	if !strings.Contains(held, "bd update bd-hold --claim") {
		t.Fatalf("stored-blocked output missing resume hint: %q", held)
	}

	deps := captureStdout(t, func() error {
		printBlockedHuman([]*types.BlockedIssue{{
			Issue:          types.Issue{ID: "bd-wait", Title: "Waiting", Priority: 1},
			BlockedByCount: 1,
			BlockedBy:      []string{"bd-blocker"},
		}})
		return nil
	})
	if !strings.Contains(deps, "Blocked by 1 open dependencies") {
		t.Fatalf("dependency-blocked output = %q", deps)
	}
}

func outputContainsDisallowedEmoji(s string) bool {
	for _, mark := range []string{"✨", "📋", "🚫", "📊", "📜"} {
		if strings.Contains(s, mark) {
			return true
		}
	}
	return false
}
