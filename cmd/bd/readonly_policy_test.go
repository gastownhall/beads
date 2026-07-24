package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func TestEffectiveRootStorePolicy(t *testing.T) {
	tests := []struct {
		name             string
		command          string
		strictReadonly   bool
		wantReadOnly     bool
		wantDisableStart bool
		wantMaintenance  bool
	}{
		{
			name:            "ordinary write command",
			command:         "create",
			wantMaintenance: true,
		},
		{
			name:            "classified read keeps compatibility maintenance",
			command:         "search",
			wantReadOnly:    true,
			wantMaintenance: true,
		},
		{
			name:             "strict readonly governs unclassified command",
			command:          "create",
			strictReadonly:   true,
			wantReadOnly:     true,
			wantDisableStart: true,
			wantMaintenance:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := effectiveRootStorePolicy(tc.command, tc.strictReadonly)
			if policy.readOnly != tc.wantReadOnly {
				t.Fatalf("readOnly = %v, want %v", policy.readOnly, tc.wantReadOnly)
			}
			if policy.disableAutoStart != tc.wantDisableStart {
				t.Fatalf("disableAutoStart = %v, want %v", policy.disableAutoStart, tc.wantDisableStart)
			}
			if policy.runMaintenance != tc.wantMaintenance {
				t.Fatalf("runMaintenance = %v, want %v", policy.runMaintenance, tc.wantMaintenance)
			}
		})
	}
}

func TestStrictReadonlyBackendSupport(t *testing.T) {
	tests := []struct {
		name string
		cfg  *configfile.Config
		want bool
	}{
		{name: "fresh embedded default", cfg: nil, want: true},
		{name: "dolt server", cfg: &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer}, want: true},
		{name: "proxied server", cfg: &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeProxiedServer}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendSupportsStrictReadonly(tc.cfg); got != tc.want {
				t.Fatalf("backendSupportsStrictReadonly() = %v, want %v", got, tc.want)
			}
		})
	}
}

type strictReadonlyPostRunStore struct {
	storage.DoltStorage
	metadataWrites int
	closeCalls     int
}

func (s *strictReadonlyPostRunStore) SetLocalMetadata(context.Context, string, string) error {
	s.metadataWrites++
	return nil
}

func (s *strictReadonlyPostRunStore) Close() error {
	s.closeCalls++
	return nil
}

func TestPersistentPostRunStrictReadonlySuppressesMaintenance(t *testing.T) {
	originalStore := store
	originalReadonly := readonlyMode
	originalProxied := proxiedServerMode
	originalRootCtx := rootCtx
	originalRootCancel := rootCancel
	originalCommandSpan := commandSpan
	originalProfileFile := profileFile
	originalTraceFile := traceFile
	storeMutex.Lock()
	originalStoreActive := storeActive
	storeMutex.Unlock()
	originalDoltAutoCommit := doltAutoCommit
	originalDidWrite := commandDidWrite.Load()
	originalDidExplicitCommit := commandDidExplicitDoltCommit
	originalDidWriteTipMetadata := commandDidWriteTipMetadata
	originalTipIDsWereNil := commandTipIDsShown == nil
	originalTipIDsShown := make(map[string]struct{}, len(commandTipIDsShown))
	for id := range commandTipIDsShown {
		originalTipIDsShown[id] = struct{}{}
	}
	originalCommit := runPostRunAutoCommit
	originalBackup := runPostRunAutoBackup
	originalExport := runPostRunAutoExport
	originalPush := runPostRunAutoPush
	t.Cleanup(func() {
		store = originalStore
		readonlyMode = originalReadonly
		proxiedServerMode = originalProxied
		rootCtx = originalRootCtx
		rootCancel = originalRootCancel
		commandSpan = originalCommandSpan
		profileFile = originalProfileFile
		traceFile = originalTraceFile
		storeMutex.Lock()
		storeActive = originalStoreActive
		storeMutex.Unlock()
		doltAutoCommit = originalDoltAutoCommit
		commandDidWrite.Store(originalDidWrite)
		commandDidExplicitDoltCommit = originalDidExplicitCommit
		commandDidWriteTipMetadata = originalDidWriteTipMetadata
		if originalTipIDsWereNil {
			commandTipIDsShown = nil
		} else {
			commandTipIDsShown = originalTipIDsShown
		}
		runPostRunAutoCommit = originalCommit
		runPostRunAutoBackup = originalBackup
		runPostRunAutoExport = originalExport
		runPostRunAutoPush = originalPush
	})

	maintenanceCalls := 0
	runPostRunAutoCommit = func(context.Context, doltAutoCommitParams) error { maintenanceCalls++; return nil }
	runPostRunAutoBackup = func(context.Context) { maintenanceCalls++ }
	runPostRunAutoExport = func(context.Context, bool) error { maintenanceCalls++; return nil }
	runPostRunAutoPush = func(context.Context) { maintenanceCalls++ }

	fake := &strictReadonlyPostRunStore{}
	store = fake
	readonlyMode = true
	proxiedServerMode = false
	rootCtx = context.Background()
	rootCancel = nil
	commandSpan = nil
	profileFile = nil
	traceFile = nil
	commandDidWrite.Store(true)
	commandDidExplicitDoltCommit = false
	commandDidWriteTipMetadata = true
	commandTipIDsShown = map[string]struct{}{"strict-readonly": {}}
	doltAutoCommit = string(doltAutoCommitOn)

	if err := rootCmd.PersistentPostRunE(&cobra.Command{Use: "create"}, nil); err != nil {
		t.Fatalf("PersistentPostRunE: %v", err)
	}
	if maintenanceCalls != 0 {
		t.Fatalf("strict readonly ran %d automatic commit/backup/export/push operation(s)", maintenanceCalls)
	}
	if fake.metadataWrites != 0 {
		t.Fatalf("strict readonly wrote %d tip metadata value(s)", fake.metadataWrites)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("store close calls = %d, want 1", fake.closeCalls)
	}
}

type readonlyTreeEntry struct {
	Mode   fs.FileMode
	SHA256 string
}

type readonlyTreeSnapshot struct {
	Exists  bool
	Entries map[string]readonlyTreeEntry
}

func snapshotReadonlyTree(t *testing.T, root string) readonlyTreeSnapshot {
	t.Helper()
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return readonlyTreeSnapshot{}
	} else if err != nil {
		t.Fatalf("stat snapshot root %s: %v", root, err)
	}

	snapshot := readonlyTreeSnapshot{Exists: true, Entries: make(map[string]readonlyTreeEntry)}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := readonlyTreeEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(target)))
		}
		snapshot.Entries[rel] = item
		return nil
	}); err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}

func readonlyCanaryEnv(home, beadsDir, circuitDir string, port int) []string {
	replace := map[string]bool{
		"HOME": true, "XDG_CONFIG_HOME": true,
		"BEADS_DIR": true, "BEADS_DB": true, "BD_DB": true,
		"BEADS_DOLT_PORT": true, "BEADS_DOLT_SERVER_PORT": true, "BEADS_DOLT_AUTO_START": true,
		"BEADS_TEST_MODE": true, "BEADS_TEST_CIRCUIT_DIR": true,
		"BD_DISABLE_METRICS": true, "BD_DISABLE_EVENT_FLUSH": true,
		"BD_OTEL_METRICS_URL": true, "BD_OTEL_LOGS_URL": true, "BD_OTEL_STDOUT": true,
	}
	env := make([]string, 0, len(os.Environ())+16)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !replace[key] {
			env = append(env, value)
		}
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "xdg"),
		"BEADS_DIR="+beadsDir,
		"BEADS_DB=", "BD_DB=",
		"BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port),
		"BEADS_DOLT_PORT="+strconv.Itoa(port),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BEADS_TEST_CIRCUIT_DIR="+circuitDir,
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_OTEL_METRICS_URL=", "BD_OTEL_LOGS_URL=", "BD_OTEL_STDOUT=false",
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
	)
}

func TestConfigValidateReadOnlyIsHermetic(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("BEADS_DOLT_PORT"))
	if err != nil || port <= 0 {
		t.Skip("shared Dolt test server is unavailable")
	}
	circuitDir := os.Getenv("BEADS_TEST_CIRCUIT_DIR")
	if !filepath.IsAbs(circuitDir) {
		t.Fatalf("suite circuit directory is not isolated: %q", circuitDir)
	}

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	doltPath := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltPath, 0o755); err != nil {
		t.Fatalf("create isolated Dolt path: %v", err)
	}
	database := fmt.Sprintf("readonly_canary_%d_%d", os.Getpid(), time.Now().UnixNano())
	cfg := &configfile.Config{
		Database:       "dolt",
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltDatabase:   database,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save isolated metadata: %v", err)
	}
	store, err := dolt.New(context.Background(), &dolt.Config{
		Path:            doltPath,
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create isolated database: %v", err)
	}
	if err := store.SetConfig(context.Background(), "issue_prefix", "readonly"); err != nil {
		_ = store.Close()
		t.Fatalf("seed isolated database config: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close isolated database: %v", err)
	}
	t.Cleanup(func() {
		db, openErr := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
		if openErr == nil {
			defer db.Close()
			_, _ = db.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", database)) //nolint:gosec // generated test name
		}
	})
	// federation.remote is required for `bd config validate` to pass since
	// the JSONL-removal change; without it the canary fails on validation
	// rather than exercising the hermeticity contract.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: readonly\ndolt.auto-start: false\nfederation:\n  remote: https://github.com/example/beads-remote\n"), 0o644); err != nil {
		t.Fatalf("write isolated config: %v", err)
	}

	beforeBeads := snapshotReadonlyTree(t, beadsDir)
	beforeCircuit := snapshotReadonlyTree(t, circuitDir)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "xdg"), 0o755); err != nil {
		t.Fatalf("create isolated XDG home: %v", err)
	}
	// The canary must execute the current worktree source, never a caller-provided
	// prebuilt binary that may predate this candidate.
	t.Setenv("BEADS_TEST_BD_BINARY", "")
	bd := buildBDForTest(t)
	cmd := exec.Command(bd, "config", "validate", "--readonly")
	cmd.Dir = repoDir
	cmd.Env = readonlyCanaryEnv(home, beadsDir, circuitDir, port)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd config validate --readonly: %v\n%s", err, output)
	}

	afterBeads := snapshotReadonlyTree(t, beadsDir)
	if !reflect.DeepEqual(afterBeads, beforeBeads) {
		t.Fatalf("target .beads changed\nbefore: %#v\nafter:  %#v\noutput: %s", beforeBeads, afterBeads, output)
	}
	afterCircuit := snapshotReadonlyTree(t, circuitDir)
	if !reflect.DeepEqual(afterCircuit, beforeCircuit) {
		t.Fatalf("test circuit state changed\nbefore: %#v\nafter:  %#v", beforeCircuit, afterCircuit)
	}
	for _, artifact := range []string{".local_version", "dolt-server.port", "dolt-server.pid", "dolt-server.log"} {
		if _, err := os.Lstat(filepath.Join(beadsDir, artifact)); !os.IsNotExist(err) {
			t.Fatalf("strict readonly created server/version artifact %s (stat error: %v)", artifact, err)
		}
	}
}
