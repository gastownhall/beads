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
// contain keywords do not affect the result. A statement whose tree calls a
// Dolt function that may write is Mixed even when its main verb reads,
// because Dolt exposes mutating procedures as SELECT-callable functions.
// Input the parser rejects is Mixed: it may still be valid for the server,
// and Mixed never discards rows or rolls back writes.
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

// readOnlyDoltFunctions lists the dolt_-prefixed functions the pinned Dolt
// engine registers that only read. Every other dolt_ name is treated as a
// write: the engine exposes mutating procedures as SELECT-callable functions,
// and a name it does not have today may be one a newer server adds.
var readOnlyDoltFunctions = map[string]bool{
	"dolt_hashof":         true,
	"dolt_hashof_table":   true,
	"dolt_hashof_db":      true,
	"dolt_merge_base":     true,
	"dolt_version":        true,
	"dolt_storage_format": true,
	"dolt_join_cost":      true,
}

// callsDoltWriteFunction reports whether the parsed statement calls a
// dolt_-prefixed function outside readOnlyDoltFunctions anywhere in its
// tree: Dolt exposes mutating procedures as SELECT-callable functions, and
// a read transaction would roll their writes back while a read routing
// skips the read-only guard. CTE bodies, subqueries, WHERE clauses and the
// statement inside EXPLAIN all count. Letter case and backtick quoting are
// absorbed by Lowered, and a database qualifier is ignored. System tables
// (dolt_log, dolt_conflicts) and table functions (dolt_diff(...)) are not
// function calls, and comments and string literals are not in the parsed
// tree, so none of them trip it.
func callsDoltWriteFunction(stmt sqlparser.Statement) bool {
	found := false
	visit := func(node sqlparser.SQLNode) (kontinue bool, err error) {
		fn, ok := node.(*sqlparser.FuncExpr)
		if !ok {
			return true, nil
		}
		name := fn.Name.Lowered()
		if strings.HasPrefix(name, "dolt_") && !readOnlyDoltFunctions[name] {
			found = true
			return false, nil
		}
		return true, nil
	}
	// The visit function never returns an error, so Walk's error is always nil.
	_ = sqlparser.Walk(visit, stmt)
	return found
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
