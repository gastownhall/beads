// Package sqlclass classifies raw SQL statements for `bd sql` with the same
// MySQL grammar the Dolt server parses, so callers can route a statement to a
// read transaction, a committing exec, or a committing query without prefix
// scanning.
package sqlclass

import (
	"strings"

	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// Kind says how a single SQL statement must be executed so that writes are
// committed and result sets are never dropped.
type Kind int

const (
	// Read changes nothing and returns a result set: run it in a read
	// transaction and render its rows.
	Read Kind = iota
	// Write changes data and returns no result set: execute it in a
	// committing transaction and report the rows affected.
	Write
	// Mixed may change data and may also return a result set (CALL, EXPLAIN
	// ANALYZE of a write, RETURNING, a read that calls a Dolt function that
	// may write, or anything the parser cannot classify): run it in a
	// committing transaction and render any rows it returns.
	Mixed
)

// String returns the kind's name for test and diagnostic output.
func (k Kind) String() string {
	switch k {
	case Read:
		return "read"
	case Write:
		return "write"
	default:
		return "mixed"
	}
}

// Classify classifies a single SQL statement by its main statement, so CTEs
// (WITH [RECURSIVE] name(cols) AS (...)), comments, and string literals that
// contain keywords do not affect the result. A read that calls a dolt_
// function outside readOnlyDoltFunctions anywhere in its parsed tree is
// Mixed: the servers bd supports expose Dolt's writes (DOLT_COMMIT,
// DOLT_RESET, ...) only as procedures, but a server that exposed one as a
// function would run the write from a SELECT. Input the parser rejects is
// Mixed: it may still be valid for the server, and Mixed never discards rows
// or rolls back writes.
func Classify(query string) Kind {
	stmt, err := sqlparser.Parse(query)
	if err != nil || stmt == nil {
		return Mixed
	}
	kind := classify(stmt)
	if kind == Read && callsDoltWriteFunction(stmt) {
		return Mixed
	}
	return kind
}

// readOnlyDoltFunctions are the dolt_ scalar functions the Dolt engine
// registers that only read. Any other dolt_ function, including one a future
// Dolt adds, may write and makes a read Mixed until it is listed here.
var readOnlyDoltFunctions = map[string]bool{
	"dolt_hashof":         true,
	"dolt_hashof_table":   true,
	"dolt_hashof_db":      true,
	"dolt_merge_base":     true,
	"dolt_version":        true,
	"dolt_storage_format": true,
	"dolt_join_cost":      true,
}

// callsDoltWriteFunction reports whether stmt calls a dolt_ function outside
// readOnlyDoltFunctions, in any clause and with any qualifier. Table functions
// (dolt_log(), dolt_diff(...)) and system tables are not function calls and
// do not match.
func callsDoltWriteFunction(stmt sqlparser.Statement) bool {
	found := false
	var visit sqlparser.Visit
	visit = func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.FuncExpr:
			if isDoltWriteFunction(n.Name.Lowered()) {
				found = true
			}
		// sqlparser.Walk skips some clauses that can hold a call, so walk
		// them here. Among them are a set operation's own WITH, ORDER BY and
		// LIMIT and a SELECT's named WINDOW clause.
		case *sqlparser.SetOp:
			_ = sqlparser.Walk(visit, n.With, n.OrderBy, n.Limit)
		case *sqlparser.Select:
			_ = sqlparser.Walk(visit, n.Window)
		// SubstrExpr.walkSubtree returns before FROM and FOR when the string
		// is a literal: SUBSTRING('abc' FROM f() FOR g()).
		case *sqlparser.SubstrExpr:
			if n.Name == nil {
				_ = sqlparser.Walk(visit, n.From, n.To)
			}
		}
		return !found, nil
	}
	_ = sqlparser.Walk(visit, stmt)
	return found
}

func isDoltWriteFunction(name string) bool {
	return strings.HasPrefix(name, "dolt_") && !readOnlyDoltFunctions[name]
}

func classify(stmt sqlparser.Statement) Kind {
	switch s := stmt.(type) {
	case *sqlparser.Select:
		if s.Into != nil {
			return Write
		}
		return Read
	case *sqlparser.SetOp, *sqlparser.Show, *sqlparser.OtherRead:
		return Read
	case *sqlparser.Explain:
		if !s.Analyze {
			return Read
		}
		if s.Statement != nil && classify(s.Statement) == Read {
			return Read
		}
		return Mixed
	case *sqlparser.Insert:
		return writeUnlessReturning(len(s.Returning))
	case *sqlparser.Update:
		return writeUnlessReturning(len(s.Returning))
	case *sqlparser.Delete:
		return writeUnlessReturning(len(s.Returning))
	case *sqlparser.DDL, *sqlparser.AlterTable, *sqlparser.DBDDL, *sqlparser.Set, *sqlparser.Use:
		return Write
	default:
		return Mixed
	}
}

func writeUnlessReturning(returning int) Kind {
	if returning > 0 {
		return Mixed
	}
	return Write
}
