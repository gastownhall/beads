package fix

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// RemoteConsistency fixes remote discrepancies between SQL server and CLI.
// For one-side-only remotes, it adds the missing side.
// Conflicts (different URLs) are skipped — they require manual resolution.
func RemoteConsistency(repoPath string) error {
	beadsDir := resolveBeadsDir(repoPath)
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	doltDir := doltserver.ResolveDoltDir(beadsDir)
	dbName := cfg.GetDoltDatabase()
	dbDir := filepath.Join(doltDir, dbName)

	// Get SQL remotes
	db, err := openFixDB(beadsDir, cfg)
	if err != nil {
		return fmt.Errorf("cannot connect to Dolt server: %w", err)
	}
	defer db.Close()

	sqlRemotes, err := queryFixRemotes(db)
	if err != nil {
		return fmt.Errorf("failed to query SQL remotes: %w", err)
	}

	// Get CLI remotes
	cliRemotes, err := doltutil.ListCLIRemotes(dbDir)
	if err != nil {
		return fmt.Errorf("failed to query CLI remotes: %w", err)
	}

	sqlMap := doltutil.ToRemoteNameMap(sqlRemotes)
	cliMap := doltutil.ToRemoteNameMap(cliRemotes)

	fixed := 0

	// SQL-only: add to CLI
	for name, url := range sqlMap {
		if _, inCLI := cliMap[name]; !inCLI {
			if err := doltutil.AddCLIRemote(dbDir, name, url); err != nil {
				fmt.Printf("  Warning: could not add CLI remote %s: %v\n", name, err)
			} else {
				fmt.Printf("  Added CLI remote: %s → %s\n", name, url)
				fixed++
			}
		}
	}

	// CLI-only: add to SQL
	for name, url := range cliMap {
		if _, inSQL := sqlMap[name]; !inSQL {
			if _, err := db.Exec("CALL DOLT_REMOTE('add', ?, ?)", name, url); err != nil {
				fmt.Printf("  Warning: could not add SQL remote %s: %v\n", name, err)
			} else {
				fmt.Printf("  Added SQL remote: %s → %s\n", name, url)
				fixed++
			}
		}
	}

	// Conflicts: skip
	for name, sqlURL := range sqlMap {
		if cliURL, ok := cliMap[name]; ok && sqlURL != cliURL {
			fmt.Printf("  Skipped %s: conflicting URLs (SQL=%s, CLI=%s) — resolve manually\n", name, sqlURL, cliURL)
		}
	}

	if fixed == 0 {
		fmt.Printf("  No fixable discrepancies found\n")
	}
	return nil
}

func openFixDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	host := cfg.GetDoltServerHost()
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()
	password := cfg.GetDoltServerPassword()
	port := doltserver.DefaultConfig(beadsDir).Port

	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, password, host, port, database)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s",
			user, host, port, database)
	}
	return sql.Open("mysql", connStr)
}

func queryFixRemotes(db *sql.DB) ([]storage.RemoteInfo, error) {
	rows, err := db.Query("SELECT name, url FROM dolt_remotes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var remotes []storage.RemoteInfo
	for rows.Next() {
		var r storage.RemoteInfo
		if err := rows.Scan(&r.Name, &r.URL); err != nil {
			return nil, err
		}
		remotes = append(remotes, r)
	}
	return remotes, rows.Err()
}
