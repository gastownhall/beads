package docsync

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestSchemaEncodingDriftRecoveryGuidance guards the runbook the
// schema-encoding-drift refusal sends operators to: the URL the CLI prints is
// schema.SchemaEncodingDriftRecoveryURL, and neither markdown link checker
// sees a Go string, so the pair is pinned here. The runbook must keep its
// preservation-first response, the two recovery routes with their maturity
// and data-loss limits stated, the validation guidance before the store
// returns to use, and the residual raw-panic limitation
// (gastownhall/beads#5699, bd-mjbau8 AC-5).
func TestSchemaEncodingDriftRecoveryGuidance(t *testing.T) {
	runbook := readRepoDoc(t, "docs", "recovery", "schema-encoding-drift.md")

	t.Run("the page is the URL the refusal prints", func(t *testing.T) {
		if !strings.Contains(runbook, schema.SchemaEncodingDriftRecoveryURL) {
			t.Errorf("docs/recovery/schema-encoding-drift.md must carry %q: the CLI's schema.SchemaEncodingDriftRecoveryURL points here, and a rename without a constant update strands the refusal", schema.SchemaEncodingDriftRecoveryURL)
		}
	})

	t.Run("symptoms name the refusal and its JSON form", func(t *testing.T) {
		section, ok := docSection(runbook, "## Symptoms")
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", "## Symptoms")
		}
		requirePhrases(t, "schema-encoding-drift.md ## Symptoms", section,
			"schema encoding drift",
			"0059_recompute_null_gate_is_blocked.up.sql",
			"invalid hash length",
			"malformed out-of-band adaptive value",
			"schema_encoding_drift",
		)
	})

	t.Run("stop and preserve first", func(t *testing.T) {
		const heading = "## Stop and preserve first"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"do not retry the migration, delete rows, or commit partial results",
			"/recovery/database-corruption#2-preserve-the-damaged-store",
			"outside any server data_dir",
			"work only on a copy",
		)
	})

	t.Run("candidate tables, not confirmed corruption locations", func(t *testing.T) {
		const heading = "## What happened"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"issues",
			"dependencies",
			"wisps",
			"wisp_dependencies",
			"candidate",
			"not confirmed corruption locations",
			"refuses instead of skipping the unreadable rows",
			"not automatic",
		)
	})

	t.Run("route 1 rebuilds from Dolt history with the recipe attributed", func(t *testing.T) {
		const heading = "## Recovery route 1"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"4380#issuecomment-4899301060",
			"as of",
			"dolt checkout <commit> -- <table>",
			"historical readability is not latest-value recovery",
			"rows deleted after that commit",
			"re-enter",
		)
	})

	t.Run("route 2 states the upstream tool's maturity limits", func(t *testing.T) {
		const heading = "## Recovery route 2"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"closed and unmerged",
			"no supported install or release",
			"4898979147",
			"4899140648",
			"a sampled check is not a full validation",
			"against the preserved copy only",
		)
	})

	t.Run("validate, then rerun the migration", func(t *testing.T) {
		const heading = "## Validate, then rerun the migration"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"a full pass, not a sample",
			"bd list --all",
			"bd stats",
			"dependency graph",
			"bd migrate schema",
		)
	})

	t.Run("residual raw-panic limitation stays stated", func(t *testing.T) {
		const heading = "## Residual raw-panic paths"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		requirePhrases(t, "schema-encoding-drift.md "+heading, section,
			"installs no recovery seam",
			"does not make the raw-panic paths safe",
			"5699",
		)
	})

	t.Run("navigation and cross-links route to the runbook", func(t *testing.T) {
		nav := readRepoDoc(t, "docs", "docs.json")
		if !strings.Contains(nav, `"recovery/schema-encoding-drift"`) {
			t.Error("docs/docs.json must list recovery/schema-encoding-drift in the Recovery group's pages")
		}
		index := readRepoDoc(t, "docs", "recovery", "index.md")
		if !strings.Contains(index, "/recovery/schema-encoding-drift") {
			t.Error("docs/recovery/index.md must link the runbook from its common-issues table")
		}
		upgrading := readRepoDoc(t, "docs", "getting-started", "upgrading.md")
		section, ok := docSection(upgrading, "#### If the migration stops on schema encoding drift")
		if !ok {
			t.Fatalf("docs/getting-started/upgrading.md missing the %q section", "#### If the migration stops on schema encoding drift")
		}
		requirePhrases(t, "upgrading.md migration-drift section", section,
			"/recovery/schema-encoding-drift",
			"do not retry the migration",
		)
	})
}
