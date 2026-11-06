package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestUpgradingAdoptRecipeMovesAsideSafely pins the upgrading guide's adopt
// recipe for a clone already on the new binary (bd-bt60zz, #6687) to the
// gate's own: the same embedded commands in the same order (so the backup
// parent exists before the move), an unused destination, the moved-aside store
// kept, and the old-release import stated as conditional rather than as a
// step bootstrap needs. The changelog must credit the report.
func TestUpgradingAdoptRecipeMovesAsideSafely(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	guide := read("docs/getting-started/upgrading.md")
	start := strings.Index(guide, "**A clone that already runs the new binary**")
	if start < 0 {
		t.Fatal("upgrading.md has no **A clone that already runs the new binary** paragraph")
	}
	section := guide[start:]
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	open := strings.Index(section, "```bash\n")
	if open < 0 {
		t.Fatalf("the adopt paragraph has no bash recipe:\n%s", section)
	}
	block := section[open+len("```bash\n"):]
	block = block[:strings.Index(block, "```")]
	prose := strings.Join(strings.Fields(section[open+len("```bash\n")+len(block):]), " ")

	var commands []string
	for _, line := range strings.Split(block, "\n") {
		if cmd, _, _ := strings.Cut(line, "#"); strings.TrimSpace(cmd) != "" {
			commands = append(commands, strings.Join(strings.Fields(cmd), " "))
		}
	}
	if want := schema.EmbeddedAdoptCommands(); strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Errorf("adopt recipe commands = %q, want the gate's %q", commands, want)
	}

	for _, want := range []string{
		"unused",
		"Keep the moved-aside directory",
		"Only if you exported",
		"bd import .beads/backup/pre-migrate.jsonl",
	} {
		if !strings.Contains(prose, want) {
			t.Errorf("adopt recipe's notes lack %q:\n%s", want, prose)
		}
	}

	if bullet := strings.Index(guide, "**stops and directs you to adopt**"); bullet < 0 {
		t.Error("upgrading.md has no **stops and directs you to adopt** bullet")
	} else if strings.HasPrefix(guide[bullet:], "**stops and directs you to adopt** (`bd bootstrap`)") {
		t.Error("the gate summary bullet still names bare `bd bootstrap` as the adopt step")
	}

	if !unreleasedFixedMentions(read("CHANGELOG.md"), "#6687", "@postoso") {
		t.Error("CHANGELOG.md [Unreleased] ### Fixed has no entry crediting @postoso for #6687")
	}
}

// unreleasedFixedMentions reports whether one bullet (wrapped lines joined)
// of the [Unreleased] release's ### Fixed section mentions every ref.
func unreleasedFixedMentions(changelog string, refs ...string) bool {
	var bullets []string
	inUnreleased, inFixed := false, false
	for _, line := range strings.Split(changelog, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			inUnreleased = strings.HasPrefix(line, "## [Unreleased]")
			inFixed = false
		case strings.HasPrefix(line, "### "):
			inFixed = strings.TrimSpace(line) == "### Fixed"
		case !inUnreleased || !inFixed:
		case strings.HasPrefix(line, "- "):
			bullets = append(bullets, line)
		case len(bullets) > 0:
			bullets[len(bullets)-1] += " " + line
		}
	}
	for _, bullet := range bullets {
		all := true
		for _, ref := range refs {
			all = all && strings.Contains(bullet, ref)
		}
		if all {
			return true
		}
	}
	return false
}
