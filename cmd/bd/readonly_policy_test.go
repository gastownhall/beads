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
			cmd, _, err := rootCmd.Find([]string{tc.command})
			if err != nil || cmd == nil || cmd.Name() != tc.command {
				t.Fatalf("resolve %q in the command tree: err=%v cmd=%v", tc.command, err, cmd)
			}
			policy := effectiveRootStorePolicy(cmd, tc.strictReadonly)
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

// setBoolFlagForTest points a bool flag at value and registers the restore
// (parsed value AND Changed bit) with the test's cleanup, so a case that
// parsed a write flag cannot leak it into the next one. These tests walk the
// real command tree, so they must not run in parallel with anything else
// touching it.
func setBoolFlagForTest(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("%s has no --%s flag", cmd.Name(), name)
	}
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s on %s: %v", name, value, cmd.Name(), err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set(name, flag.DefValue)
		flag.Changed = false
	})
}

// resolveBdCommand finds a command in the real tree by its invocation path,
// so the verdicts under test see the parsed flag state Cobra would have left.
func resolveBdCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := rootCmd.Find(path)
	if err != nil || cmd == nil {
		t.Fatalf("resolve bd %v in the command tree: %v (cmd=%v)", path, err, cmd)
	}
	return cmd
}

// TestCommandReadsOnlyIsFlagAware pins the admission-parity contract of
// bd-fwx149 (GH#8264): parsed `bd ready --claim` and `bd duplicates
// --auto-merge` select, through the root policy, the same writer open
// (provider options), journal posture, eligible JSONL auto-import and
// export/push/prune maintenance that `bd update --claim` gets — while the
// plain listings, a false flag, a preview, a nested `bd mol ready`, an
// unclassified writer and strict --readonly keep exactly the policies they
// had.
func TestCommandReadsOnlyIsFlagAware(t *testing.T) {
	ready := resolveBdCommand(t, "ready")
	duplicates := resolveBdCommand(t, "duplicates")
	update := resolveBdCommand(t, "update")
	molReady := resolveBdCommand(t, "mol", "ready")

	// The JSONL auto-import eligibility reads the config layer; reset it so
	// the default import.auto=true is what the answer depends on.
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	// shouldAutoPruneEventsJournal reads the process-wide strict-readonly
	// verdict; pin it to the off state this contract is about.
	originalReadonlyMode := readonlyMode
	readonlyMode = false
	t.Cleanup(func() { readonlyMode = originalReadonlyMode })

	fakeStore := &strictReadonlyPostRunStore{}

	// posture renders one invocation's admission answers end to end, from
	// the same inputs PersistentPreRunE/PersistentPostRunE use.
	posture := func(cmd *cobra.Command, strictReadonly bool) (readOnly, useReadOnly, autoImport, journalRetention, export, push bool) {
		t.Helper()
		previewMode := isPreviewCommand(cmd)
		policy := effectiveRootStorePolicy(cmd, strictReadonly)
		useReadOnly = policy.readOnly || previewMode
		return policy.readOnly, useReadOnly,
			shouldRunAutoImportJSONL(cmd, fakeStore, useReadOnly, false, false),
			shouldAutoPruneEventsJournal(cmd),
			shouldRunPostCommandAutoExport(cmd),
			shouldRunPostCommandAutoPush(cmd)
	}

	assertWriterLikeUpdate := func(t *testing.T, label string, cmd *cobra.Command) {
		t.Helper()
		readOnly, useReadOnly, autoImport, retention, export, push := posture(cmd, false)
		_, _, updateImport, updateRetention, _, _ := posture(update, false)
		if readOnly || useReadOnly {
			t.Errorf("%s: readOnly=%t useReadOnly=%t, want the writer open update --claim gets", label, readOnly, useReadOnly)
		}
		if !autoImport {
			t.Errorf("%s: eligible JSONL auto-import = false, want true like update --claim (%t)", label, updateImport)
		}
		if !retention {
			t.Errorf("%s: events-journal retention = false, want true like update --claim (%t)", label, updateRetention)
		}
		if !export || !push {
			t.Errorf("%s: auto-export=%t auto-push=%t, want both like update --claim", label, export, push)
		}
		opts := rootProviderOptions(isPreviewCommand(cmd), useReadOnly, false)
		if len(opts) != 0 {
			t.Errorf("%s: provider options = %d, want the plain writer open (0)", label, len(opts))
		}
	}

	assertReadLikePlainList := func(t *testing.T, label string, cmd *cobra.Command) {
		t.Helper()
		readOnly, useReadOnly, autoImport, retention, export, push := posture(cmd, false)
		if !readOnly || !useReadOnly {
			t.Errorf("%s: readOnly=%t useReadOnly=%t, want the classified-read open", label, readOnly, useReadOnly)
		}
		if autoImport || retention || export || push {
			t.Errorf("%s: auto-import=%t retention=%t export=%t push=%t, want all skipped for a read", label, autoImport, retention, export, push)
		}
		opts := rootProviderOptions(isPreviewCommand(cmd), useReadOnly, false)
		if len(opts) != 1 || uow.OpensForBeadWrites(opts...) {
			t.Errorf("%s: provider options = %d (writesBeads=%t), want the single read-only open", label, len(opts), uow.OpensForBeadWrites(opts...))
		}
	}

	t.Run("write_flag_forms_match_update_claim", func(t *testing.T) {
		setBoolFlagForTest(t, ready, "claim", "true")
		assertWriterLikeUpdate(t, "ready --claim", ready)

		setBoolFlagForTest(t, duplicates, "auto-merge", "true")
		assertWriterLikeUpdate(t, "duplicates --auto-merge", duplicates)
	})

	t.Run("plain_listings_stay_reads", func(t *testing.T) {
		setBoolFlagForTest(t, ready, "claim", "false")
		assertReadLikePlainList(t, "ready", ready)

		setBoolFlagForTest(t, duplicates, "auto-merge", "false")
		assertReadLikePlainList(t, "duplicates", duplicates)
	})

	t.Run("false_flag_by_changed_bit_only_is_still_a_read", func(t *testing.T) {
		setBoolFlagForTest(t, ready, "claim", "false")
		ready.Flags().Lookup("claim").Changed = true
		assertReadLikePlainList(t, "ready --claim=false", ready)
	})

	t.Run("merge_preview_stays_a_read", func(t *testing.T) {
		setBoolFlagForTest(t, duplicates, "auto-merge", "true")
		setBoolFlagForTest(t, duplicates, "dry-run", "true")
		assertReadLikePlainList(t, "duplicates --auto-merge --dry-run", duplicates)
	})

	t.Run("nested_mol_ready_never_answers_for_bd_ready", func(t *testing.T) {
		if commandRegistryPath(molReady) != "mol ready" {
			t.Fatalf("commandRegistryPath(mol ready) = %q", commandRegistryPath(molReady))
		}
		// The write flag lives on the top-level ready; mol's is a different
		// command object the path key must never match.
		setBoolFlagForTest(t, ready, "claim", "true")
		if !commandReadsOnly(molReady) {
			t.Errorf("bd mol ready became a writer because bd ready --claim was parsed: the registry key is the path, not the leaf")
		}
		assertReadLikePlainList(t, "mol ready", molReady)
	})

	t.Run("renamed_root_keeps_the_canonical_path", func(t *testing.T) {
		// The registry path trims the ROOT's name, whatever it is, so the
		// executable's naming cannot move the key.
		root := &cobra.Command{Use: "not-bd-at-all"}
		ready := &cobra.Command{Use: "ready"}
		ready.Flags().Bool("claim", false, "claim the first ready issue")
		mol := &cobra.Command{Use: "mol"}
		molReady := &cobra.Command{Use: "ready"}
		molReady.Flags().Bool("claim", false, "no such flag in production")
		root.AddCommand(ready, mol)
		mol.AddCommand(molReady)

		if err := ready.Flags().Set("claim", "true"); err != nil {
			t.Fatal(err)
		}
		if err := molReady.Flags().Set("claim", "true"); err != nil {
			t.Fatal(err)
		}
		if commandReadsOnly(ready) {
			t.Errorf("ready --claim under a renamed root stayed a read: the path key must not depend on the executable's name")
		}
		if !commandReadsOnly(molReady) {
			t.Errorf("mol ready under a renamed root became a writer: the nested path must stay outside the write-flag table")
		}
	})

	t.Run("unclassified_writer_unaffected", func(t *testing.T) {
		setBoolFlagForTest(t, update, "claim", "true")
		assertWriterLikeUpdate(t, "update --claim", update)
	})

	t.Run("strict_readonly_wins_over_everything", func(t *testing.T) {
		setBoolFlagForTest(t, ready, "claim", "true")
		// The retention trigger reads the process-wide strict-readonly state,
		// not the per-invocation policy, so pin it the way a --readonly run
		// leaves it.
		readonlyMode = true
		t.Cleanup(func() { readonlyMode = false })
		policy := effectiveRootStorePolicy(ready, true)
		if !policy.readOnly || !policy.disableAutoStart || policy.runMaintenance {
			t.Errorf("strict --readonly on ready --claim: readOnly=%t disableAutoStart=%t runMaintenance=%t, want the mutation-free posture", policy.readOnly, policy.disableAutoStart, policy.runMaintenance)
		}
		if shouldAutoPruneEventsJournal(ready) {
			t.Errorf("strict --readonly must suppress journal retention, also for a write-flag form")
		}
	})

	t.Run("nil_contracts_fail_closed", func(t *testing.T) {
		if commandReadsOnly(nil) {
			t.Errorf("commandReadsOnly(nil) = true, want a writer (fail closed)")
		}
		if policy := effectiveRootStorePolicy(nil, false); policy.readOnly || !policy.runMaintenance {
			t.Errorf("effectiveRootStorePolicy(nil, false) = %+v, want the unclassified writer posture", policy)
		}
		if !shouldRunPostCommandAutoExport(nil) || !shouldRunPostCommandAutoPush(nil) {
			t.Errorf("nil maintenance verdicts: export=%t push=%t, want both to keep today's nil behavior (run)", shouldRunPostCommandAutoExport(nil), shouldRunPostCommandAutoPush(nil))
		}
		if shouldAutoPruneEventsJournal(nil) {
			t.Errorf("shouldAutoPruneEventsJournal(nil) = true, want false")
		}
	})
}
