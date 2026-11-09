package docsync

import "testing"

const (
	sessionStoreHeading = "### Keep every session on the same store"
	faqModeHeading      = "### Embedded mode or server mode — which am I running?"
)

// TestModeSelectionGuidanceForConcurrentAgents guards the concurrent-agent
// mode-selection guidance (GH#4746): runtime overrides can put sessions on
// different stores, bd's explicit-embedded mismatch notice and its limits,
// and consistently configured server mode for many concurrent processes,
// without promising the notice fixes stale reads. Each assertion is scoped
// to the section a reader lands in.
func TestModeSelectionGuidanceForConcurrentAgents(t *testing.T) {
	guide := readRepoDoc(t, "docs", "architecture", "dolt.md")
	faq := readRepoDoc(t, "docs", "reference", "faq.md")

	t.Run("architecture guide", func(t *testing.T) {
		section, ok := docSubtree(guide, sessionStoreHeading)
		if !ok {
			t.Fatalf("docs/architecture/dolt.md missing the %q section", sessionStoreHeading)
		}
		where := "dolt.md " + sessionStoreHeading
		requirePhrases(t, where, section,
			"BEADS_DOLT_SHARED_SERVER",
			"BEADS_DOLT_SERVER_MODE=1",
			"BEADS_DOLT_SERVER_HOST",
			`"dolt_mode": "embedded"`,
			".beads/embeddeddolt/",
			"different stores",
			"prints a notice to stderr, once per process",
			"names the deciding variable and the embedded directory it bypasses",
			"align every session on the intended mode",
			"does not fire when the workspace leaves the mode implicit",
			"foreign dolt sql-server",
			"worktrees",
			"not a fix for stale reads",
			"same workspace, mode, database and branch",
			"for many concurrent processes on one machine, use server mode, configured the same way for every session",
		)
		forbidPhrases(t, where, section, "fixes stale reads", "prevents stale reads")
	})

	t.Run("FAQ", func(t *testing.T) {
		section, ok := docSubtree(faq, faqModeHeading)
		if !ok {
			t.Fatalf("docs/reference/faq.md missing the %q section", faqModeHeading)
		}
		where := "faq.md " + faqModeHeading
		requirePhrases(t, where, section,
			"BEADS_DOLT_SERVER_MODE=1",
			"BEADS_DOLT_SERVER_HOST",
			"different stores",
			"prints a notice to stderr, once per process",
			"implicit embedded default",
			"foreign dolt sql-server",
			"does not fix stale reads",
			"configure server mode the same way for every session",
			"/architecture/dolt#keep-every-session-on-the-same-store",
		)
		forbidPhrases(t, where, section, "fixes stale reads", "prevents stale reads")
	})
}
