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

	// TODO(#6227): az:// is a valid Dolt backup/remote scheme, but this PR only fixes the s3:// stack. Do not add a green row for today's az:// local-file fallback here.
	tests := []struct {
		name  string
		input string
		want  string
		// wantRel asserts structure instead of an exact value: cwd can move under a parallel test.
		wantRel bool
	}{
		{
			name:  "https URL passes through",
			input: "https://doltremoteapi.dolthub.com/user/repo?region=auto",
			want:  "https://doltremoteapi.dolthub.com/user/repo?region=auto",
		},
		{
			name:  "http URL passes through",
			input: "http://localhost:50051/repo?endpoint=local",
			want:  "http://localhost:50051/repo?endpoint=local",
		},
		{
			name:  "file URL passes through",
			input: "file:///tmp/backup?version=1",
			want:  "file:///tmp/backup?version=1",
		},
		{
			name:  "aws URL passes through",
			input: "aws://key:secret@bucket/path?region=auto",
			want:  "aws://key:secret@bucket/path?region=auto",
		},
		{
			name:  "bracketed aws URL passes through",
			input: "aws://[key:secret@]bucket/path",
			want:  "aws://[key:secret@]bucket/path",
		},
		{
			name:  "gs URL passes through",
			input: "gs://bucket/path?endpoint=storage.example",
			want:  "gs://bucket/path?endpoint=storage.example",
		},
		{
			name:  "s3 URL passes through",
			input: "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true",
			want:  "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true",
		},
		{
			name:  "absolute path gets file prefix",
			input: absBackup,
			want:  "file://" + absBackup,
		},
		{
			name:    "relative path gets resolved and prefixed",
			input:   "my-backup",
			wantRel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveDoltBackupURL(tt.input)
			if tt.wantRel {
				wantSuffix := string(filepath.Separator) + "my-backup"
				if !strings.HasPrefix(got, "file://") ||
					!filepath.IsAbs(strings.TrimPrefix(got, "file://")) ||
					!strings.HasSuffix(got, wantSuffix) {
					t.Errorf("resolveDoltBackupURL(%q) = %q, want file:// plus an absolute path ending in %q", tt.input, got, wantSuffix)
				}
				return
			}
			if got != tt.want {
				t.Errorf("resolveDoltBackupURL(%q) = %q, want %q", tt.input, got, tt.want)
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
