package docsync

import (
	"strings"
	"testing"
)

const accidentalReleaseRoute = "/recovery/accidental-1-2-1-release"

// TestAccidentalReleaseRecoveryMakesRestorationRequired is AC-6: the
// accidental-1-2-1 recovery guide must present events-table restoration as
// part of recovery itself, say what skipping it does to clones, and cover
// the machines the earlier optional wording left behind — and nothing in it
// may present a read-only check or a plain sync as proof of completion.
// Each assertion is scoped to the section a reader lands in: a whole-file
// keyword match stays green through a rewrite that moves the advice
// somewhere nobody reads it.
func TestAccidentalReleaseRecoveryMakesRestorationRequired(t *testing.T) {
	guide := readRepoDoc(t, "docs", "recovery", "accidental-1-2-1-release.md")

	t.Run("restoration precedes the run-bd instruction", func(t *testing.T) {
		const heading = "## Recommended fix: roll the schema cursor back"
		steps, ok := docSubtree(guide, heading)
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", heading)
		}
		requirePhrases(t, "accidental-1-2-1-release.md "+heading, steps,
			"restore the versioned events table (required)",
			"recovery: restore versioned events table",
			"events_ignore_rules",
			"do not treat reads or a bd kv set as proof of recovery",
		)
		flat := flattenProse(steps)
		restore := strings.Index(flat, "restore the versioned events table (required)")
		success := strings.Index(flat, "then run any bd command from the workspace")
		if restore < 0 || success < 0 || restore > success {
			t.Errorf("accidental-1-2-1-release.md %s: the restore step must come before the run-bd success instruction (restore at %d, run-bd at %d)", heading, restore, success)
		}
		forbidPhrases(t, "accidental-1-2-1-release.md "+heading, steps,
			"optional: restore audit-event versioning",
		)
	})

	t.Run("clone consequence and repairs are explicit", func(t *testing.T) {
		affected, ok := docSection(guide, "## Am I affected?")
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", "## Am I affected?")
		}
		requirePhrases(t, "accidental-1-2-1-release.md ## Am I affected?", affected,
			"#a-clone-that-is-missing-the-events-table",
		)

		const cloneHeading = "## A clone that is missing the events table"
		clones, ok := docSubtree(guide, cloneHeading)
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", cloneHeading)
		}
		requirePhrases(t, "accidental-1-2-1-release.md "+cloneHeading, clones,
			"error 1146: table not found: events",
			"repair the source, then pull",
			"recovery: restore events table from history",
		)
	})

	t.Run("shared sync is ordered, not unconditional", func(t *testing.T) {
		const heading = "## Shared databases: recover every machine, then sync"
		shared, ok := docSubtree(guide, heading)
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", heading)
		}
		requirePhrases(t, "accidental-1-2-1-release.md "+heading, shared,
			"including step 5, and pushes",
			"completes steps 1–6 before it pulls",
		)
		forbidPhrases(t, "accidental-1-2-1-release.md", guide,
			"everything else syncs normally",
		)
	})

	t.Run("the old optional step has a documented finish", func(t *testing.T) {
		const heading = "## Already re-tracked events with the old optional step?"
		finish, ok := docSubtree(guide, heading)
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", heading)
		}
		requirePhrases(t, "accidental-1-2-1-release.md "+heading, finish,
			"recovery: re-track events table",
			"recovery: finish re-tracking events",
			"different primary keys",
		)
	})

	t.Run("the stopgap never reads as recovery", func(t *testing.T) {
		const heading = "## Stopgap: keep working before you recover"
		stopgap, ok := docSubtree(guide, heading)
		if !ok {
			t.Fatalf("accidental-1-2-1-release.md missing the %q section", heading)
		}
		requirePhrases(t, "accidental-1-2-1-release.md "+heading, stopgap,
			"do not read success here as recovery",
		)
	})

	t.Run("recovery index routes the write-dead clone symptom", func(t *testing.T) {
		index := readRepoDoc(t, "docs", "recovery", "index.md")
		issues, ok := docSection(index, "## Common Issues")
		if !ok {
			t.Fatalf("recovery/index.md missing the %q section", "## Common Issues")
		}
		requirePhrases(t, "recovery/index.md ## Common Issues", issues,
			"table not found: events",
			accidentalReleaseRoute,
		)
	})
}
