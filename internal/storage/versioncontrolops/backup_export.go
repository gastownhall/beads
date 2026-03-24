package versioncontrolops

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// ExportTables exports issues, events, comments, dependencies, labels, and
// config to JSONL files in dir. When prefix is non-empty, only issues matching
// "prefix-%" are exported.
func ExportTables(ctx context.Context, db DBConn, dir, prefix string) (*storage.BackupCounts, error) {
	counts := &storage.BackupCounts{}
	prefixFilter := prefix + "-"
	var n int
	var err error

	if prefix != "" {
		n, err = ExportTable(ctx, db, dir, "issues.jsonl",
			"SELECT * FROM issues WHERE id LIKE ? ORDER BY id", prefixFilter+"%")
	} else {
		n, err = ExportTable(ctx, db, dir, "issues.jsonl",
			"SELECT * FROM issues ORDER BY id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup issues: %w", err)
	}
	counts.Issues = n

	if prefix != "" {
		n, err = ExportTable(ctx, db, dir, "events.jsonl",
			"SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM events WHERE issue_id LIKE ? ORDER BY created_at ASC, id ASC",
			prefixFilter+"%")
	} else {
		n, err = ExportTable(ctx, db, dir, "events.jsonl",
			"SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM events ORDER BY created_at ASC, id ASC")
	}
	if err != nil {
		return nil, fmt.Errorf("backup events: %w", err)
	}
	counts.Events = n

	if prefix != "" {
		n, err = ExportTable(ctx, db, dir, "comments.jsonl",
			"SELECT id, issue_id, author, text, created_at FROM comments WHERE issue_id LIKE ? ORDER BY id",
			prefixFilter+"%")
	} else {
		n, err = ExportTable(ctx, db, dir, "comments.jsonl",
			"SELECT id, issue_id, author, text, created_at FROM comments ORDER BY id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup comments: %w", err)
	}
	counts.Comments = n

	if prefix != "" {
		n, err = ExportTable(ctx, db, dir, "dependencies.jsonl",
			"SELECT issue_id, depends_on_id, type, created_at, created_by, metadata FROM dependencies WHERE issue_id LIKE ? ORDER BY issue_id, depends_on_id",
			prefixFilter+"%")
	} else {
		n, err = ExportTable(ctx, db, dir, "dependencies.jsonl",
			"SELECT issue_id, depends_on_id, type, created_at, created_by, metadata FROM dependencies ORDER BY issue_id, depends_on_id")
	}
	if err != nil {
		return nil, fmt.Errorf("backup dependencies: %w", err)
	}
	counts.Dependencies = n

	if prefix != "" {
		n, err = ExportTable(ctx, db, dir, "labels.jsonl",
			"SELECT issue_id, label FROM labels WHERE issue_id LIKE ? ORDER BY issue_id, label",
			prefixFilter+"%")
	} else {
		n, err = ExportTable(ctx, db, dir, "labels.jsonl",
			"SELECT issue_id, label FROM labels ORDER BY issue_id, label")
	}
	if err != nil {
		return nil, fmt.Errorf("backup labels: %w", err)
	}
	counts.Labels = n

	n, err = ExportTable(ctx, db, dir, "config.jsonl",
		"SELECT `key`, value FROM config ORDER BY `key`")
	if err != nil {
		return nil, fmt.Errorf("backup config: %w", err)
	}
	counts.Config = n

	return counts, nil
}

// ExportTable streams query results to a JSONL file using atomic write.
func ExportTable(ctx context.Context, db DBConn, dir, filename, query string, args ...any) (int, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("failed to get columns: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".backup-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	w := bufio.NewWriter(tmp)
	count, err := writeExportRows(rows, cols, w)
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

func writeExportRows(rows *sql.Rows, cols []string, w *bufio.Writer) (int, error) {
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
			row[col] = NormalizeExportValue(values[i])
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

func NormalizeExportValue(v interface{}) interface{} {
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
