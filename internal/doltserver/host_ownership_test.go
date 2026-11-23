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
// records the port of bd's own server on 127.0.0.1, names a port: only for a
// host bd runs its server at, in owned and external mode alike, whether the
// host comes from metadata.json or BEADS_DOLT_SERVER_HOST. Paired with any
// other host (the IPv6 loopback in every spelling, 127.0.0.2, another
// machine) it would dial that host on a stale port. Where it is dropped, or
// absent, the precedence chain resumes: a host bd does not run its server at
// gets the documented default in external mode and no port in owned mode,
// where the store then asks for one to be pinned.
//
// The mode is pinned too. dolt_mode: server keeps a metadata.json host from
// inferring server mode, and BEADS_DOLT_SERVER_HOST decides alone
// (configfile.HostImpliesServerMode), so an environment host spelled
// 0:0:0:0:0:0:0:1, :: or [::] is owned mode, as ::1 is, not external mode
// with the default port.
func TestDefaultConfigHostOwnershipPortFile(t *testing.T) {
	hosts := append(append([]string{}, doltserverHostSpellings...), "::", "[::]")
	const stale = 14444
	for _, external := range []bool{false, true} {
		for _, fromEnv := range []bool{false, true} {
			for _, portFile := range []bool{true, false} {
				for _, host := range hosts {
					source, setting, file := "metadata", "dolt_mode", "port file"
					if fromEnv {
						source = "env"
					}
					if external {
						setting = "BEADS_DOLT_SERVER_MODE"
					}
					if !portFile {
						file = "no port file"
					}
					t.Run(host+"/"+source+"/"+setting+"/"+file, func(t *testing.T) {
						namedPortRefusalEnv(t)
						if external {
							t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
						}
						beadsDir := t.TempDir()
						if fromEnv {
							writeServerHostMetadata(t, beadsDir, "")
							t.Setenv("BEADS_DOLT_SERVER_HOST", host)
						} else {
							writeServerHostMetadata(t, beadsDir, host)
						}
						if portFile {
							if err := writePortFile(beadsDir, stale); err != nil {
								t.Fatal(err)
							}
						}
						bd := configfile.BdRunsServerAt(host)
						wantMode := ServerModeOwned
						if external || (fromEnv && !configfile.IsLocalHostString(host)) {
							wantMode = ServerModeExternal
						}
						wantPort, wantSource := 0, PortSourceUnset
						switch {
						case portFile && bd:
							wantPort, wantSource = stale, PortSourcePortFile
						case wantMode == ServerModeExternal && !bd:
							wantPort, wantSource = configfile.DefaultDoltServerPort, PortSourceExternalHostDefault
						}
						for _, got := range []struct {
							name string
							cfg  *Config
						}{{"DefaultConfig", DefaultConfig(beadsDir)}, {"DialConfig", DialConfig(beadsDir)}} {
							if got.cfg.Mode != wantMode || got.cfg.Port != wantPort || got.cfg.PortSource != wantSource {
								t.Errorf("%s(host %q): mode %v, port %d from %q; want mode %v, port %d from %q",
									got.name, host, got.cfg.Mode, got.cfg.Port, got.cfg.PortSource, wantMode, wantPort, wantSource)
							}
						}
					})
				}
			}
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
