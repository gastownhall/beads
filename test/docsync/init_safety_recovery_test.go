package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitSafetyRecoveryDocCoversReCloneGotchas guards two gotchas discovered
// during live recovery incident ga-vrq5pu:
//
//  1. A damaged/set-aside Dolt database directory left INSIDE the
//     sql-server's data_dir crash-loops the server, because every
//     subdirectory of data_dir is treated as its own database. Symptom:
//     "root hash doesn't exist: <hash>".
//  2. A fresh clone lacks dolt-ignored clone-local tables (leases, wisps,
//     events, local_metadata, etc.) until `bd migrate schema` has run once.
//     Symptom: "table not found: leases". The fix's "Schema already at v<N>"
//     output is the expected, reassuring result — not an error.
func TestInitSafetyRecoveryDocCoversReCloneGotchas(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", "recovery", "init-safety.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lower := strings.ToLower(string(data))

	cases := []struct {
		name   string
		substr string
	}{
		{"damaged-store crash-loop symptom", "root hash doesn't exist"},
		{"fresh-clone missing-table symptom", "table not found: leases"},
		{"fresh-clone fix command", "bd migrate schema"},
		{"fresh-clone reassuring success message", "schema already at v"},
	}
	for _, c := range cases {
		if !strings.Contains(lower, strings.ToLower(c.substr)) {
			t.Errorf("docs/recovery/init-safety.md missing %s: expected to find %q", c.name, c.substr)
		}
	}

	// Scope the headline rule to Gotcha 1's own section and match it as a
	// phrase. Two independent whole-file substring checks ("outside" and
	// "data_dir" anywhere in this ~400-line document) stayed green through a
	// rewrite that inverted the advice, so they did not guard the rule this
	// test's own failure message describes.
	section, ok := docSection(string(data), gotcha1Heading)
	if !ok {
		t.Fatalf("docs/recovery/init-safety.md missing the %q section", gotcha1Heading)
	}
	if want := "move it outside data_dir"; !strings.Contains(flattenProse(section), want) {
		t.Errorf("docs/recovery/init-safety.md %q section must tell the reader to %q: the sql-server treats every data_dir subdirectory as a database, so a set-aside store left inside it crash-loops the server",
			gotcha1Heading, want)
	}

	// cmd/bd/init_safety_help.go links to #re-clone-gotchas from inside a Go
	// string, which neither markdown link checker in docsync_test.go
	// (TestDocsSiteLinks, TestEngdocsAndRootMarkdownLinks) can see. Pin the
	// heading here and the link itself in the help test below so the anchor
	// cannot rot on either side.
	if !hasHeadingLine(string(data), reCloneGotchasHeading) {
		t.Errorf("docs/recovery/init-safety.md must keep the %q heading: cmd/bd/init_safety_help.go links to #%s",
			reCloneGotchasHeading, reCloneGotchasAnchor)
	}
}

const (
	// reCloneGotchasAnchor is the in-page anchor cmd/bd/init_safety_help.go
	// sends readers to; reCloneGotchasHeading is the doc heading that
	// generates it.
	reCloneGotchasAnchor  = "re-clone-gotchas"
	reCloneGotchasHeading = "## " + reCloneGotchasAnchor

	gotcha1Heading = "### Gotcha 1"

	// engineWontOpenHeading is the runbook section every #4521 recovery
	// diagnostic points at; the doctor hint and Fix strings embed the GitHub
	// blob URL, so the heading that generates the anchor cannot silently
	// change. journalCorruptionHeading scopes the troubleshooting entry that
	// must route embedded symptoms to it.
	engineWontOpenHeading    = "## Engine won't open"
	engineWontOpenAnchor     = "engine-wont-open"
	engineWontOpenDocLink    = "/recovery/database-corruption#engine-wont-open"
	journalCorruptionHeading = "### Dolt journal corruption after restart"
	initSafetyReCloneLink    = "/recovery/init-safety#re-clone-gotchas"
)

// docSection returns the markdown between the heading line that starts with
// prefix and the next heading line, ignoring headings inside fenced code
// blocks.
func docSection(doc, prefix string) (string, bool) {
	var (
		out    []string
		inside bool
		fenced bool
	)
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		} else if !fenced && strings.HasPrefix(line, "#") {
			if inside {
				break
			}
			inside = strings.HasPrefix(line, prefix)
		}
		if inside {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n"), inside
}

// hasHeadingLine reports whether doc contains heading as a whole line, so a
// mention of the same text in prose does not satisfy the anchor pin.
func hasHeadingLine(doc, heading string) bool {
	for _, line := range strings.Split(doc, "\n") {
		if strings.TrimRight(line, " \t\r") == heading {
			return true
		}
	}
	return false
}

// flattenProse lowercases markdown prose and removes the emphasis, code
// decoration, and hard line wrapping that would otherwise break a phrase
// match. Underscores survive: `data_dir` is part of the phrase being matched.
func flattenProse(s string) string {
	s = strings.NewReplacer("*", "", "`", "").Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// requireDocSection returns the markdown under heading, failing the test when
// the heading is missing, so a rename cannot silently hollow out a guard.
func requireDocSection(t *testing.T, doc, heading, docPath string) string {
	t.Helper()
	section, ok := docSection(doc, heading)
	if !ok {
		t.Fatalf("%s missing the %q section", docPath, heading)
	}
	return section
}

// requireInSection fails when phrase is absent from the section's flattened
// prose. Assert lowercase phrases with backticks and emphasis removed
// (underscores survive).
func requireInSection(t *testing.T, sectionName, section, phrase string) {
	t.Helper()
	if !strings.Contains(flattenProse(section), phrase) {
		t.Errorf("%s must say %q (matched with emphasis and code decoration removed)", sectionName, phrase)
	}
}

// TestInitSafetyCLIHelpCoversReCloneGotchas guards the same two re-clone
// gotchas (at CLI-help brevity) in cmd/bd/init_safety_help.go's Long field,
// the generator source for docs/cli-reference/init-safety.md.
//
// The generated page itself is intentionally NOT checked here: per
// docs/cli-docs.pin, docs/cli-reference/ is regenerated from a pinned
// *released* bd tag (built in a detached worktree), not from this checkout,
// so a Long-field edit at HEAD does not flow into the committed generated
// page until a maintainer bumps the pin as part of a release (see
// docs/cli-docs.pin's own header comment and scripts/resolve-docs-bd.sh).
// CI's generated-docs drift gate (scripts/check-cli-docs-drift.sh, driven by
// scripts/check-doc-flags.sh) is blame-scoped for exactly this reason: it
// does not fail a PR whose regenerated CLI surface is unchanged from the
// merge-base. Testing the Long field directly checks the content a PR can
// actually change and that will ship in the doc at the next pin bump.
func TestInitSafetyCLIHelpCoversReCloneGotchas(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "cmd", "bd", "init_safety_help.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lower := strings.ToLower(string(data))

	cases := []struct {
		name   string
		substr string
	}{
		{"damaged-store crash-loop symptom", "root hash doesn't exist"},
		{"fresh-clone missing-table symptom", "table not found: leases"},
		{"fresh-clone fix command", "bd migrate schema"},
	}
	for _, c := range cases {
		if !strings.Contains(lower, strings.ToLower(c.substr)) {
			t.Errorf("cmd/bd/init_safety_help.go missing %s: expected to find %q in the Long help text", c.name, c.substr)
		}
	}

	// The other half of the cross-reference pinned in the doc test above.
	if want := "docs/recovery/init-safety.md#" + reCloneGotchasAnchor; !strings.Contains(string(data), want) {
		t.Errorf("cmd/bd/init_safety_help.go must send readers to %q for the full detail", want)
	}
}

// TestDatabaseCorruptionRecoveryGuidance guards the issue #4521 recovery
// guidance. An unopenable store must route through preservation-first
// recovery — a remote via bd bootstrap --dry-run, a Dolt-native snapshot via
// bd init + bd backup restore, or an existing JSONL export via bd import /
// configured import.path — verified with bd list --all and bd stats, never a
// universal stop/doctor/restart sequence that embedded mode cannot follow.
func TestDatabaseCorruptionRecoveryGuidance(t *testing.T) {
	root := repoRoot()

	runbookPath := filepath.Join(root, "docs", "recovery", "database-corruption.md")
	runbookData, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("reading %s: %v", runbookPath, err)
	}
	runbook := string(runbookData)

	t.Run("ac1_stable_engine_wont_open_heading", func(t *testing.T) {
		if !hasHeadingLine(runbook, engineWontOpenHeading) {
			t.Errorf("docs/recovery/database-corruption.md must keep the %q heading: doctor diagnostics and the troubleshooting journal-corruption entry link to #%s",
				engineWontOpenHeading, engineWontOpenAnchor)
		}
	})

	engineSectionName := "docs/recovery/database-corruption.md \"Engine won't open\""
	engine := requireDocSection(t, runbook, engineWontOpenHeading, "docs/recovery/database-corruption.md")

	t.Run("ac1_preserve_configured_store_and_configuration", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "dolt_data_dir")
		requireInSection(t, engineSectionName, engine, "metadata.json")
		requireInSection(t, engineSectionName, engine, "outside any active server data_dir")
		if !strings.Contains(engine, initSafetyReCloneLink) {
			t.Errorf("%s must link %s for the data_dir preservation gotcha", engineSectionName, initSafetyReCloneLink)
		}
	})

	t.Run("ac1_verify_without_embedded_doctor", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "bd list --all")
		requireInSection(t, engineSectionName, engine, "bd stats")
		requireInSection(t, engineSectionName, engine, "general bd doctor still requires server mode")
		requireInSection(t, engineSectionName, engine, "--check=artifacts")
	})

	t.Run("ac1_remote_route_bootstrap_dry_run", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "bd bootstrap --dry-run")
		requireInSection(t, engineSectionName, engine, "is not data recovery")
	})

	t.Run("ac1_native_snapshot_route", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "bd init")
		requireInSection(t, engineSectionName, engine, "bd backup restore")
		requireInSection(t, engineSectionName, engine, "will not read a bd export jsonl file")
		requireInSection(t, engineSectionName, engine, "requires an initialized database")
	})

	t.Run("ac1_jsonl_export_route", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "bd import /path/to/export.jsonl")
		requireInSection(t, engineSectionName, engine, "bd config set import.path recovered.jsonl")
		requireInSection(t, engineSectionName, engine, "bd init --from-jsonl")
		requireInSection(t, engineSectionName, engine, "reads the configured import.path")
	})

	t.Run("ac1_stale_or_missing_recovery_sources", func(t *testing.T) {
		requireInSection(t, engineSectionName, engine, "cannot export from an engine that will not open")
		requireInSection(t, engineSectionName, engine, "a stale remote silently loses")
		requireInSection(t, engineSectionName, engine, "a stale export silently loses")
	})

	t.Run("ac1_troubleshooting_journal_corruption_covers_embedded", func(t *testing.T) {
		tsPath := filepath.Join(root, "docs", "reference", "troubleshooting.md")
		tsData, err := os.ReadFile(tsPath)
		if err != nil {
			t.Fatalf("reading %s: %v", tsPath, err)
		}
		const where = "docs/reference/troubleshooting.md \"Dolt journal corruption after restart\""
		section := requireDocSection(t, string(tsData), journalCorruptionHeading, "docs/reference/troubleshooting.md")
		requireInSection(t, where, section, "symptom (server mode)")
		requireInSection(t, where, section, "symptom (embedded mode)")
		if !strings.Contains(section, engineWontOpenDocLink) {
			t.Errorf("%s must link %s inside the section itself; the same link elsewhere in the file does not route this symptom", where, engineWontOpenDocLink)
		}
	})
}

// ac2: an interrupted command leaves lock pathnames behind; the runbook must
// keep a leftover pathname, a live holder, and actual damage distinct.
func TestDatabaseCorruptionLockGuidance(t *testing.T) {
	root := repoRoot()
	runbookPath := filepath.Join(root, "docs", "recovery", "database-corruption.md")
	runbookData, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("reading %s: %v", runbookPath, err)
	}
	runbook := string(runbookData)

	const lockHeading = "## Locks and live holders"
	const where = "docs/recovery/database-corruption.md \"Locks and live holders\""
	locks := requireDocSection(t, runbook, lockHeading, "docs/recovery/database-corruption.md")

	requireInSection(t, where, locks, "a leftover lock file is not evidence of a held lock")
	requireInSection(t, where, locks, "waiting for or stopping the actual holder")
	requireInSection(t, where, locks, "not by rebuilding the database")
	requireInSection(t, where, locks, "never delete files inside .dolt/")
	requireInSection(t, where, locks, "bd dolt stop")
	requireInSection(t, where, locks, "cannot stop an arbitrary embedded bd process")

	engine := requireDocSection(t, runbook, engineWontOpenHeading, "docs/recovery/database-corruption.md")
	engineSectionName := "docs/recovery/database-corruption.md \"Engine won't open\""
	requireInSection(t, engineSectionName, engine, "live contention, not corruption")
}
