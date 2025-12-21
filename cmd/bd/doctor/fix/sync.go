package fix

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
	beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")

	if _, err := os.Stat(issuesJSONL); err == nil {
		jsonlPath = issuesJSONL
	} else if _, err := os.Stat(beadsJSONL); err == nil {
		jsonlPath = beadsJSONL
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

	// Run the appropriate sync command
	var cmd *exec.Cmd
	if syncDirection == "export" {
		cmd = exec.Command(bdBinary, "export") // #nosec G204 -- bdBinary from validated executable path
	} else {
		cmd = exec.Command(bdBinary, "sync", "--import-only") // #nosec G204 -- bdBinary from validated executable path
	}

	cmd.Dir = path // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to sync database with JSONL: %w", err)
	}

	return nil
}

// countDatabaseIssues counts the number of issues in the database.
func countDatabaseIssues(dbPath string) (int, error) {
	db, err := sql.Open("sqlite3", dbPath)
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
