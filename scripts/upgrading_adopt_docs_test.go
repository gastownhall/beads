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
// kept, the old-release import stated as conditional rather than as a step
// bootstrap needs, and, as in the gate, sync.remote checked before the move
// (and set in config.yaml itself, since the gate refuses `bd config set`)
// and only a clone plan accepted after it. Also as in the gate, the steps run
// from the .beads `bd where` reports with every bd stopped, agent hooks that
// run bd prime and other git worktrees included (a bd prime during the move
// creates a fresh database), and end with a per-worktree `bd where` check.
// The server-storage paragraph gives the gate's host-side steps and names no
// fixed data path. The changelog must credit the report.
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
		"backup.size-cap-mb",
		"Only if you exported",
		"bd import .beads/backup/pre-migrate.jsonl",
		// #6687 review: only a clone plan is the adopt, and bootstrap shows
		// a git-forge remote in its git+ form, without credentials.
		"Let bootstrap go ahead only if its plan reads `Bootstrap plan: clone from remote` with that remote as its `Remote:`",
		"Bootstrap shows a git URL in its `git+https://` or `git+ssh://` form, without credentials",
		"only when none of `--yes`, `BD_NON_INTERACTIVE` or `CI` is set",
		"`bd bootstrap --dry-run`",
		"did not find the remote",
		// #6687 review: a bd process can recreate the database during the
		// move, here or in another git worktree.
		"If bootstrap reports `Database already exists` instead, a `bd` process created a new database after the move",
		"run `bd where` in each of the repository's other git worktrees",
		"move that worktree's `.beads/embeddeddolt` aside",
		// The server-storage steps, as the gate prints them there.
		"in the numbered steps the refusal prints on server storage",
		"Coordinate downtime with every client of the server",
		"actual `data_dir`",
		"not through a proxy",
	} {
		if !strings.Contains(prose, want) {
			t.Errorf("adopt recipe's notes lack %q:\n%s", want, prose)
		}
	}
	for _, banned := range []string{"with that URL", "configures its own database path"} {
		if strings.Contains(prose, banned) {
			t.Errorf("adopt recipe's notes still say %q:\n%s", banned, prose)
		}
	}
	// .beads/dolt/ is only the default data_dir of a server bd runs for its
	// workspace; a shared or custom server keeps it elsewhere, so the guide
	// must not state it as where server storage lives (as the gate does not,
	// TestRemoteMigrateGateServerAdoptGuidance).
	if strings.Contains(section, ".beads/dolt/") {
		t.Errorf("the adopt section names .beads/dolt/ as the server database's location:\n%s", section)
	}

	// #6687 review: bootstrap cannot read the remote from the database the
	// recipe moves aside, so the guide checks sync.remote before the move.
	// The gate refuses `bd config set` on that database like any write, so
	// the fix is an edit of config.yaml.
	before := strings.Join(strings.Fields(section[:open]), " ")
	if !strings.Contains(section[:open], "```yaml\nsync:\n  remote: <url>\n```\n") {
		t.Errorf("adopt paragraph lacks the config.yaml sync.remote block before the recipe:\n%s", section[:open])
	}
	if strings.Contains(section, "bd config set sync.remote") {
		t.Errorf("the adopt section prescribes `bd config set sync.remote`, which the gate refuses:\n%s", section)
	}
	for _, want := range []string{
		"Bootstrap does not read the remote from the database you move aside",
		"`bd config get sync.remote` prints the remote to adopt",
		"If it does not, set it by hand in the `config.yaml` of the `.beads` that `bd where` reports: `bd config set` is refused while the database has pending migrations",
		// #6687 review: the move window and the anchor.
		"stop every `bd` process that uses this database, and start none until bootstrap has finished",
		"agent sessions whose hooks run `bd prime`",
		"`bd` in the repository's other git worktrees",
		"from the directory that contains the `.beads` that `bd where` reports",
	} {
		if !strings.Contains(before, want) {
			t.Errorf("adopt paragraph lacks %q before the recipe:\n%s", want, before)
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
