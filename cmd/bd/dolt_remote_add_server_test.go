//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDoltRemoteAddReachesAServerThatDoesNotServeTheLeftoverDatabase drives
// `bd dolt remote add` (ensureDoltRemote) and the push/pull no-remote
// decision (hasConfiguredRemote) through a real server-mode store whose
// .beads/dolt/<db> holds a Dolt database that records origin while the
// server lists no remote, the state bd init --server --remote leaves a
// gateway workspace in. When BEADS_DOLT_SERVER_PORT names the store's port,
// which may be any server's, that database is not the server's: its origin
// is not configured on the server, yet it is the remote the operator set up,
// so push and pull refuse with the command that adds it there instead of
// skipping as for a workspace without one, and the add reaches the server,
// after which the remote is configured. Without the variable the server may
// be bd's own, whose recorded remote is one its listing has not caught up with
// yet (GH#2118): the remote counts as configured and the add writes nothing.
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

			if hasNoRemoteConfigured(ctx, st) {
				t.Error("hasNoRemoteConfigured with the leftover database's origin = true: push and pull would skip as for a workspace without a remote")
			}
			configured, err := hasConfiguredRemote(ctx, st)
			var leftover *remoteOnlyInLeftoverDatabaseError
			if envNamesPort {
				if configured || !errors.As(err, &leftover) || !strings.Contains(err.Error(), cliDir) || !strings.Contains(err.Error(), "origin ("+url+")") {
					t.Errorf("hasConfiguredRemote = (%v, %v), want (false, a refusal naming %s and its origin)", configured, err, cliDir)
				}
			} else if !configured || err != nil {
				t.Errorf("hasConfiguredRemote = (%v, %v), want (true, nil): the recorded remote is the server's", configured, err)
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
			if configured, err := hasConfiguredRemote(ctx, st); !configured || err != nil {
				t.Errorf("after remote add, hasConfiguredRemote = (%v, %v), want (true, nil)", configured, err)
			}
		})
	}
}
