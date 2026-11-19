package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
)

func TestResolveDoltBackupURL(t *testing.T) {
	t.Parallel()

	// Build the absolute-path fixture with platform-native semantics.
	// A hard-coded POSIX literal like "/mnt/usb/beads-backup" is not absolute
	// on Windows (no volume name), so resolveDoltBackupURL would resolve it
	// against the current drive and return "file://C:\\mnt\\usb\\...".
	// t.TempDir() is absolute and already cleaned on every platform.
	absBackup := filepath.Join(t.TempDir(), "beads-backup")

	tests := []struct {
		name      string
		input     string
		wantPfx   string // expected prefix
		wantExact string // exact match (empty = use prefix check)
	}{
		{
			name:      "https URL passes through",
			input:     "https://doltremoteapi.dolthub.com/user/repo",
			wantExact: "https://doltremoteapi.dolthub.com/user/repo",
		},
		{
			name:      "http URL passes through",
			input:     "http://localhost:50051/repo",
			wantExact: "http://localhost:50051/repo",
		},
		{
			name:      "file:// URL passes through",
			input:     "file:///tmp/backup",
			wantExact: "file:///tmp/backup",
		},
		{
			name:      "aws:// URL passes through",
			input:     "aws://[key:secret@]bucket/path",
			wantExact: "aws://[key:secret@]bucket/path",
		},
		{
			name:      "gs:// URL passes through",
			input:     "gs://bucket/path",
			wantExact: "gs://bucket/path",
		},
		{
			name:      "az:// URL passes through",
			input:     "az://account.blob.core.windows.net/container/beads",
			wantExact: "az://account.blob.core.windows.net/container/beads",
		},
		{
			name:      "oci:// URL passes through",
			input:     "oci://registry.example/tenant/beads",
			wantExact: "oci://registry.example/tenant/beads",
		},
		{
			name:      "absolute path gets file:// prefix",
			input:     absBackup,
			wantExact: "file://" + absBackup,
		},
		{
			name:    "relative path gets resolved and prefixed",
			input:   "my-backup",
			wantPfx: "file://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveDoltBackupURL(tt.input)
			if tt.wantExact != "" {
				if got != tt.wantExact {
					t.Errorf("resolveDoltBackupURL(%q) = %q, want %q", tt.input, got, tt.wantExact)
				}
			} else if tt.wantPfx != "" {
				if len(got) < len(tt.wantPfx) || got[:len(tt.wantPfx)] != tt.wantPfx {
					t.Errorf("resolveDoltBackupURL(%q) = %q, want prefix %q", tt.input, got, tt.wantPfx)
				}
			}
		})
	}
}

func TestResolveDoltBackupURL_HomeTilde(t *testing.T) {
	t.Parallel()
	got := resolveDoltBackupURL("~/backups/beads")
	home, _ := os.UserHomeDir()
	want := "file://" + filepath.Join(home, "backups/beads")
	if got != want {
		t.Errorf("resolveDoltBackupURL(~/backups/beads) = %q, want %q", got, want)
	}
}

func TestDoltBackupConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "dolt-backup.json")
	cfg := doltBackupConfig{
		BackupURL:  "file:///mnt/usb/backup",
		BackupName: "default",
		CreatedAt:  time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read it back
	readData, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var loaded doltBackupConfig
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.BackupURL != cfg.BackupURL {
		t.Errorf("backup_url = %q, want %q", loaded.BackupURL, cfg.BackupURL)
	}
	if loaded.BackupName != cfg.BackupName {
		t.Errorf("backup_name = %q, want %q", loaded.BackupName, cfg.BackupName)
	}
}

func TestDoltBackupStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	statePath := filepath.Join(dir, "dolt-backup-state.json")
	state := doltBackupState{
		LastSync: time.Date(2026, 2, 27, 14, 30, 0, 0, time.UTC),
		Duration: "2.5s",
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	readData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var loaded doltBackupState
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.Duration != "2.5s" {
		t.Errorf("duration = %q, want %q", loaded.Duration, "2.5s")
	}
	if !loaded.LastSync.Equal(state.LastSync) {
		t.Errorf("last_sync = %v, want %v", loaded.LastSync, state.LastSync)
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := formatBytes(tt.input)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

type backupSizeStub struct {
	size int64
	err  error
}

func (s backupSizeStub) ActiveDatabaseSize(context.Context) (int64, error) {
	return s.size, s.err
}

func TestDoltBackupSizeFromSizer(t *testing.T) {
	t.Parallel()

	failure := errors.New("measurement failed")
	tests := []struct {
		name          string
		sizer         storage.ActiveDatabaseSizer
		wantSize      int64
		wantAvailable bool
		wantErr       error
	}{
		{
			name:          "available",
			sizer:         backupSizeStub{size: 42},
			wantSize:      42,
			wantAvailable: true,
		},
		{
			name: "unsupported",
			sizer: backupSizeStub{err: &storage.ErrUnsupported{
				Op:      "ActiveDatabaseSize",
				Backend: "external",
			}},
		},
		{
			name:    "declared local failure",
			sizer:   backupSizeStub{err: failure},
			wantErr: failure,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			size, available, err := doltBackupSizeFromSizer(t.Context(), tc.sizer)
			if size != tc.wantSize || available != tc.wantAvailable || !errors.Is(err, tc.wantErr) {
				t.Fatalf(
					"doltBackupSizeFromSizer = (%d, %v, %v), want (%d, %v, %v)",
					size, available, err, tc.wantSize, tc.wantAvailable, tc.wantErr,
				)
			}
		})
	}
}

func TestShowDoltBackupStatusJSON_NilWhenNotConfigured(t *testing.T) {
	t.Parallel()
	// When no .beads dir exists, should return configured=false
	result := showDoltBackupStatusJSON()
	configured, ok := result["configured"].(bool)
	if !ok || configured {
		t.Errorf("expected configured=false, got %v", result)
	}
}

// TestResolveDoltBackupURL_AzurePassesThrough pins #6227: az:// is a Dolt
// remote scheme (remotecache.remoteSchemes, the credential map), so a backup
// destination like az://container/beads must reach Dolt unchanged instead of
// being resolved as a relative filesystem path under file://.
func TestResolveDoltBackupURL_AzurePassesThrough(t *testing.T) {
	t.Parallel()
	const in = "az://container/beads"
	if got := resolveDoltBackupURL(in); got != in {
		t.Errorf("resolveDoltBackupURL(%q) = %q, want %q (az:// misrouted to a local path)", in, got, in)
	}
}

// recordingBackupInitStore is a DoltStorage stand-in for backupInitCmd.RunE:
// it embeds a nil DoltStorage for the type assertion and records every
// BackupAdd call, so a test can assert the exact destination bd registered.
type recordingBackupInitStore struct {
	storage.DoltStorage
	adds []backupAddCall
}

type backupAddCall struct{ name, url string }

func (r *recordingBackupInitStore) BackupAdd(_ context.Context, name, url string) error {
	r.adds = append(r.adds, backupAddCall{name: name, url: url})
	return nil
}

func (r *recordingBackupInitStore) BackupSync(context.Context, string) error     { return nil }
func (r *recordingBackupInitStore) BackupRemove(context.Context, string) error   { return nil }
func (r *recordingBackupInitStore) BackupDatabase(context.Context, string) error { return nil }
func (r *recordingBackupInitStore) RestoreDatabase(context.Context, string, bool) error {
	return nil
}

// TestBackupInitPreservesCloudURL pins the direct-route half of #6227
// (AC-1): `bd backup init az://…` / `oci://…` must register, save, and echo
// the exact supplied URL. At base the resolver rewrote these to
// `file://<cwd>/az:/…`, so the backup silently targeted a local directory.
//
// Not parallel: it swaps the global store, root context, and jsonOutput.
func TestBackupInitPreservesCloudURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "azure", url: "az://account.blob.core.windows.net/container/beads"},
		{name: "oci", url: "oci://registry.example/tenant/beads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBackupStatusTest(t)
			beads.ResetCaches()
			t.Cleanup(beads.ResetCaches)

			fake := &recordingBackupInitStore{}
			oldStore, oldCtx, oldWrote := store, rootCtx, commandDidWrite.Load()
			store, rootCtx = fake, context.Background()
			t.Cleanup(func() {
				store, rootCtx = oldStore, oldCtx
				commandDidWrite.Store(oldWrote)
			})

			stdout := captureStdout(t, func() error {
				return backupInitCmd.RunE(backupInitCmd, []string{tc.url})
			})

			if len(fake.adds) != 1 {
				t.Fatalf("BackupAdd called %d times, want 1 (%v)", len(fake.adds), fake.adds)
			}
			if fake.adds[0].name != "default" || fake.adds[0].url != tc.url {
				t.Errorf("registered (%q, %q), want (%q, %q): the cloud URL must reach Dolt unchanged",
					fake.adds[0].name, fake.adds[0].url, "default", tc.url)
			}

			cfg, err := loadDoltBackupConfig()
			if err != nil || cfg == nil {
				t.Fatalf("loadDoltBackupConfig: %v, %v", cfg, err)
			}
			if cfg.BackupURL != tc.url {
				t.Errorf("saved backup_url = %q, want the exact supplied URL %q", cfg.BackupURL, tc.url)
			}

			if !strings.Contains(stdout, "Backup destination configured: "+tc.url) {
				t.Errorf("text output does not echo the supplied URL: %q", stdout)
			}
			if !commandDidWrite.Load() {
				t.Errorf("commandDidWrite not set after a successful init")
			}

			// The --json success object keeps the URL too.
			jsonOutput = true
			jsonStdout := captureStdout(t, func() error {
				return backupInitCmd.RunE(backupInitCmd, []string{tc.url})
			})
			var result struct {
				BackupURL   string `json:"backup_url"`
				BackupName  string `json:"backup_name"`
				Initialized bool   `json:"initialized"`
			}
			if err := json.Unmarshal([]byte(jsonStdout), &result); err != nil {
				t.Fatalf("parse init JSON: %v\n%s", err, jsonStdout)
			}
			if !result.Initialized || result.BackupName != "default" || result.BackupURL != tc.url {
				t.Errorf("JSON init result = %+v, want the exact supplied URL %q", result, tc.url)
			}
		})
	}
}
