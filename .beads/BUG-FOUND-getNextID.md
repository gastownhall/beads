# BUG FOUND: getNextID() uses alphabetical MAX instead of numerical

## Location
`internal/storage/sqlite/sqlite.go:60-84`, function `getNextID()`

## The Bug
```go
err := db.QueryRow("SELECT MAX(id) FROM issues").Scan(&maxID)
```

This uses alphabetical MAX on the text `id` column, not numerical MAX.

## Impact
When you have bd-1 through bd-10:
- Alphabetical sort: bd-1, bd-10, bd-2, bd-3, ... bd-9
- MAX(id) returns "bd-9" (alphabetically last)
- nextID is calculated as 10
- Creating a new issue tries to use bd-10, which already exists
- Result: UNIQUE constraint failed

## Reproduction
```bash
# After creating bd-1 through bd-10
./bd create "Test issue" -t task -p 1
# Error: failed to insert issue: UNIQUE constraint failed: issues.id
```

## The Fix

Option 1: Cast to integer in SQL (BEST)
```sql
SELECT MAX(CAST(SUBSTR(id, INSTR(id, '-') + 1) AS INTEGER)) FROM issues WHERE id LIKE 'bd-%'
```

Option 2: Pad IDs with zeros
- Change ID format from "bd-10" to "bd-0010"
- Alphabetical and numerical order match
- Breaks existing IDs

Option 3: Query all IDs and find max in Go
- Less efficient but more flexible
- Works with any ID format

## Recommended Solution

Option 1 with proper prefix handling:

```go
func getNextID(db *sql.DB) int {
	// Get prefix from config (default "bd")
	var prefix string
	err := db.QueryRow("SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&prefix)
	if err != nil || prefix == "" {
		prefix = "bd"
	}

	// Find max numeric ID for this prefix
	var maxNum sql.NullInt64
	query := `
		SELECT MAX(CAST(SUBSTR(id, LENGTH(?) + 2) AS INTEGER))
		FROM issues
		WHERE id LIKE ? || '-%'
	`
	err = db.QueryRow(query, prefix, prefix).Scan(&maxNum)
	if err != nil || !maxNum.Valid {
		return 1
	}

	return int(maxNum.Int64) + 1
}
```

## Workaround for Now

Manually specify IDs when creating issues:
```bash
# This won't work because auto-ID fails:
./bd create "Title" -t task -p 1

# Workaround - manually calculate next ID:
./bd list | grep -oE 'bd-[0-9]+' | sed 's/bd-//' | sort -n | tail -1
# Then add 1 and create with explicit ID in code
```

Or fix the bug first before continuing!

## Related to bd-9

This bug is EXACTLY the kind of distributed ID collision problem that bd-9 is designed to solve! But we should also fix the root cause.

## Created Issue

Should create: "Fix getNextID() to use numerical MAX instead of alphabetical"
- Type: bug
- Priority: 0 (critical - blocks all new issue creation)
- Blocks: bd-9 (can't create child issues)
