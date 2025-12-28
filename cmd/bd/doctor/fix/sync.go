package fix

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// DBJSONLSync fixes database-JSONL sync issues by running the appropriate sync command.
// It detects which has more data and runs the correct direction:
// - If DB > JSONL: Run 'bd export' (DB→JSONL)
// - If JSONL > DB: Run 'bd sync --import-only' (JSONL→DB)
// - If equal but timestamps differ: Use file mtime to decide
func DBJSONLSync(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Get database path (same logic as doctor package)
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Find JSONL file
	var jsonlPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			p := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(p); err == nil {
				jsonlPath = p
			}
		}
	}
	if jsonlPath == "" {
		issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
		beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")

		if _, err := os.Stat(issuesJSONL); err == nil {
			jsonlPath = issuesJSONL
		} else if _, err := os.Stat(beadsJSONL); err == nil {
			jsonlPath = beadsJSONL
		}
	}

	// Check if both database and JSONL exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil // No database, nothing to sync
	}
	if jsonlPath == "" {
		return nil // No JSONL, nothing to sync
	}

	// Count issues in both
	dbCount, err := countDatabaseIssues(dbPath)
	if err != nil {
		return fmt.Errorf("failed to count database issues: %w", err)
	}

	jsonlCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to count JSONL issues: %w", err)
	}

	// Determine sync direction
	var syncDirection string

	if dbCount == jsonlCount {
		// Counts are equal, use file modification times to decide
		dbInfo, err := os.Stat(dbPath)
		if err != nil {
			return fmt.Errorf("failed to stat database: %w", err)
		}

		jsonlInfo, err := os.Stat(jsonlPath)
		if err != nil {
			return fmt.Errorf("failed to stat JSONL: %w", err)
		}

		if dbInfo.ModTime().After(jsonlInfo.ModTime()) {
			// DB was modified after JSONL → export to update JSONL
			syncDirection = "export"
		} else {
			// JSONL was modified after DB → import to update DB
			syncDirection = "import"
		}
	} else if dbCount > jsonlCount {
		// DB has more issues → export to sync JSONL
		syncDirection = "export"
	} else {
		// JSONL has more issues → import to sync DB
		syncDirection = "import"
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	if syncDirection == "export" {
		// Export DB to JSONL file (must specify -o to write to file, not stdout)
		jsonlOutputPath := jsonlPath
		exportCmd := newBdCmd(bdBinary, "--db", dbPath, "export", "-o", jsonlOutputPath, "--force")
		exportCmd.Dir = path // Set working directory without changing process dir
		exportCmd.Stdout = os.Stdout
		exportCmd.Stderr = os.Stderr
		if err := exportCmd.Run(); err != nil {
			return fmt.Errorf("failed to export database to JSONL: %w", err)
		}

		// Staleness check uses last_import_time. After exporting, JSONL mtime is newer,
		// so mark the DB as fresh by running a no-op import (skip existing issues).
		markFreshCmd := newBdCmd(bdBinary, "--db", dbPath, "import", "-i", jsonlOutputPath, "--force", "--skip-existing", "--no-git-history")
		markFreshCmd.Dir = path
		markFreshCmd.Stdout = os.Stdout
		markFreshCmd.Stderr = os.Stderr
		if err := markFreshCmd.Run(); err != nil {
			return fmt.Errorf("failed to mark database as fresh after export: %w", err)
		}

		return nil
	}

	importCmd := newBdCmd(bdBinary, "--db", dbPath, "sync", "--import-only")
	importCmd.Dir = path // Set working directory without changing process dir
	importCmd.Stdout = os.Stdout
	importCmd.Stderr = os.Stderr

	if err := importCmd.Run(); err != nil {
		return fmt.Errorf("failed to sync database with JSONL: %w", err)
	}

	return nil
}

// countDatabaseIssues counts the number of issues in the database.
func countDatabaseIssues(dbPath string) (int, error) {
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return 0, fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to query database: %w", err)
	}

	return count, nil
}

// countJSONLIssues counts the number of valid issues in a JSONL file.
// Returns only the count (doesn't need prefixes for sync direction decision).
func countJSONLIssues(jsonlPath string) (int, error) {
	// jsonlPath is safe: constructed from filepath.Join(beadsDir, hardcoded name)
	file, err := os.Open(jsonlPath) //nolint:gosec
	if err != nil {
		return 0, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse JSON to get the ID
		var issue map[string]interface{}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue // Skip malformed lines
		}

		if id, ok := issue["id"].(string); ok && id != "" {
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("failed to read JSONL file: %w", err)
	}

	return count, nil
}
