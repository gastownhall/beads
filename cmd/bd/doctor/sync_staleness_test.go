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
			// SyncStatus reports -1 for a missing tracking ref and for a
			// failed comparison alike, so the finding names both causes.
			detail: []string{"comparison with cached remote refs unavailable", "no cached origin tracking ref yet", "or the comparison failed"},
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
			// Auto-push ignores no-push, so a workspace with both still
			// pushes to origin and its recorded failure must not hide
			// behind no-push.
			name: "no-push with auto-push still reports a recorded failure and says why",
			in: syncStalenessInputs{
				HasRemotes:                  true,
				OriginConfigured:            true,
				OriginAutoPushDespiteNoPush: true,
				OriginStatus:                syncStatus(0, 0),
				PushState:                   &pushstate.State{LastAttempt: "2026-01-02T00:00:00Z", LastError: "timed out after 30s"},
			},
			status:  StatusWarning,
			message: "Origin sync staleness detected",
			detail:  []string{"origin checked despite no-push: true, because dolt.auto-push: true still pushes to origin", "failed: timed out after 30s"},
		},
		{
			name: "no-push with auto-push and a clean origin passes and says why",
			in: syncStalenessInputs{
				HasRemotes:                  true,
				OriginConfigured:            true,
				OriginAutoPushDespiteNoPush: true,
				OriginStatus:                syncStatus(0, 0),
			},
			status:  StatusOK,
			message: "No local sync staleness detected",
			detail:  []string{"origin checked despite no-push: true", "do not verify the remote's current contents"},
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
// of Sync Staleness off (GH#6289). They are read from the inspected
// workspace's own config, not the config of the directory bd doctor runs
// from, and a config.local.yaml beside config.yaml overrides it in both
// directions, as it does for every bd command there. no-push is a skip reason
// only while dolt.auto-push is off, because auto-push ignores no-push.
func TestOriginCheckSkipReason(t *testing.T) {
	tests := []struct {
		name         string
		config       string // "" writes no config.yaml
		local        string // "" writes no config.local.yaml
		want         string
		wantAutoPush bool // the leg runs only because auto-push ignores no-push
	}{
		{name: "no config", want: ""},
		{name: "no-push", config: "no-push: true\n", want: "no-push"},
		{name: "no-push with auto-push off", config: "no-push: true\ndolt:\n  auto-push: false\n", want: "no-push"},
		{name: "no-push with auto-push on still checks origin", config: "no-push: true\ndolt:\n  auto-push: true\n", want: "", wantAutoPush: true},
		{name: "auto-push alone", config: "dolt:\n  auto-push: true\n", want: ""},
		{name: "dolt.local-only nested", config: "dolt:\n  local-only: true\n", want: "dolt.local-only"},
		{name: "dolt.local-only flat key", config: "dolt.local-only: \"true\"\n", want: "dolt.local-only"},
		{name: "dolt.local-only also stops auto-push", config: "dolt:\n  local-only: true\n  auto-push: true\n", want: "dolt.local-only"},
		{name: "both, dolt.local-only named first", config: "no-push: true\ndolt:\n  local-only: true\n", want: "dolt.local-only"},
		{name: "explicitly false", config: "no-push: false\ndolt:\n  local-only: false\n", want: ""},
		// bd reads these with viper's GetBool, which does not trim or
		// guess: a value it reads as false must not skip the leg here.
		{name: "value padded with whitespace", config: "no-push: \" true \"\n", want: ""},
		{name: "unparseable value", config: "no-push: maybe\n", want: ""},

		{name: "local no-push false overrides config true", config: "no-push: true\n", local: "no-push: false\n", want: ""},
		{name: "local no-push true overrides config false", config: "no-push: false\n", local: "no-push: true\n", want: "no-push"},
		{name: "local local-only false overrides config true", config: "dolt:\n  local-only: true\n", local: "dolt:\n  local-only: false\n", want: ""},
		{name: "local local-only true overrides config false", config: "dolt:\n  local-only: false\n", local: "dolt.local-only: true\n", want: "dolt.local-only"},
		{name: "local auto-push true overrides config false under no-push", config: "no-push: true\ndolt:\n  auto-push: false\n", local: "dolt:\n  auto-push: true\n", want: "", wantAutoPush: true},
		{name: "local auto-push false overrides config true under no-push", config: "no-push: true\ndolt:\n  auto-push: true\n", local: "dolt:\n  auto-push: false\n", want: "no-push"},
		{name: "local file without the key keeps config's value", config: "no-push: true\n", local: "dolt:\n  port: 3307\n", want: "no-push"},
		// bd merges config.local.yaml only beside a config.yaml, so a lone
		// local file sets nothing.
		{name: "lone config.local.yaml is ignored", local: "no-push: true\n", want: ""},
		// An unreadable setting counts as unset: the leg runs rather than
		// hide a recorded failure behind a skip.
		{name: "unparseable config.local.yaml runs the leg", config: "no-push: true\n", local: "no-push: [\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			for name, body := range map[string]string{"config.yaml": tt.config, "config.local.yaml": tt.local} {
				if body == "" {
					continue
				}
				if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, gotAutoPush := originCheckSkipReason(beadsDir)
			if got != tt.want || gotAutoPush != tt.wantAutoPush {
				t.Errorf("originCheckSkipReason = (%q, %v), want (%q, %v)", got, gotAutoPush, tt.want, tt.wantAutoPush)
			}
		})
	}
}
