package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
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

func TestValidateBackupPruneGracePeriod(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		provided bool
		want     time.Duration
		prune    bool
		wantErr  string
	}{
		{name: "omitted"},
		{name: "minimum", input: 10 * time.Minute, provided: true, want: 10 * time.Minute, prune: true},
		{name: "compound duration", input: 90 * time.Minute, provided: true, want: 90 * time.Minute, prune: true},
		{name: "below minimum", input: 9*time.Minute + 59*time.Second, provided: true, prune: true, wantErr: "must be at least 10m"},
		{name: "zero", input: 0, provided: true, prune: true, wantErr: "must be at least 10m"},
		{name: "negative", input: -time.Hour, provided: true, prune: true, wantErr: "must be at least 10m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prune, err := validateBackupPruneGracePeriod(tt.input, tt.provided)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validateBackupPruneGracePeriod(%s) error = %v, want %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateBackupPruneGracePeriod(%s): %v", tt.input, err)
			}
			if got != tt.want || prune != tt.prune {
				t.Fatalf("validateBackupPruneGracePeriod(%s) = (%s, %v), want (%s, %v)", tt.input, got, prune, tt.want, tt.prune)
			}
		})
	}
}

func TestBackupSyncWithPruneErrorMapping(t *testing.T) {
	driverErr := errors.New("connection reset")
	missingChunkErr := errors.New("table file abc123 not found")
	destinationErr := fmt.Errorf("%w (configured destination: https://example.invalid/beads)",
		versioncontrolops.ErrBackupPruneRequiresFileDestination)
	tests := []struct {
		name      string
		err       error
		prune     bool
		want      string
		wantIsErr error
	}{
		{
			name:  "missing registered destination on the prune path",
			err:   fmt.Errorf("%w: default", versioncontrolops.ErrBackupRemoteNotFound),
			prune: true,
			want:  "no backup destination configured",
		},
		{
			name: "plain sync reports Dolt's not-found text",
			err:  errors.New("backup 'default' not found"),
			want: "no backup destination configured",
		},
		{
			name:      "prune sync failure mentioning not found keeps its cause",
			err:       fmt.Errorf("sync backup default with pruning: %w", missingChunkErr),
			prune:     true,
			want:      "backup sync failed: sync backup default with pruning: table file abc123 not found",
			wantIsErr: missingChunkErr,
		},
		{
			name:      "non-file destination is returned unchanged",
			err:       destinationErr,
			prune:     true,
			want:      destinationErr.Error(),
			wantIsErr: versioncontrolops.ErrBackupPruneRequiresFileDestination,
		},
		{
			name:      "other sync failure keeps its cause",
			err:       fmt.Errorf("sync backup default with pruning: %w", driverErr),
			prune:     true,
			want:      "backup sync failed: sync backup default with pruning: connection reset",
			wantIsErr: driverErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backupSyncFailure(tt.err, tt.prune)
			if !strings.Contains(got.Error(), tt.want) {
				t.Fatalf("backupSyncFailure(%v, %v) = %q, want %q", tt.err, tt.prune, got, tt.want)
			}
			if tt.wantIsErr != nil && !errors.Is(got, tt.wantIsErr) {
				t.Fatalf("backupSyncFailure(%v, %v) = %v, want it to wrap %v", tt.err, tt.prune, got, tt.wantIsErr)
			}
			if tt.wantIsErr != nil && errors.Is(got, errNoBackupDestination) {
				t.Fatalf("backupSyncFailure(%v, %v) = %v, must not report a missing destination", tt.err, tt.prune, got)
			}
		})
	}
}

func TestBackupSyncWithPruneOutcomeText(t *testing.T) {
	files := func(n int) *int { return &n }
	bytes := func(n int64) *int64 { return &n }
	tests := []struct {
		name    string
		outcome storage.BackupPruneOutcome
		want    string
	}{
		{
			name:    "unavailable with message",
			outcome: storage.BackupPruneOutcome{Status: storage.BackupPruneUnavailable, Message: "Dolt did not return a prune summary"},
			want:    "unavailable: Dolt did not return a prune summary",
		},
		{
			name:    "unavailable without message or counts",
			outcome: storage.BackupPruneOutcome{Status: storage.BackupPruneUnavailable},
			want:    "unavailable",
		},
		{
			name:    "pruned",
			outcome: storage.BackupPruneOutcome{Status: storage.BackupPrunePruned, FilesDeleted: files(2), BytesReclaimed: bytes(471490)},
			want:    "deleted 2 file(s), reclaimed 471490 bytes",
		},
		{
			name: "partial lists every skip reason",
			outcome: storage.BackupPruneOutcome{
				Status: storage.BackupPrunePartial, FilesDeleted: files(1), BytesReclaimed: bytes(8),
				Skipped: []string{"manifest lock was busy", "destination has a recent file"},
			},
			want: "deleted 1 file(s), reclaimed 8 bytes; skipped manifest lock was busy; skipped destination has a recent file",
		},
		{
			name:    "counts missing falls back to status",
			outcome: storage.BackupPruneOutcome{Status: storage.BackupPruneSkipped, FilesDeleted: files(0)},
			want:    "skipped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBackupPruneOutcome(tt.outcome); got != tt.want {
				t.Fatalf("formatBackupPruneOutcome(%+v) = %q, want %q", tt.outcome, got, tt.want)
			}
		})
	}
}

// TestReportBackupSyncResultPruneOnlyWhenRequested pins the shared sync
// renderer's contract for both routes: without --prune-with-grace-period the
// JSON is exactly {synced, duration} (plus schema_version) and text mode prints
// no "Prune outcome:" line; with it, the prune object and line appear.
func TestReportBackupSyncResultPruneOnlyWhenRequested(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	t.Setenv("BD_JSON_ENVELOPE", "")

	deleted := 1
	outcome := storage.BackupPruneOutcome{Status: storage.BackupPrunePruned, FilesDeleted: &deleted}
	for _, tt := range []struct {
		name  string
		json  bool
		prune bool
	}{
		{name: "json without prune", json: true, prune: false},
		{name: "json with prune", json: true, prune: true},
		{name: "text without prune", json: false, prune: false},
		{name: "text with prune", json: false, prune: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jsonOutput = tt.json
			out := captureStdout(t, func() error {
				return reportBackupSyncResult(1500*time.Millisecond, tt.prune, outcome)
			})

			if !tt.json {
				if !strings.Contains(out, "Backup synced in 1.5s") {
					t.Fatalf("text output missing sync line: %q", out)
				}
				if got := strings.Contains(out, "Prune outcome:"); got != tt.prune {
					t.Fatalf("text output has Prune outcome line = %v, want %v: %q", got, tt.prune, out)
				}
				return
			}

			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("output is not a JSON object: %v\n%s", err, out)
			}
			delete(result, "schema_version")
			want := []string{"duration", "synced"}
			if tt.prune {
				want = append(want, "prune")
			}
			if len(result) != len(want) {
				t.Fatalf("JSON keys = %v, want exactly %v", result, want)
			}
			for _, key := range want {
				if _, ok := result[key]; !ok {
					t.Fatalf("JSON missing %q: %s", key, out)
				}
			}
			if tt.prune {
				var got storage.BackupPruneOutcome
				if err := json.Unmarshal(result["prune"], &got); err != nil {
					t.Fatalf("prune is not a BackupPruneOutcome: %v", err)
				}
				if got.Status != storage.BackupPrunePruned || got.FilesDeleted == nil || *got.FilesDeleted != 1 {
					t.Fatalf("prune = %+v, want status pruned with 1 file deleted", got)
				}
			}
		})
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
