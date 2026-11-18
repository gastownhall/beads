package storage

import "strings"

// IsBookkeepingTable reports whether table is one of beads' internal
// bookkeeping tables: the machine-local and audit surfaces bd maintains
// outside the issue-data model (wisps, leases, local_metadata, repo_mtimes,
// events, and the wisp_* scratch family).
//
// It is the one shared filter behind every "uncommitted changes"
// presentation — `bd doctor`'s Dolt Status / Dolt Locks checks and `bd vc
// status` — so the two can never drift apart again the way doctor's own two
// checks once did (one excluded only wisp tables while the other excluded
// the full set, so the same clean store was clean by one check and dirty by
// the other, permanently).
//
// The filter is a static name list rather than a read of the store's own
// dolt_ignore rows, which is worth explaining because the reverse looks
// obvious. dolt_ignore does not mean "do not report changes to this table".
// Per doltdb.ShouldIgnoreDelta, "only newly added or dropped tables are
// matched against the patterns. Changes to already tracked tables are always
// included." Every table listed here is tracked, so Dolt reports it dirty
// forever by design and dolt_ignore has no say in it. Deriving the skip-set
// from that table would therefore borrow a mechanism that answers a different
// question, and would additionally require reimplementing Dolt's pattern
// syntax — doltdb.compilePattern treats '*' and '?' and '%' as wildcards, '_'
// as a literal, and matches case-sensitively, none of which is SQL LIKE.
//
// Silencing these tables is a beads decision about which tables are
// bookkeeping, so it belongs in beads code.
func IsBookkeepingTable(table string) bool {
	switch table {
	case "wisps", "leases", "local_metadata", "repo_mtimes", "events":
		return true
	}
	return strings.HasPrefix(table, "wisp_")
}
