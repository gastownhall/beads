// Package sqlclass classifies raw SQL statements for `bd sql` with the same
// MySQL grammar the Dolt server parses, so callers can route a statement to a
// read transaction, a committing exec, or a committing query without prefix
// scanning.
package sqlclass

import (
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
	// ANALYZE of a write, RETURNING, or anything the parser cannot classify):
	// run it in a committing transaction and render any rows it returns.
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
// contain keywords do not affect the result. Input the parser rejects is
// Mixed: it may still be valid for the server, and Mixed never discards rows
// or rolls back writes.
func Classify(query string) Kind {
	stmt, err := sqlparser.Parse(query)
	if err != nil || stmt == nil {
		return Mixed
	}
	return classify(stmt)
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
