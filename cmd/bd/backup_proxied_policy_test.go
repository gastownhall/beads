package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestProxiedRestoreFailureMessage pins the one distinction the operator acts
// on: whether the data came back. A teardown failure happens after the step
// succeeded, so reporting it as "restore failed" is not a wording nit — it
// tells someone whose database IS restored to restore it again.
func TestProxiedRestoreFailureMessage(t *testing.T) {
	boom := errors.New("proxy shutdown: connection refused")

	for _, tc := range []struct {
		name           string
		afterReconcile bool
		err            error
		wantContains   []string
		wantMissing    []string
	}{
		{
			name: "restore itself failed",
			err:  errors.New("DOLT_BACKUP: no such backup"),
			// No claim that anything was restored.
			wantContains: []string{"restore failed", "no such backup"},
			wantMissing:  []string{"the data is restored"},
		},
		{
			name:         "restore succeeded, teardown failed",
			err:          &proxiedTeardownError{err: boom},
			wantContains: []string{"the data is restored", "bd dolt stop --force", "bd backup init"},
			wantMissing:  []string{"restore failed"},
		},
		{
			name:           "reconcile failed for real",
			afterReconcile: true,
			err:            errors.New("open provider: schema is behind"),
			wantContains:   []string{"the restored database did not reopen", "schema is behind"},
			wantMissing:    []string{"restore failed"},
		},
		{
			name:           "reconcile succeeded, teardown failed",
			afterReconcile: true,
			err:            &proxiedTeardownError{err: boom},
			wantContains:   []string{"the data is restored and reconciled", "bd dolt stop --force"},
			// Nothing left to re-register: the reconcile already ran.
			wantMissing: []string{"restore failed", "bd backup init"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := proxiedRestoreFailureMessage("/tmp/backup", tc.afterReconcile, tc.err)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tc.wantMissing {
				if strings.Contains(got, unwanted) {
					t.Errorf("message %q must not contain %q", got, unwanted)
				}
			}
		})
	}
}

// backupFamilyPaths is every registry path the backup family owns. Keeping the
// list in one place is what makes "the whole family moves together" an
// assertion rather than a convention.
var backupFamilyPaths = []string{
	"backup", "backup init", "backup sync", "backup remove", "backup status", "backup restore",
}

// TestProxiedBackupHonoredOnlyOnManagedLocal is the locality policy.
//
// A Dolt backup destination is resolved by the SERVER: CALL DOLT_BACKUP('add',
// name, 'file:///…') names a path on the machine running dolt, not on the
// machine running bd. On managed-local that is the same machine and the same
// user, because bd spawned the child itself. On every other proxied shape it is
// somebody else's filesystem, so honoring the command would either fail at the
// point of use or — much worse — succeed and write the backup somewhere the
// operator cannot see. That is why these stay refused with Reason=design.
func TestProxiedBackupHonoredOnlyOnManagedLocal(t *testing.T) {
	for _, path := range backupFamilyPaths {
		row, ok := LookupCapabilityRow(path, "")
		if !ok {
			t.Fatalf("%q has no registry row", path)
		}

		if got := row.ruleFor(ProxyTopologyManagedLocal); got.Outcome != ProxyOutcomeHonored {
			t.Errorf("%q on managed-local: outcome %q, want honored", path, got.Outcome)
		}

		for _, topology := range []ProxyTopology{
			ProxyTopologyExternalTCP, ProxyTopologyExternalUnix,
			ProxyTopologyTeamServer, ProxyTopologyUnknown,
		} {
			got := row.ruleFor(topology)
			if got.Outcome != ProxyOutcomeRefused {
				t.Errorf("%q on %s: outcome %q, want refused", path, topology, got.Outcome)
			}
			if got.Reason != ProxyReasonDesign {
				t.Errorf("%q on %s: reason %q, want design", path, topology, got.Reason)
			}
			if got.Code != "proxy.backup.unsupported" {
				t.Errorf("%q on %s: code %q, want the frozen proxy.backup.unsupported", path, topology, got.Code)
			}
			if !strings.Contains(got.Message, "is not supported in proxied-server mode") {
				t.Errorf("%q on %s: message %q dropped the frozen prefix", path, topology, got.Message)
			}
		}
	}
}

// TestProxiedBackupGateRefusesByTopology exercises the gate itself rather than
// the table, so a row that is right while the validator ignores the topology
// still fails.
func TestProxiedBackupGateRefusesByTopology(t *testing.T) {
	newBackupCmd := func(sub string) *cobra.Command {
		root := &cobra.Command{Use: "bd"}
		parent := &cobra.Command{Use: "backup"}
		child := &cobra.Command{Use: sub}
		parent.AddCommand(child)
		root.AddCommand(parent)
		return child
	}

	for _, sub := range []string{"init", "sync", "remove", "status", "restore"} {
		t.Run(sub, func(t *testing.T) {
			if err := validateProxyRegistryBeforeProvider(newBackupCmd(sub), ProxyTopologyManagedLocal); err != nil {
				t.Fatalf("managed-local: gate refused %q: %v", sub, err)
			}
			err := validateProxyRegistryBeforeProvider(newBackupCmd(sub), ProxyTopologyExternalTCP)
			if err == nil {
				t.Fatalf("external-tcp: gate permitted %q", sub)
			}
		})
	}
}

// TestProxiedBackupRefusalJSONCarriesDesignReason pins what a consumer sees.
// The three frozen fields are unchanged and reason now says "design", which is
// the difference that matters: this refusal is not waiting on a later slice.
func TestProxiedBackupRefusalJSONCarriesDesignReason(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	root := &cobra.Command{Use: "bd"}
	parent := &cobra.Command{Use: "backup"}
	child := &cobra.Command{Use: "sync"}
	parent.AddCommand(child)
	root.AddCommand(parent)

	out := captureStdout(t, func() error {
		_ = validateProxyRegistryBeforeProvider(child, ProxyTopologyExternalTCP)
		return nil
	})
	for _, want := range []string{
		`"code": "proxy.backup.unsupported"`,
		`"mutates": false`,
		`"reason": "design"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("refusal JSON missing %s:\n%s", want, out)
		}
	}
}

// TestResolveProxiedTopology covers the resolution the gate depends on. The
// team-server case is the one worth reading twice: ownership beats transport,
// because a bts-managed database is somebody else's store whether bd reaches it
// over a socket, a port, or a child it spawned itself.
func TestResolveProxiedTopology(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     configfile.Config
		sidecar *configfile.ProxiedServerClientInfo
		rawCfg  string // when set, written verbatim instead of cfg
		rawCar  string // when set, written verbatim instead of sidecar
		want    ProxyTopology
	}{
		{
			name:    "managed local has no external block",
			cfg:     configfile.Config{DoltMode: configfile.DoltModeProxiedServer},
			sidecar: &configfile.ProxiedServerClientInfo{},
			want:    ProxyTopologyManagedLocal,
		},
		{
			name:    "no sidecar at all is still managed local",
			cfg:     configfile.Config{DoltMode: configfile.DoltModeProxiedServer},
			sidecar: nil,
			want:    ProxyTopologyManagedLocal,
		},
		{
			name: "external host and port",
			cfg:  configfile.Config{DoltMode: configfile.DoltModeProxiedServer},
			sidecar: &configfile.ProxiedServerClientInfo{
				External: &configfile.ExternalDoltConfig{Host: "db.example.com", Port: 3306},
			},
			want: ProxyTopologyExternalTCP,
		},
		{
			name: "external unix socket",
			cfg:  configfile.Config{DoltMode: configfile.DoltModeProxiedServer},
			sidecar: &configfile.ProxiedServerClientInfo{
				External: &configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"},
			},
			want: ProxyTopologyExternalUnix,
		},
		{
			name: "team server beats the transport",
			cfg:  configfile.Config{DoltMode: configfile.DoltModeProxiedServer, DoltTeamServer: true},
			sidecar: &configfile.ProxiedServerClientInfo{
				External: &configfile.ExternalDoltConfig{Host: "bts.example.com", Port: 3306},
			},
			want: ProxyTopologyTeamServer,
		},
		{
			name:    "team server on a local child is still team server",
			cfg:     configfile.Config{DoltMode: configfile.DoltModeProxiedServer, DoltTeamServer: true},
			sidecar: &configfile.ProxiedServerClientInfo{},
			want:    ProxyTopologyTeamServer,
		},
		{
			name:   "unreadable sidecar fails closed",
			cfg:    configfile.Config{DoltMode: configfile.DoltModeProxiedServer},
			rawCar: "{ not json",
			want:   ProxyTopologyUnknown,
		},
		{
			name:   "unreadable workspace config fails closed",
			rawCfg: "{ not json",
			want:   ProxyTopologyUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.rawCfg != "":
				writeTestFile(t, configfile.ConfigPath(beadsDir), tc.rawCfg)
			default:
				cfg := tc.cfg
				cfg.Backend = configfile.BackendDolt
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, configfile.ConfigPath(beadsDir), string(data))
			}
			switch {
			case tc.rawCar != "":
				writeTestFile(t, configfile.ProxiedServerClientInfoPath(beadsDir), tc.rawCar)
			case tc.sidecar != nil:
				data, err := json.Marshal(tc.sidecar)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, configfile.ProxiedServerClientInfoPath(beadsDir), string(data))
			}

			if got := resolveProxiedTopology(beadsDir); got != tc.want {
				t.Fatalf("resolveProxiedTopology = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
