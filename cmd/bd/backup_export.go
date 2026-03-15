package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// dbQuerier abstracts query execution so callers can use a retry-wrapped
// DoltStore.QueryContext instead of a raw *sql.DB.  Both *sql.DB and
// *dolt.DoltStore satisfy this interface.
type dbQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// backupState tracks watermarks for incremental backup.
type backupCounts struct {
	Issues       int `json:"issues"`
	Events       int `json:"events"`
	Comments     int `json:"comments"`
	Dependencies int `json:"dependencies"`
	Labels       int `json:"labels"`
	Config       int `json:"config"`
}

type backupState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	Counts         struct {
		Issues       int `json:"issues"`
		Events       int `json:"events"`
		Comments     int `json:"comments"`
		Dependencies int `json:"dependencies"`
		Labels       int `json:"labels"`
		Config       int `json:"config"`
	} `json:"counts"`
}

// backupDir returns the backup directory path, creating it if needed.
// When backup.git-repo is set to a valid git repo, returns a backup/ subdirectory
// inside that repo. Otherwise falls back to .beads/backup/.
func backupDir() (string, error) {
	gitRepo := config.GetString("backup.git-repo")
	if gitRepo != "" {
		if strings.HasPrefix(gitRepo, "~/") {
			home, _ := os.UserHomeDir()
			gitRepo = filepath.Join(home, gitRepo[2:])
		}
		if _, err := os.Stat(filepath.Join(gitRepo, ".git")); err != nil {
			debug.Logf("backup: git-repo %s is not a git repo, falling back to .beads/backup\n", gitRepo)
		} else {
			dir := filepath.Join(gitRepo, "backup")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return "", fmt.Errorf("failed to create backup dir in git-repo: %w", err)
			}
			return dir, nil
		}
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		beadsDir = ".beads"
	}
	dir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}
	return dir, nil
}

// loadBackupState reads the backup state file, returning a zero state if missing.
func loadBackupState(dir string) (*backupState, error) {
	path := filepath.Join(dir, "backup_state.json")
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally
	if os.IsNotExist(err) {
		return &backupState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read backup state: %w", err)
	}
	var state backupState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse backup state: %w", err)
	}
	return &state, nil
}

// saveBackupState writes the backup state file atomically.
func saveBackupState(dir string, state *backupState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal backup state: %w", err)
	}
	return atomicWriteFile(filepath.Join(dir, "backup_state.json"), data)
}

// atomicWriteFile writes data to a temp file and renames it into place (crash-safe).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".backup-tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// getBackupPrefix returns the issue prefix for the current project.
// It checks the YAML config first (authoritative in shared-server mode),
// then falls back to the database config table.
func getBackupPrefix(ctx context.Context) string {
	if yamlPrefix := config.GetString("issue-prefix"); yamlPrefix != "" {
		return yamlPrefix
	}
	if store != nil {
		if dbPrefix, err := store.GetConfig(ctx, "issue_prefix"); err == nil && dbPrefix != "" {
			return dbPrefix
		}
	}
	return ""
}

// runBackupExport exports all tables to JSONL files in .beads/backup/.
// Returns the updated state.
// When a project prefix is configured, only issues belonging to this project
// are exported. This prevents cross-project contamination on shared Dolt servers.
func runBackupExport(ctx context.Context, force bool) (*backupState, error) {
	dir, err := backupDir()
	if err != nil {
		return nil, err
	}

	state, err := loadBackupState(dir)
	if err != nil {
		return nil, err
	}

	// Change detection: skip if nothing changed (unless forced)
	if !force {
		currentCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
			debug.Logf("backup: no changes since last backup (commit %s)\n", truncateHash(currentCommit))
			return state, nil
		}
	}

	// Resolve the project prefix for scoping.
	// On shared Dolt servers, the database contains issues from ALL projects.
	// We must filter by prefix to avoid exporting (and later restoring) foreign issues.
	prefix := getBackupPrefix(ctx)
	prefixFilter := prefix + "-" // e.g. "Prosa-"

	var n int

	// Export issues only — wisps are ephemeral and excluded from backup.
	// They can be regenerated from the database if needed for disaster recovery.
	if prefix != "" {
		n, err = exportTable(ctx, store, dir, "issues.jsonl",
			"SELECT * FROM issues WHERE id LIKE ? ORDER BY id", prefixFilter+"%")
	} else {
		n, err = exportTable(ctx, store, dir, "issues.jsonl",
			"SELECT * FROM issues ORDER BY id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup issues: %w", err)
	}
	state.Counts.Issues = n

	if prefix != "" {
		n, err = exportTable(ctx, store, dir, "events.jsonl",
			"SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM events WHERE issue_id LIKE ? ORDER BY created_at ASC, id ASC",
			prefixFilter+"%")
	} else {
		n, err = exportTable(ctx, store, dir, "events.jsonl",
			"SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM events ORDER BY created_at ASC, id ASC")
	}
	if err != nil {
		return nil, fmt.Errorf("backup events: %w", err)
	}
	state.Counts.Events = n

	if prefix != "" {
		n, err = exportTable(ctx, store, dir, "comments.jsonl",
			"SELECT id, issue_id, author, text, created_at FROM comments WHERE issue_id LIKE ? ORDER BY id",
			prefixFilter+"%")
	} else {
		n, err = exportTable(ctx, store, dir, "comments.jsonl",
			"SELECT id, issue_id, author, text, created_at FROM comments ORDER BY id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup comments: %w", err)
	}
	state.Counts.Comments = n

	if prefix != "" {
		// For dependencies, both issue_id and depends_on_id should belong to this project.
		// We filter on issue_id (the dependent) having our prefix.
		n, err = exportTable(ctx, store, dir, "dependencies.jsonl",
			"SELECT issue_id, depends_on_id, type, created_at, created_by, metadata FROM dependencies WHERE issue_id LIKE ? ORDER BY issue_id, depends_on_id",
			prefixFilter+"%")
	} else {
		n, err = exportTable(ctx, store, dir, "dependencies.jsonl",
			"SELECT issue_id, depends_on_id, type, created_at, created_by, metadata FROM dependencies ORDER BY issue_id, depends_on_id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup dependencies: %w", err)
	}
	state.Counts.Dependencies = n

	if prefix != "" {
		n, err = exportTable(ctx, store, dir, "labels.jsonl",
			"SELECT issue_id, label FROM labels WHERE issue_id LIKE ? ORDER BY issue_id, label",
			prefixFilter+"%")
	} else {
		n, err = exportTable(ctx, store, dir, "labels.jsonl",
			"SELECT issue_id, label FROM labels ORDER BY issue_id, label")
	}
	if err != nil {
		return nil, fmt.Errorf("backup labels: %w", err)
	}
	state.Counts.Labels = n

	n, err = exportTable(ctx, store, dir, "config.jsonl",
		"SELECT `key`, value FROM config ORDER BY `key`")
	if err != nil {
		return nil, fmt.Errorf("backup config: %w", err)
	}
	state.Counts.Config = n

	// Update watermarks
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current commit for state: %w", err)
	}
	state.LastDoltCommit = currentCommit
	state.Timestamp = time.Now().UTC()

	if err := saveBackupState(dir, state); err != nil {
		return nil, err
	}

	return state, nil
}

// truncateHash returns the first 8 characters of a hash, or the full string if shorter.
func truncateHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// exportTable streams query results to a JSONL file using atomic write (temp file + rename).
// Uses bounded memory regardless of result set size.
// Optional args are passed as query parameters (for WHERE clause filtering).
func exportTable(ctx context.Context, q dbQuerier, dir, filename, query string, args ...any) (int, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("failed to get columns: %w", err)
	}

	// Write to temp file, then rename atomically for crash safety.
	tmp, err := os.CreateTemp(dir, ".backup-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // cleanup on error path

	w := bufio.NewWriter(tmp)
	count, err := writeRows(rows, cols, w)
	if err != nil {
		_ = tmp.Close()
		return 0, err
	}

	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("flush failed: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("sync failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("close failed: %w", err)
	}

	dest := filepath.Join(dir, filename)
	if err := os.Rename(tmpPath, dest); err != nil {
		return 0, fmt.Errorf("rename failed: %w", err)
	}
	return count, nil
}

// writeRows scans rows and writes each as a JSON line to w.
// Allocates scan buffers once and reuses them across all rows.
func writeRows(rows *sql.Rows, cols []string, w *bufio.Writer) (int, error) {
	values := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}

	count := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("scan failed: %w", err)
		}

		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			row[col] = normalizeValue(values[i])
		}

		data, err := json.Marshal(row)
		if err != nil {
			return 0, fmt.Errorf("marshal failed: %w", err)
		}
		data = append(data, '\n')
		if _, err := w.Write(data); err != nil {
			return 0, fmt.Errorf("write failed: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("row iteration failed: %w", err)
	}
	return count, nil
}

// normalizeValue converts database driver types to JSON-friendly values.
func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case time.Time:
		if val.IsZero() {
			return nil
		}
		return val.Format(time.RFC3339)
	case nil:
		return nil
	default:
		return val
	}
}
