package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
)

var sqlCmd = &cobra.Command{
	Use:     "sql <query>",
	GroupID: "maint",
	Short:   "Execute raw SQL against the beads database",
	Long: `Execute a raw SQL query against the underlying database (SQLite or Dolt).

Useful for debugging, maintenance, and working around bugs in higher-level commands.

Examples:
  bd sql 'SELECT COUNT(*) FROM issues'
  bd sql 'SELECT id, title FROM issues WHERE status = "open" LIMIT 5'
  bd sql 'DELETE FROM dirty_issues WHERE issue_id = "bd-abc123"'
  bd sql --csv 'SELECT id, title, status FROM issues'

The query is passed directly to the database. SELECT queries return results as a
table (or JSON/CSV with --json/--csv). Non-SELECT queries (INSERT, UPDATE, DELETE)
report the number of rows affected.

WARNING: Direct database access bypasses the storage layer. Use with caution.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("sql")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		query := args[0]
		csvOutput, _ := cmd.Flags().GetBool("csv")

		if usesProxiedServer() {
			return runSQLProxiedServer(rootCtx, query, csvOutput)
		}

		if store == nil {
			return HandleErrorRespectJSON("no database connection available (%s)", diagHint())
		}

		ctx := rootCtx

		if !rawSQLIsRead(query) {
			CheckReadonly("sql")
		}

		result, err := executeRawSQL(ctx, storage.UnwrapStore(store), query)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if result.Read {
			if jsonOutput {
				return outputJSON(result.Rows)
			}

			if csvOutput {
				w := csv.NewWriter(os.Stdout)
				if err := w.Write(result.Columns); err != nil {
					return HandleErrorRespectJSON("writing CSV header: %v", err)
				}
				for _, row := range result.Rows {
					record := make([]string, len(result.Columns))
					for i, col := range result.Columns {
						record[i] = fmt.Sprintf("%v", row[col])
					}
					if err := w.Write(record); err != nil {
						return HandleErrorRespectJSON("writing CSV row: %v", err)
					}
				}
				w.Flush()
				if err := w.Error(); err != nil {
					return HandleErrorRespectJSON("flushing CSV: %v", err)
				}
				return nil
			}

			if len(result.Rows) == 0 {
				fmt.Println("(0 rows)")
				return nil
			}

			// Calculate column widths
			widths := make([]int, len(result.Columns))
			for i, col := range result.Columns {
				widths[i] = len(col)
			}
			for _, row := range result.Rows {
				for i, col := range result.Columns {
					s := fmt.Sprintf("%v", row[col])
					if len(s) > widths[i] {
						widths[i] = len(s)
					}
				}
			}

			// Cap column widths at 60 chars for readability
			for i := range widths {
				if widths[i] > 60 {
					widths[i] = 60
				}
			}

			// Print header
			for i, col := range result.Columns {
				if i > 0 {
					fmt.Print(" | ")
				}
				fmt.Printf("%-*s", widths[i], col)
			}
			fmt.Println()

			// Print separator
			for i := range result.Columns {
				if i > 0 {
					fmt.Print("-+-")
				}
				fmt.Print(strings.Repeat("-", widths[i]))
			}
			fmt.Println()

			// Print rows
			for _, row := range result.Rows {
				for i, col := range result.Columns {
					if i > 0 {
						fmt.Print(" | ")
					}
					s := fmt.Sprintf("%v", row[col])
					if len(s) > 60 {
						s = s[:57] + "..."
					}
					fmt.Printf("%-*s", widths[i], s)
				}
				fmt.Println()
			}

			fmt.Printf("(%d rows)\n", len(result.Rows))
			return nil
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"rows_affected": result.RowsAffected,
			})
		}

		fmt.Printf("OK, %d rows affected\n", result.RowsAffected)
		return nil
	},
}

func init() {
	sqlCmd.Flags().Bool("csv", false, "Output results in CSV format")

	// Register as a read-only command for SELECT queries.
	// Write queries will be caught by CheckReadonly.
	// We don't add to readOnlyCommands because it can do writes too.

	rootCmd.AddCommand(sqlCmd)
}

func rawSQLIsRead(query string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(trimmed, "SELECT") ||
		strings.HasPrefix(trimmed, "EXPLAIN") ||
		strings.HasPrefix(trimmed, "PRAGMA") ||
		strings.HasPrefix(trimmed, "SHOW") ||
		strings.HasPrefix(trimmed, "DESCRIBE") ||
		strings.HasPrefix(trimmed, "WITH")
}

func executeRawSQL(ctx context.Context, store storage.Storage, query string) (storage.RawSQLResult, error) {
	if executor, ok := store.(storage.RawSQLExecutor); ok {
		return executor.ExecuteRawSQL(ctx, query)
	}
	accessor, ok := store.(storage.RawDBAccessor)
	if !ok {
		return storage.RawSQLResult{}, fmt.Errorf("storage backend does not support raw DB access")
	}
	db := accessor.UnderlyingDB()
	if db == nil {
		return storage.RawSQLResult{}, fmt.Errorf("underlying database not available")
	}
	return executeRawSQLOnDB(ctx, db, query)
}

func executeRawSQLOnDB(ctx context.Context, db *sql.DB, query string) (storage.RawSQLResult, error) {
	if !rawSQLIsRead(query) {
		result, err := db.ExecContext(ctx, query)
		if err != nil {
			return storage.RawSQLResult{}, fmt.Errorf("exec error: %w", err)
		}
		affected, _ := result.RowsAffected()
		return storage.RawSQLResult{RowsAffected: affected, Read: false}, nil
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return storage.RawSQLResult{}, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	result, err := scanRawSQLRows(rows)
	if err != nil {
		return storage.RawSQLResult{}, err
	}
	result.Read = true
	return result, nil
}

func scanRawSQLRows(rows *sql.Rows) (storage.RawSQLResult, error) {
	columns, err := rows.Columns()
	if err != nil {
		return storage.RawSQLResult{}, fmt.Errorf("getting columns: %w", err)
	}

	allRows := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return storage.RawSQLResult{}, fmt.Errorf("scanning row: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		allRows = append(allRows, row)
	}
	if err := rows.Err(); err != nil {
		return storage.RawSQLResult{}, fmt.Errorf("reading rows: %w", err)
	}
	return storage.RawSQLResult{Columns: columns, Rows: allRows, Read: true}, nil
}
