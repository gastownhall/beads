package sqlite

import "strings"

// dsn builds a modernc.org/sqlite DSN for a database file path. Pragmas:
//   - foreign_keys(1): FK enforcement (off by default in SQLite).
//   - journal_mode(WAL): readers don't block the writer and vice versa, so a read
//     no longer collides with an in-flight write.
//   - busy_timeout(5000): on lock contention, wait up to 5s for the lock instead of
//     immediately surfacing a raw "database is locked" (SQLITE_BUSY). This is the
//     cross-process analog of Dolt's transparent serialization retry; combined with
//     the single-connection pool (see sqliteDialect.Open) it covers both intra- and
//     inter-process contention.
//   - _txlock=immediate: writers take the write lock up front and fail fast rather
//     than deadlocking mid-transaction on lock upgrade.
//
// _time_format=datetime is required for Dolt parity, not a nicety. Without it modernc
// binds every time.Time through t.String() ("2026-07-09 12:34:56.123 +0000 UTC"), a
// shape SQLite's date functions cannot parse — so the DATE_FORMAT(created_at,…) the
// counts mega-query renders (translated to strftime by sqlitedialect) returns NULL and
// deps_json exposes a zero created_at instead of the real timestamp. datetime stores
// "2026-07-09 12:34:56", which strftime yields as "2026-07-09T12:34:56Z", giving the
// same whole-second GRANULARITY as Dolt/MySQL datetime(0) and unifying bound timestamps
// with the DEFAULT CURRENT_TIMESTAMP rows into one sortable, strftime-parseable format.
//
// Note the granularity matches but the sub-second RULE does not: Go's Format truncates
// the fraction, whereas Dolt/MySQL datetime(0) round half-up (Postgres keeps microseconds).
// This sub-second divergence is an accepted, documented waiver — the portable contract is
// a whole-second round-trip (see conformance.testAuditImportCommentSubSecond); forcing
// round-down parity would only discard the SQL backends' native precision.
func dsn(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate&_time_format=datetime"
}
