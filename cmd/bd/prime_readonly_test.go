//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// primeLazyOpenForTest clears the active store and points BEADS_DIR at
// beadsDir, so formatMemoriesForPrime takes prime's own lazy open, and closes
// whatever that open leaves behind.
func primeLazyOpenForTest(t *testing.T, beadsDir string) {
	t.Helper()
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	oldStore, oldProxied, oldDBPath := store, proxiedServerMode, dbPath
	storeMutex.Lock()
	oldActive := storeActive
	storeActive = false
	storeMutex.Unlock()
	store, proxiedServerMode = nil, false
	t.Cleanup(func() {
		if store != nil && store != oldStore {
			_ = store.Close()
		}
		store, proxiedServerMode, dbPath = oldStore, oldProxied, oldDBPath
		storeMutex.Lock()
		storeActive = oldActive
		storeMutex.Unlock()
	})
}

// TestPrimeOpensServerStoreReadOnly pins that `bd prime` in direct server mode
// opens its lazy store read-only, and that the memories still render through
// it.
func TestPrimeOpensServerStoreReadOnly(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	seed := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "pro")
	if err := seed.SetConfig(ctx, kvPrefix+memoryPrefix+"prime-readonly", "read through a read-only store"); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	primeLazyOpenForTest(t, beadsDir)
	out := formatMemoriesForPrime(false)
	if !strings.Contains(out, "read through a read-only store") {
		t.Fatalf("prime did not render the seeded memory:\n%s", out)
	}
	ds, ok := storage.UnwrapStore(store).(*dolt.DoltStore)
	if !ok {
		t.Fatalf("prime opened %T, want *dolt.DoltStore", storage.UnwrapStore(store))
	}
	if !ds.IsReadOnly() {
		t.Fatal("prime opened a writable server store")
	}
}

// TestEmbeddedPrimeOpensReadOnlyStore pins that `bd prime` on embedded Dolt
// opens its lazy store with OpenReadOnly, whose write transactions fail with
// ErrReadOnly, rather than OpenForReadOnlyCommand, which is writable
// underneath. The memories still render, and a workspace whose database is
// missing gets the "storage unavailable" banner instead of a newly created
// database.
func TestEmbeddedPrimeOpensReadOnlyStore(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	ctx := context.Background()

	writeEmbeddedMetadata := func(t *testing.T, beadsDir string) {
		t.Helper()
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", beadsDir, err)
		}
		if err := (&configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads",
		}).Save(beadsDir); err != nil {
			t.Fatalf("save metadata: %v", err)
		}
	}

	t.Run("memories render through a write-refusing store", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeEmbeddedMetadata(t, beadsDir)
		seed, err := embeddeddolt.Open(ctx, beadsDir, "beads", "main")
		if err != nil {
			t.Fatalf("create embedded database: %v", err)
		}
		err = seed.SetConfig(ctx, kvPrefix+memoryPrefix+"prime-readonly", "read through a read-only store")
		if closeErr := seed.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("seed memory: %v", err)
		}

		primeLazyOpenForTest(t, beadsDir)
		out := formatMemoriesForPrime(false)
		if !strings.Contains(out, "read through a read-only store") {
			t.Fatalf("prime did not render the seeded memory:\n%s", out)
		}
		if _, ok := storage.UnwrapStore(store).(*embeddeddolt.EmbeddedDoltStore); !ok {
			t.Fatalf("prime opened %T, want *embeddeddolt.EmbeddedDoltStore", storage.UnwrapStore(store))
		}
		if err := store.SetLocalMetadata(ctx, "prime_readonly_probe", "x"); !errors.Is(err, embeddeddolt.ErrReadOnly) {
			t.Fatalf("a write through prime's store = %v, want embeddeddolt.ErrReadOnly", err)
		}
	})

	t.Run("a missing database is reported, not created", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeEmbeddedMetadata(t, beadsDir)

		primeLazyOpenForTest(t, beadsDir)
		out := formatMemoriesForPrime(false)
		if !strings.Contains(out, "Skipped: beads storage unavailable") {
			t.Fatalf("prime did not report the missing database:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(err) {
			t.Fatalf("prime created an embedded database directory: stat err = %v", err)
		}
	})
}

// runBDForTest runs bd in dir with bdEnv plus extraEnv and fails the test on a
// non-zero exit.
func runBDForTest(t *testing.T, bd, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestEmbeddedPrimeLeavesDueAutoBackupToNextWrite pins that `bd prime`, whose
// embedded store refuses writes, does not attempt the post-command auto-backup.
// A failed attempt still records its time in backup_state.json, and the
// interval throttle would then skip the backup of the next command that could
// take one.
func TestEmbeddedPrimeLeavesDueAutoBackupToNextWrite(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pbk")
	bdCreate(t, bd, dir, "issue before prime", "--type", "task")

	stateDir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", stateDir, err)
	}
	statePath := filepath.Join(stateDir, "backup_state.json")
	seeded := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	data, err := json.Marshal(backupState{Timestamp: seeded})
	if err != nil {
		t.Fatalf("marshal backup state: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("seed backup state: %v", err)
	}
	readState := func(t *testing.T) backupState {
		t.Helper()
		raw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read backup state: %v", err)
		}
		var st backupState
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("parse backup state %s: %v", raw, err)
		}
		return st
	}

	autoBackup := []string{"BD_BACKUP_ENABLED=true"}
	out := runBDForTest(t, bd, dir, autoBackup, "prime", "--memories-only")
	if strings.Contains(out, "storage unavailable") {
		t.Fatalf("prime did not open its store, so this test exercises nothing:\n%s", out)
	}
	if st := readState(t); !st.Timestamp.Equal(seeded) || st.LastDoltCommit != "" {
		t.Fatalf("bd prime changed the backup state: timestamp %s (seeded %s), last commit %q",
			st.Timestamp, seeded, st.LastDoltCommit)
	}

	runBDForTest(t, bd, dir, autoBackup, "create", "issue after prime", "--silent")
	st := readState(t)
	if st.LastDoltCommit == "" || !st.Timestamp.After(seeded) {
		t.Fatalf("the write after bd prime did not back up: timestamp %s (seeded %s), last commit %q",
			st.Timestamp, seeded, st.LastDoltCommit)
	}
}

// TestEmbeddedReadCommandLeavesJSONLRecoveryToImport pins that a command the
// read-only path set classifies (`bd status`) does not run the empty-database
// JSONL auto-import, as `bd list` does not, and that an explicit `bd import`
// still recovers the issues.
func TestEmbeddedReadCommandLeavesJSONLRecoveryToImport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	srcDir, _, _ := bdInit(t, bd, "--prefix", "rcv")
	for _, title := range []string{"recover one", "recover two", "recover three"} {
		bdCreate(t, bd, srcDir, title, "--type", "task")
	}
	exported := filepath.Join(srcDir, "export.jsonl")
	runBDForTest(t, bd, srcDir, nil, "export", "-o", exported)
	data, err := os.ReadFile(exported)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	dstDir, dstBeadsDir, _ := bdInit(t, bd, "--prefix", "rcv")
	if err := os.WriteFile(filepath.Join(dstBeadsDir, "issues.jsonl"), data, 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	runBDForTest(t, bd, dstDir, nil, "status")
	if got := len(bdListJSON(t, bd, dstDir)); got != 0 {
		t.Fatalf("bd status imported issues.jsonl: %d issues after it, want 0", got)
	}

	runBDForTest(t, bd, dstDir, nil, "import")
	if got := len(bdListJSON(t, bd, dstDir)); got != 3 {
		t.Fatalf("bd import recovered %d issues, want 3", got)
	}
}
