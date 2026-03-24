// Package versioncontrolops provides shared implementations for Dolt version
// control operations (branches, status, log, merge, remotes). Unlike issueops,
// these functions accept a DBConn rather than a *sql.Tx because Dolt stored
// procedures (CALL DOLT_BRANCH, CALL DOLT_MERGE, etc.) cannot run inside
// explicit SQL transactions.
package versioncontrolops

import (
	"context"
	"database/sql"
)

// DBConn is the minimal interface satisfied by *sql.DB and *sql.Conn.
// It provides query and exec methods without transaction semantics.
type DBConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
