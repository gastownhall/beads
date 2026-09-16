//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// TestProxiedServerBackupRefusedOnExternalTopology is the other half of slice
// S3: the backup family is honored on managed-local and refused BY DESIGN
// against a Dolt server bd does not own.
//
// The refusal matters more than the capability. `CALL DOLT_BACKUP('add', …)`
// registers the remote on the server, where it is global to every client of
// that server, so one workspace's backup decision becomes everyone's. With a
// file:/// destination there is a second problem on top: the server resolves
// that path on its own filesystem, so honoring it here would either fail at the
// point of use or write the backup to a directory on the server's host that the
// operator asking for it will never look in. The second outcome is worse than
// any refusal.
//
// Named TestProxiedServer* so .github/scripts/proxied-test-shard.sh runs it in
// the external lane, which is the only one with an external Dolt server.
func TestProxiedServerBackupRefusedOnExternalTopology(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "bkext")
	dest := filepath.Join(t.TempDir(), "dolt-backup")

	// Refusals are decided before the provider is constructed, so take the
	// topology down first: a pidfile appearing during one of these commands
	// would mean the refusal came too late to have cost nothing.
	if err := proxy.Shutdown(p.proxyRoot); err != nil {
		t.Logf("proxy.Shutdown(%s) before the refusal probes: %v", p.proxyRoot, err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"status", []string{"backup", "status"}},
		{"init", []string{"backup", "init", dest}},
		{"sync", []string{"backup", "sync"}},
		{"remove", []string{"backup", "remove"}},
		{"restore", []string{"backup", "restore", dest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--json"}, tc.args...)
			out, err := bdProxiedRun(t, bd, p.dir, args...)
			if err == nil {
				t.Fatalf("bd %s succeeded on an external proxied topology:\n%s",
					strings.Join(tc.args, " "), out)
			}

			var refusal struct {
				Code    string `json:"code"`
				Error   string `json:"error"`
				Mutates bool   `json:"mutates"`
				Reason  string `json:"reason"`
			}
			start := strings.Index(string(out), "{")
			if start < 0 {
				t.Fatalf("no typed refusal JSON:\n%s", out)
			}
			if err := json.Unmarshal(out[start:], &refusal); err != nil {
				t.Fatalf("parse refusal JSON: %v\n%s", err, out)
			}
			if refusal.Code != "proxy.backup.unsupported" {
				t.Errorf("code = %q, want proxy.backup.unsupported", refusal.Code)
			}
			if refusal.Mutates {
				t.Errorf("refusal reported mutates=true")
			}
			// design, not unimplemented: no later slice is going to lift this.
			if refusal.Reason != string(ProxyReasonDesign) {
				t.Errorf("reason = %q, want design", refusal.Reason)
			}
			// The reason has to be the one that is true of every destination
			// scheme. "resolved on the server's filesystem" is a file://
			// property, and `bd backup init` also takes https/aws/gs.
			if !strings.Contains(refusal.Error, "the backup remote is registered on the server, where it is global to every client") {
				t.Errorf("refusal message does not say why: %q", refusal.Error)
			}

			// Nothing was started and nothing was written: the refusal is free.
			for _, name := range []string{proxy.PIDFileName, server.PIDFileName} {
				if _, err := os.Stat(filepath.Join(p.proxyRoot, name)); err == nil {
					t.Errorf("a refused backup command started the topology (%s)", name)
				}
			}
			if _, err := os.Stat(filepath.Join(p.beadsDir, "dolt-backup.json")); err == nil {
				t.Errorf("a refused backup command wrote the local backup config")
			}
			if _, err := os.Stat(dest); err == nil {
				t.Errorf("a refused backup command created %s", dest)
			}
		})
	}
}
