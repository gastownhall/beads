package doltserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// doltserverHostSpellings covers each host kind in the spellings that used to
// get different answers from this package's private host lists.
var doltserverHostSpellings = []string{
	"127.0.0.1", "localhost", "LOCALHOST", "0.0.0.0", "::ffff:127.0.0.1",
	"::1", "[::1]", "0:0:0:0:0:0:0:1",
	"127.0.0.2",
	"db.example.com",
}

func writeServerHostMetadata(t *testing.T, beadsDir, host string) {
	t.Helper()
	meta := configfile.DefaultConfig()
	meta.Backend = configfile.BackendDolt
	meta.DoltMode = configfile.DoltModeServer
	meta.DoltServerHost = host
	if err := meta.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
}

// TestResolvePhysicalRootsHostOwnership pins gate planning to
// configfile.BdRunsServerAt: .beads/dolt is the physical root of a server
// only when bd runs that server. A server at the IPv6 loopback or another
// loopback address keeps its data wherever its own data directory is.
func TestResolvePhysicalRootsHostOwnership(t *testing.T) {
	for _, host := range doltserverHostSpellings {
		t.Run(host, func(t *testing.T) {
			resetPhysicalRootEnv(t)
			beadsDir := newTestBeadsDir(t)
			writeServerHostMetadata(t, beadsDir, host)
			pr, err := ResolvePhysicalRoots(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			if configfile.BdRunsServerAt(host) {
				assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "dolt"))
				return
			}
			if !pr.RemoteBackend || len(pr.Roots) != 0 {
				t.Errorf("host %q: RemoteBackend=%v Roots=%v; a server bd does not run has no local physical root (provenance: %s)",
					host, pr.RemoteBackend, pr.Roots, pr.Provenance)
			}
		})
	}
}

// TestDefaultConfigHostOwnershipPortFile pins when bd's port file, which
// records the port of bd's own server on 127.0.0.1, names a port. In external
// mode it does only for a host bd runs its server at. In owned mode it does
// for every host except ::1 and [::1], as on main: dropping it for the other
// IPv6-loopback spellings (0:0:0:0:0:0:0:1, ::, [::]) made a nested bd init
// under a configuration naming one print port 0 in its summary. Where it is
// dropped the precedence chain resumes, and in external mode a host no
// setting gives a port gets the documented default.
func TestDefaultConfigHostOwnershipPortFile(t *testing.T) {
	ownedDrops := map[string]bool{"::1": true, "[::1]": true}
	hosts := append(append([]string{}, doltserverHostSpellings...), "::", "[::]")
	const stale = 14444
	for _, external := range []bool{false, true} {
		for _, host := range hosts {
			name := host + "/owned"
			if external {
				name = host + "/external"
			}
			t.Run(name, func(t *testing.T) {
				namedPortRefusalEnv(t)
				if external {
					t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
				}
				beadsDir := t.TempDir()
				writeServerHostMetadata(t, beadsDir, host)
				if err := writePortFile(beadsDir, stale); err != nil {
					t.Fatal(err)
				}
				for _, got := range []struct {
					name string
					cfg  *Config
				}{{"DefaultConfig", DefaultConfig(beadsDir)}, {"DialConfig", DialConfig(beadsDir)}} {
					usesFile := got.cfg.PortSource == PortSourcePortFile || got.cfg.Port == stale
					wantFile := configfile.BdRunsServerAt(host)
					if !external {
						wantFile = !ownedDrops[host]
					}
					if usesFile != wantFile {
						t.Errorf("%s(host %q): port %d from %q; port file used = %v, want %v",
							got.name, host, got.cfg.Port, got.cfg.PortSource, usesFile, wantFile)
						continue
					}
					if wantFile {
						continue
					}
					wantPort, wantSource := 0, PortSourceUnset
					if got.cfg.Mode == ServerModeExternal {
						wantPort, wantSource = configfile.DefaultDoltServerPort, PortSourceExternalHostDefault
					}
					if got.cfg.Port != wantPort || got.cfg.PortSource != wantSource {
						t.Errorf("%s(host %q, mode %v): port %d from %q, want %d from %q",
							got.name, host, got.cfg.Mode, got.cfg.Port, got.cfg.PortSource, wantPort, wantSource)
					}
				}
			})
		}
	}
}

// TestEnsureRunningAdviceHostOwnership pins the advice EnsureRunning and the
// store give for a server bd cannot reach: for a host bd does not run its
// server at it never says `bd dolt start`, and for one it does it never says
// the server is external.
func TestEnsureRunningAdviceHostOwnership(t *testing.T) {
	for _, host := range doltserverHostSpellings {
		t.Run(host, func(t *testing.T) {
			namedPortRefusalEnv(t)
			beadsDir := t.TempDir()
			writeServerHostMetadata(t, beadsDir, host)
			bd := configfile.BdRunsServerAt(host)

			if _, ok := externalNonLocalhostHost(beadsDir); ok == bd {
				t.Errorf("externalNonLocalhostHost(host %q) ok = %v, want %v", host, ok, !bd)
			}
			if got := UnresolvedPortAdvice(host) == ""; got != bd {
				t.Errorf("UnresolvedPortAdvice(%q) empty = %v, want %v", host, got, bd)
			}
			advice := UnreachableEndpointAdvice(beadsDir, host, 3307, PortSourceUnset)
			if got := strings.Contains(advice, "Verify the external server"); got == bd {
				t.Errorf("UnreachableEndpointAdvice(%q) = %q; external-server advice = %v, want %v", host, advice, got, !bd)
			}
		})
	}
}
