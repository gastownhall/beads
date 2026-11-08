//go:build cgo

package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/pushstate"
	"github.com/steveyegge/beads/internal/storage"
)

// syncStatus is a shorthand constructor for the cached-ref comparison the
// evaluator consumes.
func syncStatus(ahead, behind int) *storage.SyncStatus {
	return &storage.SyncStatus{Peer: "origin", LocalAhead: ahead, LocalBehind: behind}
}

// TestSyncStalenessResult pins the Sync Staleness aggregation (GH#6289):
// a recorded failed push warns even beside equal cached refs, cached-ref
// divergence and unavailable comparisons warn, peer findings keep their
// established wording beside any origin finding, and a workspace that opted
// out of pushing gets its skipped origin named rather than judged.
func TestSyncStalenessResult(t *testing.T) {
	tests := []struct {
		name    string
		in      syncStalenessInputs
		status  string
		message string
		detail  []string // substrings Detail must contain
		fix     string
	}{
		{
			name:    "no remotes",
			in:      syncStalenessInputs{},
			status:  StatusOK,
			message: "No peers configured",
		},
		{
			name: "recorded failure warns beside equal cached refs",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 0),
				PushState: &pushstate.State{
					LastPush:    "2026-01-01T00:00:00Z",
					LastCommit:  "abc",
					LastAttempt: "2026-01-01T00:00:00Z", // equal to last_push: second precision permits it
					LastError:   "remote unreachable",
				},
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"last push attempt at 2026-01-01T00:00:00Z failed: remote unreachable", "bd dolt push"},
			fix:     "bd dolt push",
		},
		{
			name: "recorded failure with missing attempt time still warns",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 0),
				PushState:        &pushstate.State{LastError: "remote unreachable"},
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"time unknown", "remote unreachable"},
		},
		{
			name: "cleared error with equal cached refs is clean",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 0),
				PushState:        &pushstate.State{LastPush: "2026-01-01T00:00:00Z", LastCommit: "abc"},
			},
			status:  StatusOK,
			message: "No local sync staleness detected",
			detail:  []string{"do not verify the remote's current contents"},
		},
		{
			name: "missing push state is neutral",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 0),
			},
			status:  StatusOK,
			message: "No local sync staleness detected",
		},
		{
			name: "unreadable push state warns",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 0),
				PushStateErr:     errors.New("parse push-state.json: not json"),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"local push history could not be read", "not json"},
		},
		{
			name: "ahead only warns with push guidance",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(3, 0),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"3 commit(s) ahead", "bd dolt push"},
		},
		{
			name: "behind only warns with pull guidance",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(0, 2),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"2 commit(s) behind", "bd dolt pull"},
		},
		{
			name: "diverged warns with pull then push guidance",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(1, 2),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"diverged", "bd dolt pull", "bd dolt push"},
		},
		{
			name: "unknown counts warn that comparison is unavailable",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(-1, -1),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"comparison with cached remote refs unavailable", "cached origin tracking ref"},
		},
		{
			name: "status error warns that comparison is unavailable",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatusErr:  errors.New("engine closed"),
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"comparison with cached remote refs unavailable: engine closed"},
		},
		{
			name: "recorded failure and an ahead cached ref both report",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(1, 0),
				PushState: &pushstate.State{
					LastPush:    "2026-01-01T00:00:00Z",
					LastAttempt: "2026-01-02T00:00:00Z",
					LastError:   "timed out after 30s",
				},
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"failed: timed out after 30s", "1 commit(s) ahead of the cached origin tracking ref"},
			fix:     "bd dolt push",
		},
		{
			name: "peer-only findings keep the established wording",
			in: syncStalenessInputs{
				HasRemotes:      true,
				PeerWarnings:    []string{"mini: 4 commits behind"},
				TotalPeerBehind: 4,
			},
			status:  StatusWarning,
			message: "4 total commits behind peers",
			detail:  []string{"mini: 4 commits behind"},
			fix:     "Run 'bd federation sync' to synchronize with peers",
		},
		{
			name: "origin and peer findings aggregate",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginStatus:     syncStatus(2, 0),
				PeerWarnings:     []string{"mini: 4 commits behind"},
				TotalPeerBehind:  4,
			},
			status:  StatusWarning,
			message: "Origin and peer sync staleness detected",
			detail:  []string{"2 commit(s) ahead", "mini: 4 commits behind", "can be stale"},
			fix:     "bd federation sync",
		},
		{
			name: "disabled origin is named and peer warnings survive",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginSkipReason: "no-push",
				PeerWarnings:     []string{"mini: 4 commits behind"},
				TotalPeerBehind:  4,
			},
			status:  StatusWarning,
			message: "4 total commits behind peers",
			detail:  []string{"origin check skipped (no-push: true)", "mini: 4 commits behind"},
			fix:     "Run 'bd federation sync' to synchronize with peers",
		},
		{
			name: "disabled origin without peer findings reports the skip",
			in: syncStalenessInputs{
				HasRemotes:       true,
				OriginConfigured: true,
				OriginSkipReason: "dolt.local-only",
			},
			status:  StatusOK,
			message: "Origin check skipped (dolt.local-only: true); no peer staleness detected",
			detail:  []string{"left unread"},
		},
		{
			name: "peers only, no findings, keep the legacy all-clear",
			in: syncStalenessInputs{
				HasRemotes: true,
			},
			status:  StatusOK,
			message: "Sync is up to date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateSyncStaleness(tt.in)
			if got.Status != tt.status {
				t.Errorf("Status = %s, want %s (%s)", got.Status, tt.status, got.Detail)
			}
			if got.Message != tt.message {
				t.Errorf("Message = %q, want %q", got.Message, tt.message)
			}
			for _, want := range tt.detail {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("Detail %q does not contain %q", got.Detail, want)
				}
			}
			if tt.fix != "" && !strings.Contains(got.Fix, tt.fix) {
				t.Errorf("Fix %q does not contain %q", got.Fix, tt.fix)
			}
			if got.Name != "Sync Staleness" || got.Category != CategoryFederation {
				t.Errorf("Name/Category = %q/%q, want Sync Staleness/%s", got.Name, got.Category, CategoryFederation)
			}
		})
	}
}

// TestOriginCheckSkipReason pins which workspace settings turn the origin leg
// of Sync Staleness off (GH#6289): the inspected workspace's
// .beads/config.yaml, not the config of the directory bd doctor runs from.
func TestOriginCheckSkipReason(t *testing.T) {
	tests := []struct {
		name   string
		config string // "" writes no config.yaml
		want   string
	}{
		{name: "no config", want: ""},
		{name: "no-push", config: "no-push: true\n", want: "no-push"},
		{name: "dolt.local-only nested", config: "dolt:\n  local-only: true\n", want: "dolt.local-only"},
		{name: "dolt.local-only flat key", config: "dolt.local-only: \"true\"\n", want: "dolt.local-only"},
		{name: "value padded with whitespace", config: "no-push: \" true \"\n", want: "no-push"},
		{name: "both, no-push named first", config: "no-push: true\ndolt:\n  local-only: true\n", want: "no-push"},
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
			if got := originCheckSkipReason(beadsDir); got != tt.want {
				t.Errorf("originCheckSkipReason = %q, want %q", got, tt.want)
			}
		})
	}
}
