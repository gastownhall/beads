package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// collationParityOK reports whether a Postgres database-level collation orders text
// by byte / Unicode code point — the ordering the shared SQL layer needs to match
// Dolt's utf8mb4_0900_bin oracle. C and POSIX are exact code-point order, and the
// C.* family (e.g. C.UTF-8) also sorts by code point. glibc/ICU locales such as
// en_US.UTF-8 sort linguistically and silently diverge from Dolt.
func collationParityOK(collation string) bool {
	c := strings.ToUpper(strings.TrimSpace(collation))
	return c == "C" || c == "POSIX" || strings.HasPrefix(c, "C.")
}

// databaseCollation returns the connected database's lc_collate.
func databaseCollation(ctx context.Context, db *sql.DB) (string, error) {
	var collation string
	if err := db.QueryRowContext(ctx, "SHOW lc_collate").Scan(&collation); err != nil {
		return "", err
	}
	return collation, nil
}

// warnOnCollationDivergence prints a prominent stderr warning when the connected
// database is not code-point ordered. Text columns use the database default
// collation — per-column COLLATE "C" was dropped because it breaks the shared
// recursive CTEs (see schema.go) — so a non-C/POSIX database makes every text
// ORDER BY the shared layer emits (bd list, bd ready, label ordering) sort
// linguistically and diverge from Dolt with no error and no other signal.
//
// It is best-effort: a query failure is swallowed so the check can never block
// opening the store. Running from Provision means `bd init --backend=postgres`
// surfaces the requirement at setup and every later open re-warns until the
// operator recreates the database with a code-point collation. This mirrors the
// Dolt backend's open-time stderr warnings.
func warnOnCollationDivergence(ctx context.Context, db *sql.DB) {
	collation, err := databaseCollation(ctx, db)
	if err != nil || collationParityOK(collation) {
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: Postgres database collation is %q, not C/POSIX. Text ORDER BY (bd list, bd ready, labels) sorts linguistically and diverges from Dolt's code-point order.\n", collation)
	fmt.Fprintln(os.Stderr, "  Recreate the database with a code-point collation, e.g. CREATE DATABASE ... TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C';")
}
