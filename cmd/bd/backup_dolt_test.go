package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveDoltBackupURL(t *testing.T) {
	t.Parallel()

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
			name:    "absolute path gets file:// prefix",
			input:   "/mnt/usb/beads-backup",
			wantPfx: "file:///mnt/usb/beads-backup",
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

func TestDirSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a few test files
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("world!"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	size, err := dirSize(dir)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	// "hello" = 5 bytes, "world!" = 6 bytes = 11 total
	if size != 11 {
		t.Errorf("dirSize = %d, want 11", size)
	}
}

// TestCheckBackupInitRemoteGuard verifies that a local filesystem path is
// rejected when the Dolt server is remote. DoltHub/cloud URLs should pass.
//
// User story (docs/REMOTE_SERVER_USER_STORIES.md - Backup):
//
//	Given beads is connected to a remote dolt server
//	When I run `bd backup init /some/local/path`
//	Then beads does NOT send that local path to the remote server
//	And instead tells me this operation isn't supported in remote mode
func TestCheckBackupInitRemoteGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		isRemote bool
		wantErr  bool
	}{
		{name: "file URL + remote = error", url: "file:///mnt/backup", isRemote: true, wantErr: true},
		{name: "file URL + local = ok", url: "file:///mnt/backup", isRemote: false, wantErr: false},
		{name: "dolthub URL + remote = ok", url: "https://doltremoteapi.dolthub.com/user/repo", isRemote: true, wantErr: false},
		{name: "aws URL + remote = ok", url: "aws://bucket/path", isRemote: true, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBackupInitRemoteGuard(tt.url, tt.isRemote)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for url=%q isRemote=%v, got nil", tt.url, tt.isRemote)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for url=%q isRemote=%v: %v", tt.url, tt.isRemote, err)
			}
		})
	}
}

// TestCheckBackupSyncRemoteGuard verifies that bd backup sync on a remote
// server returns a helpful error when no cloud backup URL is configured,
// guiding the user toward `bd export -o` for JSONL export instead.
//
// User stories (docs/REMOTE_SERVER_USER_STORIES.md - Backup):
//
//	Given beads is connected to a remote dolt server
//	When I run `bd backup sync` without a cloud backup destination
//	Then beads errors with a clear message explaining that server mode
//	  requires an explicit backup location
//	And suggests: `bd export -o /path/to/backup.jsonl`
func TestCheckBackupSyncRemoteGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		isRemote  bool
		backupCfg *doltBackupConfig
		wantErr   bool
		wantMsg   string // substring expected in error message
	}{
		{
			name:      "remote + no backup config = error",
			isRemote:  true,
			backupCfg: nil,
			wantErr:   true,
			wantMsg:   "bd export",
		},
		{
			name:     "remote + file:// backup = error",
			isRemote: true,
			backupCfg: &doltBackupConfig{
				BackupURL: "file:///mnt/backup",
			},
			wantErr: true,
			wantMsg: "bd export",
		},
		{
			name:     "remote + dolthub URL = ok",
			isRemote: true,
			backupCfg: &doltBackupConfig{
				BackupURL: "https://doltremoteapi.dolthub.com/user/repo",
			},
			wantErr: false,
		},
		{
			name:     "remote + aws URL = ok",
			isRemote: true,
			backupCfg: &doltBackupConfig{
				BackupURL: "aws://bucket/path",
			},
			wantErr: false,
		},
		{
			name:      "local + no backup config = ok (normal flow handles this)",
			isRemote:  false,
			backupCfg: nil,
			wantErr:   false,
		},
		{
			name:     "local + file:// backup = ok",
			isRemote: false,
			backupCfg: &doltBackupConfig{
				BackupURL: "file:///mnt/backup",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBackupSyncRemoteGuard(tt.isRemote, tt.backupCfg)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for isRemote=%v backupCfg=%+v, got nil", tt.isRemote, tt.backupCfg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for isRemote=%v backupCfg=%+v: %v", tt.isRemote, tt.backupCfg, err)
			}
			if tt.wantErr && err != nil && tt.wantMsg != "" {
				if !strings.Contains(err.Error(), tt.wantMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantMsg)
				}
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
