package doctor

import (
	"errors"
	"os"
	"path/filepath"
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
		{
			name:    "AC-7: equal cached ref and no recorded error is a local-only pass",
			origin:  originSyncFacts{checked: true},
			status:  StatusOK,
			message: "No local sync staleness detected",
			details: []string{"cached origin ref", "does not prove what origin holds now"},
		},
		{
			name:    "AC-7: local commits not in the cached origin ref",
			origin:  originSyncFacts{checked: true, ahead: 2},
			status:  StatusWarning,
			message: "Local history ahead of cached origin refs",
			details: []string{"origin: 2 local commits are not in the cached origin ref"},
			fixes:   []string{"bd dolt push"},
		},
		{
			name:    "AC-7: cached origin ref ahead of local history",
			origin:  originSyncFacts{checked: true, behind: 3},
			status:  StatusWarning,
			message: "Local history behind cached origin refs",
			details: []string{"origin: the cached origin ref has 3 commits not in local history"},
			fixes:   []string{"bd dolt pull"},
		},
		{
			name:    "AC-7: diverged from the cached origin ref",
			origin:  originSyncFacts{checked: true, ahead: 1, behind: 1},
			status:  StatusWarning,
			message: "Local history diverged from cached origin refs",
			details: []string{"origin: 1 local commits are not in the cached origin ref, which has 1 commits not in local history"},
			fixes:   []string{"Run 'bd dolt pull', then 'bd dolt push'"},
		},
		{
			name:    "AC-7: no cached origin ref to compare",
			origin:  originSyncFacts{checked: true, ahead: -1, behind: -1},
			status:  StatusWarning,
			message: "Comparison with cached origin refs unavailable",
			details: []string{"origin: no cached origin ref to compare local history with"},
			fixes:   []string{"bd dolt pull"},
		},
		{
			name:    "AC-7: comparison error",
			origin:  originSyncFacts{checked: true, statusErr: errors.New("connection reset")},
			status:  StatusWarning,
			message: "Comparison with cached origin refs unavailable",
			details: []string{"origin: cannot compare local history with the cached origin ref: connection reset"},
		},
		{
			name:    "AC-7: recorded failure leads, divergence still reported",
			origin:  originSyncFacts{checked: true, state: failed, ahead: 1},
			status:  StatusWarning,
			message: "Last push to origin failed",
			details: []string{"timed out after 30s", "origin: 1 local commits are not in the cached origin ref"},
		},
		{
			name:    "AC-8: disabled origin with no peer finding names the skipped check",
			origin:  originSyncFacts{skipped: "dolt.local-only=true"},
			status:  StatusOK,
			message: "Origin check skipped (dolt.local-only=true)",
			details: []string{"Push to origin is disabled (dolt.local-only=true in .beads/config.yaml)", "peers are still checked"},
		},
		{
			name:    "AC-8: disabled origin keeps the peer warning, detail and fix",
			origin:  originSyncFacts{skipped: "no-push=true"},
			peers:   behindPeer,
			status:  StatusWarning,
			message: "2 total commits behind peers; origin check skipped (no-push=true)",
			details: []string{
				"peer1: 2 commits behind",
				"origin: check skipped: push to origin is disabled (no-push=true in .beads/config.yaml)",
			},
			fixes: []string{peerFix},
			exact: true,
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

// TestOriginSkipReason pins which workspace settings turn the origin leg of
// Sync Staleness off (GH#6289): the inspected workspace's .beads/config.yaml,
// not the config of the directory bd doctor runs from.
func TestOriginSkipReason(t *testing.T) {
	tests := []struct {
		name   string
		config string // "" writes no config.yaml
		want   string
	}{
		{name: "no config", want: ""},
		{name: "no-push", config: "no-push: true\n", want: "no-push=true"},
		{name: "dolt.local-only nested", config: "dolt:\n  local-only: true\n", want: "dolt.local-only=true"},
		{name: "dolt.local-only flat key", config: "dolt.local-only: \"true\"\n", want: "dolt.local-only=true"},
		{name: "both, no-push named first", config: "no-push: true\ndolt:\n  local-only: true\n", want: "no-push=true"},
		{name: "explicitly false", config: "no-push: false\ndolt:\n  local-only: false\n", want: ""},
		{name: "unparseable value", config: "no-push: maybe\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tt.config != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tt.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := originSkipReason(beadsDir); got != tt.want {
				t.Errorf("originSkipReason = %q, want %q", got, tt.want)
			}
		})
	}
}
