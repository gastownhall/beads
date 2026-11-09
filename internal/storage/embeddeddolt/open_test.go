//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDSN_SpacesInPath(t *testing.T) {
	// Regression test for #2920: paths with spaces must not be
	// percent-encoded (%20) because the Dolt driver's ParseDataSource
	// uses the path as a literal filesystem path.
	dir := "/Users/bbrenner/Documents/Scripting Projects/beads/.beads/embeddeddolt"
	dsn := buildDSN(dir, "beads")

	if !strings.HasPrefix(dsn, "file://") {
		t.Fatalf("DSN should start with file://, got %q", dsn)
	}

	if strings.Contains(dsn, "%20") {
		t.Errorf("DSN must not percent-encode spaces; got %q", dsn)
	}

	if !strings.Contains(dsn, "Scripting Projects") {
		t.Errorf("DSN must preserve literal spaces in path; got %q", dsn)
	}

	// Verify the path portion is between "file://" and "?"
	afterScheme := strings.TrimPrefix(dsn, "file://")
	qIdx := strings.Index(afterScheme, "?")
	if qIdx == -1 {
		t.Fatalf("DSN missing query parameters: %q", dsn)
	}
	pathPortion := afterScheme[:qIdx]
	if pathPortion != dir {
		t.Errorf("path portion = %q, want %q", pathPortion, dir)
	}
}

func TestBuildDSN_NoDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "")
	if strings.Contains(dsn, "database=") {
		t.Errorf("DSN should not contain database param when empty; got %q", dsn)
	}
}

func TestBuildDSN_WithDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "mydb")
	if !strings.Contains(dsn, "database=mydb") {
		t.Errorf("DSN should contain database=mydb; got %q", dsn)
	}
}

// openWaitResult is what a background OpenSQL call returned.
type openWaitResult struct {
	db      *sql.DB
	cleanup func() error
	err     error
	elapsed time.Duration
}

// initOpenWaitDB creates an initialized embedded database "testdb" under a
// fresh directory and returns the directory. Opening an empty directory with
// no database holds no journal lock, so contention needs a real database.
func initOpenWaitDB(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "embeddeddolt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, cleanup, err := OpenSQL(context.Background(), dir, "", "")
	if err != nil {
		t.Fatalf("init OpenSQL: %v", err)
	}
	if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		_ = cleanup()
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("init cleanup: %v", err)
	}
	return dir
}

// holdOpenWaitDB opens testdb under dir and keeps it open, as a concurrent
// bd process would. release closes the holder once; it is also registered as
// a test cleanup.
func holdOpenWaitDB(t *testing.T, dir string) (holder *sql.DB, release func()) {
	t.Helper()
	db, cleanup, err := OpenSQL(context.Background(), dir, "testdb", "")
	if err != nil {
		t.Fatalf("holder OpenSQL: %v", err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		if err := cleanup(); err != nil {
			t.Errorf("holder cleanup: %v", err)
		}
	}
	t.Cleanup(release)
	return db, release
}

// startOpenWait runs OpenSQL on testdb under dir in the background. The
// returned join cancels ctx's cancel func and waits for the call to return,
// closing any handle it opened; it is registered as a test cleanup after the
// holder's, so it runs first.
func startOpenWait(t *testing.T, ctx context.Context, cancel context.CancelFunc, dir string) (<-chan openWaitResult, func()) {
	t.Helper()
	results := make(chan openWaitResult, 1)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		db, cleanup, err := OpenSQL(ctx, dir, "testdb", "")
		results <- openWaitResult{db: db, cleanup: cleanup, err: err, elapsed: time.Since(start)}
	}()
	joined := false
	join := func() {
		if joined {
			return
		}
		joined = true
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("background OpenSQL did not return 30s after cancellation")
			return
		}
		select {
		case r := <-results:
			if r.err == nil && r.cleanup != nil {
				_ = r.cleanup()
			}
		default:
		}
	}
	t.Cleanup(join)
	return results, join
}

func skipUnlessEmbeddedOpenWait(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

// TestOpenSQLWaitBudget pins how long OpenSQL waits for a database another
// handle holds (bd-2nijqd, PR #4284).
func TestOpenSQLWaitBudget(t *testing.T) {
	skipUnlessEmbeddedOpenWait(t)

	t.Run("held_database_times_out", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1s")
		dir := initOpenWaitDB(t)
		holder, release := holdOpenWaitDB(t, dir)

		ctx, cancel := context.WithCancel(context.Background())
		results, join := startOpenWait(t, ctx, cancel, dir)

		var r openWaitResult
		select {
		case r = <-results:
		case <-time.After(10 * time.Second):
			join()
			t.Fatalf("OpenSQL without a deadline still waiting on a held database after 10s")
		}
		if r.err == nil {
			_ = r.cleanup()
			t.Fatalf("OpenSQL succeeded after %v while the database was held", r.elapsed)
		}
		t.Logf("OpenSQL failed after %v: %v", r.elapsed, r.err)
		if !errors.Is(r.err, ErrOpenTimeout) {
			t.Fatalf("OpenSQL error = %v, want ErrOpenTimeout", r.err)
		}
		for _, want := range []string{dir, "BEADS_EMBEDDED_OPEN_TIMEOUT"} {
			if !strings.Contains(r.err.Error(), want) {
				t.Errorf("OpenSQL error %q does not name %q", r.err, want)
			}
		}

		var n int
		if err := holder.QueryRow("SELECT 1").Scan(&n); err != nil {
			t.Fatalf("holder query after the waiter gave up: %v", err)
		}
		release()

		db, cleanup, err := OpenSQL(context.Background(), dir, "testdb", "")
		if err != nil {
			t.Fatalf("reopen after holder cleanup: %v", err)
		}
		defer func() { _ = cleanup() }()
		if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
			t.Fatalf("query after reopen: %v", err)
		}
	})
}

// TestOpenWaitBudget pins how BEADS_EMBEDDED_OPEN_TIMEOUT resolves: positive
// Go durations or bare seconds, and the 30s default for anything else.
func TestOpenWaitBudget(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "")
		if err := os.Unsetenv("BEADS_EMBEDDED_OPEN_TIMEOUT"); err != nil {
			t.Fatalf("unsetenv: %v", err)
		}
		if got := openWaitBudget(); got != 30*time.Second {
			t.Errorf("openWaitBudget() with the variable unset = %v, want 30s", got)
		}
	})

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 30 * time.Second},
		{"   ", 30 * time.Second},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"1ms", time.Millisecond},
		{"90", 90 * time.Second},
		{" 45s ", 45 * time.Second},
		{"\t90\n", 90 * time.Second},
		{"0", 30 * time.Second},
		{"0s", 30 * time.Second},
		{"-5s", 30 * time.Second},
		{"-5", 30 * time.Second},
		{"soon", 30 * time.Second},
		{"5 s", 30 * time.Second},
		{"99999999999999999999", 30 * time.Second},
		{"99999999999999999999h", 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.raw, " ", "_"), func(t *testing.T) {
			t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", tc.raw)
			if got := openWaitBudget(); got != tc.want {
				t.Errorf("openWaitBudget() with %q = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
