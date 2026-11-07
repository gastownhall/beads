package main

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// Tests for openForeignProxiedExternalRead, the foreign read of a
// proxied-server workspace whose sidecar names an external Dolt server
// (gastownhall/beads#5861). They need no Dolt server: fakeMySQLLogin
// (dolt_handshake_fake_test.go) records the user and password each login
// presents and refuses it, so every read here fails after the dial. The
// shared-server integration lane covers reads that succeed
// (routed_external_proxied_integration_test.go).

// closedTCPPort returns a loopback port nothing listens on.
func closedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// externalReadTown lays out a town whose routes.jsonl sends gt- to the rig
// at rig/, a proxied-server workspace with dolt_database "gt" and, when
// external is non-nil, a sidecar naming that external server. bd runs in the
// town (dbPath). It returns the rig's .beads directory.
func externalReadTown(t *testing.T, external *configfile.ExternalDoltConfig) string {
	t.Helper()
	isolateBeadsDirForTest(t)
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	for _, env := range []string{
		configfile.ExternalDoltPasswordEnvVar, "BEADS_DOLT_PASSWORD",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_DATABASE",
		"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER",
	} {
		t.Setenv(env, "")
	}
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	rigCfg := configfile.DefaultConfig()
	rigCfg.DoltMode = configfile.DoltModeProxiedServer
	rigCfg.DoltDatabase = "gt"
	if err := rigCfg.Save(rigBeadsDir); err != nil {
		t.Fatalf("save rig metadata.json: %v", err)
	}
	if external != nil {
		if err := configfile.SaveProxiedServerClientInfo(rigBeadsDir, &configfile.ProxiedServerClientInfo{External: external}); err != nil {
			t.Fatalf("save rig sidecar: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	oldDBPath := dbPath
	dbPath = filepath.Join(townBeadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })
	return rigBeadsDir
}

func isProxiedStoreUnrouted(err error) bool {
	var capErr *ProxyCapabilityError
	return errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted"
}

// snapshotForeignTree maps every path under root to its mode and contents.
func snapshotForeignTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		files[path] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return files
}

func assertForeignTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotForeignTree(t, root)
	for path, entry := range after {
		if was, ok := before[path]; !ok {
			t.Errorf("foreign read created %s", path)
		} else if was != entry {
			t.Errorf("foreign read changed %s", path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			t.Errorf("foreign read removed %s", path)
		}
	}
}

// TestForeignProxiedExternalReadUsesTargetEndpoint is AC-1's unit half: a
// routed read into a proxied-server rig whose sidecar names an external
// server dials that server as the sidecar's user, and nothing of the
// launcher's connection settings (port, socket, user, database, password)
// takes their place. The integration lane proves the database.
func TestForeignProxiedExternalReadUsesTargetEndpoint(t *testing.T) {
	srv := newFakeMySQLLogin(t)
	externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port(), User: "rig_user"})
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", filepath.Join(t.TempDir(), "launcher.sock"))
	t.Setenv("BEADS_DOLT_SERVER_USER", "launcher_user")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "launcher_db")
	t.Setenv("BEADS_DOLT_PASSWORD", "launcher-secret")

	_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
	if err == nil {
		t.Fatal("routed read against a server that refuses every login succeeded")
	}
	if isProxiedStoreUnrouted(err) {
		t.Fatalf("routed read refused the external proxied-server rig instead of reading it: %v", err)
	}
	logins := srv.Logins()
	if len(logins) == 0 {
		t.Fatalf("routed read never logged in to the rig's external server; error: %v", err)
	}
	for _, login := range logins {
		if login.User != "rig_user" {
			t.Errorf("logged in as %q, want the sidecar's user rig_user", login.User)
		}
		if !srv.PresentsPassword(login, "") {
			t.Error("presented a password: the launcher's BEADS_DOLT_PASSWORD reached the rig's server")
		}
	}
}

// TestForeignProxiedExternalReadGateStaysUnconfirmed is AC-5's unit half: a
// route into an external proxied-server rig that cannot be read leaves a bead
// gate's target unconfirmed with the real cause, never the generic refusal,
// and the read tries no other endpoint.
func TestForeignProxiedExternalReadGateStaysUnconfirmed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) (want string)
	}{
		{
			name: "unreachable_server",
			setup: func(t *testing.T) string {
				externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: closedTCPPort(t)})
				return "unreachable"
			},
		},
		{
			name: "server_drops_the_session",
			setup: func(t *testing.T) string {
				srv := newFakeMySQLLogin(t)
				externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port()})
				t.Cleanup(func() {
					if srv.Conns() == 0 {
						t.Error("the read never dialed the rig's external server")
					}
				})
				return ""
			},
		},
		{
			name: "malformed_sidecar",
			setup: func(t *testing.T) string {
				rig := externalReadTown(t, nil)
				if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(rig), []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
				return configfile.ProxiedServerClientInfoFileName
			},
		},
		{
			name: "invalid_external_endpoint",
			setup: func(t *testing.T) string {
				externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1"})
				return "Host requires Port"
			},
		},
		{
			name: "no_dolt_database",
			setup: func(t *testing.T) string {
				rig := externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: closedTCPPort(t)})
				cfg, err := configfile.Load(rig)
				if err != nil {
					t.Fatal(err)
				}
				cfg.DoltDatabase = ""
				if err := cfg.Save(rig); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", "launcher_db")
				return "no dolt_database"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.setup(t)
			issue, err := prefixRoutedBeadGateTarget(context.Background(), "gt-target1")
			if issue != nil {
				t.Fatalf("got bead %s from a rig that cannot be read", issue.ID)
			}
			if !errors.Is(err, errBeadGateTargetUnconfirmed) {
				t.Fatalf("err = %v, want errBeadGateTargetUnconfirmed", err)
			}
			if isProxiedStoreUnrouted(err) {
				t.Fatalf("gate target refused as unrouted instead of read: %v", err)
			}
			if want != "" && !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		})
	}
}

// TestForeignProxiedExternalReadCredentialsAndTLS is AC-6's unit test: the
// external read presents BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD and only it,
// sends no credential to a host the driver would read as another server, and
// honors the sidecar's TLS settings rather than falling back to plaintext.
func TestForeignProxiedExternalReadCredentialsAndTLS(t *testing.T) {
	t.Run("external_password_is_presented", func(t *testing.T) {
		srv := newFakeMySQLLogin(t)
		externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port(), User: "rig_user"})
		t.Setenv(configfile.ExternalDoltPasswordEnvVar, "rig-secret")

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if isProxiedStoreUnrouted(err) {
			t.Fatalf("routed read refused: %v", err)
		}
		logins := srv.Logins()
		if len(logins) == 0 {
			t.Fatalf("no login reached the rig's server; error: %v", err)
		}
		for _, login := range logins {
			if !srv.PresentsPassword(login, "rig-secret") {
				t.Errorf("login as %q presented no password; want %s's", login.User, configfile.ExternalDoltPasswordEnvVar)
			}
		}
	})

	t.Run("launcher_password_is_not_inherited", func(t *testing.T) {
		srv := newFakeMySQLLogin(t)
		externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port(), User: "rig_user"})
		t.Setenv("BEADS_DOLT_PASSWORD", "launcher-secret")

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if isProxiedStoreUnrouted(err) {
			t.Fatalf("routed read refused: %v", err)
		}
		logins := srv.Logins()
		if len(logins) == 0 {
			t.Fatalf("no login reached the rig's server; error: %v", err)
		}
		for _, login := range logins {
			if !srv.PresentsPassword(login, "") {
				t.Error("the launcher's BEADS_DOLT_PASSWORD was presented to the rig's server")
			}
		}
	})

	t.Run("untrusted_destination_gets_no_credential", func(t *testing.T) {
		externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1#@tcp(evil.example", Port: closedTCPPort(t)})
		t.Setenv(configfile.ExternalDoltPasswordEnvVar, "rig-secret")

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if err == nil || !strings.Contains(err.Error(), "credential was not sent") {
			t.Fatalf("err = %v, want the endpoint trust refusal", err)
		}
	})

	t.Run("tls_required_is_not_downgraded", func(t *testing.T) {
		srv := newFakeMySQLLogin(t)
		externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port(), TLSRequired: true, TLSSkipVerify: true})
		t.Setenv(configfile.ExternalDoltPasswordEnvVar, "rig-secret")

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if err == nil || !strings.Contains(err.Error(), "TLS requested but server does not support TLS") {
			t.Fatalf("err = %v, want the driver's refusal of a server without TLS", err)
		}
		if logins := srv.Logins(); len(logins) != 0 {
			t.Errorf("a TLS-required read logged in over plaintext: %+v", logins)
		}
	})

	t.Run("tls_ca_is_read", func(t *testing.T) {
		missingCA := filepath.Join(t.TempDir(), "missing-ca.pem")
		externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: closedTCPPort(t), TLSRequired: true, TLSCACert: missingCA})

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if err == nil || !strings.Contains(err.Error(), "read TLSCACert") {
			t.Fatalf("err = %v, want the sidecar's TLSCACert read failure", err)
		}
	})
}

// TestForeignProxiedExternalReadWritesNothingInTarget is AC-4's unit half: a
// foreign read that reaches the rig's external server, and one that cannot,
// leave every file in the rig's workspace as it was (no dolt directory,
// runtime file, sidecar or metadata rewrite).
func TestForeignProxiedExternalReadWritesNothingInTarget(t *testing.T) {
	t.Run("server_reached", func(t *testing.T) {
		srv := newFakeMySQLLogin(t)
		rig := externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: srv.port()})
		rigRoot := filepath.Dir(rig)
		before := snapshotForeignTree(t, rigRoot)

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if srv.Conns() == 0 {
			t.Fatalf("the read never dialed the rig's external server; error: %v", err)
		}
		assertForeignTreeUnchanged(t, rigRoot, before)
	})

	t.Run("server_unreachable", func(t *testing.T) {
		rig := externalReadTown(t, &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: closedTCPPort(t)})
		rigRoot := filepath.Dir(rig)
		before := snapshotForeignTree(t, rigRoot)

		_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
		if err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("err = %v, want the rig's server reported unreachable", err)
		}
		assertForeignTreeUnchanged(t, rigRoot, before)
	})
}
