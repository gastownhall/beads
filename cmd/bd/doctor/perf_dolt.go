//go:build cgo
package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "github.com/dolthub/driver"
	_ "github.com/go-sql-driver/mysql"
)

// DoltPerfMetrics holds performance metrics for Dolt operations
type DoltPerfMetrics struct {
	Backend      string        // "dolt-embedded" or "dolt-server"
	ServerMode   bool          // Whether connected via sql-server
	ServerStatus string        // "running", "not running", or N/A
	Platform     string        // OS/arch
	GoVersion    string        // Go runtime version
	DoltVersion  string        // Dolt version if available
	TotalIssues  int           // Total issue count
	OpenIssues   int           // Open issue count
	ClosedIssues int           // Closed issue count
	Dependencies int           // Dependency count
	DatabaseSize string        // Size of .dolt directory

	// Timing metrics (milliseconds)
	ConnectionTime   int64 // Time to establish connection
	ReadyWorkTime    int64 // Time for GetReadyWork equivalent
	ListOpenTime     int64 // Time to list open issues
	ShowIssueTime    int64 // Time to get single issue
	ComplexQueryTime int64 // Time for complex filter query
	CommitLogTime    int64 // Time to query dolt_log

	// Profile file path if profiling was enabled
	ProfilePath string
}

// RunDoltPerformanceDiagnostics runs performance diagnostics for Dolt backend
func RunDoltPerformanceDiagnostics(path string, enableProfiling bool) (*DoltPerfMetrics, error) {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Verify this is a Dolt backend
	if !IsDoltBackend(beadsDir) {
		return nil, fmt.Errorf("not a Dolt backend (detected: SQLite). Use 'bd doctor perf' for SQLite")
	}

	metrics := &DoltPerfMetrics{
		Platform:   fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		GoVersion:  runtime.Version(),
	}

	// Check if server mode is available
	doltDir := filepath.Join(beadsDir, "dolt")
	serverRunning := isDoltServerRunning("127.0.0.1", 3306)
	if serverRunning {
		metrics.ServerStatus = "running"
	} else {
		metrics.ServerStatus = "not running"
	}

	// Start profiling if requested
	if enableProfiling {
		profilePath := fmt.Sprintf("beads-dolt-perf-%s.prof", time.Now().Format("2006-01-02-150405"))
		if err := startCPUProfile(profilePath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to start CPU profiling: %v\n", err)
		} else {
			metrics.ProfilePath = profilePath
			defer stopCPUProfile()
		}
	}

	// Connect and run diagnostics - try server mode first if available
	if serverRunning {
		if err := runDoltServerDiagnostics(metrics, "127.0.0.1", 3306); err != nil {
			fmt.Fprintf(os.Stderr, "Server mode diagnostics failed, falling back to embedded: %v\n", err)
			if err := runDoltEmbeddedDiagnostics(metrics, doltDir); err != nil {
				return metrics, fmt.Errorf("embedded mode diagnostics failed: %w", err)
			}
		}
	} else {
		if err := runDoltEmbeddedDiagnostics(metrics, doltDir); err != nil {
			return metrics, fmt.Errorf("embedded mode diagnostics failed: %w", err)
		}
	}

	// Calculate database size
	metrics.DatabaseSize = getDoltDatabaseSize(doltDir)

	return metrics, nil
}

// runDoltServerDiagnostics runs diagnostics via dolt sql-server
func runDoltServerDiagnostics(metrics *DoltPerfMetrics, host string, port int) error {
	metrics.Backend = "dolt-server"
	metrics.ServerMode = true

	dsn := fmt.Sprintf("root:@tcp(%s:%d)/beads?parseTime=true", host, port)

	// Measure connection time
	start := time.Now()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Set connection pool settings
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping server: %w", err)
	}
	metrics.ConnectionTime = time.Since(start).Milliseconds()

	// Run all diagnostics
	return runDoltDiagnosticQueries(ctx, db, metrics)
}

// runDoltEmbeddedDiagnostics runs diagnostics via embedded Dolt
func runDoltEmbeddedDiagnostics(metrics *DoltPerfMetrics, doltDir string) error {
	metrics.Backend = "dolt-embedded"
	metrics.ServerMode = false

	connStr := fmt.Sprintf("file://%s?commitname=beads&commitemail=beads@local", doltDir)

	// Measure connection time (includes bootstrap overhead)
	start := time.Now()
	db, err := sql.Open("dolt", connStr)
	if err != nil {
		return fmt.Errorf("failed to open Dolt database: %w", err)
	}
	defer closeDoltDBWithTimeout(db)

	// Single connection for embedded mode
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()

	// Switch to beads database
	if _, err := db.ExecContext(ctx, "USE beads"); err != nil {
		return fmt.Errorf("failed to switch to beads database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}
	metrics.ConnectionTime = time.Since(start).Milliseconds()

	// Run all diagnostics
	return runDoltDiagnosticQueries(ctx, db, metrics)
}

// runDoltDiagnosticQueries runs the diagnostic queries and populates metrics
func runDoltDiagnosticQueries(ctx context.Context, db *sql.DB, metrics *DoltPerfMetrics) error {
	// Get issue counts
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&metrics.TotalIssues); err != nil {
		return fmt.Errorf("failed to count issues: %w", err)
	}

	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE status != 'closed'").Scan(&metrics.OpenIssues); err != nil {
		metrics.OpenIssues = -1 // Mark as unavailable
	}

	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE status = 'closed'").Scan(&metrics.ClosedIssues); err != nil {
		metrics.ClosedIssues = -1
	}

	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies").Scan(&metrics.Dependencies); err != nil {
		metrics.Dependencies = -1
	}

	// Try to get Dolt version
	if err := db.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&metrics.DoltVersion); err != nil {
		metrics.DoltVersion = "unknown"
	}

	// Measure GetReadyWork equivalent
	metrics.ReadyWorkTime = measureQueryTime(ctx, db, `
		SELECT id FROM issues
		WHERE status IN ('open', 'in_progress')
		AND id NOT IN (
			SELECT issue_id FROM dependencies
			WHERE depends_on_id IN (SELECT id FROM issues WHERE status != 'closed')
		)
		LIMIT 100
	`)

	// Measure list open issues
	metrics.ListOpenTime = measureQueryTime(ctx, db, `
		SELECT id, title, status FROM issues
		WHERE status != 'closed'
		LIMIT 100
	`)

	// Measure show single issue (get a random one first)
	var issueID string
	if err := db.QueryRowContext(ctx, "SELECT id FROM issues LIMIT 1").Scan(&issueID); err == nil && issueID != "" {
		start := time.Now()
		rows, qErr := db.QueryContext(ctx, "SELECT * FROM issues WHERE id = ?", issueID)
		if qErr != nil {
			metrics.ShowIssueTime = -1
		} else {
			for rows.Next() {
			}
			_ = rows.Close()
			metrics.ShowIssueTime = time.Since(start).Milliseconds()
		}
	}

	// Measure complex query with filters
	metrics.ComplexQueryTime = measureQueryTime(ctx, db, `
		SELECT i.id, i.title, i.status, i.priority
		FROM issues i
		LEFT JOIN labels l ON i.id = l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND i.priority <= 2
		GROUP BY i.id
		LIMIT 100
	`)

	// Measure Dolt-specific: commit log query
	metrics.CommitLogTime = measureQueryTime(ctx, db, `
		SELECT commit_hash, committer, message
		FROM dolt_log
		LIMIT 10
	`)

	return nil
}

// measureQueryTime measures how long a query takes to execute
func measureQueryTime(ctx context.Context, db *sql.DB, query string) int64 {
	start := time.Now()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return -1 // Mark as failed
	}
	defer rows.Close()

	// Drain rows to ensure we measure full execution
	for rows.Next() {
		// Just iterate through
	}
	return time.Since(start).Milliseconds()
}

// isDoltServerRunning checks if a dolt sql-server is responding
func isDoltServerRunning(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// getDoltDatabaseSize returns the total size of the Dolt database directory
func getDoltDatabaseSize(doltDir string) string {
	var totalSize int64

	err := filepath.WalkDir(doltDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				totalSize += info.Size()
			}
		}
		return nil
	})

	if err != nil {
		return "unknown"
	}

	// Format size
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case totalSize >= GB:
		return fmt.Sprintf("%.2f GB", float64(totalSize)/float64(GB))
	case totalSize >= MB:
		return fmt.Sprintf("%.2f MB", float64(totalSize)/float64(MB))
	case totalSize >= KB:
		return fmt.Sprintf("%.2f KB", float64(totalSize)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", totalSize)
	}
}

// PrintDoltPerfReport prints a formatted performance report
func PrintDoltPerfReport(metrics *DoltPerfMetrics) {
	fmt.Println("\nDolt Performance Diagnostics")
	fmt.Println(strings.Repeat("=", 50))

	fmt.Printf("\nBackend: %s\n", metrics.Backend)
	fmt.Printf("Server Mode: %v (status: %s)\n", metrics.ServerMode, metrics.ServerStatus)
	fmt.Printf("Platform: %s\n", metrics.Platform)
	fmt.Printf("Go: %s\n", metrics.GoVersion)
	fmt.Printf("Dolt: %s\n", metrics.DoltVersion)

	fmt.Printf("\nDatabase Statistics:\n")
	fmt.Printf("  Total issues:      %d\n", metrics.TotalIssues)
	fmt.Printf("  Open issues:       %d\n", metrics.OpenIssues)
	fmt.Printf("  Closed issues:     %d\n", metrics.ClosedIssues)
	fmt.Printf("  Dependencies:      %d\n", metrics.Dependencies)
	fmt.Printf("  Database size:     %s\n", metrics.DatabaseSize)

	fmt.Printf("\nOperation Performance (ms):\n")
	fmt.Printf("  Connection/Bootstrap:     %s\n", formatTiming(metrics.ConnectionTime))
	fmt.Printf("  bd ready (GetReadyWork):  %s\n", formatTiming(metrics.ReadyWorkTime))
	fmt.Printf("  bd list --status=open:    %s\n", formatTiming(metrics.ListOpenTime))
	fmt.Printf("  bd show <issue>:          %s\n", formatTiming(metrics.ShowIssueTime))
	fmt.Printf("  Complex filter query:     %s\n", formatTiming(metrics.ComplexQueryTime))
	fmt.Printf("  dolt_log query:           %s\n", formatTiming(metrics.CommitLogTime))

	// Performance assessment
	fmt.Printf("\nPerformance Assessment:\n")
	assessDoltPerformance(metrics)

	if metrics.ProfilePath != "" {
		fmt.Printf("\nCPU Profile saved: %s\n", metrics.ProfilePath)
		fmt.Printf("View flamegraph:\n")
		fmt.Printf("  go tool pprof -http=:8080 %s\n", metrics.ProfilePath)
	}
	fmt.Println()
}

func formatTiming(ms int64) string {
	if ms < 0 {
		return "failed"
	}
	return fmt.Sprintf("%dms", ms)
}

// assessDoltPerformance provides performance recommendations
func assessDoltPerformance(metrics *DoltPerfMetrics) {
	var warnings []string
	var recommendations []string

	// Check connection time (embedded mode bootstrap overhead)
	if !metrics.ServerMode && metrics.ConnectionTime > 500 {
		warnings = append(warnings, fmt.Sprintf("High bootstrap time (%dms) in embedded mode", metrics.ConnectionTime))
		recommendations = append(recommendations, "Consider using server mode: set BEADS_DOLT_SERVER_MODE=1")
	}

	// Check if server mode is available but not being used
	if !metrics.ServerMode && metrics.ServerStatus == "running" {
		recommendations = append(recommendations, "Server is running but not being used. Enable server mode for better performance.")
	}

	// Check ready work query time
	if metrics.ReadyWorkTime > 200 {
		warnings = append(warnings, fmt.Sprintf("Slow ready-work query (%dms)", metrics.ReadyWorkTime))
		recommendations = append(recommendations, "Check index on issues.status column")
	}

	// Check complex query time
	if metrics.ComplexQueryTime > 500 {
		warnings = append(warnings, fmt.Sprintf("Slow complex query (%dms)", metrics.ComplexQueryTime))
		recommendations = append(recommendations, "Consider reviewing query patterns and indexes")
	}

	// Check database size
	if metrics.TotalIssues > 5000 && metrics.ClosedIssues > 4000 {
		recommendations = append(recommendations, "Many closed issues. Consider 'bd cleanup' to prune old issues.")
	}

	if len(warnings) == 0 {
		fmt.Println("  [OK] Performance looks healthy")
	} else {
		for _, w := range warnings {
			fmt.Printf("  [WARN] %s\n", w)
		}
	}

	if len(recommendations) > 0 {
		fmt.Printf("\nRecommendations:\n")
		for _, r := range recommendations {
			fmt.Printf("  - %s\n", r)
		}
	}
}

// CheckDoltPerformance runs a quick performance check as a doctor check
func CheckDoltPerformance(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Only run for Dolt backend
	if !IsDoltBackend(beadsDir) {
		return DoctorCheck{
			Name:     "Dolt Performance",
			Status:   StatusOK,
			Message:  "N/A (SQLite backend)",
			Category: CategoryPerformance,
		}
	}

	metrics, err := RunDoltPerformanceDiagnostics(path, false)
	if err != nil {
		return DoctorCheck{
			Name:     "Dolt Performance",
			Status:   StatusWarning,
			Message:  "Unable to run diagnostics",
			Detail:   err.Error(),
			Category: CategoryPerformance,
		}
	}

	// Assess performance
	var issues []string

	if metrics.ConnectionTime > 1000 {
		issues = append(issues, fmt.Sprintf("slow bootstrap (%dms)", metrics.ConnectionTime))
	}

	if metrics.ReadyWorkTime > 500 {
		issues = append(issues, fmt.Sprintf("slow ready-work (%dms)", metrics.ReadyWorkTime))
	}

	if len(issues) > 0 {
		fix := "Run 'bd doctor perf-dolt' for detailed analysis"
		if !metrics.ServerMode && metrics.ServerStatus != "running" {
			fix = "Consider enabling server mode: BEADS_DOLT_SERVER_MODE=1"
		}
		return DoctorCheck{
			Name:     "Dolt Performance",
			Status:   StatusWarning,
			Message:  strings.Join(issues, "; "),
			Fix:      fix,
			Category: CategoryPerformance,
		}
	}

	mode := "embedded"
	if metrics.ServerMode {
		mode = "server"
	}
	return DoctorCheck{
		Name:     "Dolt Performance",
		Status:   StatusOK,
		Message:  fmt.Sprintf("OK (mode: %s, connect: %dms, ready: %dms)", mode, metrics.ConnectionTime, metrics.ReadyWorkTime),
		Category: CategoryPerformance,
	}
}

// CompareDoltModes runs diagnostics in both embedded and server mode for comparison
func CompareDoltModes(path string) error {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	if !IsDoltBackend(beadsDir) {
		return fmt.Errorf("not a Dolt backend")
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	serverRunning := isDoltServerRunning("127.0.0.1", 3306)

	fmt.Println("\nDolt Mode Comparison")
	fmt.Println(strings.Repeat("=", 50))

	// Run embedded mode diagnostics
	fmt.Println("\n[Embedded Mode]")
	embeddedMetrics := &DoltPerfMetrics{}
	if err := runDoltEmbeddedDiagnostics(embeddedMetrics, doltDir); err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		fmt.Printf("  Connection time: %dms\n", embeddedMetrics.ConnectionTime)
		fmt.Printf("  Ready-work query: %dms\n", embeddedMetrics.ReadyWorkTime)
		fmt.Printf("  List open query: %dms\n", embeddedMetrics.ListOpenTime)
		fmt.Printf("  Complex query: %dms\n", embeddedMetrics.ComplexQueryTime)
	}

	// Run server mode diagnostics if available
	if serverRunning {
		fmt.Println("\n[Server Mode]")
		serverMetrics := &DoltPerfMetrics{}
		if err := runDoltServerDiagnostics(serverMetrics, "127.0.0.1", 3306); err != nil {
			fmt.Printf("  Error: %v\n", err)
		} else {
			fmt.Printf("  Connection time: %dms\n", serverMetrics.ConnectionTime)
			fmt.Printf("  Ready-work query: %dms\n", serverMetrics.ReadyWorkTime)
			fmt.Printf("  List open query: %dms\n", serverMetrics.ListOpenTime)
			fmt.Printf("  Complex query: %dms\n", serverMetrics.ComplexQueryTime)

			// Print comparison
			if embeddedMetrics.ConnectionTime > 0 && serverMetrics.ConnectionTime > 0 {
				fmt.Println("\n[Comparison]")
				fmt.Printf("  Connection speedup: %.1fx faster in server mode\n",
					float64(embeddedMetrics.ConnectionTime)/float64(serverMetrics.ConnectionTime))
				if embeddedMetrics.ReadyWorkTime > 0 && serverMetrics.ReadyWorkTime > 0 {
					fmt.Printf("  Ready-work speedup: %.1fx\n",
						float64(embeddedMetrics.ReadyWorkTime)/float64(serverMetrics.ReadyWorkTime))
				}
			}
		}
	} else {
		fmt.Println("\n[Server Mode]")
		fmt.Println("  Not available (start with: dolt sql-server)")
	}

	fmt.Println()
	return nil
}


