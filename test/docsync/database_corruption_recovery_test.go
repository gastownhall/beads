package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// engineWontOpenHeading is the runbook section that cmd/bd/doctor's
	// recovery URL (doctor.EngineWontOpenRecoveryURL) deep-links as
	// #engine-wont-open. Neither markdown link checker sees a Go string, so
	// the heading is pinned here.
	engineWontOpenHeading = "## Engine won't open"
	engineWontOpenRoute   = "/recovery/database-corruption#engine-wont-open"
)

// TestDatabaseCorruptionRecoveryGuidance guards the recovery guidance for a
// Beads database that will not open, an interrupted command that leaves a
// lock symptom behind, and batch workloads (GH#4521). Each assertion is
// scoped to the section a reader lands in: a whole-file keyword match stays
// green through a rewrite that moves the advice somewhere nobody reads it.
func TestDatabaseCorruptionRecoveryGuidance(t *testing.T) {
	runbook := readRepoDoc(t, "docs", "recovery", "database-corruption.md")

	t.Run("AC-1 engine won't open", func(t *testing.T) {
		section, ok := docSubtree(runbook, engineWontOpenHeading)
		if !ok {
			t.Fatalf("docs/recovery/database-corruption.md missing the %q section", engineWontOpenHeading)
		}
		requirePhrases(t, "database-corruption.md "+engineWontOpenHeading, section,
			"stop every bd process",
			"copy the whole store and its configuration",
			"outside any server data_dir",
			"general bd doctor checks are not supported in embedded mode",
			"bd list --all",
			"bd stats",
			"stale",
			"no recovery source",
		)

		routes := []struct {
			heading string
			phrases []string
		}{
			{"### From a Dolt remote", []string{
				"bd bootstrap --dry-run",
				"a plan that creates an empty database is not a recovery",
			}},
			{"### From a Dolt-native backup", []string{
				"dolt-native",
				"bd init",
				"bd backup restore",
				"initialized recovery workspace",
			}},
			{"### From a JSONL export", []string{
				"not a dolt backup",
				"bd import /path/to/export.jsonl",
				"bd config set import.path recovered.jsonl",
				"bd init --from-jsonl",
			}},
		}
		for _, r := range routes {
			body, ok := docSection(runbook, r.heading)
			if !ok {
				t.Errorf("docs/recovery/database-corruption.md missing the %q recovery route", r.heading)
				continue
			}
			requirePhrases(t, "database-corruption.md "+r.heading, body, r.phrases...)
		}
	})

	t.Run("AC-2 lock symptoms", func(t *testing.T) {
		const heading = "## Lock errors after an interrupted command"
		section, ok := docSection(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/database-corruption.md missing the %q section", heading)
		}
		requirePhrases(t, "database-corruption.md "+heading, section,
			"a leftover lock file is not evidence of a held lock",
			"waiting for or stopping the actual holder",
			"not by rebuilding the database",
			"bd dolt stop stops a server that bd manages",
			"cannot stop an arbitrary embedded bd process",
			"never delete files inside .dolt/",
		)
		symptoms, _ := docSection(runbook, "## Symptoms")
		requirePhrases(t, "database-corruption.md ## Symptoms", symptoms,
			"(#lock-errors-after-an-interrupted-command)")
	})

	t.Run("AC-3 batch and concurrent workloads", func(t *testing.T) {
		steering := []string{
			"bd batch runs a limited set of write commands",
			"in one transaction",
			"server or proxied-server mode",
			"neither repairs a damaged journal",
		}
		prevention, ok := docSection(runbook, "## Prevention")
		if !ok {
			t.Fatalf("docs/recovery/database-corruption.md missing the %q section", "## Prevention")
		}
		requirePhrases(t, "database-corruption.md ## Prevention", prevention, steering...)

		const embedded = "### Embedded Mode"
		arch, ok := docSection(readRepoDoc(t, "docs", "architecture", "dolt.md"), embedded)
		if !ok {
			t.Fatalf("docs/architecture/dolt.md missing the %q section", embedded)
		}
		requirePhrases(t, "architecture/dolt.md "+embedded, arch, steering...)
	})

	t.Run("AC-1 troubleshooting journal-corruption entry", func(t *testing.T) {
		doc := readRepoDoc(t, "docs", "reference", "troubleshooting.md")
		const heading = "### Dolt journal corruption"
		section, ok := docSection(doc, heading)
		if !ok {
			t.Fatalf("docs/reference/troubleshooting.md missing the %q section", heading)
		}
		requirePhrases(t, "troubleshooting.md "+heading, section,
			"symptom (embedded mode)",
			"symptom (server mode)",
			"dolt-server.log",
			engineWontOpenRoute,
		)
	})

	// Corruption advice outside the runbook must route to it, not rebuild in
	// place: deleting the data directory destroys whatever the remote lacks.
	t.Run("AC-1 corruption entries elsewhere route to the runbook", func(t *testing.T) {
		entries := []struct {
			doc     []string
			heading string
		}{
			{[]string{"docs", "reference", "faq.md"}, "### What's the difference between database corruption and ID collisions?"},
			{[]string{"docs", "reference", "troubleshooting.md"}, "### Corrupted database"},
			{[]string{"docs", "architecture", "dolt.md"}, "### Database Corruption"},
		}
		for _, e := range entries {
			where := strings.Join(e.doc, "/") + " " + e.heading
			section, ok := docSection(readRepoDoc(t, e.doc...), e.heading)
			if !ok {
				t.Errorf("%s: section missing", where)
				continue
			}
			requirePhrases(t, where, section,
				engineWontOpenRoute,
				"preserve the damaged store",
				"embedded mode runs only bd doctor --check=artifacts",
			)
			forbidPhrases(t, where, section, "rm -rf .beads/")
		}
	})
}

// readRepoDoc reads a repository file by its path elements.
func readRepoDoc(t *testing.T, elem ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot()}, elem...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// requirePhrases reports each phrase missing from the flattened section.
func requirePhrases(t *testing.T, where, section string, phrases ...string) {
	t.Helper()
	flat := flattenProse(section)
	for _, p := range phrases {
		if !strings.Contains(flat, flattenProse(p)) {
			t.Errorf("%s must say %q", where, p)
		}
	}
}

// forbidPhrases reports each phrase present in the flattened section.
func forbidPhrases(t *testing.T, where, section string, phrases ...string) {
	t.Helper()
	flat := flattenProse(section)
	for _, p := range phrases {
		if strings.Contains(flat, flattenProse(p)) {
			t.Errorf("%s must not say %q", where, p)
		}
	}
}

// docSubtree returns the markdown under the heading line equal to heading,
// including its deeper subsections, up to the next heading of the same or a
// higher level. Headings inside fenced code blocks are ignored.
func docSubtree(doc, heading string) (string, bool) {
	level := headingLevel(heading)
	var (
		out    []string
		inside bool
		found  bool
		fenced bool
	)
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		} else if !fenced && strings.HasPrefix(line, "#") {
			if inside && headingLevel(line) <= level {
				break
			}
			if strings.TrimRight(line, " \t\r") == heading {
				inside, found = true, true
			}
		}
		if inside {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n"), found
}

func headingLevel(line string) int {
	return len(line) - len(strings.TrimLeft(line, "#"))
}
