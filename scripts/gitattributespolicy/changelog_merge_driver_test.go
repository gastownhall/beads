package gitattributespolicy_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// changelogMergeDriver is the merge.changelog.driver command the .gitattributes
// comment tells a clone to configure. The fixtures below configure it verbatim,
// with the repository's .gitattributes and driver script committed at the
// paths it names, and merge with real git.
const changelogMergeDriver = "sh scripts/changelog-merge-driver.sh %O %A %B %L"

const changelogFixture = `# Changelog

## [Unreleased]

### Fixed

- **X older entry.**
  x detail

- **Y oldest entry.**
  y detail
`

const (
	mainChangelogEntry  = "- **M main entry.**\n  m detail\n\n"
	prChangelogEntry    = "- **P pr entry.**\n  p detail\n\n"
	lowerChangelogEntry = "- **L lower entry.**\n  l detail\n\n"
)

// changelogWithEntries is the fixture with entries added, in order, at the top
// of its Fixed section, where nearly every PR adds its entry.
func changelogWithEntries(entries ...string) string {
	return strings.Replace(
		changelogFixture,
		"- **X older entry.**",
		strings.Join(entries, "")+"- **X older entry.**",
		1)
}

// changelogWithSection is the fixture with a section added above Fixed.
func changelogWithSection(section string) string {
	return strings.Replace(changelogFixture, "### Fixed", section+"### Fixed", 1)
}

// releasedChangelogFixture holds a released version below [Unreleased], the
// spot a release forward-ported to main fills with its section.
const releasedChangelogFixture = `# Changelog

## [Unreleased]

### Security

- **S older entry.**
  s detail

## [1.3.2] - 2026-10-01

### Fixed

- **R released entry.**
  r detail
`

const (
	releasedChangelogEntry = "- **R released entry.**\n  r detail\n\n"
	hotfixChangelogRelease = "## [1.3.3] - 2026-11-03\n\n### Fixed\n\n- **H hotfix entry.**\n  h detail\n\n"
)

// releasedChangelogWithTop is the released fixture with lines added at the top
// of its Security section.
func releasedChangelogWithTop(lines string) string {
	return strings.Replace(releasedChangelogFixture, "- **S older entry.**", lines+"- **S older entry.**", 1)
}

// releasedChangelogWithEnd is the released fixture with lines added at the end
// of [Unreleased], above the released version.
func releasedChangelogWithEnd(lines string) string {
	return strings.Replace(releasedChangelogFixture, "## [1.3.2]", lines+"## [1.3.2]", 1)
}

// releaseUnreleased cuts release 1.3.3 from changelog: every entry of
// [Unreleased] becomes one of 1.3.3.
func releaseUnreleased(changelog string) string {
	return strings.Replace(changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n## [1.3.3] - 2026-11-03\n\n", 1)
}

// changelogUpgradeNote is an upgrade note whose shell snippet opens with a #
// comment, as the 1.3.0 release notes do.
const changelogUpgradeNote = "### Upgrading\n\n```bash\n# with your CURRENT bd, before installing:\nbd export --all -o backup.jsonl\n```\n\n"

// withChangelogUpgradeNote is changelog with changelogUpgradeNote added above
// the first occurrence of heading.
func withChangelogUpgradeNote(changelog, heading string) string {
	return strings.Replace(changelog, heading, changelogUpgradeNote+heading, 1)
}

// changelogMergeCommands bring the branch pr up to date with main. A rebase
// replays the branch onto main, so main's version is the current side of each
// conflict; a merge of main keeps the branch's.
var changelogMergeCommands = []struct {
	name    string
	command []string
}{
	{name: "rebase", command: []string{"rebase", "main"}},
	{name: "merge", command: []string{"merge", "--no-edit", "main"}},
}

func TestChangelogMergeDriverCommandIsTheDocumentedOne(t *testing.T) {
	want := "git config merge.changelog.driver '" + changelogMergeDriver + "'"
	for _, path := range []string{
		filepath.Join("..", "..", ".gitattributes"),
		filepath.Join("..", "changelog-merge-driver.sh"),
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(contents), want) {
			t.Fatalf("%s does not document the driver these tests configure:\nwant %s", path, want)
		}
	}
}

func TestChangelogMergeDriverKeepsBothSidesOfAnAddAddConflict(t *testing.T) {
	for _, test := range []struct {
		name    string
		command []string
		want    string
	}{
		// A rebase replays the PR onto main, so main's entry is the current side.
		{name: "rebase", command: []string{"rebase", "main"}, want: changelogWithEntries(mainChangelogEntry, prChangelogEntry)},
		{name: "merge", command: []string{"merge", "--no-edit", "main"}, want: changelogWithEntries(prChangelogEntry, mainChangelogEntry)},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newChangelogMergeRepo(t, true)
			repo.branch("main", "base")
			repo.commit("main entry", map[string]string{"CHANGELOG.md": changelogWithEntries(mainChangelogEntry)})
			repo.branch("pr", "base")
			repo.commit("pr entry", map[string]string{"CHANGELOG.md": changelogWithEntries(prChangelogEntry)})

			if output, err := repo.tryGit(test.command...); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(test.command, " "), err, output)
			}
			if unmerged := repo.unmerged(); len(unmerged) != 0 {
				t.Fatalf("unmerged paths after the merge: %q", unmerged)
			}
			// Each entry keeps its trailing blank line: git's default
			// conflict style would fold the one both sides end with.
			if got := repo.read("CHANGELOG.md"); got != test.want {
				t.Fatalf("CHANGELOG.md after the merge:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestChangelogMergeDriverLeavesOtherConflictsToTheTextMerge(t *testing.T) {
	mainEntry := []map[string]string{{"CHANGELOG.md": changelogWithEntries(mainChangelogEntry)}}
	for _, test := range []struct {
		name string
		main []map[string]string
		pr   []map[string]string
		path string
	}{
		{
			name: "an entry reworded below an insertion",
			main: mainEntry,
			pr: []map[string]string{{
				"CHANGELOG.md": strings.Replace(changelogFixture, "X older entry.", "X older entry, reworded.", 1),
			}},
		},
		{
			// A review fixup on the PR's own entry: the rebase keeps both
			// sides of the first commit and stops on the second.
			name: "a fixup rewording the branch's own new entry",
			main: mainEntry,
			pr: []map[string]string{
				{"CHANGELOG.md": changelogWithEntries("- **P first wording.**\n  p detail\n\n")},
				{"CHANGELOG.md": changelogWithEntries("- **P reviewed wording.**\n  p detail\n\n")},
			},
		},
		{
			name: "an entry removed below an insertion",
			main: mainEntry,
			pr: []map[string]string{{
				"CHANGELOG.md": strings.Replace(changelogFixture, "- **X older entry.**\n  x detail\n\n", "", 1),
			}},
		},
		{
			name: "both sides adding the same heading",
			main: []map[string]string{{"CHANGELOG.md": changelogWithSection("### Added\n\n- **M added.**\n\n")}},
			pr:   []map[string]string{{"CHANGELOG.md": changelogWithSection("### Added\n\n- **P added.**\n\n")}},
		},
		{
			// main holds a lower PR's entry under a newer one; the branch
			// adds the same entry in a commit git does not drop as applied.
			name: "an entry the other side already holds",
			main: []map[string]string{
				{"CHANGELOG.md": changelogWithEntries(lowerChangelogEntry)},
				{"CHANGELOG.md": changelogWithEntries(mainChangelogEntry, lowerChangelogEntry)},
			},
			pr: []map[string]string{{
				"CHANGELOG.md": changelogWithEntries(lowerChangelogEntry),
				"upper.txt":    "the upper PR's own change\n",
			}},
		},
		{
			name: "a version without its final newline",
			main: mainEntry,
			pr: []map[string]string{{
				"CHANGELOG.md": strings.TrimSuffix(changelogWithEntries(prChangelogEntry), "\n"),
			}},
		},
		{
			name: "a changelog both sides created",
			main: []map[string]string{{"pkg/CHANGELOG.md": mainChangelogEntry}},
			pr:   []map[string]string{{"pkg/CHANGELOG.md": prChangelogEntry}},
			path: "pkg/CHANGELOG.md",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newChangelogMergeRepo(t, true)
			repo.branch("main", "base")
			for index, files := range test.main {
				repo.commit(fmt.Sprintf("main %d", index+1), files)
			}
			repo.branch("pr", "base")
			for index, files := range test.pr {
				repo.commit(fmt.Sprintf("pr %d", index+1), files)
			}
			path := test.path
			if path == "" {
				path = "CHANGELOG.md"
			}

			if output, err := repo.tryGit("rebase", "main"); err == nil {
				t.Fatalf("git rebase main merged %s cleanly:\n%s\n%s", path, output, repo.read(path))
			}
			repo.requireTextMergeConflict(path)
		})
	}
}

func TestChangelogMergeDriverLeavesARevertedEntryConflicted(t *testing.T) {
	repo := newChangelogMergeRepo(t, true)
	repo.branch("main", "base")
	featureEntry := "- **F feature entry.**\n  f detail\n\n"
	feature := repo.commit("feature", map[string]string{
		"CHANGELOG.md": changelogWithEntries(featureEntry),
		"feature.go":   "package feature\n",
	})
	repo.commit("later entry", map[string]string{
		"CHANGELOG.md": changelogWithEntries(mainChangelogEntry, featureEntry),
	})

	if output, err := repo.tryGit("revert", "--no-edit", feature); err == nil {
		t.Fatalf("git revert kept the reverted feature's entry without a conflict:\n%s\n%s",
			output, repo.read("CHANGELOG.md"))
	}
	repo.requireTextMergeConflict("CHANGELOG.md")
}

func TestChangelogMergeWithoutTheDriverConfiguredIsATextMerge(t *testing.T) {
	repo := newChangelogMergeRepo(t, false)
	repo.branch("main", "base")
	repo.commit("main entry", map[string]string{"CHANGELOG.md": changelogWithEntries(mainChangelogEntry)})
	repo.branch("pr", "base")
	repo.commit("pr entry", map[string]string{"CHANGELOG.md": changelogWithEntries(prChangelogEntry)})

	if output, err := repo.tryGit("rebase", "main"); err == nil {
		t.Fatalf("git rebase main merged CHANGELOG.md with no driver configured:\n%s\n%s",
			output, repo.read("CHANGELOG.md"))
	}
	repo.requireTextMergeConflict("CHANGELOG.md")
}

func TestChangelogMergeDriverKeepsEachEntryInItsSection(t *testing.T) {
	mainFixed := "### Fixed\n\n- **M main fix.**\n  m detail\n\n"
	prFixed := "### Fixed\n\n- **P pr fix.**\n  p detail\n\n"
	// Only the detail line of this entry places it: its first line also
	// heads the released entry.
	sharedTitleEntry := "- **R released entry.**\n  p detail\n\n"
	for _, test := range []struct {
		name string
		// base is the version main and pr start from, the released fixture
		// when empty.
		base   string
		main   string
		pr     string
		rebase string
		merge  string
	}{
		{
			// The release section main gained goes below the branch's
			// entry, which stays in [Unreleased].
			name:   "a release section main added where the branch added an entry",
			main:   releasedChangelogWithEnd(hotfixChangelogRelease),
			pr:     releasedChangelogWithEnd(prChangelogEntry),
			rebase: releasedChangelogWithEnd(prChangelogEntry + hotfixChangelogRelease),
			merge:  releasedChangelogWithEnd(prChangelogEntry + hotfixChangelogRelease),
		},
		{
			name:   "a subsection main opened where the branch added an entry",
			main:   releasedChangelogWithEnd(mainFixed),
			pr:     releasedChangelogWithEnd(prChangelogEntry),
			rebase: releasedChangelogWithEnd(prChangelogEntry + mainFixed),
			merge:  releasedChangelogWithEnd(prChangelogEntry + mainFixed),
		},
		{
			name:   "a subsection the branch opened where main added an entry",
			main:   releasedChangelogWithEnd(mainChangelogEntry),
			pr:     releasedChangelogWithEnd(prFixed),
			rebase: releasedChangelogWithEnd(mainChangelogEntry + prFixed),
			merge:  releasedChangelogWithEnd(mainChangelogEntry + prFixed),
		},
		{
			name:   "an entry whose first line a released entry shares",
			main:   releasedChangelogWithEnd(mainChangelogEntry),
			pr:     releasedChangelogWithEnd(sharedTitleEntry),
			rebase: releasedChangelogWithEnd(mainChangelogEntry + sharedTitleEntry),
			merge:  releasedChangelogWithEnd(sharedTitleEntry + mainChangelogEntry),
		},
		{
			// The # comment in the code block of the released version sits
			// below the conflict, so the check of each entry still sees
			// every heading above it.
			name:   "an entry above a # comment in a code block",
			base:   withChangelogUpgradeNote(releasedChangelogFixture, "### Fixed"),
			main:   withChangelogUpgradeNote(releasedChangelogWithTop(mainChangelogEntry), "### Fixed"),
			pr:     withChangelogUpgradeNote(releasedChangelogWithTop(prChangelogEntry), "### Fixed"),
			rebase: withChangelogUpgradeNote(releasedChangelogWithTop(mainChangelogEntry+prChangelogEntry), "### Fixed"),
			merge:  withChangelogUpgradeNote(releasedChangelogWithTop(prChangelogEntry+mainChangelogEntry), "### Fixed"),
		},
	} {
		for _, merge := range changelogMergeCommands {
			t.Run(test.name+"/"+merge.name, func(t *testing.T) {
				repo := newReleasedChangelogMergeRepo(t, test.base, test.main, test.pr)
				if output, err := repo.tryGit(merge.command...); err != nil {
					t.Fatalf("git %s: %v\n%s", strings.Join(merge.command, " "), err, output)
				}
				want := test.rebase
				if merge.name == "merge" {
					want = test.merge
				}
				if got := repo.read("CHANGELOG.md"); got != want {
					t.Fatalf("CHANGELOG.md after the %s:\n%s\nwant:\n%s", merge.name, got, want)
				}
			})
		}
	}
}

func TestChangelogMergeDriverLeavesASectionChangeToTheTextMerge(t *testing.T) {
	for _, test := range []struct {
		name string
		// base is the version main and pr start from, the released fixture
		// when empty.
		base string
		main string
		pr   string
	}{
		{
			// [Unreleased]'s entries became release 1.3.3's on main, so
			// keeping both would ship the branch's entry in 1.3.3.
			name: "a release main cut above the branch's entry",
			main: releaseUnreleased(releasedChangelogWithTop(mainChangelogEntry)),
			pr:   releasedChangelogWithTop(prChangelogEntry),
		},
		{
			name: "subsections both sides opened at the same place",
			main: releasedChangelogWithEnd("### Added\n\n- **M main feature.**\n  m detail\n\n"),
			pr:   releasedChangelogWithEnd("### Changed\n\n- **P pr change.**\n  p detail\n\n"),
		},
		{
			// Each line of the branch's entry also sits in 1.3.2, so none of
			// them shows which section it belongs to.
			name: "an entry the released version also holds, below a release main cut",
			main: releaseUnreleased(releasedChangelogWithTop(mainChangelogEntry)),
			pr:   releasedChangelogWithTop(releasedChangelogEntry),
		},
		{
			// The branch's Fixed heading replaces the section the renamed
			// one names, so the check of its own entry cannot see where
			// the entry above that heading went.
			name: "an entry the released version also holds, above a heading of its side",
			main: strings.Replace(releasedChangelogWithEnd(mainChangelogEntry), "### Security", "### Changed", 1),
			pr:   releasedChangelogWithEnd(releasedChangelogEntry + "### Fixed\n\n" + prChangelogEntry),
		},
		{
			// The release heading below the branch's empty Fixed heading
			// hides from the check of its entry which release that Fixed
			// heading went to.
			name: "a heading of the branch above a shallower one, below a release main cut",
			main: releaseUnreleased(releasedChangelogWithEnd(mainChangelogEntry)),
			pr:   releasedChangelogWithEnd("### Fixed\n\n## [1.3.2-1] - 2026-10-20\n\n" + prChangelogEntry),
		},
		{
			// As in the first case, but the # comment in the code block
			// above the branch's entry reads like a heading and, in every
			// version, clears the release heading from the path the check
			// compares.
			name: "a release main cut above the branch's entry, below a # comment in a code block",
			base: withChangelogUpgradeNote(releasedChangelogFixture, "### Security"),
			main: withChangelogUpgradeNote(releaseUnreleased(releasedChangelogWithTop(mainChangelogEntry)), "### Security"),
			pr:   withChangelogUpgradeNote(releasedChangelogWithTop(prChangelogEntry), "### Security"),
		},
	} {
		for _, merge := range changelogMergeCommands {
			t.Run(test.name+"/"+merge.name, func(t *testing.T) {
				repo := newReleasedChangelogMergeRepo(t, test.base, test.main, test.pr)
				if output, err := repo.tryGit(merge.command...); err == nil {
					t.Fatalf("git %s merged CHANGELOG.md cleanly:\n%s\n%s",
						strings.Join(merge.command, " "), output, repo.read("CHANGELOG.md"))
				}
				repo.requireTextMergeConflict("CHANGELOG.md")
			})
		}
	}
}

// Most CHANGELOG.md merges conflict nowhere, and the driver writes git's own
// merge of them.
func TestChangelogMergeDriverWritesACleanMerge(t *testing.T) {
	added := "### Added\n\n- **P added entry.**\n  p detail\n\n"
	main := releasedChangelogWithTop(mainChangelogEntry)
	pr := strings.Replace(releasedChangelogFixture, "### Security", added+"### Security", 1)
	want := strings.Replace(main, "### Security", added+"### Security", 1)
	for _, merge := range changelogMergeCommands {
		t.Run(merge.name, func(t *testing.T) {
			repo := newReleasedChangelogMergeRepo(t, releasedChangelogFixture, main, pr)
			if output, err := repo.tryGit(merge.command...); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(merge.command, " "), err, output)
			}
			if got := repo.read("CHANGELOG.md"); got != want {
				t.Fatalf("CHANGELOG.md after the %s:\n%s\nwant:\n%s", merge.name, got, want)
			}
		})
	}
}

// changelogMergeRepo is a scratch repository on a branch named base, holding
// the repository's .gitattributes and driver script and the fixture changelog.
type changelogMergeRepo struct {
	t       *testing.T
	gitPath string
	path    string
}

func newChangelogMergeRepo(t *testing.T, configureDriver bool) *changelogMergeRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the merge driver fixtures run a POSIX sh script; Linux and macOS cover them")
	}
	repo := &changelogMergeRepo{t: t, gitPath: resolveEOLPolicyGit(t), path: t.TempDir()}
	requireEOLPolicyGitVersion(t, repo.gitPath, repo.path)
	files := map[string]string{"CHANGELOG.md": changelogFixture}
	for name, source := range map[string]string{
		".gitattributes":                    filepath.Join("..", "..", ".gitattributes"),
		"scripts/changelog-merge-driver.sh": filepath.Join("..", "changelog-merge-driver.sh"),
	} {
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		files[name] = string(contents)
	}

	repo.git("init", "--quiet")
	repo.git("symbolic-ref", "HEAD", "refs/heads/base")
	repo.git("config", "--local", "user.name", "Changelog Merge Test")
	repo.git("config", "--local", "user.email", "changelog-merge@example.invalid")
	if configureDriver {
		repo.git("config", "--local", "merge.changelog.driver", changelogMergeDriver)
	}
	repo.commit("base", files)
	return repo
}

// newReleasedChangelogMergeRepo is a scratch repository with the driver
// configured, baseChangelog (the released fixture when empty) on base,
// mainChangelog committed on main and prChangelog on pr, and pr checked out.
func newReleasedChangelogMergeRepo(t *testing.T, baseChangelog, mainChangelog, prChangelog string) *changelogMergeRepo {
	t.Helper()
	if baseChangelog == "" {
		baseChangelog = releasedChangelogFixture
	}
	repo := newChangelogMergeRepo(t, true)
	repo.commit("released", map[string]string{"CHANGELOG.md": baseChangelog})
	repo.branch("main", "base")
	repo.commit("main", map[string]string{"CHANGELOG.md": mainChangelog})
	repo.branch("pr", "base")
	repo.commit("pr", map[string]string{"CHANGELOG.md": prChangelog})
	return repo
}

// tryGit runs git in the repository. The driver runs sh, awk, tail and cat,
// so PATH keeps the host's directories after Git's own.
func (repo *changelogMergeRepo) tryGit(arguments ...string) (string, error) {
	repo.t.Helper()
	command := exec.Command(repo.gitPath, arguments...)
	command.Dir = repo.path
	command.Env = eolPolicyGitEnvironment(repo.path, repo.gitPath)
	for index, entry := range command.Env {
		if strings.HasPrefix(entry, "PATH=") {
			command.Env[index] = entry + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func (repo *changelogMergeRepo) git(arguments ...string) string {
	repo.t.Helper()
	output, err := repo.tryGit(arguments...)
	if err != nil {
		repo.t.Fatalf("git %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func (repo *changelogMergeRepo) branch(name, start string) {
	repo.t.Helper()
	repo.git("checkout", "--quiet", "-b", name, start)
}

// commit writes files on the checked-out branch, commits them and returns the
// commit.
func (repo *changelogMergeRepo) commit(message string, files map[string]string) string {
	repo.t.Helper()
	for name, contents := range files {
		path := filepath.Join(repo.path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			repo.t.Fatalf("create the directory of %s: %v", name, err)
		}
		writeEOLPolicyFixture(repo.t, path, []byte(contents))
	}
	repo.git("add", "--all")
	repo.git("commit", "--quiet", "-m", message)
	return strings.TrimSpace(repo.git("rev-parse", "HEAD"))
}

func (repo *changelogMergeRepo) read(name string) string {
	repo.t.Helper()
	contents, err := os.ReadFile(filepath.Join(repo.path, filepath.FromSlash(name)))
	if err != nil {
		repo.t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func (repo *changelogMergeRepo) unmerged() []string {
	repo.t.Helper()
	return strings.Fields(repo.git("diff", "--name-only", "--diff-filter=U"))
}

// requireTextMergeConflict fails unless path is the one unmerged path and holds
// the conflict markers of git's text merge, which the driver falls back to.
func (repo *changelogMergeRepo) requireTextMergeConflict(path string) {
	repo.t.Helper()
	if unmerged := repo.unmerged(); len(unmerged) != 1 || unmerged[0] != path {
		repo.t.Fatalf("unmerged paths = %q, want only %s", unmerged, path)
	}
	contents := repo.read(path)
	for _, marker := range []string{"<<<<<<< ", "=======", ">>>>>>> "} {
		found := false
		for _, line := range strings.Split(contents, "\n") {
			if strings.HasPrefix(line, marker) {
				found = true
				break
			}
		}
		if !found {
			repo.t.Fatalf("%s holds no %q line of a text merge conflict:\n%s", path, marker, contents)
		}
	}
}
