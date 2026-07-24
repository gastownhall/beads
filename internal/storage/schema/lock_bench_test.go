package schema

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// startBenchDoltServer launches a throwaway dolt sql-server, creates a fresh
// database, and migrates it to current so MigrateUpWithLock benchmarks (and
// the real-server lock tests) exercise the steady-state no-work path every bd
// command pays at open.
func startBenchDoltServer(tb testing.TB) *sql.DB {
	tb.Helper()
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		tb.Skip("dolt binary not in PATH")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("pick free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	home := tb.TempDir()
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "bench"},
		{"config", "--global", "--add", "user.email", "bench@example.com"},
	} {
		cmd := exec.Command(doltBin, args...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("dolt %v: %v: %s", args, err, out)
		}
	}

	dataDir := tb.TempDir()
	server := exec.Command(doltBin, "sql-server", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--data-dir", dataDir)
	server.Env = append(os.Environ(), "HOME="+home)
	if err := server.Start(); err != nil {
		tb.Fatalf("start dolt sql-server: %v", err)
	}
	tb.Cleanup(func() {
		_ = server.Process.Kill()
		_, _ = server.Process.Wait()
	})

	base := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port)
	const params = "?multiStatements=true&parseTime=true"
	setupDB, err := sql.Open("mysql", base+params)
	if err != nil {
		tb.Fatalf("open dolt connection: %v", err)
	}
	tb.Cleanup(func() { _ = setupDB.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := setupDB.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			tb.Fatalf("dolt sql-server did not become ready on port %d", port)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := setupDB.Exec("CREATE DATABASE benchdb"); err != nil {
		tb.Fatalf("create bench database: %v", err)
	}
	_ = setupDB.Close()

	db, err := sql.Open("mysql", base+"benchdb"+params)
	if err != nil {
		tb.Fatalf("open bench database: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })

	if _, err := MigrateUp(context.Background(), db); err != nil {
		tb.Fatalf("migrate bench database to current: %v", err)
	}
	return db
}

// BenchmarkMigrateUpWithLockCurrent measures the per-open cost of
// MigrateUpWithLock on an already-migrated database — the path every bd
// command in server mode pays at store open.
func BenchmarkMigrateUpWithLockCurrent(b *testing.B) {
	db := startBenchDoltServer(b)
	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		b.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MigrateUpWithLock(ctx, conn, "benchdb"); err != nil {
			b.Fatalf("MigrateUpWithLock: %v", err)
		}
	}
}

// BenchmarkMigrateUpWithLockCurrentParallel is the fleet shape: N concurrent
// sessions (each on its own pinned connection) opening against the same
// current database. Without the read-only fast path they all serialize on the
// per-database GET_LOCK.
func BenchmarkMigrateUpWithLockCurrentParallel(b *testing.B) {
	db := startBenchDoltServer(b)
	db.SetMaxOpenConns(64)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		conn, err := db.Conn(ctx)
		if err != nil {
			b.Errorf("pin connection: %v", err)
			return
		}
		defer conn.Close()
		for pb.Next() {
			if _, err := MigrateUpWithLock(ctx, conn, "benchdb"); err != nil {
				b.Errorf("MigrateUpWithLock: %v", err)
				return
			}
		}
	})
}
