package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestResolveInitIdentity pins what counts as a repoint (#6172): a flag that
// only restates the configured identity is not one.
func TestResolveInitIdentity(t *testing.T) {
	server := func(db, host string, port int) initDBRef {
		return initDBRef{Database: db, Server: true, Host: host, Port: port}
	}
	recorded := &configfile.Config{}
	tests := []struct {
		name       string
		ws         initIdentity
		flags      initIdentityFlags
		serverMode bool
		repoints   bool
		countPort  int // Configured.Port after resolution; 0 = not checked
	}{
		{name: "identical values",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Database: "a", Host: "127.0.0.1", Port: 3307}, serverMode: true},
		{name: "case-only database",
			ws:    initIdentity{Recorded: recorded, Configured: server("Proj", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Database: "proj"}, serverMode: true},
		{name: "localhost restates 127.0.0.1",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Host: "localhost"}, serverMode: true},
		{name: "bracketed upper-case host restates itself",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "db.example", 3307)},
			flags: initIdentityFlags{Host: "[DB.example]"}, serverMode: true},
		{name: "::1 is not 127.0.0.1",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Host: "::1"}, serverMode: true, repoints: true},
		{name: "port flag naming the workspace's own port over a stale export (GH#5177)",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 9999), ownPort: 3307},
			flags: initIdentityFlags{Port: 3307}, serverMode: true, countPort: 3307},
		{name: "port flag naming another port",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 3307), ownPort: 3307},
			flags: initIdentityFlags{Port: 3308}, serverMode: true, repoints: true, countPort: 3307},
		{name: "unknown configured port never repoints",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 0)},
			flags: initIdentityFlags{Port: 3308}, serverMode: true},
		{name: "another database",
			ws:    initIdentity{Recorded: recorded, Configured: server("a", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Database: "b"}, serverMode: true, repoints: true},
		{name: "mode change compares names only",
			ws:    initIdentity{Recorded: recorded, Configured: initDBRef{Database: "a"}},
			flags: initIdentityFlags{Host: "db.example", Port: 3308}, serverMode: true},
		{name: "unset recorded name never repoints",
			ws:    initIdentity{Recorded: recorded, Configured: server("", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Database: "b"}, serverMode: true},
		{name: "fresh workspace never repoints",
			ws:    initIdentity{Configured: server("a", "127.0.0.1", 3307)},
			flags: initIdentityFlags{Database: "b", Port: 3308}, serverMode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := resolveInitIdentity(tt.ws, tt.flags, tt.serverMode)
			if got := id.Repoints(); got != tt.repoints {
				t.Errorf("Repoints() = %v, want %v (configured %+v, target %+v)", got, tt.repoints, id.Configured, id.Target)
			}
			if tt.countPort != 0 && id.Configured.Port != tt.countPort {
				t.Errorf("Configured.Port = %d, want %d", id.Configured.Port, tt.countPort)
			}
			if tt.flags.Database != "" && id.Target.Database != tt.flags.Database {
				t.Errorf("Target.Database = %q, want the flag %q", id.Target.Database, tt.flags.Database)
			}
		})
	}
}
