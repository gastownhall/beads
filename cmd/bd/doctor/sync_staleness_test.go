package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/pushstate"
)

// TestSyncStalenessResult pins how Sync Staleness aggregates its origin and
// peer legs (GH#6289). Every input is local state; none of it proves what a
// remote holds now.
func TestSyncStalenessResult(t *testing.T) {
	const peerFix = "Run 'bd federation sync' to synchronize with peers"
	failed := &pushstate.State{
		LastPush:    "2026-11-08T10:00:00Z",
		LastCommit:  "abc",
		LastAttempt: "2026-11-08T10:00:00Z",
		LastError:   "timed out after 30s",
	}
	behindPeer := peerStaleness{lines: []string{"peer1: 2 commits behind"}, totalBehind: 2}

	tests := []struct {
		name   string
		origin originSyncFacts
		peers  peerStaleness
		status string
		// message is matched exactly; details and fixes are substrings of
		// Detail and Fix, or, when exact, their whole joined values.
		message string
		details []string
		fixes   []string
		exact   bool
	}{
		{
			name:    "AC-6: recorded failure warns even with equal timestamps",
			origin:  originSyncFacts{checked: true, state: failed},
			status:  StatusWarning,
			message: "Last push to origin failed",
			details: []string{"origin: the last push attempt (2026-11-08T10:00:00Z) failed: timed out after 30s"},
			fixes:   []string{"Retry with 'bd dolt push' or 'bd sync'"},
		},
		{
			name:    "AC-6: recorded failure without an attempt time",
			origin:  originSyncFacts{checked: true, state: &pushstate.State{LastError: "rejected"}},
			status:  StatusWarning,
			message: "Last push to origin failed",
			details: []string{"origin: the last push attempt (an unrecorded time) failed: rejected"},
		},
		{
			name:    "AC-6: unreadable push history warns",
			origin:  originSyncFacts{checked: true, stateErr: errors.New("invalid character 'n'")},
			status:  StatusWarning,
			message: "Push history unreadable",
			details: []string{"origin: cannot read the local push history in .beads/push-state.json: invalid character 'n'"},
			fixes:   []string{"Repair or delete .beads/push-state.json"},
		},
		{
			name:    "AC-6: recorded failure and a peer behind both report",
			origin:  originSyncFacts{checked: true, state: failed},
			peers:   behindPeer,
			status:  StatusWarning,
			message: "Last push to origin failed; 2 total commits behind peers",
			details: []string{"timed out after 30s", "peer1: 2 commits behind"},
			fixes:   []string{"Retry with 'bd dolt push'", peerFix},
		},
		{
			name:    "AC-6: peer-only finding keeps its message, detail and fix",
			peers:   behindPeer,
			status:  StatusWarning,
			message: "2 total commits behind peers",
			details: []string{"peer1: 2 commits behind"},
			fixes:   []string{peerFix},
			exact:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := syncStalenessResult(tt.origin, tt.peers)
			if got.Name != "Sync Staleness" || got.Category != CategoryFederation {
				t.Errorf("Name/Category = %q/%q, want Sync Staleness/%q", got.Name, got.Category, CategoryFederation)
			}
			if got.Status != tt.status || got.Message != tt.message {
				t.Fatalf("got %s %q, want %s %q (detail %q, fix %q)", got.Status, got.Message, tt.status, tt.message, got.Detail, got.Fix)
			}
			for _, want := range tt.details {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("Detail = %q, want it to contain %q", got.Detail, want)
				}
			}
			for _, want := range tt.fixes {
				if !strings.Contains(got.Fix, want) {
					t.Errorf("Fix = %q, want it to contain %q", got.Fix, want)
				}
			}
			if tt.exact {
				if want := strings.Join(tt.details, "\n"); got.Detail != want {
					t.Errorf("Detail = %q, want exactly %q", got.Detail, want)
				}
				if want := strings.Join(tt.fixes, "; "); got.Fix != want {
					t.Errorf("Fix = %q, want exactly %q", got.Fix, want)
				}
			}
		})
	}
}
