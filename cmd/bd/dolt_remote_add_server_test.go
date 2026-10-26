//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestDoltRemoteAddReachesAServerThatDoesNotServeTheLeftoverDatabase drives
// `bd dolt remote add` (ensureDoltRemote) and the push/pull no-remote gate
// (hasNoRemoteConfigured) through a real server-mode store whose
// .beads/dolt/<db> holds a Dolt database that records origin while the
// server lists no remote, the state bd init --server --remote leaves a
// gateway workspace in. When BEADS_DOLT_SERVER_PORT names the store's port,
// which may be any server's, that database is not the server's: no remote is
// configured, so push and pull skip as for a workspace without it, and the
// add reaches the server. Without the variable the server may be bd's own,
// whose recorded remote is one its listing has not caught up with yet
// (GH#2118): the remote counts as configured and the add writes nothing.
func TestDoltRemoteAddReachesAServerThatDoesNotServeTheLeftoverDatabase(t *testing.T) {
	const url = "git+ssh://git@example.com/org/repo.git"
	for _, envNamesPort := range []bool{true, false} {
		t.Run("port variable set="+strconv.FormatBool(envNamesPort), func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
			st := newTestStoreIsolatedDB(t, filepath.Join(t.TempDir(), ".beads", "dolt"), "ra")
			port := ""
			if envNamesPort {
				port = strconv.Itoa(testDoltServerPort)
			}
			t.Setenv("BEADS_DOLT_SERVER_PORT", port)
			t.Setenv("BEADS_DOLT_PORT", port)
			ctx := context.Background()

			cliDir := st.CLIDir()
			if err := os.MkdirAll(filepath.Join(cliDir, ".dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			state := `{"remotes":{"origin":{"name":"origin","url":"` + url + `"}}}`
			if err := os.WriteFile(filepath.Join(cliDir, ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
				t.Fatal(err)
			}

			if got := hasNoRemoteConfigured(ctx, st); got != envNamesPort {
				t.Errorf("hasNoRemoteConfigured with the leftover database's origin = %v, want %v", got, envNamesPort)
			}
			if _, err := ensureDoltRemote(ctx, st, "origin", url, func(surface, name, existingURL, newURL string) bool {
				t.Errorf("remote add asked to overwrite %s on %s with the same URL", existingURL, surface)
				return false
			}); err != nil {
				t.Fatalf("ensureDoltRemote: %v", err)
			}
			remotes, err := st.ListRemotes(ctx)
			if err != nil {
				t.Fatalf("ListRemotes: %v", err)
			}
			if got := findDoltRemoteURL(remotes, "origin") != ""; got != envNamesPort {
				t.Errorf("after remote add, the server lists origin = %v, want %v (server remotes %+v)", got, envNamesPort, remotes)
			}
		})
	}
}
