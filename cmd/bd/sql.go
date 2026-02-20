package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
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
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		query := args[0]
		csvOutput, _ := cmd.Flags().GetBool("csv")

		if store == nil {
			FatalErrorRespectJSON("no database connection available (run 'bd init' first)")
		}

		db := store.UnderlyingDB()
		if db == nil {
			FatalErrorRespectJSON("underlying database not available")
		}

		ctx := rootCtx

		// Detect if it's a read query (SELECT, EXPLAIN, PRAGMA, SHOW, DESCRIBE, WITH)
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
				FatalErrorRespectJSON("query error: %v", err)
			}
			defer rows.Close()

			columns, err := rows.Columns()
			if err != nil {
				FatalErrorRespectJSON("getting columns: %v", err)
			}

			// Collect all rows
			allRows := make([]map[string]interface{}, 0)
			for rows.Next() {
				values := make([]interface{}, len(columns))
				valuePtrs := make([]interface{}, len(columns))
				for i := range values {
					valuePtrs[i] = &values[i]
				}

				if err := rows.Scan(valuePtrs...); err != nil {
					FatalErrorRespectJSON("scanning row: %v", err)
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
				FatalErrorRespectJSON("reading rows: %v", err)
			}

			if jsonOutput {
				outputJSON(allRows)
				return
			}

			if csvOutput {
				w := csv.NewWriter(os.Stdout)
				// Header
				if err := w.Write(columns); err != nil {
					FatalErrorRespectJSON("writing CSV header: %v", err)
				}
				for _, row := range allRows {
					record := make([]string, len(columns))
					for i, col := range columns {
						record[i] = fmt.Sprintf("%v", row[col])
					}
					if err := w.Write(record); err != nil {
						FatalErrorRespectJSON("writing CSV row: %v", err)
					}
				}
				w.Flush()
				if err := w.Error(); err != nil {
					FatalErrorRespectJSON("flushing CSV: %v", err)
				}
				return
			}

			// Table output
			if len(allRows) == 0 {
				fmt.Println("(0 rows)")
				return
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
		} else {
			// Write query
			CheckReadonly("sql")

			result, err := db.ExecContext(ctx, query)
			if err != nil {
				FatalErrorRespectJSON("exec error: %v", err)
			}

			affected, _ := result.RowsAffected()

			if jsonOutput {
				outputJSON(map[string]interface{}{
					"rows_affected": affected,
				})
				return
			}

			fmt.Printf("OK, %d rows affected\n", affected)
		}
	},
}

func init() {
	sqlCmd.Flags().Bool("csv", false, "Output results in CSV format")

	// Register as a read-only command for SELECT queries.
	// Write queries will be caught by CheckReadonly.
	// We don't add to readOnlyCommands because it can do writes too.

	rootCmd.AddCommand(sqlCmd)
}
