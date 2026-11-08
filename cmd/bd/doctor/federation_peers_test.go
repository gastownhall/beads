package doctor

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestRemotesAPIServerDownCheck pins the Federation remotesapi result when no
// server is running. origin is the default sync remote, not a peer: the check
// counted every remote, so an origin-only workspace was told to start a
// server for "1 peers" while Peer Connectivity reported none (GH#6150).
func TestRemotesAPIServerDownCheck(t *testing.T) {
	origin := storage.RemoteInfo{Name: "origin", URL: "git+https://github.com/org/repo.git"}
	peer1 := storage.RemoteInfo{Name: "peer1", URL: "http://127.0.0.1:50051/beads"}
	peer2 := storage.RemoteInfo{Name: "peer2", URL: "file:///srv/beads-peer2"}
	for _, tc := range []struct {
		name        string
		remotes     []storage.RemoteInfo
		err         error
		wantStatus  string
		wantMessage string
	}{
		{"no remotes", nil, nil, StatusOK, "N/A (no peers configured)"},
		{"origin only", []storage.RemoteInfo{origin}, nil, StatusOK, "N/A (no peers configured)"},
		{"origin and one peer", []storage.RemoteInfo{origin, peer1}, nil, StatusWarning, "Server not running (1 peers configured)"},
		{"origin and two peers", []storage.RemoteInfo{origin, peer1, peer2}, nil, StatusWarning, "Server not running (2 peers configured)"},
		{"two peers, no origin", []storage.RemoteInfo{peer1, peer2}, nil, StatusWarning, "Server not running (2 peers configured)"},
		{"listing fails", nil, errors.New("table not found: dolt_remotes"), StatusOK, "Skipped (SQL remotes unavailable)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := remotesAPIServerDownCheck(tc.remotes, tc.err)
			if check.Name != "Federation remotesapi" || check.Status != tc.wantStatus || check.Message != tc.wantMessage {
				t.Fatalf("got %q %s %q, want %q %s %q", check.Name, check.Status, check.Message,
					"Federation remotesapi", tc.wantStatus, tc.wantMessage)
			}
			if tc.err != nil && check.Detail != tc.err.Error() {
				t.Errorf("detail = %q, want the listing error %q", check.Detail, tc.err.Error())
			}
		})
	}
}
