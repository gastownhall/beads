package dolt

// Test-only answers for the named commit lock (GH#8590): the scripted drivers
// in this package's boundary tests consult commitLockTestAnswer before their
// own switch, so the GET_LOCK/RELEASE_LOCK statements the commit planes now
// issue resolve to a granted lock instead of falling into each driver's
// default (empty) result set, which the lock acquire reads as "no rows".

import (
	"database/sql/driver"
	"io"
	"strings"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// expectCommitLockGrant and expectCommitLockRelease queue the sqlmock
// expectations for the named commit lock statements (GH#8590) the dolt
// package's commit planes now issue around their DOLT_ADD/DOLT_COMMIT pairs:
// GET_LOCK granted immediately, RELEASE_LOCK confirmed. Every sqlmock test in
// this package runs with QueryMatcherRegexp, so the prefixes match.
func expectCommitLockGrant(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT GET_LOCK`).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
}

func expectCommitLockRelease(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT RELEASE_LOCK`).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

type commitLockTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *commitLockTestRows) Columns() []string { return r.columns }
func (r *commitLockTestRows) Close() error      { return nil }
func (r *commitLockTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

// commitLockTestAnswer reports the rows a scripted driver should return for a
// commit-lock statement, and whether the query was one at all. GET_LOCK
// answers 1 (granted immediately) and RELEASE_LOCK answers 1 (released).
func commitLockTestAnswer(query string) (driver.Rows, bool) {
	switch {
	case strings.Contains(query, "GET_LOCK"):
		return &commitLockTestRows{columns: []string{"locked"}, values: [][]driver.Value{{int64(1)}}}, true
	case strings.Contains(query, "RELEASE_LOCK"):
		return &commitLockTestRows{columns: []string{"released"}, values: [][]driver.Value{{int64(1)}}}, true
	}
	return nil, false
}
