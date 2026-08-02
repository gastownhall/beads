package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHelpListOutputsSortedTopLevelCommands(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(
		testHelpCmd("show", "Show an issue"),
		testHelpCmd("create", "Create an issue"),
		testHelpCmd("mol", "Molecule commands"),
	)

	var out bytes.Buffer
	listAllCommands(&out, root)

	got := strings.TrimSpace(out.String())
	want := "create\nmol\nshow"
	if got != want {
		t.Fatalf("listAllCommands() = %q, want %q", got, want)
	}
}

func TestHelpAllIncludesTopLevelAndNestedCommands(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	mol := testHelpCmd("mol", "Molecule commands")
	mol.AddCommand(testHelpCmd("pour", "Start a workflow"))
	root.AddCommand(mol)

	var out bytes.Buffer
	writeAllHelp(&out, root)
	got := out.String()

	for _, want := range []string{
		"# bd — Complete Command Reference",
		"[bd mol](#bd-mol)",
		"[bd mol pour](#bd-mol-pour)",
		"### bd mol",
		"#### bd mol pour",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writeAllHelp() missing %q in:\n%s", want, got)
		}
	}
}

// The per-command doc output is generic Markdown: title/description
// frontmatter only. Vendor-specific formats (Docusaurus, Mintlify, ...) are
// produced by repo post-processors, never by bd itself.
func TestHelpDocWritesGenericMarkdown(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	show := testHelpCmd("show <id>", "Show an issue")
	root.AddCommand(show)

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "show"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"title: \"bd show\"",
		"description: \"Show an issue\"",
		"<!-- AUTO-GENERATED: do not edit manually -->",
		"Generated from `bd help --doc show`",
		"bd show <id>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writeSingleCommandDoc() missing %q in:\n%s", want, got)
		}
	}
	for _, banned := range []string{"id: show", "slug:", "sidebar_position", "{/*"} {
		if strings.Contains(got, banned) {
			t.Fatalf("writeSingleCommandDoc() contains vendor-specific %q in:\n%s", banned, got)
		}
	}
}

func TestHelpDocNestedCommandUsesFullCommandPath(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	mol := testHelpCmd("mol", "Molecule commands")
	pour := testHelpCmd("pour <formula>", "Start a workflow")
	mol.AddCommand(pour)
	root.AddCommand(mol)

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "mol pour"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"title: \"bd mol pour\"",
		"Generated from `bd help --doc mol pour`",
		"bd mol pour <formula>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writeSingleCommandDoc() missing %q in:\n%s", want, got)
		}
	}
}

func TestHelpDocInvalidCommandReturnsError(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(testHelpCmd("show", "Show an issue"))

	var out bytes.Buffer
	err := writeSingleCommandDoc(&out, root, "missing")
	if err == nil {
		t.Fatal("writeSingleCommandDoc() error = nil, want command-not-found error")
	}
	if !strings.Contains(err.Error(), "command not found: missing") {
		t.Fatalf("writeSingleCommandDoc() error = %q", err)
	}
	if out.Len() != 0 {
		t.Fatalf("writeSingleCommandDoc() wrote output for invalid command:\n%s", out.String())
	}
}

func testHelpCmd(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Run:   func(cmd *cobra.Command, args []string) {},
	}
}

func TestHelpDocEscapesAngleBracketProse(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(testHelpCmd("assign <id> <name>", "Assign <id> to {name}"))

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "assign"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "Assign &lt;id&gt; to &#123;name&#125;") {
		t.Fatalf("writeSingleCommandDoc() did not escape prose:\n%s", got)
	}
	if !strings.Contains(got, "bd assign <id> <name>") {
		t.Fatalf("writeSingleCommandDoc() should keep usage code fences unescaped:\n%s", got)
	}
}

func TestHelpDocFlagTextDoesNotClaimDashMeansStdout(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	registerHelpAllFlag()

	helpCmd, _, err := rootCmd.Find([]string{"help"})
	if err != nil {
		t.Fatalf("find help command: %v", err)
	}
	flag := helpCmd.Flags().Lookup("doc")
	if flag == nil {
		t.Fatal("help --doc flag is not registered")
	}
	if strings.Contains(flag.Usage, "use - for stdout") {
		t.Fatalf("help --doc flag still documents unsupported '-' stdout sentinel: %q", flag.Usage)
	}
}

// Parent commands must document the usage line the binary actually prints:
// Cobra shows `bd mol [command]` for a non-runnable parent (and both lines
// for a runnable one), while UseLine() alone yields the misleading
// `bd mol [flags]` (reproduced against the live binary on 30 pages).
func TestCommandBodyUsageMatchesCobraForParentCommands(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	mol := &cobra.Command{Use: "mol", Short: "Molecule commands"} // no Run: not runnable
	mol.AddCommand(testHelpCmd("pour <formula>", "Start a workflow"))
	root.AddCommand(mol)

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "mol"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "bd mol [command]") {
		t.Errorf("parent usage missing 'bd mol [command]':\n%s", got)
	}
	if strings.Contains(got, "bd mol [flags]") {
		t.Errorf("parent usage shows 'bd mol [flags]', which the binary never prints:\n%s", got)
	}

	// A runnable command with subcommands gets both lines, like Cobra help.
	root2 := &cobra.Command{Use: "bd"}
	mail := testHelpCmd("mail [subcommand]", "Mail commands")
	mail.AddCommand(testHelpCmd("inbox", "List inbox"))
	root2.AddCommand(mail)

	out.Reset()
	if err := writeSingleCommandDoc(&out, root2, "mail"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got = out.String()
	if !strings.Contains(got, "bd mail [subcommand]") {
		t.Errorf("runnable parent should keep its UseLine:\n%s", got)
	}
	if !strings.Contains(got, "bd mail [command]") {
		t.Errorf("runnable parent should also show the [command] form:\n%s", got)
	}
}

// The generated index must publish the global (persistent) flags — the
// binary prints them on every --help, and no per-command page carries them.
func TestGenericIndexIncludesGlobalFlags(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.PersistentFlags().Bool("json", false, "JSON output for scripting")
	root.AddCommand(testHelpCmd("show <id>", "Show an issue"))
	dir := t.TempDir()

	if err := writeGeneratedCLIDocs(root, dir); err != nil {
		t.Fatalf("writeGeneratedCLIDocs() error = %v", err)
	}
	indexPath := filepath.Join(dir, "build", "cli-docs", "index.md")
	assertFileContains(t, indexPath, "## Global Flags")
	assertFileContains(t, indexPath, "--json")
}

func TestWriteGeneratedCLIDocsWritesGenericStagingTree(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	mol := testHelpCmd("mol", "Molecule commands")
	mol.AddCommand(testHelpCmd("pour <formula>", "Start a workflow"))
	root.AddCommand(
		testHelpCmd("show <id>", "Show an issue"),
		testHelpCmd("create", "Create an issue"),
		mol,
	)
	dir := t.TempDir()

	if err := writeGeneratedCLIDocs(root, dir); err != nil {
		t.Fatalf("writeGeneratedCLIDocs() error = %v", err)
	}

	assertFileContains(t, filepath.Join(dir, "docs", "CLI_REFERENCE.md"), "# bd — Complete Command Reference")

	// Staging tree: generic pages for the post-processors to consume.
	indexPath := filepath.Join(dir, "build", "cli-docs", "index.md")
	assertFileContains(t, indexPath, "title: CLI Reference")
	assertFileContains(t, indexPath, "](./show.md)")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"sidebar_position", "{/*", "slug:"} {
		if strings.Contains(string(index), banned) {
			t.Errorf("generic index.md contains vendor-specific %q:\n%s", banned, index)
		}
	}

	showPath := filepath.Join(dir, "build", "cli-docs", "show.md")
	assertFileContains(t, showPath, "title: \"bd show\"")
	assertFileContains(t, showPath, "<!-- AUTO-GENERATED: do not edit manually -->")

	molPath := filepath.Join(dir, "build", "cli-docs", "mol.md")
	assertFileContains(t, molPath, "## bd mol pour")

	// bd must not write vendor trees: no website output, no docs/cli-reference.
	if _, err := os.Stat(filepath.Join(dir, "website")); !os.IsNotExist(err) {
		t.Errorf("writeGeneratedCLIDocs() wrote a website/ tree; vendor outputs belong to post-processors")
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "cli-reference")); !os.IsNotExist(err) {
		t.Errorf("writeGeneratedCLIDocs() wrote docs/cli-reference/; that tree belongs to the post-processor")
	}
}

func TestWriteGeneratedCLIDocsReplacesStaleStagingFiles(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(testHelpCmd("show <id>", "Show an issue"))
	dir := t.TempDir()

	staging := filepath.Join(dir, "build", "cli-docs")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(staging, "removed-command.md")
	if err := os.WriteFile(stale, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeGeneratedCLIDocs(root, dir); err != nil {
		t.Fatalf("writeGeneratedCLIDocs() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale staging page survived regeneration")
	}
}

// commandDocID collapses punctuation to dashes, so distinct commands like
// `foo-bar` and `foo_bar` map to the same page file. The emitter must fail
// loudly instead of letting the later write silently win.
func TestWriteGenericCLIDocsDirFailsOnDocIDCollision(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(
		testHelpCmd("foo-bar", "First"),
		testHelpCmd("foo_bar", "Second"),
	)

	err := writeGenericCLIDocsDir(filepath.Join(t.TempDir(), "cli-docs"), root)
	if err == nil {
		t.Fatal("writeGenericCLIDocsDir() succeeded with colliding doc IDs; want error")
	}
	for _, want := range []string{"foo-bar", "foo_bar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error should name %q: %v", want, err)
		}
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q in:\n%s", path, want, string(data))
	}
}
