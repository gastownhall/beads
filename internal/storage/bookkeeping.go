package storage

import "strings"

// IsBookkeepingTable reports whether table is one of beads' bookkeeping
// tables: clone-local or ephemeral state (wisps and every wisp_ table,
// leases, local_metadata, repo_mtimes, events) that is expected to carry
// uncommitted changes and is never reported as one. bd doctor and bd vc
// status both filter their "uncommitted changes" through this one predicate,
// so the same store cannot be clean by one and dirty by the other.
//
// The set is a static name list rather than a read of the store's own
// dolt_ignore rows, which is worth explaining because the reverse looks
// obvious. dolt_ignore does not mean "do not report changes to this table".
// Per doltdb.ShouldIgnoreDelta, "only newly added or dropped tables are
// matched against the patterns. Changes to already tracked tables are always
// included." A store that tracked these tables before their ignore rows
// existed therefore sees them dirty forever by design, and dolt_ignore has no
// say in it. Deriving the set from that table would borrow a mechanism that
// answers a different question, and would additionally require
// reimplementing Dolt's pattern syntax — doltdb.compilePattern treats '*' and
// '?' and '%' as wildcards, '_' as a literal, and matches case-sensitively,
// none of which is SQL LIKE. Silencing these tables is a beads decision about
// which tables are bookkeeping, so it belongs in beads code.
func IsBookkeepingTable(table string) bool {
	switch table {
	case "wisps", "leases", "local_metadata", "repo_mtimes", "events":
		return true
	}
	return strings.HasPrefix(table, "wisp_")
}
