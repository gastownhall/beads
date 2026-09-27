//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestIsBackupAutoEnabled(t *testing.T) {
	// Cannot be parallel: modifies global primeHasGitRemote and env vars.

	tests := []struct {
		name         string
		envVal       string // "\x00" = not set, "" = set to empty, "true"/"false"/"0" = explicit
		hasRemote    bool
		sharedServer bool // BEADS_DOLT_SHARED_SERVER=1 → usesSQLServer() true
		wantResult   bool
	}{
		{
			name:       "default + git remote → enabled",
			envVal:     "\x00",
			hasRemote:  true,
			wantResult: true,
		},
		{
			name:       "default + no git remote → disabled",
			envVal:     "\x00",
			hasRemote:  false,
			wantResult: false,
		},
		{
			name:       "explicit true + no remote → enabled",
			envVal:     "true",
			hasRemote:  false,
			wantResult: true,
		},
		{
			name:       "explicit false + remote → disabled",
			envVal:     "false",
			hasRemote:  true,
			wantResult: false,
		},
		{
			name:       "explicit 0 + remote → disabled",
			envVal:     "0",
			hasRemote:  true,
			wantResult: false,
		},
		{
			name:       "empty string + remote → disabled (env set = explicit)",
			envVal:     "",
			hasRemote:  true,
			wantResult: false,
		},
		{
			// wy-zrmqr: unset default must NOT auto-enable in sql-server
			// mode even with a git remote — N clients racing one backup
			// name was the storm amplifier in the 2026-07 CPU-pin incident.
			name:         "default + git remote + sql-server mode → disabled",
			envVal:       "\x00",
			hasRemote:    true,
			sharedServer: true,
			wantResult:   false,
		},
		{
			// Explicit opt-in still honored in sql-server mode: operators
			// who coordinate destinations themselves may turn it on.
			name:         "explicit true + sql-server mode → enabled",
			envVal:       "true",
			hasRemote:    false,
			sharedServer: true,
			wantResult:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Isolate CWD and BEADS_DIR before config.Initialize() below.
			// Without this, a leaked BEADS_DIR (or cwd left inside the repo
			// tree by an earlier no-DB command in the same test binary) lets
			// Initialize() load a real ambient config.yaml with an explicit
			// backup.enabled value, which wins over the computed default
			// these cases assert on (be-yjp4z).
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

			// Stub primeHasGitRemote
			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			if tt.sharedServer {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			} else {
				os.Unsetenv("BEADS_DOLT_SHARED_SERVER")
				t.Cleanup(func() { os.Unsetenv("BEADS_DOLT_SHARED_SERVER") })
			}

			// Set env var: "\x00" = unset, anything else = set to that value
			if tt.envVal == "\x00" {
				os.Unsetenv("BD_BACKUP_ENABLED")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_ENABLED") })
			} else {
				t.Setenv("BD_BACKUP_ENABLED", tt.envVal)
			}

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := isBackupAutoEnabled()
			if got != tt.wantResult {
				t.Errorf("isBackupAutoEnabled() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

// TestBackupAutoStatusNote pins the reason `bd backup status` prints beside
// the effective backup.enabled value against the reason isBackupAutoEnabled
// actually used — the two are computed in different files and drifted apart
// once already.
//
// The regression this locks down arrived with the proxied-parity S3 slice
// (PR #6584): honoring the backup family on managed-local made `bd backup
// status` reachable on a proxied server for the first time, where it rendered
// the embedded-mode footnote "(auto: no git remote)". On any sql-server shape
// the value is false because usesSQLServer() short-circuits, whether or not a
// remote exists, so that reason was simply untrue; and on proxied specifically
// even an explicit backup.enabled=true changes nothing, because the proxied
// arm of PersistentPostRunE never calls runPostRunAutoBackup.
//
// Cannot be parallel: mutates the proxiedServerMode global, primeHasGitRemote
// and env vars.
func TestBackupAutoStatusNote(t *testing.T) {
	tests := []struct {
		name         string
		envVal       string // "\x00" = not set, otherwise explicit backup.enabled
		hasRemote    bool
		sharedServer bool // BEADS_DOLT_SHARED_SERVER=1 → usesSQLServer() true
		proxied      bool // proxiedServerMode → usesProxiedServer() true
		wantEnabled  bool
		wantNote     string
	}{
		{
			name:        "embedded default + git remote → unchanged",
			envVal:      "\x00",
			hasRemote:   true,
			wantEnabled: true,
			wantNote:    "auto: git remote detected",
		},
		{
			name:        "embedded default + no git remote → unchanged",
			envVal:      "\x00",
			hasRemote:   false,
			wantEnabled: false,
			wantNote:    "auto: no git remote",
		},
		{
			name:        "embedded explicit → no note, the source explains it",
			envVal:      "true",
			hasRemote:   false,
			wantEnabled: true,
			wantNote:    "",
		},
		{
			// Pre-fix this case printed "auto: no git remote" even with a
			// remote configured: server mode, not the remote, is the reason.
			name:         "sql-server default + git remote → server mode, not the remote",
			envVal:       "\x00",
			hasRemote:    true,
			sharedServer: true,
			wantEnabled:  false,
			wantNote:     "auto: off in sql-server mode",
		},
		{
			// Non-proxied server mode keeps its post-run hook, so an explicit
			// opt-in really does work there and needs no caveat.
			name:         "sql-server explicit opt-in → no note",
			envVal:       "true",
			hasRemote:    false,
			sharedServer: true,
			wantEnabled:  true,
			wantNote:     "",
		},
		{
			name:        "proxied default → names the missing hook, not the remote",
			envVal:      "\x00",
			hasRemote:   true,
			proxied:     true,
			wantEnabled: false,
			wantNote:    "auto-backup does not run on proxied-server; use 'bd backup sync'",
		},
		{
			// The dead opt-in: enabled=true with nothing to run it. The note
			// must survive an explicit source here, or status reports a bare
			// "enabled=true" and the operator believes backups are happening.
			name:        "proxied explicit opt-in → still inert, still annotated",
			envVal:      "true",
			hasRemote:   false,
			proxied:     true,
			wantEnabled: true,
			wantNote:    "auto-backup does not run on proxied-server; use 'bd backup sync'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Same isolation as TestIsBackupAutoEnabled: a leaked BEADS_DIR or
			// a cwd inside the repo tree lets Initialize() load an ambient
			// config.yaml whose explicit backup.enabled wins over the computed
			// default these cases assert on (be-yjp4z).
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			origProxied := proxiedServerMode
			proxiedServerMode = tt.proxied
			t.Cleanup(func() { proxiedServerMode = origProxied })

			if tt.sharedServer {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			} else {
				os.Unsetenv("BEADS_DOLT_SHARED_SERVER")
				t.Cleanup(func() { os.Unsetenv("BEADS_DOLT_SHARED_SERVER") })
			}

			if tt.envVal == "\x00" {
				os.Unsetenv("BD_BACKUP_ENABLED")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_ENABLED") })
			} else {
				t.Setenv("BD_BACKUP_ENABLED", tt.envVal)
			}

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			// Take the value the way backup status does, so the note is pinned
			// against the number it is printed beside rather than a literal.
			enabled := isBackupAutoEnabled()
			if enabled != tt.wantEnabled {
				t.Fatalf("isBackupAutoEnabled() = %v, want %v", enabled, tt.wantEnabled)
			}
			if got := backupAutoStatusNote(enabled); got != tt.wantNote {
				t.Errorf("backupAutoStatusNote(%v) = %q, want %q", enabled, got, tt.wantNote)
			}
		})
	}
}

// TestClientServerShareFilesystem_GH3523 pins the gating logic that
// suppresses auto-backup's file:// register when the Dolt server
// runs on a different filesystem from the client. Pre-fix every
// command emitted "auto-backup failed: register backup remote: ...
// failed to create directory ..." for operators with an external
// (non-localhost) Dolt server.
func TestClientServerShareFilesystem_GH3523(t *testing.T) {
	tests := []struct {
		name      string
		envHost   string // "\x00" = unset
		yamlHost  string // "\x00" = unset
		wantShare bool
	}{
		{
			name:      "no env, no yaml → embedded/local, share=true",
			envHost:   "\x00",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=localhost → local, share=true",
			envHost:   "localhost",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=127.0.0.1 → local, share=true",
			envHost:   "127.0.0.1",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=non-localhost IP → external, share=false",
			envHost:   "192.0.2.10",
			yamlHost:  "\x00",
			wantShare: false,
		},
		{
			name:      "env=non-localhost FQDN → external, share=false",
			envHost:   "dolt-primary.tailnet.example.com",
			yamlHost:  "\x00",
			wantShare: false,
		},
		{
			name:      "yaml dolt.host=non-localhost → external, share=false",
			envHost:   "\x00",
			yamlHost:  "10.0.0.5",
			wantShare: false,
		},
		{
			name:      "env=empty (set to empty), yaml=non-localhost → external, share=false",
			envHost:   "",
			yamlHost:  "10.0.0.5",
			wantShare: false,
		},
		{
			name:      "env=localhost overrides yaml=non-localhost → share=true",
			envHost:   "localhost",
			yamlHost:  "10.0.0.5",
			wantShare: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost == "\x00" {
				os.Unsetenv("BEADS_DOLT_SERVER_HOST")
			} else {
				t.Setenv("BEADS_DOLT_SERVER_HOST", tt.envHost)
			}

			// Construct a fresh config.yaml for the dolt.host case.
			configDir := t.TempDir()
			if tt.yamlHost != "\x00" {
				yamlPath := filepath.Join(configDir, "config.yaml")
				content := "dolt:\n  host: " + tt.yamlHost + "\n"
				if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEADS_DIR", configDir)
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := clientServerShareFilesystem()
			if got != tt.wantShare {
				t.Errorf("clientServerShareFilesystem() = %v, want %v", got, tt.wantShare)
			}
		})
	}
}
