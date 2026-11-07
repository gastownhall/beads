package docsync

import "testing"

// TestExportDocsDescribePassiveExportLag guards the JSONL export contract on
// the two pages a reader checks it on (GH#6955). Both used to read as if the
// export tracked every write: export.auto "after every write", an interval
// with no word about writes inside it, a hook that always refreshes, and
// sync-concepts listing JSONL as a backup.
func TestExportDocsDescribePassiveExportLag(t *testing.T) {
	t.Run("configuration export settings", func(t *testing.T) {
		const heading = "## Tool-Level Settings (config.yaml)"
		section, ok := docSubtree(readRepoDoc(t, "docs", "reference", "configuration.md"), heading)
		if !ok {
			t.Fatalf("docs/reference/configuration.md missing the %q section", heading)
		}
		where := "configuration.md " + heading
		requirePhrases(t, where, section,
			"is the *minimum* time between those refreshes",
			"not a maximum staleness bound and not a timer",
			"the file can remain stale indefinitely",
			"only when the commit stages at least one `.beads/` path",
			"`bd doctor --check=export` is the missing-ID staleness detector",
			"names `bd export -o <resolved-path>` as the refresh action",
			"ID coverage only",
			"Bare `bd doctor` and `bd doctor --check=artifacts` do not perform this coverage check",
			"The export file is not a database backup: for recovery, use `bd backup`",
		)
		forbidPhrases(t, where, section, "after every write")
	})

	t.Run("sync concepts", func(t *testing.T) {
		doc := readRepoDoc(t, "docs", "core-concepts", "sync-concepts.md")
		forJSONL, ok := docSubtree(doc, "## What JSONL Is For")
		if !ok {
			t.Fatal("docs/core-concepts/sync-concepts.md missing the \"## What JSONL Is For\" section")
		}
		where := "sync-concepts.md ## What JSONL Is For"
		requirePhrases(t, where, forJSONL,
			"is not a database backup: for recoverable database backups use `bd backup`",
		)
		forbidPhrases(t, where, forJSONL, "migration, and backup")

		lag, ok := docSubtree(doc, "## Export Lag")
		if !ok {
			t.Fatal("docs/core-concepts/sync-concepts.md missing the \"## Export Lag\" section")
		}
		requirePhrases(t, "sync-concepts.md ## Export Lag", lag,
			"is only a *minimum* time between refreshes",
			"It is not a staleness deadline",
			"the file can lag the database indefinitely",
			"`bd doctor --check=export` is the missing-ID staleness detector",
			"`bd export -o <resolved-path>`",
			"coverage only",
		)

		hooks, ok := docSubtree(doc, "## Hooks")
		if !ok {
			t.Fatal("docs/core-concepts/sync-concepts.md missing the \"## Hooks\" section")
		}
		requirePhrases(t, "sync-concepts.md ## Hooks", hooks,
			"the commit stages at least one `.beads/` path",
		)
	})
}
