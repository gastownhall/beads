// repro-dolt-hang: Compare old vs new Dolt transaction patterns under concurrent load
//
// This creates a temp Dolt database, starts a sql-server, then fires N concurrent
// goroutines each doing INSERT + DOLT_COMMIT. It runs two modes:
//
//   - "old": BEGIN -> INSERT -> DOLT_COMMIT -> tx.Commit()  (redundant COMMIT)
//   - "new": BEGIN -> INSERT -> DOLT_COMMIT                 (Tim's blessed pattern)
//
// A watchdog goroutine monitors server responsiveness throughout.
//
// Usage:
//
//	go run ./scripts/repro-dolt-hang [workers] [ops-per-worker] [mode]
//	go run ./scripts/repro-dolt-hang 20 10 old    # old pattern with explicit Commit
//	go run ./scripts/repro-dolt-hang 20 10 new    # new pattern without Commit
//	go run ./scripts/repro-dolt-hang 20 10 both   # run both and compare (default)
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultWorkers      = 20
	defaultOpsPerWorker = 10
	serverPort          = 13399
	watchdogInterval    = 2 * time.Second
	watchdogTimeout     = 5 * time.Second
	workerQueryTimeout  = 30 * time.Second
	overallTimeout      = 5 * time.Minute
)

type result struct {
	workerID int
	opNum    int
	ok       bool
	latency  time.Duration
	err      string
}

type runStats struct {
	mode         string
	duration     time.Duration
	totalOps     int
	successes    int
	failures     int
	maxLatency   time.Duration
	unresponsive int32
	failSamples  []result
}

func main() {
	workers := defaultWorkers
	opsPerWorker := defaultOpsPerWorker
	mode := "both"
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil {
			workers = n
		}
	}
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			opsPerWorker = n
		}
	}
	if len(os.Args) > 3 {
		mode = os.Args[3]
	}

	fmt.Println("=== Dolt Transaction Pattern Comparison ===")
	out, _ := exec.Command("dolt", "version").Output()
	fmt.Printf("Dolt: %s", out)
	fmt.Printf("Workers: %d, Ops/worker: %d, Total: %d\n", workers, opsPerWorker, workers*opsPerWorker)
	fmt.Printf("Mode: %s\n", mode)
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nInterrupted, cleaning up...")
		cancel()
	}()

	var allStats []runStats

	modes := []string{mode}
	if mode == "both" {
		modes = []string{"old", "new"}
	}

	for _, m := range modes {
		stats := runMode(ctx, m, workers, opsPerWorker)
		allStats = append(allStats, stats)
	}

	// Summary
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("=== COMPARISON SUMMARY ===")
	fmt.Println("========================================")
	for _, s := range allStats {
		fmt.Printf("\n[%s] %s pattern (BEGIN -> INSERT -> DOLT_COMMIT%s)\n",
			s.mode, s.mode, map[string]string{"old": " -> tx.Commit()", "new": ""}[s.mode])
		fmt.Printf("  Duration:     %s\n", s.duration.Round(time.Millisecond))
		fmt.Printf("  Success:      %d/%d (%.1f%%)\n", s.successes, s.totalOps,
			float64(s.successes)*100/float64(s.totalOps))
		fmt.Printf("  Max latency:  %s\n", s.maxLatency.Round(time.Millisecond))
		fmt.Printf("  Unresponsive: %d watchdog events\n", s.unresponsive)
		if len(s.failSamples) > 0 {
			fmt.Printf("  Sample errors:\n")
			for i, f := range s.failSamples {
				if i >= 3 {
					break
				}
				fmt.Printf("    w%d/op%d: %s (%s)\n", f.workerID, f.opNum, f.err, f.latency.Round(time.Millisecond))
			}
		}
	}

	fmt.Println()
	anyHang := false
	for _, s := range allStats {
		if s.unresponsive > 0 {
			anyHang = true
		}
	}
	if anyHang {
		fmt.Println("*** SERVER HANG DETECTED ***")
		os.Exit(1)
	}
}

func runMode(ctx context.Context, mode string, workers, opsPerWorker int) runStats {
	fmt.Printf("--- Running [%s] pattern ---\n", mode)

	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("dolt-repro-%s-*", mode))
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := setupDoltDB(ctx, tmpDir); err != nil {
		log.Fatalf("Failed to setup Dolt DB: %v", err)
	}

	serverCmd, err := startDoltServer(tmpDir)
	if err != nil {
		log.Fatalf("Failed to start Dolt server: %v", err)
	}
	defer func() {
		_ = serverCmd.Process.Signal(syscall.SIGTERM)
		_ = serverCmd.Wait()
	}()

	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/repro_db", serverPort)
	if err := waitForServer(ctx, dsn); err != nil {
		log.Fatalf("Server not ready: %v", err)
	}
	fmt.Println("  Server ready.")

	var unresponsiveCount atomic.Int32
	watchdogCtx, watchdogCancel := context.WithCancel(ctx)
	defer watchdogCancel()
	go watchdog(watchdogCtx, dsn, &unresponsiveCount)

	useExplicitCommit := mode == "old"
	start := time.Now()
	results := runWorkers(ctx, dsn, workers, opsPerWorker, useExplicitCommit)
	elapsed := time.Since(start)
	watchdogCancel()

	stats := runStats{
		mode:         mode,
		duration:     elapsed,
		totalOps:     len(results),
		unresponsive: unresponsiveCount.Load(),
	}
	for _, r := range results {
		if r.ok {
			stats.successes++
		} else {
			stats.failures++
			stats.failSamples = append(stats.failSamples, r)
		}
		if r.latency > stats.maxLatency {
			stats.maxLatency = r.latency
		}
	}

	fmt.Printf("  Done: %d/%d success (%.1f%%), max latency %s, %d unresponsive events\n",
		stats.successes, stats.totalOps,
		float64(stats.successes)*100/float64(stats.totalOps),
		stats.maxLatency.Round(time.Millisecond),
		stats.unresponsive)
	fmt.Println()

	// Brief pause between runs to let server fully stop
	time.Sleep(2 * time.Second)
	return stats
}

func setupDoltDB(ctx context.Context, dir string) error {
	cmds := []struct {
		name string
		args []string
	}{
		{"dolt", []string{"init", "--name", "repro", "--email", "repro@test.com"}},
		{"dolt", []string{"sql", "-q", `CREATE DATABASE IF NOT EXISTS repro_db`}},
		{"dolt", []string{"sql", "-q", `USE repro_db; CREATE TABLE issues (
			id VARCHAR(64) PRIMARY KEY,
			title VARCHAR(255),
			status VARCHAR(32) DEFAULT 'open',
			notes TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		); CALL DOLT_ADD('.'); CALL DOLT_COMMIT('-m', 'Initial schema', '--author', 'repro <repro@test.com>')`}},
	}
	for _, c := range cmds {
		cmd := exec.CommandContext(ctx, c.name, c.args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s %v: %v\n%s", c.name, c.args, err, out)
		}
	}
	return nil
}

func startDoltServer(dir string) (*exec.Cmd, error) {
	logFile, err := os.Create(filepath.Join(dir, "server.log")) //nolint:gosec // G304: path from controlled temp dir
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", strconv.Itoa(serverPort),
		"--loglevel=warning",
	)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func waitForServer(ctx context.Context, dsn string) error {
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("server startup timeout")
		default:
		}
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			err = db.PingContext(ctx)
			_ = db.Close()
			if err == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func watchdog(ctx context.Context, dsn string, unresponsive *atomic.Int32) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		wCtx, wCancel := context.WithTimeout(ctx, watchdogTimeout)
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			unresponsive.Add(1)
			fmt.Printf("  [watchdog] %s UNRESPONSIVE (open: %v)\n", time.Now().Format("15:04:05"), err)
			wCancel()
			continue
		}
		start := time.Now()
		err = db.PingContext(wCtx)
		elapsed := time.Since(start)
		_ = db.Close()
		wCancel()

		if err != nil {
			unresponsive.Add(1)
			fmt.Printf("  [watchdog] %s UNRESPONSIVE after %s: %v\n",
				time.Now().Format("15:04:05"), elapsed.Round(time.Millisecond), err)
		}
	}
}

func runWorkers(ctx context.Context, dsn string, numWorkers, opsPerWorker int, useExplicitCommit bool) []result {
	var mu sync.Mutex
	var allResults []result
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Each worker gets its own sql.DB (mimics separate bd processes)
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				mu.Lock()
				allResults = append(allResults, result{
					workerID: workerID, ok: false, err: err.Error(),
				})
				mu.Unlock()
				return
			}
			db.SetMaxOpenConns(10)
			db.SetMaxIdleConns(5)
			db.SetConnMaxLifetime(5 * time.Minute)
			defer db.Close()

			for op := 0; op < opsPerWorker; op++ {
				r := doOperation(ctx, db, workerID, op, useExplicitCommit)
				mu.Lock()
				allResults = append(allResults, r)
				mu.Unlock()
			}
		}(w)
	}

	wg.Wait()
	return allResults
}

func doOperation(ctx context.Context, db *sql.DB, workerID, opNum int, useExplicitCommit bool) result {
	opCtx, cancel := context.WithTimeout(ctx, workerQueryTimeout)
	defer cancel()

	issueID := fmt.Sprintf("w%d-op%d-%d", workerID, opNum, time.Now().UnixNano())
	start := time.Now()

	tx, err := db.BeginTx(opCtx, nil)
	if err != nil {
		return result{workerID: workerID, opNum: opNum, ok: false,
			latency: time.Since(start), err: fmt.Sprintf("begin: %v", err)}
	}

	_, err = tx.ExecContext(opCtx,
		"INSERT INTO issues (id, title, status, notes) VALUES (?, ?, 'open', ?)",
		issueID, fmt.Sprintf("Worker %d op %d", workerID, opNum), "stress test")
	if err != nil {
		_ = tx.Rollback()
		return result{workerID: workerID, opNum: opNum, ok: false,
			latency: time.Since(start), err: fmt.Sprintf("insert: %v", err)}
	}

	_, err = tx.ExecContext(opCtx,
		"CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
		fmt.Sprintf("Worker %d op %d", workerID, opNum),
		"repro <repro@test.com>")
	if err != nil {
		_ = tx.Rollback()
		return result{workerID: workerID, opNum: opNum, ok: false,
			latency: time.Since(start), err: fmt.Sprintf("dolt_commit: %v", err)}
	}

	if useExplicitCommit {
		// OLD pattern: explicit tx.Commit() after DOLT_COMMIT
		// Tim Sehn says this "adds raciness" since DOLT_COMMIT already
		// implicitly commits the SQL transaction.
		err = tx.Commit()
		if err != nil {
			return result{workerID: workerID, opNum: opNum, ok: false,
				latency: time.Since(start), err: fmt.Sprintf("explicit_commit: %v", err)}
		}
	}
	// NEW pattern: no tx.Commit() — DOLT_COMMIT already ended the transaction

	return result{workerID: workerID, opNum: opNum, ok: true, latency: time.Since(start)}
}
