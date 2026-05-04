package main

import (
	"bytes"
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

func TestHelpDocWritesSingleCommandMarkdownToProvidedWriter(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	show := testHelpCmd("show <id>", "Show an issue")
	root.AddCommand(show)

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "show"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"id: show",
		"title: bd show",
		"slug: /cli-reference/show",
		"Generated from `bd help --doc show`",
		"## bd show",
		"bd show <id>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writeSingleCommandDoc() missing %q in:\n%s", want, got)
		}
	}
}

func TestHelpDocNestedCommandUsesSafeIDAndFullCommandPath(t *testing.T) {
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
		"id: mol-pour",
		"title: bd mol pour",
		"slug: /cli-reference/mol-pour",
		"Generated from `bd help --doc mol pour`",
		"## bd mol pour",
		"bd mol pour <formula>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writeSingleCommandDoc() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## bd pour") {
		t.Fatalf("nested doc collapsed command path:\n%s", got)
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

func TestHelpDocEscapesMDXProse(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	root.AddCommand(testHelpCmd("assign <id> <name>", "Assign <id> to {name}"))

	var out bytes.Buffer
	if err := writeSingleCommandDoc(&out, root, "assign"); err != nil {
		t.Fatalf("writeSingleCommandDoc() error = %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "Assign &lt;id&gt; to &#123;name&#125;") {
		t.Fatalf("writeSingleCommandDoc() did not escape MDX prose:\n%s", got)
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
