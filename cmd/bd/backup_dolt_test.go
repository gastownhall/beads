package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
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
			// s3:// used to go through filepath.Abs, which also collapsed the
			// "//" inside the endpoint query.
			name:      "s3:// URL with endpoint query passes through byte-for-byte",
			input:     "s3://bucket/beads?endpoint=https://minio.local&region=auto&path-style=true",
			wantExact: "s3://bucket/beads?endpoint=https://minio.local&region=auto&path-style=true",
		},
		{
			// #6227: used to become file://$PWD/az:/container/beads.
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

// backupAddCall is one BackupAdd registration seen by recordingInitBackupStore.
type backupAddCall struct{ name, url string }

// recordingInitBackupStore records the destinations `bd backup init`
// registers on the direct path, so a test can see the exact URL handed to
// Dolt instead of only the command's own output.
type recordingInitBackupStore struct {
	failingBackupStore
	adds []backupAddCall
}

func (r *recordingInitBackupStore) BackupAdd(_ context.Context, name, url string) error {
	r.adds = append(r.adds, backupAddCall{name: name, url: url})
	return nil
}

// sqlmockMaintenanceProvider is a proxied provider stand-in whose RunNonTx
// hands the callback a real *sql.Conn from a sqlmock database, so a test can
// pin the exact SQL arguments the proxied route sends to the server.
type sqlmockMaintenanceProvider struct {
	uow.UnitOfWorkProvider
	db   *sql.DB
	runs int
}

func (p *sqlmockMaintenanceProvider) RunNonTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	p.runs++
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, conn)
}

func (p *sqlmockMaintenanceProvider) Close(context.Context) error { return nil }

// prepareBackupInitTest isolates a workspace for driving `bd backup init`'s
// RunE directly and restores every global the command reads or writes.
// Cannot be used from parallel tests.
func prepareBackupInitTest(t *testing.T, asJSON bool) string {
	t.Helper()
	t.Chdir(t.TempDir())
	beadsDir := prepareBackupStatusTest(t)

	oldStore, oldProvider := store, uowProvider
	oldRootCtx, oldWrote := rootCtx, commandDidWrite.Load()
	oldReadonly := readonlyMode
	t.Cleanup(func() {
		store, uowProvider = oldStore, oldProvider
		rootCtx = oldRootCtx
		commandDidWrite.Store(oldWrote)
		readonlyMode = oldReadonly
	})
	rootCtx = context.Background()
	readonlyMode = false
	commandDidWrite.Store(false)
	jsonOutput = asJSON
	return beadsDir
}

// runBackupInitForTest runs `bd backup init <arg>`'s RunE and returns what it
// printed on stdout and stderr.
func runBackupInitForTest(t *testing.T, arg string) (string, string, error) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	captureDir := t.TempDir()
	stdoutFile, err := os.Create(filepath.Join(captureDir, "stdout"))
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	stderrFile, err := os.Create(filepath.Join(captureDir, "stderr"))
	if err != nil {
		_ = stdoutFile.Close()
		t.Fatalf("create stderr capture: %v", err)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	var runErr error
	func() {
		defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()
		os.Stdout, os.Stderr = stdoutFile, stderrFile
		runErr = backupInitCmd.RunE(backupInitCmd, []string{arg})
	}()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	stdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	stderr, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	return string(stdout), string(stderr), runErr
}

// assertBackupInitConfigured checks the saved backup config and the success
// output both carry exactly wantURL.
func assertBackupInitConfigured(t *testing.T, beadsDir, wantURL string, asJSON bool, stdout string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-backup.json"))
	if err != nil {
		t.Fatalf("read saved backup config: %v", err)
	}
	var cfg doltBackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("saved backup config is not JSON: %v\n%s", err, data)
	}
	if cfg.BackupURL != wantURL {
		t.Errorf("saved backup_url = %q, want %q", cfg.BackupURL, wantURL)
	}
	if asJSON {
		var resp struct {
			BackupURL   string `json:"backup_url"`
			Initialized bool   `json:"initialized"`
		}
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
		}
		if resp.BackupURL != wantURL || !resp.Initialized {
			t.Errorf("JSON output = %+v, want backup_url %q and initialized", resp, wantURL)
		}
		return
	}
	if want := "Backup destination configured: " + wantURL + "\n"; !strings.HasPrefix(stdout, want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout, want)
	}
}

// cloudBackupInitURLs are advertised Dolt remote schemes (remotecache's
// remote scheme list) that `bd backup init` used to rewrite into
// file://$PWD/<scheme>:/... (#6227).
var cloudBackupInitURLs = []string{
	"az://account.blob.core.windows.net/container/beads",
	"oci://registry.example/tenant/beads",
}

// TestBackupInitPreservesCloudURL pins #6227 on the direct path: an Azure or
// OCI destination reaches Dolt's BackupAdd, the saved config and the success
// output byte-for-byte instead of becoming a local file:// path.
//
// Cannot be parallel: mutates store, rootCtx, jsonOutput and stdio.
func TestBackupInitPreservesCloudURL(t *testing.T) {
	for _, url := range cloudBackupInitURLs {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", url, asJSON), func(t *testing.T) {
				beadsDir := prepareBackupInitTest(t, asJSON)
				fake := &recordingInitBackupStore{}
				store = fake

				stdout, stderr, err := runBackupInitForTest(t, url)
				if err != nil {
					t.Fatalf("backup init %s: %v\nstderr: %s", url, err, stderr)
				}
				want := []backupAddCall{{name: defaultDoltBackupName, url: url}}
				if !slices.Equal(fake.adds, want) {
					t.Errorf("BackupAdd calls = %+v, want %+v", fake.adds, want)
				}
				assertBackupInitConfigured(t, beadsDir, url, asJSON, stdout)
			})
		}
	}
}

// TestBackupInitProxiedPreservesCloudURL is the managed-local proxied twin of
// TestBackupInitPreservesCloudURL: the URL the server is asked to register in
// CALL DOLT_BACKUP('add', ...) is the one the user supplied.
//
// Cannot be parallel: mutates proxiedServerMode, uowProvider and stdio.
func TestBackupInitProxiedPreservesCloudURL(t *testing.T) {
	for _, url := range cloudBackupInitURLs {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", url, asJSON), func(t *testing.T) {
				beadsDir := prepareBackupInitTest(t, asJSON)
				proxiedServerMode = true
				if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte(`{}`), 0o600); err != nil {
					t.Fatalf("write managed-local sidecar: %v", err)
				}
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BACKUP('add', ?, ?)")).
					WithArgs(proxiedBackupTargetName, url).
					WillReturnResult(sqlmock.NewResult(0, 0))
				uowProvider = &sqlmockMaintenanceProvider{db: db}

				stdout, stderr, err := runBackupInitForTest(t, url)
				if err != nil {
					t.Fatalf("proxied backup init %s: %v\nstdout: %s\nstderr: %s", url, err, stdout, stderr)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Errorf("server registration: %v", err)
				}
				assertBackupInitConfigured(t, beadsDir, url, asJSON, stdout)
			})
		}
	}
}
