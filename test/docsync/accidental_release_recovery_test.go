package docsync

import (
	"strings"
	"testing"
)

// TestAccidentalReleaseRecoveryGuidance pins the shape of the accidental
// v1.2.1 recovery guide (gastownhall/beads#6488). The guide once ended with
// the success instruction after the cursor rollback and kept restoring the
// versioned events table optional, so a recovered database was pushed
// without events and every clone of it failed its first issue write. The
// guide's commands themselves run in
// internal/storage/embeddeddolt/recovery_guide_events_test.go; this test
// guards the prose around them.
func TestAccidentalReleaseRecoveryGuidance(t *testing.T) {
	const where = "docs/recovery/accidental-1-2-1-release.md"
	guide := readRepoDoc(t, "docs", "recovery", "accidental-1-2-1-release.md")

	t.Run("restoring events precedes the success instruction", func(t *testing.T) {
		section, ok := docSection(guide, "## Recommended fix: roll the schema cursor back")
		if !ok {
			t.Fatalf("%s has no recommended-fix section", where)
		}
		flatSection := flattenProse(section)
		success := strings.Index(flatSection, "it should work with no warnings")
		if success < 0 {
			t.Fatalf("%s recommended fix lost its success instruction", where)
		}
		for _, step := range []string{
			"restore the versioned events table",
			"call dolt_checkout(@preflip, '--', 'events')",
			"events_rows_in_head",
		} {
			at := strings.Index(flatSection, step)
			if at < 0 || at > success {
				t.Errorf("%s recommended fix must have %q before the success instruction", where, step)
			}
		}
		requirePhrases(t, where, guide, "Both steps are required")
		requirePhrases(t, where+" recommended fix", section,
			"Do not stop after step 4",
			"A smoke test of reads or `bd kv set` does not show that recovery is complete; step 6 does.",
		)
	})

	t.Run("the clone consequence is explicit", func(t *testing.T) {
		requirePhrases(t, where, guide,
			"v1.2.2 has no way to recreate it",
			"every clone of a database pushed in that state can read but not write issues",
			"fails with `table not found: events`",
			"Error 1146: table not found: events",
		)
	})

	t.Run("existing clones have a repair", func(t *testing.T) {
		missing, ok := docSection(guide, "### If `events` is missing or empty")
		if !ok {
			t.Fatalf("%s has no section for a missing events table", where)
		}
		requirePhrases(t, where+" missing-events section", missing,
			"Another machine still has the audit rows",
			"`bd dolt pull` before any issue write",
			"No machine has them",
			"Restore the table from history on **one** machine",
			"cannot be reconstructed",
			"Do not run one briefly",
		)
		retrack, ok := docSection(guide, "### If you already ran the old re-track command")
		if !ok {
			t.Fatalf("%s has no section for the old re-track command", where)
		}
		requirePhrases(t, where+" old re-track section", retrack,
			"CALL DOLT_ADD('dolt_ignore')",
			"on the machine that ran it first and then on each clone, before any of them pulls",
		)
		shared, ok := docSection(guide, "### Machines that share the database")
		if !ok {
			t.Fatalf("%s has no section for machines that share the database", where)
		}
		requirePhrases(t, where+" shared-database section", shared,
			"**before** it pulls",
			"Machines that never ran v1.2.1 just `bd dolt pull`",
		)
	})

	t.Run("nothing misrepresents completion", func(t *testing.T) {
		for _, line := range strings.Split(guide, "\n") {
			if strings.HasPrefix(line, "#") && strings.Contains(strings.ToLower(line), "optional") {
				t.Errorf("%s heading %q makes a recovery step look optional", where, line)
			}
		}
		forbidPhrases(t, where, guide,
			"Optional: restore",
			"everything else syncs normally",
			"strictly additive",
			"nothing the 1.1 line reads or writes was dropped",
			"recover one and push, then have the others pull",
			"Audit-event versioning is paused",
		)
		stopgap, ok := docSection(guide, "## Stopgap: keep working before you recover")
		if !ok {
			t.Fatalf("%s has no stopgap section", where)
		}
		requirePhrases(t, where+" stopgap", stopgap,
			"on this machine reads are identical and writes work",
			"not fit to be pushed and cloned yet",
		)
	})

	t.Run("the recovery index routes the clone symptom", func(t *testing.T) {
		index := readRepoDoc(t, "docs", "recovery", "index.md")
		var row string
		for _, line := range strings.Split(index, "\n") {
			if strings.Contains(line, "table not found: events") {
				row = line
				break
			}
		}
		if row == "" {
			t.Fatal("docs/recovery/index.md has no row for `table not found: events`")
		}
		requirePhrases(t, "docs/recovery/index.md events row", row,
			"/recovery/accidental-1-2-1-release#if-events-is-missing-or-empty",
		)
	})
}
