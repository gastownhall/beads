package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func runSQLProxiedServer(ctx context.Context, query string, csvOutput bool, database string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	multiStatement := isMultiStatementSQL(query)

	if !multiStatement && sqlQueryIsRead(query) {
		result, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*domain.RawSQLResult, error) {
			if database != "" {
				if err := uw.SwitchDatabase(ctx, database); err != nil {
					return nil, err
				}
			}
			return uw.RawSQLUseCase().Query(ctx, query)
		})
		if err != nil {
			return HandleErrorRespectJSON("query error: %v", err)
		}
		return renderRawSQLResult(result, csvOutput)
	}

	CheckReadonly("sql")

	affected, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (int64, string, error) {
		if database != "" {
			if err := uw.SwitchDatabase(ctx, database); err != nil {
				return 0, "", err
			}
		}
		affected, err := uw.RawSQLUseCase().Exec(ctx, query)
		if err != nil {
			return 0, "", err
		}
		return affected, "bd sql: " + query, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("exec error: %v", err)
	}

	if multiStatement {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status": "ok",
			})
		}
		fmt.Println("OK")
		return nil
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"rows_affected": affected,
		})
	}

	fmt.Printf("OK, %d rows affected\n", affected)
	return nil
}

func isMultiStatementSQL(query string) bool {
	return topLevelStatementCount(query) > 1
}

func topLevelStatementCount(query string) int {
	count := 0
	hasContent := false
	n := len(query)
	for i := 0; i < n; {
		c := query[i]
		switch c {
		case '\'', '"', '`':
			quote := c
			i++
			for i < n {
				if query[i] == '\\' && quote != '`' {
					i += 2
					continue
				}
				if query[i] == quote {
					if i+1 < n && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			hasContent = true
		case '-':
			if i+1 < n && query[i+1] == '-' && (i+2 >= n || query[i+2] == ' ' || query[i+2] == '\t' || query[i+2] == '\n' || query[i+2] == '\r') {
				for i < n && query[i] != '\n' && query[i] != '\r' {
					i++
				}
			} else {
				hasContent = true
				i++
			}
		case '#':
			for i < n && query[i] != '\n' && query[i] != '\r' {
				i++
			}
		case '/':
			if i+1 < n && query[i+1] == '*' {
				i += 2
				for i+1 < n && !(query[i] == '*' && query[i+1] == '/') {
					i++
				}
				i += 2
			} else {
				hasContent = true
				i++
			}
		case ';':
			if hasContent {
				count++
				hasContent = false
			}
			i++
		case ' ', '\t', '\n', '\r':
			i++
		default:
			hasContent = true
			i++
		}
	}
	if hasContent {
		count++
	}
	return count
}

func sqlQueryIsRead(query string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	switch {
	case strings.HasPrefix(trimmed, "SELECT"),
		strings.HasPrefix(trimmed, "EXPLAIN"),
		strings.HasPrefix(trimmed, "PRAGMA"),
		strings.HasPrefix(trimmed, "SHOW"),
		strings.HasPrefix(trimmed, "DESCRIBE"):
		return true
	case strings.HasPrefix(trimmed, "WITH"):
		return withOuterStatementIsRead(trimmed)
	default:
		return false
	}
}

func withOuterStatementIsRead(upperTrimmed string) bool {
	depth := 0
	var quote byte
	closedCTE := false
	for i := 0; i < len(upperTrimmed); i++ {
		c := upperTrimmed[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closedCTE = true
			}
		case ' ', '\t', '\n', '\r', ',':
			if c == ',' && depth == 0 {
				closedCTE = false
			}
		default:
			if depth == 0 && closedCTE {
				rest := strings.TrimLeft(upperTrimmed[i:], " \t\n\r")
				return strings.HasPrefix(rest, "SELECT") ||
					strings.HasPrefix(rest, "EXPLAIN")
			}
		}
	}
	return true
}

func renderRawSQLResult(result *domain.RawSQLResult, csvOutput bool) error {
	columns := result.Columns

	if jsonOutput {
		out := make([]map[string]interface{}, 0, len(result.Rows))
		for _, row := range result.Rows {
			m := make(map[string]interface{}, len(columns))
			for i, col := range columns {
				m[col] = row[i]
			}
			out = append(out, m)
		}
		return outputJSON(out)
	}

	if csvOutput {
		w := csv.NewWriter(os.Stdout)
		if err := w.Write(columns); err != nil {
			return HandleErrorRespectJSON("writing CSV header: %v", err)
		}
		for _, row := range result.Rows {
			record := make([]string, len(columns))
			for i := range columns {
				record[i] = fmt.Sprintf("%v", row[i])
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

	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = len(col)
	}
	for _, row := range result.Rows {
		for i := range columns {
			s := fmt.Sprintf("%v", row[i])
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
	}
	for i := range widths {
		if widths[i] > 60 {
			widths[i] = 60
		}
	}

	for i, col := range columns {
		if i > 0 {
			fmt.Print(" | ")
		}
		fmt.Printf("%-*s", widths[i], col)
	}
	fmt.Println()

	for i := range columns {
		if i > 0 {
			fmt.Print("-+-")
		}
		fmt.Print(strings.Repeat("-", widths[i]))
	}
	fmt.Println()

	for _, row := range result.Rows {
		for i := range columns {
			if i > 0 {
				fmt.Print(" | ")
			}
			s := fmt.Sprintf("%v", row[i])
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
