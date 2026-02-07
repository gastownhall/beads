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

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
)

// DoltPerfMetrics holds performance metrics for Dolt operations
type DoltPerfMetrics struct {
	Backend      string // "dolt-server"
	ServerStatus string // "running" or "not running"
	Platform     string // OS/arch
	GoVersion    string // Go runtime version
	DoltVersion  string // Dolt version if available
	TotalIssues  int    // Total issue count
	OpenIssues   int    // Open issue count
	ClosedIssues int    // Closed issue count
	Dependencies int    // Dependency count
	DatabaseSize string // Size of .dolt directory

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
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		GoVersion: runtime.Version(),
	}

	// Load config for server connection settings
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()

	// Check if server is running
	doltDir := filepath.Join(beadsDir, "dolt")
	serverRunning := isDoltServerRunning(host, port)
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

	// Run server mode diagnostics
	if !serverRunning {
		return metrics, fmt.Errorf("dolt sql-server is not running; start it with: dolt sql-server")
	}

	if err := runDoltServerDiagnostics(metrics, host, port); err != nil {
		return metrics, fmt.Errorf("server mode diagnostics failed: %w", err)
	}

	// Calculate database size
	metrics.DatabaseSize = getDoltDatabaseSize(doltDir)

	return metrics, nil
}

// runDoltServerDiagnostics runs diagnostics via dolt sql-server
func runDoltServerDiagnostics(metrics *DoltPerfMetrics, host string, port int) error {
	metrics.Backend = "dolt-server"

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
	fmt.Printf("Server Status: %s\n", metrics.ServerStatus)
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
	fmt.Printf("  Connection:               %s\n", formatTiming(metrics.ConnectionTime))
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
		issues = append(issues, fmt.Sprintf("slow connection (%dms)", metrics.ConnectionTime))
	}

	if metrics.ReadyWorkTime > 500 {
		issues = append(issues, fmt.Sprintf("slow ready-work (%dms)", metrics.ReadyWorkTime))
	}

	if len(issues) > 0 {
		return DoctorCheck{
			Name:     "Dolt Performance",
			Status:   StatusWarning,
			Message:  strings.Join(issues, "; "),
			Fix:      "Run 'bd doctor perf-dolt' for detailed analysis",
			Category: CategoryPerformance,
		}
	}

	return DoctorCheck{
		Name:     "Dolt Performance",
		Status:   StatusOK,
		Message:  fmt.Sprintf("OK (server mode, connect: %dms, ready: %dms)", metrics.ConnectionTime, metrics.ReadyWorkTime),
		Category: CategoryPerformance,
	}
}
