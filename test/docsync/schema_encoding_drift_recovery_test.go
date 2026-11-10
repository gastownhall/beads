package docsync

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	schemaEncodingDriftRoute = "/recovery/schema-encoding-drift"
	// preserveStoreHeading is the whole-store preservation procedure the drift
	// runbook deep-links as #2-preserve-the-damaged-store; the site link
	// checker does not resolve anchors, so the heading is pinned here.
	preserveStoreHeading = "### 2. Preserve the damaged store"
	preserveStoreRoute   = "/recovery/database-corruption#2-preserve-the-damaged-store"
)

// driftRecoveryURLConst matches the runbook URL a SchemaEncodingDriftError
// prints (internal/storage/schema/encoding_drift.go).
var driftRecoveryURLConst = regexp.MustCompile(`SchemaEncodingDriftRecoveryURL = "https://beads\.gascity\.com(/[^"#]*)"`)

// TestSchemaEncodingDriftRecoveryGuidance guards the runbook a schema-encoding
// drift migration refusal links (GH#5699): the refusal's URL resolves to a
// published page, and the page gives preservation steps, source-attributed
// recovery options with their limits, full validation guidance, and the raw
// panic scope limitation, each in the section a reader lands in. Upstream tool
// status is deliberately not pinned: it changes outside this repository.
func TestSchemaEncodingDriftRecoveryGuidance(t *testing.T) {
	t.Run("refusal URL resolves to a published, linked page", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join(repoRoot(), "internal", "storage", "schema", "encoding_drift.go"))
		if err != nil {
			t.Fatalf("reading the refusal source: %v", err)
		}
		m := driftRecoveryURLConst.FindSubmatch(src)
		if m == nil {
			t.Fatal("encoding_drift.go no longer defines SchemaEncodingDriftRecoveryURL on the docs site")
		}
		if got := string(m[1]); got != schemaEncodingDriftRoute {
			t.Errorf("SchemaEncodingDriftRecoveryURL route = %q, want %q", got, schemaEncodingDriftRoute)
		}
		readRepoDoc(t, "docs", "recovery", "schema-encoding-drift.md")
		if nav := readRepoDoc(t, "docs", "docs.json"); !strings.Contains(nav, `"recovery/schema-encoding-drift"`) {
			t.Error("docs/docs.json does not publish recovery/schema-encoding-drift")
		}
		for _, page := range [][]string{
			{"docs", "recovery", "index.md"},
			{"docs", "getting-started", "upgrading.md"},
		} {
			if !strings.Contains(readRepoDoc(t, page...), "]("+schemaEncodingDriftRoute+")") {
				t.Errorf("%s does not link %s", filepath.Join(page...), schemaEncodingDriftRoute)
			}
		}
	})

	runbook := readRepoDoc(t, "docs", "recovery", "schema-encoding-drift.md")
	section := func(t *testing.T, heading string) string {
		t.Helper()
		body, ok := docSubtree(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/schema-encoding-drift.md missing the %q section", heading)
		}
		return body
	}

	t.Run("preservation before any further writes", func(t *testing.T) {
		const heading = "## Before any further writes"
		requirePhrases(t, "schema-encoding-drift.md "+heading, section(t, heading),
			"nothing was rolled back",
			"partial changes may remain",
			"]("+preserveStoreRoute+")",
			"do not commit the working set",
			"do not delete rows",
			"work only on the copy",
		)
		if !hasHeadingLine(readRepoDoc(t, "docs", "recovery", "database-corruption.md"), preserveStoreHeading) {
			t.Errorf("docs/recovery/database-corruption.md lost the %q heading the drift runbook links", preserveStoreHeading)
		}
	})

	t.Run("diagnosis names candidates, not confirmed damage", func(t *testing.T) {
		const heading = "## Diagnosis"
		requirePhrases(t, "schema-encoding-drift.md "+heading, section(t, heading),
			"issues", "dependencies", "wisps", "wisp_dependencies",
			"candidate tables",
			"not confirmed corruption locations",
			"cannot detect readable garbage",
		)
	})

	t.Run("source-attributed recovery options with limits", func(t *testing.T) {
		const heading = "## Recovery options"
		body := section(t, heading)
		requirePhrases(t, "schema-encoding-drift.md "+heading, body,
			"(https://github.com/gastownhall/beads/issues/4380#issuecomment-4899301060)",
			"(https://github.com/dolthub/dolt/pull/11133)",
			"(https://github.com/gastownhall/beads/issues/4380#issuecomment-4898979147)",
			"(https://github.com/gastownhall/beads/issues/4380#issuecomment-4899140648)",
			"as of",
			"deleted rows",
			"rows created since",
			"schema differences",
			"cannot guarantee",
			"experimental",
			"verify current upstream guidance",
		)
		forbidPhrases(t, "schema-encoding-drift.md "+heading, body,
			"zero data loss guaranteed",
			"guarantees no data loss",
		)
	})

	t.Run("full validation before production use", func(t *testing.T) {
		const heading = "## Validate before production use"
		requirePhrases(t, "schema-encoding-drift.md "+heading, section(t, heading),
			"every row",
			"issue and dependency semantics",
			"sampling alone is not a full validation",
			"rerun the migration on the copy",
		)
	})

	t.Run("raw panic scope limitation", func(t *testing.T) {
		const heading = "## What this refusal does not cover"
		requirePhrases(t, "schema-encoding-drift.md "+heading, section(t, heading),
			"returned error",
			"raw engine panic",
			"does not make every embedded panic path safe",
		)
	})
}
