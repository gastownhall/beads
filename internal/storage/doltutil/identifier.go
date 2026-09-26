package doltutil

import "strings"

// QuoteIdentifierUnvalidated renders name as a backtick-quoted MySQL/Dolt
// identifier, doubling any embedded backtick so the name cannot break out of
// the quotes. Doubling is the complete escape inside a backtick-quoted
// identifier: no other character is special there, and identifiers cannot
// contain NUL.
//
// Use it in non-test code for every identifier that is not a compile-time
// constant or already validated. Database names returned by SHOW DATABASES are
// the motivating case: they reflect whatever created the database (typically an
// on-disk directory name), not a bd-controlled value, so interpolating one raw
// lets it terminate the identifier and append arbitrary SQL.
//
// Unvalidated names the contract, not a caveat: this helper accepts every
// input, which is what names bd does not control require. For a bd-minted name
// that must satisfy the identifier allowlist before the database or table is
// addressed, use the validating db.QuoteIdentifier
// (internal/storage/domain/db) instead. The two are not interchangeable:
// rejecting a SHOW DATABASES name would skip the candidate rather than address
// it, and escaping a bd-minted name would let an invalid one through.
//
// The result is already quoted; interpolate it with a bare %s rather than
// wrapping it in backticks again.
//
// Callers still need a `//nolint:gosec` on the surrounding fmt.Sprintf: G201
// fires on any non-constant format argument and cannot see that this function
// makes the interpolation safe.
func QuoteIdentifierUnvalidated(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
