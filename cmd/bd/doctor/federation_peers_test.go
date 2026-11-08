package doctor

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestRemotesAPIServerDownCheck pins the remotesapi check's server-down
// decision (GH#6150), exposed as the pure helper production uses so tests can
// drive it without a live server: origin is the default sync remote, not a
// federation peer, so an origin-only topology is N/A, and only the remotes
// other than origin are counted — never the total remote count.
func TestRemotesAPIServerDownCheck(t *testing.T) {
	origin := storage.RemoteInfo{Name: "origin", URL: "git+https://github.com/org/repo.git"}
	peer1 := storage.RemoteInfo{Name: "peer1", URL: "https://doltremoteapi.dolthub.com/org/peer1"}
	peer2 := storage.RemoteInfo{Name: "peer2", URL: "https://doltremoteapi.dolthub.com/org/peer2"}

	for _, tc := range []struct {
		name     string
		remotes  []storage.RemoteInfo
		wantWarn bool
		wantMsg  string
	}{
		{"no remotes", nil, false, "N/A (no peers configured)"},
		{"origin only", []storage.RemoteInfo{origin}, false, "N/A (no peers configured)"},
		{"origin plus one peer", []storage.RemoteInfo{origin, peer1}, true, "Server not running (1 peers configured)"},
		{"origin plus two peers", []storage.RemoteInfo{origin, peer1, peer2}, true, "Server not running (2 peers configured)"},
		{"one peer without origin", []storage.RemoteInfo{peer1}, true, "Server not running (1 peers configured)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, warn := remotesAPIServerDownCheck(tc.remotes)
			if warn != tc.wantWarn || msg != tc.wantMsg {
				t.Errorf("remotesAPIServerDownCheck = (%q, %v), want (%q, %v)",
					msg, warn, tc.wantMsg, tc.wantWarn)
			}
		})
	}
}
