package main

import (
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

		if !usesSQLServer() {
			return HandleError("'bd sql' is not yet supported in embedded mode")
		}
		query := args[0]
		csvOutput, _ := cmd.Flags().GetBool("csv")

		if store == nil {
			return HandleErrorRespectJSON("no database connection available (%s)", diagHint())
		}

		accessor, ok := storage.UnwrapStore(store).(storage.RawDBAccessor)
		if !ok {
			return HandleErrorRespectJSON("storage backend does not support raw DB access")
		}
		db := accessor.UnderlyingDB()
		if db == nil {
			return HandleErrorRespectJSON("underlying database not available")
		}

		ctx := rootCtx

		trimmed := strings.TrimSpace(strings.ToUpper(query))
		isRead := strings.HasPrefix(trimmed, "SELECT") ||
			strings.HasPrefix(trimmed, "EXPLAIN") ||
			strings.HasPrefix(trimmed, "PRAGMA") ||
			strings.HasPrefix(trimmed, "SHOW") ||
			strings.HasPrefix(trimmed, "DESCRIBE") ||
			strings.HasPrefix(trimmed, "WITH")

		if isRead {
			rows, err := db.QueryContext(ctx, query)
			if err != nil {
				return HandleErrorRespectJSON("query error: %v", err)
			}
			defer rows.Close()

			columns, err := rows.Columns()
			if err != nil {
				return HandleErrorRespectJSON("getting columns: %v", err)
			}

			allRows := make([]map[string]interface{}, 0)
			for rows.Next() {
				values := make([]interface{}, len(columns))
				valuePtrs := make([]interface{}, len(columns))
				for i := range values {
					valuePtrs[i] = &values[i]
				}

				if err := rows.Scan(valuePtrs...); err != nil {
					return HandleErrorRespectJSON("scanning row: %v", err)
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
				return HandleErrorRespectJSON("reading rows: %v", err)
			}

			if jsonOutput {
				return outputJSON(allRows)
			}

			if csvOutput {
				w := csv.NewWriter(os.Stdout)
				if err := w.Write(columns); err != nil {
					return HandleErrorRespectJSON("writing CSV header: %v", err)
				}
				for _, row := range allRows {
					record := make([]string, len(columns))
					for i, col := range columns {
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

			if len(allRows) == 0 {
				fmt.Println("(0 rows)")
				return nil
			}

			// Calculate column widths
			widths := make([]int, len(columns))
			for i, col := range columns {
				widths[i] = len(col)
			}
			for _, row := range allRows {
				for i, col := range columns {
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
			for i, col := range columns {
				if i > 0 {
					fmt.Print(" | ")
				}
				fmt.Printf("%-*s", widths[i], col)
			}
			fmt.Println()

			// Print separator
			for i := range columns {
				if i > 0 {
					fmt.Print("-+-")
				}
				fmt.Print(strings.Repeat("-", widths[i]))
			}
			fmt.Println()

			// Print rows
			for _, row := range allRows {
				for i, col := range columns {
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

			fmt.Printf("(%d rows)\n", len(allRows))
			return nil
		}

		CheckReadonly("sql")

		result, err := db.ExecContext(ctx, query)
		if err != nil {
			return HandleErrorRespectJSON("exec error: %v", err)
		}

		affected, _ := result.RowsAffected()

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"rows_affected": affected,
			})
		}

		fmt.Printf("OK, %d rows affected\n", affected)
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
