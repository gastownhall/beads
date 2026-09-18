//go:build cgo && unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestHistoryRemoteRefusalFrontDoorMatrix runs the real bd binary through all
// supported proxied transports. Valid command arguments are intentional: a
// typed capability refusal must win over Cobra usage validation and must not
// start a provider or touch durable state.
func TestHistoryRemoteRefusalFrontDoorMatrix(t *testing.T) {
	bd := buildEmbeddedBD(t)
	type fixture struct {
		name string
		make func(*testing.T) proxiedProject
	}
	fixtures := []fixture{{
		name: "managed-local",
		make: func(t *testing.T) proxiedProject {
			requireManagedLocalProxiedEnv(t)
			return bdManagedLocalInit(t, bd, "hm_managed", 5*time.Minute)
		},
	}}
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") == "1" {
		fixtures = append(fixtures,
			fixture{name: "external-tcp", make: func(t *testing.T) proxiedProject {
				return newSharedProxiedProject(t, bd, "hm_tcp")
			}},
			fixture{name: "external-unix", make: func(t *testing.T) proxiedProject {
				requireProxiedServerEnv(t)
				upstream := testutil.StartIsolatedDoltContainerHandle(t)
				socket := filepath.Join(t.TempDir(), "dolt.sock")
				bridge := startOutageBridge(t, socket, upstream.Port, true)
				t.Cleanup(func() { _ = bridge.Process.Kill() })
				return bdProxiedInit(t, bd, "hm_unix", "--proxied-server-external-socket-path", socket)
			}},
		)
	}
	commands := []struct {
		name, code, message string
		args                []string
	}{
		{"branch", "proxy.branch.unsupported", "branch is not supported in proxied-server mode", []string{"branch", "feature"}},
		{"conflicts list", "proxy.conflicts.unsupported", "conflicts list is not supported in proxied-server mode", []string{"conflicts", "list"}},
		{"repo add", "proxy.repo.unsupported", "repo add is not supported in proxied-server mode", []string{"repo", "add", "."}},
		{"federation sync", "proxy.federation.unsupported", "federation sync is not supported in proxied-server mode", []string{"federation", "sync"}},
		{"vc merge", "proxy.vc.unsupported", "vc merge is not supported in proxied-server mode", []string{"vc", "merge", "feature"}},
		{"vc commit", "proxy.vc.unsupported", "vc commit is not supported in proxied-server mode", []string{"vc", "commit"}},
		{"flatten", "proxy.flatten.unsupported", "flatten is not supported in proxied-server mode", []string{"flatten"}},
		{"dolt push", "proxy.dolt_push.unsupported", "dolt push is not supported in proxied-server mode", []string{"dolt", "push"}},
		{"dolt pull", "proxy.dolt_pull.unsupported", "dolt pull is not supported in proxied-server mode", []string{"dolt", "pull"}},
		{"dolt commit", "proxy.dolt_commit.unsupported", "dolt commit is not supported in proxied-server mode", []string{"dolt", "commit"}},
		{"dolt remote add", "proxy.dolt_remote.unsupported", "dolt remote add is not supported in proxied-server mode", []string{"dolt", "remote", "add", "backup", "https://example.invalid/backup"}},
		{"dolt remote list", "proxy.dolt_remote.unsupported", "dolt remote list is not supported in proxied-server mode", []string{"dolt", "remote", "list"}},
		{"dolt remote reset-data", "proxy.dolt_remote.unsupported", "dolt remote reset-data is not supported in proxied-server mode", []string{"dolt", "remote", "reset-data", "backup"}},
		{"sync", "proxy.sync.unsupported", "sync is not supported in proxied-server mode", []string{"sync"}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			p := f.make(t)
			bdProxiedCreate(t, bd, p.dir, "matrix sentinel")
			_ = proxy.Shutdown(p.proxyRoot)
			before := snapshotHistoryArtifacts(t, p)
			for _, tc := range commands {
				t.Run(tc.name, func(t *testing.T) {
					// Put the global flag before the subcommand. A few nested
					// front doors do not inherit persistent flags after their args.
					args := append([]string{"--json"}, tc.args...)
					stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_JSON=1"}, args...)
					if err == nil {
						t.Fatalf("%v unexpectedly succeeded", tc.args)
					}
					var got struct {
						Code    string `json:"code"`
						Error   string `json:"error"`
						Mutates bool   `json:"mutates"`
					}
					if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); jsonErr == nil {
						if got.Code != tc.code || got.Error != tc.message || got.Mutates {
							t.Fatalf("refusal=%+v, want code=%q error=%q mutates=false", got, tc.code, tc.message)
						}
					} else if strings.TrimSpace(stderr) != "Error: "+tc.message {
						// Some nested commands render the same typed refusal through
						// the text path before config-backed JSON is applied.
						t.Fatalf("refusal JSON=%v stdout=%q stderr=%q", jsonErr, stdout, stderr)
					}
					if _, statErr := os.Stat(filepath.Join(p.proxyRoot, proxy.PIDFileName)); statErr == nil {
						t.Fatal("provider proxy started for direct-only refusal")
					}
					after := snapshotHistoryArtifacts(t, p)
					if string(before) != string(after) {
						t.Fatal("durable artifacts changed during refusal")
					}
				})
			}
		})
	}
}

func snapshotHistoryArtifacts(t *testing.T, p proxiedProject) []byte {
	t.Helper()
	var out []byte
	for _, path := range []string{
		filepath.Join(p.beadsDir, "config.yaml"), filepath.Join(p.beadsDir, "metadata.json"),
		filepath.Join(p.beadsDir, "events.jsonl"), filepath.Join(p.beadsDir, ".local_version"),
		filepath.Join(p.proxyRoot, proxy.LockFileName), filepath.Join(p.proxyRoot, proxy.PIDFileName),
	} {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("snapshot %s: %v", path, err)
		}
		out = append(out, []byte(strconv.Itoa(len(path))+":"+path+":"+string(b)+"\n")...)
	}
	return out
}

// TestHistoryRemoteSupportedFrontDoorParity checks the two operations that
// are intentionally shared by direct and proxied providers.
func TestHistoryRemoteSupportedFrontDoorParity(t *testing.T) {
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" && os.Getenv(managedLocalProxiedEnvVar) != "1" {
		t.Skip("set a proxied test lane to run history parity")
	}
	bd := buildEmbeddedBD(t)
	for _, tc := range []struct {
		name string
		make func(*testing.T) proxiedProject
	}{
		{"managed-local", func(t *testing.T) proxiedProject {
			requireManagedLocalProxiedEnv(t)
			return bdManagedLocalInit(t, bd, "hs_managed", 5*time.Minute)
		}},
		{"external-tcp", func(t *testing.T) proxiedProject {
			requireProxiedServerEnv(t)
			return newSharedProxiedProject(t, bd, "hs_tcp")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.make(t)
			issue := bdProxiedCreate(t, bd, p.dir, "history parity")
			bdProxiedUpdate(t, bd, p.dir, issue.ID, "--title", "history parity updated")
			out, err := bdProxiedRun(t, bd, p.dir, "--json", "history", issue.ID, "--events")
			if err != nil || !strings.Contains(string(out), issue.ID) {
				t.Fatalf("history --events failed: %v\n%s", err, out)
			}
			db := openProxiedDB(t, p)
			if _, err := db.ExecContext(context.Background(), "CALL DOLT_REMOTE('add', ?, ?)", "backup", "https://example.invalid/backup"); err != nil {
				t.Fatalf("seed remote: %v", err)
			}
			out, err = bdProxiedRun(t, bd, p.dir, "--json", "dolt", "remote", "remove", "backup")
			if err != nil || !strings.Contains(string(out), "backup") {
				t.Fatalf("remote remove failed: %v\n%s", err, out)
			}
		})
	}
}
