package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
)

var cpuProfileFile *os.File

// RunPerformanceDiagnostics runs performance diagnostics and generates a CPU profile
func RunPerformanceDiagnostics(path string) {
	fmt.Println("\nBeads Performance Diagnostics")
	fmt.Println(strings.Repeat("=", 50))

	// Check if .beads directory exists
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: No .beads/ directory found at %s\n", path)
		fmt.Fprintf(os.Stderr, "Run 'bd init' to initialize beads\n")
		os.Exit(1)
	}

	// Get database path
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: No database found at %s\n", dbPath)
		os.Exit(1)
	}

	// Collect platform info
	platformInfo := CollectPlatformInfo(path)
	fmt.Printf("\nPlatform: %s\n", platformInfo["os_arch"])
	fmt.Printf("Go: %s\n", platformInfo["go_version"])
	fmt.Printf("SQLite: %s\n", platformInfo["sqlite_version"])

	// Collect database stats
	dbStats := collectDatabaseStats(dbPath)
	fmt.Printf("\nDatabase Statistics:\n")
	fmt.Printf("  Total issues:      %s\n", dbStats["total_issues"])
	fmt.Printf("  Open issues:       %s\n", dbStats["open_issues"])
	fmt.Printf("  Closed issues:     %s\n", dbStats["closed_issues"])
	fmt.Printf("  Dependencies:      %s\n", dbStats["dependencies"])
	fmt.Printf("  Labels:            %s\n", dbStats["labels"])
	fmt.Printf("  Database size:     %s\n", dbStats["db_size"])

	// Start CPU profiling
	profilePath := fmt.Sprintf("beads-perf-%s.prof", time.Now().Format("2006-01-02-150405"))
	if err := startCPUProfile(profilePath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to start CPU profiling: %v\n", err)
	} else {
		defer stopCPUProfile()
		fmt.Printf("\nCPU profiling enabled: %s\n", profilePath)
	}

	// Time key operations
	fmt.Printf("\nOperation Performance:\n")

	// Measure GetReadyWork
	readyDuration := measureOperation(func() error {
		return runReadyWork(dbPath)
	})
	fmt.Printf("  bd ready                  %dms\n", readyDuration.Milliseconds())

	// Measure SearchIssues (list open)
	listDuration := measureOperation(func() error {
		return runListOpen(dbPath)
	})
	fmt.Printf("  bd list --status=open     %dms\n", listDuration.Milliseconds())

	// Measure GetIssue (show random issue)
	showDuration := measureOperation(func() error {
		return runShowRandom(dbPath)
	})
	if showDuration > 0 {
		fmt.Printf("  bd show <random-issue>    %dms\n", showDuration.Milliseconds())
	}

	// Measure SearchIssues with filters
	searchDuration := measureOperation(func() error {
		return runComplexSearch(dbPath)
	})
	fmt.Printf("  bd list (complex filters) %dms\n", searchDuration.Milliseconds())

	fmt.Printf("\nProfile saved: %s\n", profilePath)
	fmt.Printf("Share this file with bug reports for performance issues.\n\n")
	fmt.Printf("View flamegraph:\n")
	fmt.Printf("  go tool pprof -http=:8080 %s\n\n", profilePath)
}

// CollectPlatformInfo gathers platform information for diagnostics.
func CollectPlatformInfo(path string) map[string]string {
	info := make(map[string]string)

	// OS and architecture
	info["os_arch"] = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	// Go version
	info["go_version"] = runtime.Version()

	// SQLite version - try to find database
	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err == nil {
		defer db.Close()
		var version string
		if err := db.QueryRow("SELECT sqlite_version()").Scan(&version); err == nil {
			info["sqlite_version"] = version
		} else {
			info["sqlite_version"] = "unknown"
		}
	} else {
		info["sqlite_version"] = "unknown"
	}

	return info
}

func collectDatabaseStats(dbPath string) map[string]string {
	stats := make(map[string]string)

	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		stats["total_issues"] = "error"
		stats["open_issues"] = "error"
		stats["closed_issues"] = "error"
		stats["dependencies"] = "error"
		stats["labels"] = "error"
		stats["db_size"] = "error"
		return stats
	}
	defer db.Close()

	// Total issues
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&total); err == nil {
		stats["total_issues"] = fmt.Sprintf("%d", total)
	} else {
		stats["total_issues"] = "error"
	}

	// Open issues
	var open int
	if err := db.QueryRow("SELECT COUNT(*) FROM issues WHERE status != 'closed'").Scan(&open); err == nil {
		stats["open_issues"] = fmt.Sprintf("%d", open)
	} else {
		stats["open_issues"] = "error"
	}

	// Closed issues
	var closed int
	if err := db.QueryRow("SELECT COUNT(*) FROM issues WHERE status = 'closed'").Scan(&closed); err == nil {
		stats["closed_issues"] = fmt.Sprintf("%d", closed)
	} else {
		stats["closed_issues"] = "error"
	}

	// Dependencies
	var deps int
	if err := db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&deps); err == nil {
		stats["dependencies"] = fmt.Sprintf("%d", deps)
	} else {
		stats["dependencies"] = "error"
	}

	// Labels
	var labels int
	if err := db.QueryRow("SELECT COUNT(DISTINCT label) FROM labels").Scan(&labels); err == nil {
		stats["labels"] = fmt.Sprintf("%d", labels)
	} else {
		stats["labels"] = "error"
	}

	// Database file size
	if info, err := os.Stat(dbPath); err == nil {
		sizeMB := float64(info.Size()) / (1024 * 1024)
		stats["db_size"] = fmt.Sprintf("%.2f MB", sizeMB)
	} else {
		stats["db_size"] = "error"
	}

	return stats
}

func startCPUProfile(path string) error {
	// #nosec G304 -- profile path supplied by CLI flag in trusted environment
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	cpuProfileFile = f
	return pprof.StartCPUProfile(f)
}

// stopCPUProfile stops CPU profiling and closes the profile file.
// Must be called after pprof.StartCPUProfile() to flush profile data to disk.
func stopCPUProfile() {
	pprof.StopCPUProfile()
	if cpuProfileFile != nil {
		_ = cpuProfileFile.Close() // best effort cleanup
	}
}

func measureOperation(op func() error) time.Duration {
	start := time.Now()
	if err := op(); err != nil {
		return 0
	}
	return time.Since(start)
}

// runQuery executes a read-only database query and returns any error
func runQuery(dbPath string, queryFn func(*sql.DB) error) error {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	return queryFn(db)
}

func runReadyWork(dbPath string) error {
	return runQuery(dbPath, func(db *sql.DB) error {
		// simplified ready work query (the real one is more complex)
		_, err := db.Query(`
			SELECT id FROM issues
			WHERE status IN ('open', 'in_progress')
			AND id NOT IN (
				SELECT issue_id FROM dependencies WHERE type = 'blocks'
			)
			LIMIT 100
		`)
		return err
	})
}

func runListOpen(dbPath string) error {
	return runQuery(dbPath, func(db *sql.DB) error {
		_, err := db.Query("SELECT id, title, status FROM issues WHERE status != 'closed' LIMIT 100")
		return err
	})
}

func runShowRandom(dbPath string) error {
	return runQuery(dbPath, func(db *sql.DB) error {
		// get a random issue
		var issueID string
		if err := db.QueryRow("SELECT id FROM issues ORDER BY RANDOM() LIMIT 1").Scan(&issueID); err != nil {
			return err
		}

		// get issue details
		_, err := db.Query("SELECT * FROM issues WHERE id = ?", issueID)
		return err
	})
}

func runComplexSearch(dbPath string) error {
	return runQuery(dbPath, func(db *sql.DB) error {
		// complex query with filters
		_, err := db.Query(`
			SELECT i.id, i.title, i.status, i.priority
			FROM issues i
			LEFT JOIN labels l ON i.id = l.issue_id
			WHERE i.status IN ('open', 'in_progress')
			AND i.priority <= 2
			GROUP BY i.id
			LIMIT 100
		`)
		return err
	})
}
