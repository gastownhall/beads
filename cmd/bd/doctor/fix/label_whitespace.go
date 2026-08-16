package fix

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// labelTables are the label tables scanned for whitespace damage, mirroring
// issueops.WispTableRouting.
var labelTables = []string{"labels", "wisp_labels"}

// LabelWhitespaceAnomalies summarizes one table's labels that no filter can
// match (#5812).
//
// Both classes are unambiguous corruption: bd normalizes labels on every FILTER
// path but historically did not on any WRITE path, so a stored " a" can never
// match its own `--label a` filter and a filtered list is silently short.
//
// A label whose trimmed form still contains whitespace (`--labels 'a b'` storing
// one label rather than two) is deliberately NOT reported here. That case turns
// on what a space between labels should mean, which is a semantic question this
// change does not settle — and "good first issue" is a legitimate multi-word
// label, so it cannot be called corruption without deciding that first.
type LabelWhitespaceAnomalies struct {
	Table     string
	Untrimmed []LabelRow // label differs from its trimmed form
	Blank     []LabelRow // label is empty or whitespace-only
}

// LabelRow identifies one damaged label by the issue carrying it.
type LabelRow struct {
	IssueID string
	Label   string
}

// LabelWhitespaceClass is how ScanLabelWhitespace classifies a single label.
type LabelWhitespaceClass int

const (
	// LabelClean is matchable by its own filter.
	LabelClean LabelWhitespaceClass = iota
	// LabelBlank is empty or whitespace-only.
	LabelBlank
	// LabelUntrimmed has leading or trailing whitespace.
	LabelUntrimmed
)

// ClassifyLabelWhitespace reports whether a label is one no filter can match,
// using strings.TrimSpace so that tabs, newlines and Unicode spaces are treated
// exactly as utils.NormalizeLabels treats them.
//
// A label whose trimmed form still contains whitespace is LabelClean here: it is
// matchable by an identical filter string, so it is not corruption by this
// definition, whatever else it may be.
func ClassifyLabelWhitespace(label string) LabelWhitespaceClass {
	trimmed := strings.TrimSpace(label)
	switch {
	case trimmed == "":
		return LabelBlank
	case trimmed != label:
		return LabelUntrimmed
	default:
		return LabelClean
	}
}

// Total returns the number of damaged rows across both classes.
func (a LabelWhitespaceAnomalies) Total() int {
	return len(a.Untrimmed) + len(a.Blank)
}

// ScanLabelWhitespace reports labels carrying whitespace damage in both label
// tables. Tables absent from the schema are skipped; only tables with anomalies
// appear in the result.
//
// Classification happens in Go rather than SQL so that tabs, newlines and
// Unicode spaces are caught the same way strings.TrimSpace catches them —
// a TRIM()-based predicate would silently miss them.
func ScanLabelWhitespace(ctx context.Context, db *sql.DB) ([]LabelWhitespaceAnomalies, error) {
	var out []LabelWhitespaceAnomalies
	for _, table := range labelTables {
		exists, err := labelTableExists(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if !exists {
			continue
		}

		a := LabelWhitespaceAnomalies{Table: table}
		//nolint:gosec // G201: table is a hardcoded constant, never user input.
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT issue_id, label FROM %s`, table))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		for rows.Next() {
			var issueID string
			var label sql.NullString
			if err := rows.Scan(&issueID, &label); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("%s: %w", table, err)
			}
			if !label.Valid {
				continue
			}
			row := LabelRow{IssueID: issueID, Label: label.String}
			switch ClassifyLabelWhitespace(label.String) {
			case LabelBlank:
				a.Blank = append(a.Blank, row)
			case LabelUntrimmed:
				a.Untrimmed = append(a.Untrimmed, row)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if a.Total() > 0 {
			sortLabelRows(a.Untrimmed)
			sortLabelRows(a.Blank)
			out = append(out, a)
		}
	}
	return out, nil
}

// sortLabelRows gives the scan a stable order so doctor output does not churn
// between runs on an unchanged database.
func sortLabelRows(rows []LabelRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].IssueID != rows[j].IssueID {
			return rows[i].IssueID < rows[j].IssueID
		}
		return rows[i].Label < rows[j].Label
	})
}

func labelTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
		table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
