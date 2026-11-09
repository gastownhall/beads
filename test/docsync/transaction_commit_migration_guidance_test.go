package docsync

import "testing"

const (
	// migration0040NothingToCommitHeading is the troubleshooting entry for a
	// schema migration stuck at version 39 because automatic transaction
	// commits consumed 0040's in-body commit (GH#4800). docs/architecture/
	// dolt.md links it as migration0040NothingToCommitRoute; the markdown
	// link checkers do not resolve in-page anchors, so both ends are pinned.
	migration0040NothingToCommitHeading = `### Migration 0040 fails with "nothing to commit"`
	migration0040NothingToCommitRoute   = "/reference/troubleshooting#migration-0040-fails-with-nothing-to-commit"
)

// TestTransactionCommitMigrationGuidance_4800 guards the recovery guidance
// for a database stuck at schema version 39 by Dolt's automatic transaction
// commits: how to read the setting, the consented recovery with a bd that
// suspends it, the manual workaround for older builds (which needs the
// partial-0040 repair, first released in 1.2.0), and that the Dolt 2.6.0
// minimum is a separate requirement.
func TestTransactionCommitMigrationGuidance_4800(t *testing.T) {
	doc := readRepoDoc(t, "docs", "reference", "troubleshooting.md")
	section, ok := docSubtree(doc, migration0040NothingToCommitHeading)
	if !ok {
		t.Fatalf("docs/reference/troubleshooting.md missing the %q section", migration0040NothingToCommitHeading)
	}
	where := "troubleshooting.md " + migration0040NothingToCommitHeading
	requirePhrases(t, where, section,
		// The symptom a reader searches for.
		"0040_ignored_tables_also_nonlocal_tables.up.sql: Error 1105: nothing to commit",
		"schema version 39",
		// Setting diagnostics.
		"behavior.dolt_transaction_commit: true",
		"SELECT @@GLOBAL.dolt_transaction_commit, @@SESSION.dolt_transaction_commit;",
		// The version floor is separate.
		"separate from the Dolt 2.6.0 minimum",
		// Consented recovery with a bd that suspends the setting.
		"turns the setting off for its own migration session and restores it afterwards",
		"never changes the server-wide setting",
		"not migrated without consent",
		"bd migrate schema",
		// The older-build workaround and its limits.
		"bd 1.2.0 or newer is required",
		"SET @@GLOBAL.dolt_transaction_commit = 0;",
		"affects new sessions of every client",
		"from a new bd process",
		"restore the previous value",
		"does not survive a server restart",
		"takes effect only after a restart",
	)

	arch := readRepoDoc(t, "docs", "architecture", "dolt.md")
	requirePhrases(t, "architecture/dolt.md", arch, "("+migration0040NothingToCommitRoute+")")
}
