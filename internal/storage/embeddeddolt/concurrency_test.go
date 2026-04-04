//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"golang.org/x/sync/errgroup"
)

func TestConcurrencyMultiProcess(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}

	procs := envInt("BEADS_EMBEDDED_DOLT_PROCS", 10)
	iters := envInt("BEADS_EMBEDDED_DOLT_ITERS", 5)
	timeout := envDuration("BEADS_EMBEDDED_DOLT_TIMEOUT", 5*time.Minute)

	t.Logf("config: procs=%d iters=%d timeout=%s", procs, iters, timeout)

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	modRoot := mustFindModuleRoot(t)

	// All subprocesses share one directory to stress filesystem locking.
	sharedDir := filepath.Join(t.TempDir(), "embeddeddolt-test")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Initialize the embedded repo and database once, serially.
	t.Logf("initializing embedded dolt repo at %s", sharedDir)
	initDB, initCleanup, err := embeddeddolt.OpenSQL(ctx, sharedDir, "", "")
	if err != nil {
		t.Fatalf("init OpenSQL: %v", err)
	}
	if _, err := initDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		if cerr := initCleanup(); cerr != nil {
			t.Logf("cleanup: %v", cerr)
		}
		t.Fatalf("CREATE DATABASE: %v", err)
	}

	if err := initCleanup(); err != nil {
		t.Fatalf("init cleanup: %v", err)
	}
	t.Log("init complete")

	// Build the test binary that subprocesses will exec.
	// If BEADS_TEST_EMBEDDED_TEST_BINARY is set, skip the build.
	testBin := os.Getenv("BEADS_TEST_EMBEDDED_TEST_BINARY")
	if testBin != "" {
		if _, err := os.Stat(testBin); err != nil {
			t.Fatalf("BEADS_TEST_EMBEDDED_TEST_BINARY=%q not found: %v", testBin, err)
		}
		t.Logf("using pre-built test binary: %s", testBin)
	} else {
		testBin = filepath.Join(t.TempDir(), "embeddeddolt.test")
		t.Logf("building test binary: go test -c -o %s ./internal/storage/embeddeddolt/", testBin)
		build := exec.CommandContext(ctx, "go", "test",
			"-c",
			"-o", testBin,
			"./internal/storage/embeddeddolt/",
		)
		build.Dir = modRoot
		build.Env = append(os.Environ(), "CGO_ENABLED=1")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build test binary: %v\n%s", err, string(out))
		}
		t.Log("build complete")
	}

	t.Logf("launching %d subprocesses (%d iterations each)", procs, iters)
	eg, egCtx := errgroup.WithContext(ctx)

	for p := 0; p < procs; p++ {
		procN := p
		eg.Go(func() error {
			t.Logf("proc %d: starting", procN)
			cmd := exec.CommandContext(egCtx, testBin, "-test.run=^TestHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(),
				"BEADS_EMBEDDED_DOLT_HELPER=1",
				"BEADS_EMBEDDED_DOLT_DIR="+sharedDir,
				"BEADS_EMBEDDED_DOLT_ITERS="+strconv.Itoa(iters),
				"BEADS_EMBEDDED_DOLT_PROC_ID="+strconv.Itoa(procN),
				"CGO_ENABLED=1",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("proc %d failed: %v\n%s", procN, err, string(out))
			}
			if !strings.Contains(string(out), "OK") {
				return fmt.Errorf("proc %d: missing OK in output:\n%s", procN, string(out))
			}
			t.Logf("proc %d: done", procN)
			return nil
		})
	}

	t.Log("waiting for subprocesses")
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}
	t.Logf("all %d subprocesses passed", procs)
}

// TestHelperProcess is the subprocess entry point used by the multi-process
// concurrency test. It is not a real test — it exits early unless the
// BEADS_EMBEDDED_DOLT_HELPER env var is set.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("BEADS_EMBEDDED_DOLT_HELPER") != "1" {
		return
	}

	dir := os.Getenv("BEADS_EMBEDDED_DOLT_DIR")
	if dir == "" {
		t.Fatal("BEADS_EMBEDDED_DOLT_DIR not set")
	}
	iters, _ := strconv.Atoi(os.Getenv("BEADS_EMBEDDED_DOLT_ITERS"))
	if iters < 1 {
		iters = 5
	}
	procID := os.Getenv("BEADS_EMBEDDED_DOLT_PROC_ID")

	ctx := t.Context()
	database := "testdb"

	for i := 0; i < iters; i++ {
		func() {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, dir, database, "")
			if err != nil {
				t.Fatalf("OpenSQL failed: %v", err)
			}
			defer func() {
				if err := cleanup(); err != nil {
					t.Fatalf("cleanup failed: %v", err)
				}
			}()

			tag := fmt.Sprintf("proc_%s_iter_%d_pid_%d", procID, i, os.Getpid())

			// CREATE TABLE IF NOT EXISTS
			mustExec(t, db, ctx, `CREATE TABLE IF NOT EXISTS concurrency_test (
				id INT AUTO_INCREMENT PRIMARY KEY,
				tag VARCHAR(255) NOT NULL,
				value INT NOT NULL
			)`)

			// INSERT
			res := mustExec(t, db, ctx, "INSERT INTO concurrency_test (tag, value) VALUES (?, ?)", tag, i)
			rowID, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("LastInsertId: %v", err)
			}

			// SELECT — verify the inserted row.
			var gotTag string
			var gotVal int
			if err := mustQueryRow(t, db, ctx, "SELECT tag, value FROM concurrency_test WHERE id = ?", rowID).Scan(&gotTag, &gotVal); err != nil {
				t.Fatalf("scan tag/value: %v", err)
			}
			if gotTag != tag || gotVal != i {
				t.Fatalf("data mismatch: got (%q, %d), want (%q, %d)", gotTag, gotVal, tag, i)
			}

			// UPDATE
			mustExec(t, db, ctx, "UPDATE concurrency_test SET value = ? WHERE id = ?", i+1000, rowID)

			// DELETE
			mustExec(t, db, ctx, "DELETE FROM concurrency_test WHERE id = ?", rowID)

			// Verify row is gone.
			var count int
			if err := mustQueryRow(t, db, ctx, "SELECT COUNT(*) FROM concurrency_test WHERE id = ?", rowID).Scan(&count); err != nil {
				t.Fatalf("scan count: %v", err)
			}
			if count != 0 {
				t.Fatalf("row not deleted: id=%d still present", rowID)
			}
		}()
	}

	fmt.Println("OK")
}

func mustExec(t *testing.T, db *sql.DB, ctx context.Context, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s failed: %v", query, err)
	}
	return res
}

func mustQueryRow(t *testing.T, db *sql.DB, ctx context.Context, query string, args ...any) *sql.Row {
	t.Helper()
	row := db.QueryRowContext(ctx, query, args...)
	return row
}

// TestConcurrentNewQueues verifies that opening a second EmbeddedDoltStore
// on the same data directory blocks until the first is closed, rather than
// failing immediately or panicking (GH#2571).
func TestConcurrentNewQueues(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}

	ctx := t.Context()
	beadsDir := t.TempDir()

	// First store opens successfully and holds the exclusive flock.
	store1, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("first New: %v", err)
	}

	// Launch second New in a goroutine — it should block on the lock.
	type result struct {
		store *embeddeddolt.EmbeddedDoltStore
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main")
		ch <- result{s, err}
	}()

	// Give the goroutine time to start waiting, then release the first store.
	time.Sleep(200 * time.Millisecond)
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("second New returned early with error: %v", r.err)
		}
		t.Fatal("second New returned before first store was closed")
	default:
		// Good — still blocking.
	}

	store1.Close()

	// The second New should now succeed.
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("second New failed after first close: %v", r.err)
		}
		r.store.Close()
		t.Log("second New succeeded after first store closed")
	case <-time.After(10 * time.Second):
		t.Fatal("second New did not complete within 10s after first store closed")
	}
}

// TestWaitLockBlocksAndSucceeds verifies that WaitLock blocks until the lock
// is released, then acquires it successfully.
func TestWaitLockBlocksAndSucceeds(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}

	dataDir := filepath.Join(t.TempDir(), "embeddeddolt")

	// Acquire with TryLock first.
	lock1, err := embeddeddolt.TryLock(dataDir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}

	ctx := t.Context()
	type result struct {
		lock *embeddeddolt.Lock
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		l, err := embeddeddolt.WaitLock(ctx, dataDir)
		ch <- result{l, err}
	}()

	// WaitLock should be blocking.
	time.Sleep(200 * time.Millisecond)
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("WaitLock returned early with error: %v", r.err)
		}
		t.Fatal("WaitLock returned before first lock was released")
	default:
	}

	// Release first lock.
	lock1.Unlock()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("WaitLock failed after unlock: %v", r.err)
		}
		r.lock.Unlock()
		t.Log("WaitLock succeeded after first lock released")
	case <-time.After(10 * time.Second):
		t.Fatal("WaitLock did not complete within 10s after unlock")
	}
}

// TestWaitLockRespectsContextCancellation verifies that WaitLock returns a
// context error when the context is cancelled while waiting.
func TestWaitLockRespectsContextCancellation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}

	dataDir := filepath.Join(t.TempDir(), "embeddeddolt")

	// Hold the lock.
	lock1, err := embeddeddolt.TryLock(dataDir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer lock1.Unlock()

	// WaitLock with a short timeout should fail with context error.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err = embeddeddolt.WaitLock(ctx, dataDir)
	if err == nil {
		t.Fatal("WaitLock succeeded; expected context deadline error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got: %v", err)
	}
	t.Logf("WaitLock correctly returned: %v", err)
}

// TestWithLockBypassesDoubleLock verifies that WithLock allows the caller to
// supply a pre-acquired lock so New does not attempt a second flock (which
// would fail on macOS where flock is per-fd, not per-process).
func TestWithLockBypassesDoubleLock(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}

	ctx := t.Context()
	beadsDir := t.TempDir()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Acquire the lock externally (as bd init does).
	lock, err := embeddeddolt.TryLock(dataDir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer lock.Unlock()

	// New with WithLock must succeed without double-locking.
	store, err := embeddeddolt.New(ctx, beadsDir, "testdb", "main", embeddeddolt.WithLock(lock))
	if err != nil {
		t.Fatalf("New with WithLock: %v", err)
	}
	// Close must NOT release the caller-supplied lock.
	store.Close()

	// The lock should still be held — verify by trying to lock again (should fail).
	_, err = embeddeddolt.TryLock(dataDir)
	if err == nil {
		t.Fatal("TryLock succeeded after Close; expected lock to still be held by caller")
	}
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}

func envDuration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func mustFindModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatalf("could not find module root from %q", thisFile)
		}
		dir = next
	}
}
