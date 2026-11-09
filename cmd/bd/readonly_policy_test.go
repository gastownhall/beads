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
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

func TestEffectiveRootStorePolicy(t *testing.T) {
	tests := []struct {
		name             string
		command          []string
		strictReadonly   bool
		wantReadOnly     bool
		wantDisableStart bool
		wantMaintenance  bool
	}{
		{
			name:            "ordinary write command",
			command:         []string{"create"},
			wantMaintenance: true,
		},
		{
			name:            "classified read keeps compatibility maintenance",
			command:         []string{"search"},
			wantReadOnly:    true,
			wantMaintenance: true,
		},
		{
			name:             "strict readonly governs unclassified command",
			command:          []string{"create"},
			strictReadonly:   true,
			wantReadOnly:     true,
			wantDisableStart: true,
			wantMaintenance:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := effectiveRootStorePolicy(parseInvocationForTest(t, tc.command...), tc.strictReadonly)
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

// parseInvocationForTest resolves args against the real command tree and
// parses the leaf's flags the way cobra does before the root pre-run, then
// restores every flag it set: the commands are package-level singletons.
func parseInvocationForTest(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	return parseInvocationInTreeForTest(t, rootCmd, args...)
}

func parseInvocationInTreeForTest(t *testing.T, root *cobra.Command, args ...string) *cobra.Command {
	t.Helper()
	target, rest, err := root.Find(args)
	if err != nil {
		t.Fatalf("resolve %v: %v", args, err)
	}
	t.Cleanup(func() {
		target.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	})
	if err := target.ParseFlags(rest); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return target
}

// TestCommandReadsOnlyIsFlagAware pins the per-invocation classification of
// bd-fwx149 (GH#8264): `bd ready --claim` and `bd duplicates --auto-merge`
// write, so they are admitted exactly like `bd update --claim` — writer open,
// identity check, journal admission and JSONL import — while the plain
// listings, false flags, previews, `bd mol ready` and strict readonly keep the
// policies they had.
func TestCommandReadsOnlyIsFlagAware(t *testing.T) {
	initConfigForTest(t)
	config.Set("import.auto", true)

	// A tree whose root is not named bd: classification keys on the command
	// path below the root, never on the executable's name.
	renamed := &cobra.Command{Use: "beads"}
	renamedReady := &cobra.Command{Use: "ready", Run: func(*cobra.Command, []string) {}}
	renamedReady.Flags().Bool("claim", false, "")
	renamed.AddCommand(renamedReady)

	tests := []struct {
		name       string
		root       *cobra.Command
		args       []string
		strict     bool
		wantWriter bool
	}{
		{name: "update --claim is the writer control", args: []string{"update", "x-1", "--claim"}, wantWriter: true},
		{name: "plain ready reads", args: []string{"ready"}},
		{name: "ready --claim writes", args: []string{"ready", "--claim"}, wantWriter: true},
		{name: "ready --claim=false reads", args: []string{"ready", "--claim=false"}},
		{name: "ready with an unrelated flag reads", args: []string{"ready", "--limit", "3"}},
		{name: "plain duplicates reads", args: []string{"duplicates"}},
		{name: "duplicates --auto-merge writes", args: []string{"duplicates", "--auto-merge"}, wantWriter: true},
		{name: "duplicates --auto-merge=false reads", args: []string{"duplicates", "--auto-merge=false"}},
		{name: "duplicates --auto-merge --dry-run is a preview", args: []string{"duplicates", "--auto-merge", "--dry-run"}},
		{name: "duplicates --auto-merge --dry-run=false writes", args: []string{"duplicates", "--auto-merge", "--dry-run=false"}, wantWriter: true},
		{name: "mol ready is not bd ready", args: []string{"mol", "ready"}},
		{name: "renamed root ready reads", root: renamed, args: []string{"ready"}},
		{name: "renamed root ready --claim writes", root: renamed, args: []string{"ready", "--claim"}, wantWriter: true},
		{name: "strict readonly still governs ready --claim", args: []string{"ready", "--claim"}, strict: true},
		{name: "strict readonly still governs update --claim", args: []string{"update", "x-1", "--claim"}, strict: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root
			if root == nil {
				root = rootCmd
			}
			cmd := parseInvocationInTreeForTest(t, root, tc.args...)
			if !tc.strict {
				if got := commandReadsOnly(cmd); got == tc.wantWriter {
					t.Errorf("commandReadsOnly(%v) = %t, want %t", tc.args, got, !tc.wantWriter)
				}
			}

			// The root pre-run's decision, as it makes it.
			previewMode := isPreviewCommand(cmd)
			policy := effectiveRootStorePolicy(cmd, tc.strict)
			useReadOnly := policy.readOnly || previewMode
			if wantReadOnly := !tc.wantWriter; useReadOnly != wantReadOnly {
				t.Errorf("useReadOnly(%v) = %t, want %t: the open, freeze gate, identity check and journal admission all follow it", tc.args, useReadOnly, wantReadOnly)
			}
			// Classification never moves auto-start or maintenance; only
			// strict readonly does.
			if policy.disableAutoStart != tc.strict || policy.runMaintenance == tc.strict {
				t.Errorf("policy(%v, strict=%t) = %+v: auto-start/maintenance must follow strict readonly alone", tc.args, tc.strict, policy)
			}
			// Proxied open posture: a writer opens for bead writes, so it takes
			// the migration gate's refusal instead of the read-through.
			if got := uow.OpensForBeadWrites(rootProviderOptions(previewMode, useReadOnly, false)...); got != tc.wantWriter {
				t.Errorf("proxied provider opens for bead writes = %t, want %t", got, tc.wantWriter)
			}
			// Empty-database JSONL recovery runs for writers only.
			if got := shouldRunAutoImportJSONL(cmd, &fakeFallbackStore{}, useReadOnly, false, false); got != tc.wantWriter {
				t.Errorf("shouldRunAutoImportJSONL = %t, want %t", got, tc.wantWriter)
			}
		})
	}

	t.Run("nil classifies nothing as a read", func(t *testing.T) {
		if commandReadsOnly(nil) || commandWritesByFlag(nil) {
			t.Fatal("a nil command must be neither a classified read nor a flag writer")
		}
		if policy := effectiveRootStorePolicy(nil, false); policy.readOnly || policy.disableAutoStart || !policy.runMaintenance {
			t.Fatalf("effectiveRootStorePolicy(nil, false) = %+v", policy)
		}
	})
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

	beforeBeads := withoutGateLockFiles(snapshotReadonlyTree(t, beadsDir))
	beforeCircuit := snapshotReadonlyTree(t, circuitDir)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "xdg"), 0o755); err != nil {
		t.Fatalf("create isolated XDG home: %v", err)
	}
	// The canary must execute the current worktree source, never a caller-provided
	// prebuilt binary that may predate this candidate. Under Bazel the injected
	// binary (//cmd/bd:bd_for_tests) is built from this source tree, and there is
	// no toolchain to build another one.
	if !bazeltest.IsBazel() {
		t.Setenv("BEADS_TEST_BD_BINARY", "")
	}
	bd := buildBDForTest(t)
	cmd := exec.Command(bd, "config", "validate", "--readonly")
	cmd.Dir = repoDir
	cmd.Env = readonlyCanaryEnv(home, beadsDir, circuitDir, port)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd config validate --readonly: %v\n%s", err, output)
	}

	// A store-opening command, readonly ones included, takes a SHARED
	// workspace gate (cmd/bd/workspace_gate.go), and workspacegate creates
	// each gate's lock file beside the directory it guards on first use and
	// never removes it (.beads/dolt.gate.lock for the .beads/dolt root): an
	// empty coordination file, not database, server or version state, so
	// it is not a readonly side effect this canary guards against.
	afterBeads := withoutGateLockFiles(snapshotReadonlyTree(t, beadsDir))
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

// withoutGateLockFiles drops the physical-root gate a readonly command takes
// (workspacegate.ForPhysicalRoot(.beads/dolt): .beads/dolt.gate.lock) and its
// advisory holder-info sidecar from a snapshot. Any other new file, another
// gate file included, still trips the canary.
func withoutGateLockFiles(s readonlyTreeSnapshot) readonlyTreeSnapshot {
	for rel := range s.Entries {
		switch filepath.ToSlash(rel) {
		case "dolt.gate.lock", "dolt.gate.lock.info":
			delete(s.Entries, rel)
		}
	}
	return s
}

// TestPostCommandMaintenanceIsFlagAware pins the post-run half of bd-fwx149
// (GH#8264): a classified read invoked with a write flag pays for the same
// maintenance as any writer — auto-export, auto-push and events-journal
// retention — while the plain listings and the --dry-run preview stay out of it.
func TestPostCommandMaintenanceIsFlagAware(t *testing.T) {
	originalReadonly := readonlyMode
	t.Cleanup(func() { readonlyMode = originalReadonly })
	readonlyMode = false

	tests := []struct {
		name         string
		command      []string
		export       bool
		push         bool
		pruneJournal bool
	}{
		{name: "update --claim", command: []string{"update", "x-1", "--claim"}, export: true, push: true, pruneJournal: true},
		{name: "ready --claim", command: []string{"ready", "--claim"}, export: true, push: true, pruneJournal: true},
		{name: "duplicates --auto-merge", command: []string{"duplicates", "--auto-merge"}, export: true, push: true, pruneJournal: true},
		{name: "ready", command: []string{"ready"}},
		{name: "ready --claim=false", command: []string{"ready", "--claim=false"}},
		{name: "duplicates", command: []string{"duplicates"}},
		{name: "duplicates --auto-merge --dry-run", command: []string{"duplicates", "--auto-merge", "--dry-run"}},
		{name: "mol ready", command: []string{"mol", "ready"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := parseInvocationForTest(t, tc.command...)
			if got := shouldRunPostCommandAutoExport(cmd); got != tc.export {
				t.Errorf("shouldRunPostCommandAutoExport = %v, want %v", got, tc.export)
			}
			if got := shouldRunPostCommandAutoPush(cmd); got != tc.push {
				t.Errorf("shouldRunPostCommandAutoPush = %v, want %v", got, tc.push)
			}
			if got := shouldAutoPruneEventsJournal(cmd); got != tc.pruneJournal {
				t.Errorf("shouldAutoPruneEventsJournal = %v, want %v", got, tc.pruneJournal)
			}
		})
	}
}

// bd-fwx149 (GH#8264): `bd ready --claim` and `bd duplicates --auto-merge`
// write, so the per-invocation policy must treat them as writers (schema
// gate, identity check, JSONL import, auto-export, auto-push), while the
// plain listings stay reads.
func TestWritingFlagsOnClassifiedReadsAreWriters(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		flag string
	}{
		{name: "ready --claim", cmd: readyCmd, flag: "claim"},
		{name: "duplicates --auto-merge", cmd: duplicatesCmd, flag: "auto-merge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.cmd.Flags().Lookup(tc.flag)
			if f == nil {
				t.Fatalf("%s has no --%s flag", tc.cmd.Name(), tc.flag)
			}
			t.Cleanup(func() {
				_ = tc.cmd.Flags().Set(tc.flag, f.DefValue)
				f.Changed = false
			})

			// Plain listing stays a read.
			if !(effectiveRootStorePolicy(tc.cmd, false).readOnly || isPreviewCommand(tc.cmd)) {
				t.Fatalf("plain %s should stay a classified read", tc.cmd.Name())
			}

			if err := tc.cmd.Flags().Set(tc.flag, "true"); err != nil {
				t.Fatal(err)
			}
			// Same decision PersistentPreRunE makes for useReadOnly.
			useReadOnly := effectiveRootStorePolicy(tc.cmd, false).readOnly || isPreviewCommand(tc.cmd)
			if useReadOnly {
				t.Errorf("%s writes but opens read-only: skips the schema migrate gate, identity check and JSONL import", tc.name)
			}
			if !shouldRunPostCommandAutoExport(tc.cmd) {
				t.Errorf("%s writes but skips post-command auto-export", tc.name)
			}
		})
	}
}
