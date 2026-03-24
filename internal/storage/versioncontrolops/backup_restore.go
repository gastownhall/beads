// TODO: This is not the correct approach for "restoring" a backup. The
// CALL DOLT_BACKUP('restore', ...) stored procedure should be used instead.
package versioncontrolops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// ConfigSetter is the subset of DoltStorage needed by restore to set config values.
type ConfigSetter interface {
	SetConfig(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)
}

// RestoreFromDir restores all JSONL tables from dir. When prefix is non-empty,
// only entries matching the prefix are imported. Config is restored via the
// store's SetConfig; all other tables use raw INSERT IGNORE through db.
func RestoreFromDir(ctx context.Context, db DBConn, cfgStore ConfigSetter, dir, prefix string, dryRun bool) (*storage.BackupRestoreResult, error) {
	result := &storage.BackupRestoreResult{}

	// 1. Restore config
	configPath := filepath.Join(dir, "config.jsonl")
	if _, err := os.Stat(configPath); err == nil {
		n, warnings, err := restoreConfig(ctx, cfgStore, configPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore config: %w", err)
		}
		result.Config = n
		result.Warnings += warnings
	}

	// 2. Restore issues (must come before related tables)
	issuesPath := filepath.Join(dir, "issues.jsonl")
	n, err := restoreIssues(ctx, db, cfgStore, issuesPath, dryRun, prefix)
	if err != nil {
		return nil, fmt.Errorf("restore issues: %w", err)
	}
	result.Issues = n

	// 3. Restore comments
	commentsPath := filepath.Join(dir, "comments.jsonl")
	if _, err := os.Stat(commentsPath); err == nil {
		n, warnings, err := restoreSimpleTable(ctx, db, commentsPath, dryRun, prefix,
			"comments",
			func(line json.RawMessage) (string, string, []interface{}, error) {
				var c struct {
					ID        string `json:"id"`
					IssueID   string `json:"issue_id"`
					Author    string `json:"author"`
					Text      string `json:"text"`
					CreatedAt string `json:"created_at"`
				}
				if err := json.Unmarshal(line, &c); err != nil {
					return "", "", nil, err
				}
				return c.IssueID, `INSERT IGNORE INTO comments (issue_id, author, text, created_at) VALUES (?, ?, ?, ?)`,
					[]interface{}{c.IssueID, c.Author, c.Text, parseTimeOrNow(c.CreatedAt)}, nil
			})
		if err != nil {
			return nil, fmt.Errorf("restore comments: %w", err)
		}
		result.Comments = n
		result.Warnings += warnings
	}

	// 4. Restore dependencies
	depsPath := filepath.Join(dir, "dependencies.jsonl")
	if _, err := os.Stat(depsPath); err == nil {
		n, warnings, err := restoreSimpleTable(ctx, db, depsPath, dryRun, prefix,
			"dependencies",
			func(line json.RawMessage) (string, string, []interface{}, error) {
				var d struct {
					IssueID     string  `json:"issue_id"`
					DependsOnID string  `json:"depends_on_id"`
					Type        string  `json:"type"`
					CreatedAt   string  `json:"created_at"`
					CreatedBy   string  `json:"created_by"`
					Metadata    *string `json:"metadata"`
				}
				if err := json.Unmarshal(line, &d); err != nil {
					return "", "", nil, err
				}
				if d.IssueID == "" || d.DependsOnID == "" {
					return "", "", nil, nil // skip
				}
				meta := "{}"
				if d.Metadata != nil {
					raw := strings.TrimSpace(*d.Metadata)
					if raw != "" && json.Valid([]byte(raw)) {
						meta = raw
					}
				}
				return d.IssueID,
					`INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, ?, ?)`,
					[]interface{}{d.IssueID, d.DependsOnID, d.Type, parseTimeOrNow(d.CreatedAt), d.CreatedBy, meta}, nil
			})
		if err != nil {
			return nil, fmt.Errorf("restore dependencies: %w", err)
		}
		result.Dependencies = n
		result.Warnings += warnings
	}

	// 5. Restore labels
	labelsPath := filepath.Join(dir, "labels.jsonl")
	if _, err := os.Stat(labelsPath); err == nil {
		n, warnings, err := restoreSimpleTable(ctx, db, labelsPath, dryRun, prefix,
			"labels",
			func(line json.RawMessage) (string, string, []interface{}, error) {
				var l struct {
					IssueID string `json:"issue_id"`
					Label   string `json:"label"`
				}
				if err := json.Unmarshal(line, &l); err != nil {
					return "", "", nil, err
				}
				if l.IssueID == "" || l.Label == "" {
					return "", "", nil, nil
				}
				return l.IssueID,
					`INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
					[]interface{}{l.IssueID, l.Label}, nil
			})
		if err != nil {
			return nil, fmt.Errorf("restore labels: %w", err)
		}
		result.Labels = n
		result.Warnings += warnings
	}

	// 6. Restore events
	eventsPath := filepath.Join(dir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		n, warnings, err := restoreSimpleTable(ctx, db, eventsPath, dryRun, prefix,
			"events",
			func(line json.RawMessage) (string, string, []interface{}, error) {
				var e struct {
					ID        string  `json:"id"`
					IssueID   string  `json:"issue_id"`
					EventType string  `json:"event_type"`
					Actor     string  `json:"actor"`
					OldValue  *string `json:"old_value"`
					NewValue  *string `json:"new_value"`
					Comment   *string `json:"comment"`
					CreatedAt string  `json:"created_at"`
				}
				if err := json.Unmarshal(line, &e); err != nil {
					return "", "", nil, err
				}
				if e.IssueID == "" {
					return "", "", nil, nil
				}
				return e.IssueID,
					`INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					[]interface{}{e.IssueID, e.EventType, e.Actor, e.OldValue, e.NewValue, e.Comment, parseTimeOrNow(e.CreatedAt)}, nil
			})
		if err != nil {
			return nil, fmt.Errorf("restore events: %w", err)
		}
		result.Events = n
		result.Warnings += warnings
	}

	return result, nil
}

func restoreConfig(ctx context.Context, cfgStore ConfigSetter, path string, dryRun bool) (int, int, error) {
	type configEntry struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}

	lines, err := ReadJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var entry configEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid config line: %v\n", err)
			warnings++
			continue
		}
		if entry.Key == "" {
			continue
		}
		if !dryRun {
			if err := cfgStore.SetConfig(ctx, entry.Key, entry.Value); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore config %q: %v\n", entry.Key, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// restoreIssues handles the complex issue restoration including denormalized
// labels/dependencies extraction and raw INSERT with dynamic columns.
//
//nolint:gosec // G201: col names come from backup JSONL (our own export)
func restoreIssues(ctx context.Context, db DBConn, cfgStore ConfigSetter, path string, dryRun bool, prefix string) (int, error) {
	lines, err := ReadJSONLFile(path)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, nil
	}

	if dryRun {
		if prefix == "" {
			return len(lines), nil
		}
		count := 0
		for _, line := range lines {
			var row map[string]interface{}
			if err := json.Unmarshal(line, &row); err != nil {
				continue
			}
			if id, ok := row["id"].(string); ok && issueIDMatchesPrefix(id, prefix) {
				count++
			}
		}
		return count, nil
	}

	// Auto-detect prefix from first issue
	var firstRow map[string]interface{}
	if err := json.Unmarshal(lines[0], &firstRow); err == nil {
		if id, ok := firstRow["id"].(string); ok {
			configuredPrefix, _ := cfgStore.GetConfig(ctx, "issue_prefix")
			if strings.TrimSpace(configuredPrefix) == "" {
				firstPrefix := extractIssuePrefix(id)
				if firstPrefix != "" {
					_ = cfgStore.SetConfig(ctx, "issue_prefix", firstPrefix)
				}
			}
		}
	}

	count := 0
	for _, line := range lines {
		var row map[string]interface{}
		if err := json.Unmarshal(line, &row); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid issue line: %v\n", err)
			continue
		}
		if _, ok := row["id"]; !ok {
			continue
		}
		issueID, _ := row["id"].(string)
		if !issueIDMatchesPrefix(issueID, prefix) {
			continue
		}

		// Extract denormalized relational data
		var labels []interface{}
		if v, ok := row["labels"]; ok {
			if arr, ok := v.([]interface{}); ok {
				labels = arr
			}
			delete(row, "labels")
		}
		var deps []interface{}
		if v, ok := row["dependencies"]; ok {
			if arr, ok := v.([]interface{}); ok {
				deps = arr
			}
			delete(row, "dependencies")
		}
		delete(row, "dependency_count")
		delete(row, "dependent_count")
		delete(row, "comment_count")
		delete(row, "parent")

		n := restoreTableRow(ctx, db, "issues", row)
		count += n
		if n == 0 {
			continue
		}

		// Insert extracted labels
		for _, l := range labels {
			if label, ok := l.(string); ok && label != "" {
				_, _ = db.ExecContext(ctx,
					"INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)",
					issueID, label)
			}
		}

		// Insert extracted dependencies
		for _, d := range deps {
			dep, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			depIssueID, _ := dep["issue_id"].(string)
			dependsOnID, _ := dep["depends_on_id"].(string)
			depType, _ := dep["type"].(string)
			createdBy, _ := dep["created_by"].(string)
			metadata, _ := dep["metadata"].(string)
			if metadata == "" {
				metadata = "{}"
			}
			if depIssueID == "" || dependsOnID == "" {
				continue
			}
			createdAtStr, _ := dep["created_at"].(string)
			createdAt := parseTimeOrNow(createdAtStr)
			_, _ = db.ExecContext(ctx,
				"INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, ?, ?)",
				depIssueID, dependsOnID, depType, createdAt, createdBy, metadata)
		}
	}
	return count, nil
}

// restoreTableRow inserts a single row from a JSONL map into the given table.
//
//nolint:gosec // G201: col names come from backup JSONL (our own export)
func restoreTableRow(ctx context.Context, db DBConn, table string, row map[string]interface{}) int {
	if len(row) == 0 {
		return 0
	}

	cols := make([]string, 0, len(row))
	vals := make([]interface{}, 0, len(row))
	placeholders := make([]string, 0, len(row))

	for col, val := range row {
		switch v := val.(type) {
		case []interface{}:
			serialized, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(serialized)
		case map[string]interface{}:
			serialized, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(serialized)
		}
		cols = append(cols, "`"+col+"`")
		placeholders = append(placeholders, "?")
		vals = append(vals, val)
	}

	query := fmt.Sprintf("INSERT IGNORE INTO `%s` (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))

	if _, err := db.ExecContext(ctx, query, vals...); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to restore %s row: %v\n", table, err)
		return 0
	}
	return 1
}

// restoreSimpleTable is a generic restore function for comments, dependencies,
// labels, and events. parseFn extracts the issueID, query, and args from a JSONL line.
// If parseFn returns empty issueID or nil args, the line is skipped.
func restoreSimpleTable(
	ctx context.Context, db DBConn, path string, dryRun bool, prefix, tableName string,
	parseFn func(json.RawMessage) (issueID, query string, args []interface{}, err error),
) (int, int, error) {
	lines, err := ReadJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		issueID, query, args, err := parseFn(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid %s line: %v\n", tableName, err)
			warnings++
			continue
		}
		if issueID == "" || args == nil {
			continue
		}
		if !issueIDMatchesPrefix(issueID, prefix) {
			continue
		}
		if !dryRun {
			if _, err := db.ExecContext(ctx, query, args...); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore %s for %s: %v\n", tableName, issueID, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

func issueIDMatchesPrefix(issueID, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(issueID, prefix+"-")
}

func parseTimeOrNow(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}

// extractIssuePrefix extracts the prefix from an issue ID (e.g., "proj-abc123" → "proj").
func extractIssuePrefix(issueID string) string {
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}
	return issueID[:lastIdx]
}

// ReadJSONLFile reads a JSONL file and returns each non-empty line as raw JSON.
func ReadJSONLFile(path string) ([]json.RawMessage, error) {
	//nolint:gosec // G304: path is from backup directory
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	var lines []json.RawMessage
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, json.RawMessage(cp))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan %s: %w", path, err)
	}
	return lines, nil
}
