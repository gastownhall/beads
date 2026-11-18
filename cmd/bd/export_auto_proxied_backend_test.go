package main

import (
	"testing"
)

// TestAutoExportBackendForCommand pins what auto-export may read from. In
// proxied-server mode it reads only through the provider the command already
// opened, and only when that provider exposes the maintenance seam; it skips
// a command pointed at another database (--database), whose issues must
// never overwrite the workspace's JSONL. Resolving a backend never touches
// the server.
//
// Cannot be parallel: mutates the store, uowProvider, proxiedServerMode and
// commandDatabaseOverride globals.
func TestAutoExportBackendForCommand(t *testing.T) {
	for _, tc := range []struct {
		name        string
		proxied     bool
		provider    string // "", "maintenance", "plain"
		dbOverride  string
		wantBackend string // "" for no backend
	}{
		{name: "direct with no store open"},
		{name: "proxied with no provider open", proxied: true},
		{name: "proxied provider without the maintenance seam", proxied: true, provider: "plain"},
		{name: "proxied command against another database", proxied: true, provider: "maintenance", dbOverride: "other_db"},
		{name: "proxied with a maintenance provider", proxied: true, provider: "maintenance", wantBackend: "proxied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStore, oldProvider := store, uowProvider
			oldProxied, oldOverride := proxiedServerMode, commandDatabaseOverride
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				proxiedServerMode, commandDatabaseOverride = oldProxied, oldOverride
			})

			store = nil
			proxiedServerMode = tc.proxied
			commandDatabaseOverride = tc.dbOverride
			fake := &fakeMaintenanceProvider{}
			switch tc.provider {
			case "":
				uowProvider = nil
			case "maintenance":
				uowProvider = fake
			case "plain":
				var events []string
				uowProvider = &closeRecordingProvider{events: &events}
			}

			backend, ok := autoExportBackendForCommand()
			switch {
			case tc.wantBackend == "" && ok:
				t.Fatalf("autoExportBackendForCommand() = %T, want no backend", backend)
			case tc.wantBackend == "proxied":
				if _, isProxied := backend.(proxiedAutoExportBackend); !ok || !isProxied {
					t.Fatalf("autoExportBackendForCommand() = %T, %v; want proxiedAutoExportBackend", backend, ok)
				}
			}
			if fake.runs != 0 {
				t.Fatalf("resolving the backend ran %d maintenance call(s); it must not touch the server", fake.runs)
			}
		})
	}
}
