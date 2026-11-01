//go:build cgo

// cgo only: the goldens pin the cgo build, which every lane that runs this
// package's full suite compiles. The !cgo build replaces the federation
// subtree with a one-command stub (federation_nocgo.go), so its tree differs
// by design.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The committed command-surface goldens. Regenerate after an intentional CLI
// change, then review the diff like code:
//
//	BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 go test -tags gms_pure_go ./cmd/bd -run '^TestCommand(Tree|Help)Golden$'
//
// CGO_ENABLED=1 because this file builds only with cgo: with cgo off the
// command matches no test, exits 0 and leaves the goldens as they were.
const (
	commandTreeGoldenPath = "testdata/command_tree/tree.golden"
	commandHelpGoldenDir  = "testdata/command_tree/help"
	commandGoldenRegen    = "BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 go test -tags gms_pure_go ./cmd/bd -run '^TestCommand(Tree|Help)Golden$'"
)

// commandHelpGoldenArgs is the representative set of commands whose exact
// --help output is pinned, each as the argv after "bd". The tree golden
// covers every command's metadata; these cover the rendering (usage line,
// flag table layout, inherited global flags, root command groups).
var commandHelpGoldenArgs = [][]string{
	{},
	{"create"},
	{"show"},
	{"list"},
	{"update"},
	{"close"},
	{"version"},
	{"versions"},
	{"config"},
	{"serve"},
}

// TestCommandTreeGolden pins the whole bd command tree: for every command and
// subcommand, hidden ones included, its Use, Aliases, Short, Long, Example,
// group, hidden/deprecated state, runnability and annotations, and every
// local and persistent flag it defines (name, shorthand, type, default,
// usage, hidden, deprecation, annotations). Any added, removed or reworded
// command or flag fails this test until the golden is regenerated, so the
// change shows up as a reviewed testdata diff. It reads the in-memory tree
// only: no store or subprocess.
//
// docs/cli-docs.pin pins the generated CLI reference (and check-doc-flags)
// to a release, so drift on main is not caught there; this golden is the
// live guard for the command surface.
func TestCommandTreeGolden(t *testing.T) {
	pkgDir := enterOrdinaryWorkspaceForGolden(t)
	primeCommandTreeForGolden()
	flagCounts := commandFlagCounts(rootCmd)
	got := renderCommandTreeGolden(rootCmd)
	// A renderer that iterated a map would pass on the run that regenerated
	// the golden and flake afterwards.
	if again := renderCommandTreeGolden(rootCmd); again != got {
		t.Fatalf("command tree rendering is not deterministic:\n%s", goldenLineDiff(got, again))
	}
	// Nor may rendering change the tree it reads: tests sharing this binary
	// pin a command's exact flag set (TestServeFlags). See goldenLocalFlags.
	if after := commandFlagCounts(rootCmd); after != flagCounts {
		t.Fatalf("rendering the command tree added flags to a command's own flag set:\n%s", goldenLineDiff(flagCounts, after))
	}
	checkCommandGolden(t, pkgDir, commandTreeGoldenPath, got)
}

// TestCommandHelpGolden pins the exact `bd <command> --help` output of a
// representative set of commands, executed in-process through the real root
// command. --help returns before the root pre-run, so no store is opened.
// Color escapes are stripped so the snapshot is independent of terminal
// detection; otherwise the snapshots are byte-identical to what a built bd
// prints.
func TestCommandHelpGolden(t *testing.T) {
	pkgDir := enterOrdinaryWorkspaceForGolden(t)
	// Parsing `bd serve --help` merges the root's persistent flags into
	// serveCmd's own flag set, which TestServeFlags pins; restoreServeGlobals
	// re-registers that set afterwards.
	restoreServeGlobals(t)

	want := map[string]bool{}
	for _, args := range commandHelpGoldenArgs {
		name := strings.Join(append([]string{"bd"}, args...), "_")
		file := name + ".golden"
		want[file] = true
		t.Run(name, func(t *testing.T) {
			checkCommandGolden(t, pkgDir, filepath.Join(commandHelpGoldenDir, file), renderCommandHelpGolden(t, args))
		})
	}

	// The directory holds exactly the listed snapshots: a command dropped
	// from the list must not leave a stale file that still looks pinned.
	dir := filepath.Join(pkgDir, commandHelpGoldenDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", commandHelpGoldenDir, err)
	}
	for _, e := range entries {
		if want[e.Name()] {
			continue
		}
		if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				t.Fatalf("remove stale help golden %s: %v", e.Name(), err)
			}
			t.Logf("removed stale help golden %s", e.Name())
			continue
		}
		t.Errorf("%s/%s is not in commandHelpGoldenArgs; delete it or regenerate with %s", commandHelpGoldenDir, e.Name(), commandGoldenRegen)
	}
}

// enterOrdinaryWorkspaceForGolden moves the test into a fresh ordinary
// (dependency-mode) workspace: a .beads/metadata.json with no graph marker
// and no database behind it, so nothing can be read from or written to a
// store. It returns the package directory, where the goldens live.
func enterOrdinaryWorkspaceForGolden(t *testing.T) string {
	t.Helper()
	isolateBeadsDirForTest(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	beadsDir := filepath.Join(ws, ".beads")
	if err := os.Mkdir(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)
	return pkgDir
}

// primeCommandTreeForGolden completes the tree the way the bd binary does
// before it dispatches: main() adds cobra's help command and its
// --all/--doc/--list/--docs-root flags, and ExecuteC adds the completion
// command. All three are idempotent, so the tree is the same whether or not
// an earlier in-process test already executed rootCmd.
func primeCommandTreeForGolden() {
	rootCmd.InitDefaultHelpCmd()
	registerHelpAllFlag()
	rootCmd.InitDefaultCompletionCmd()
}

// renderCommandTreeGolden walks cmd's tree depth first, children sorted by
// name, and renders one block per command.
func renderCommandTreeGolden(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("# bd command tree (cgo build), pinned by TestCommandTreeGolden.\n")
	b.WriteString("# Regenerate after an intentional CLI change:\n")
	b.WriteString("#   " + commandGoldenRegen + "\n")

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		writeCommandGoldenBlock(&b, c)
		children := append([]*cobra.Command(nil), c.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, sub := range children {
			// cobra adds the hidden shell-completion request commands only
			// for an execution that invokes them, and then never removes
			// them: whether they are present depends on test history, not
			// on the bd surface.
			if sub.Name() == cobra.ShellCompRequestCmd || sub.Name() == cobra.ShellCompNoDescRequestCmd {
				continue
			}
			walk(sub)
		}
	}
	walk(root)
	return b.String()
}

func writeCommandGoldenBlock(b *strings.Builder, c *cobra.Command) {
	fmt.Fprintf(b, "\n%s\n", c.CommandPath())
	fmt.Fprintf(b, "  use: %q\n", c.Use)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(b, "  aliases: %q\n", c.Aliases)
	}
	fmt.Fprintf(b, "  short: %q\n", c.Short)
	if c.GroupID != "" {
		fmt.Fprintf(b, "  group: %q\n", c.GroupID)
	}
	fmt.Fprintf(b, "  hidden: %t\n", c.Hidden)
	if c.Deprecated != "" {
		fmt.Fprintf(b, "  deprecated: %q\n", c.Deprecated)
	}
	fmt.Fprintf(b, "  runnable: %t\n", c.Runnable())
	if c.DisableFlagParsing {
		b.WriteString("  disable-flag-parsing: true\n")
	}
	if len(c.ValidArgs) > 0 {
		fmt.Fprintf(b, "  valid-args: %q\n", c.ValidArgs)
	}
	if len(c.Annotations) > 0 {
		fmt.Fprintf(b, "  annotations: %s\n", formatGoldenAnnotations(c.Annotations))
	}
	writeGoldenTextBlock(b, "long", c.Long)
	writeGoldenTextBlock(b, "example", c.Example)
	for _, line := range commandGoldenFlagLines(c.PersistentFlags()) {
		fmt.Fprintf(b, "  persistent-flag %s\n", line)
	}
	for _, line := range commandGoldenFlagLines(goldenLocalFlags(c)) {
		fmt.Fprintf(b, "  flag %s\n", line)
	}
}

// goldenLocalFlags returns the non-persistent flags c registers itself,
// including any that shadow an inherited flag: the set
// c.LocalNonPersistentFlags() reports, read without calling it. That
// accessor, like LocalFlags and InheritedFlags, runs cobra's
// mergePersistentFlags, which copies every inherited persistent flag into
// c.Flags() for the rest of the process. Over the whole tree that would leave
// `bd serve` carrying the root's --json, --db and the rest, and break
// TestServeFlags (see TestNoSubcommandShadowsRootJSONFlag).
//
// c.Flags() may already hold such copies from an earlier in-process
// execution. They are recognized by identity, the way cobra itself tells
// them apart: an inherited flag is the nearest ancestor's own *pflag.Flag of
// that name, and a shadowing flag is a different one.
func goldenLocalFlags(c *cobra.Command) *pflag.FlagSet {
	local := pflag.NewFlagSet(c.Name(), pflag.ContinueOnError)
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if c.PersistentFlags().Lookup(f.Name) != nil {
			return // rendered as a persistent-flag line
		}
		for p := c.Parent(); p != nil; p = p.Parent() {
			if inherited := p.PersistentFlags().Lookup(f.Name); inherited != nil {
				if inherited == f {
					return
				}
				break
			}
		}
		local.AddFlag(f)
	})
	return local
}

// commandFlagCounts renders how many flags each command's own flag set holds,
// one line per command, read without merging anything into it.
func commandFlagCounts(root *cobra.Command) string {
	var b strings.Builder
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		n := 0
		c.Flags().VisitAll(func(*pflag.Flag) { n++ })
		fmt.Fprintf(&b, "%s %d\n", c.CommandPath(), n)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return b.String()
}

// writeGoldenTextBlock writes a multi-line field one source line per golden
// line, behind a "|" gutter so leading whitespace and blank lines survive.
func writeGoldenTextBlock(b *strings.Builder, name, text string) {
	if text == "" {
		return
	}
	fmt.Fprintf(b, "  %s:\n", name)
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteString("    |\n")
			continue
		}
		fmt.Fprintf(b, "    | %s\n", line)
	}
}

// commandGoldenFlagLines renders each flag of fs on one line, sorted by name.
// cobra's own lazily added --help flag (FlagSetByCobraAnnotation) is skipped:
// it exists only on commands an earlier in-process execution reached.
func commandGoldenFlagLines(fs *pflag.FlagSet) []string {
	var flags []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) {
		if _, byCobra := f.Annotations[cobra.FlagSetByCobraAnnotation]; byCobra {
			return
		}
		flags = append(flags, f)
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })

	lines := make([]string, 0, len(flags))
	for _, f := range flags {
		var b strings.Builder
		fmt.Fprintf(&b, "--%s", f.Name)
		if f.Shorthand != "" {
			fmt.Fprintf(&b, " -%s", f.Shorthand)
		}
		fmt.Fprintf(&b, " type=%s default=%q", f.Value.Type(), f.DefValue)
		if f.NoOptDefVal != "" && !implicitNoOptDefault(f) {
			fmt.Fprintf(&b, " no-opt-default=%q", f.NoOptDefVal)
		}
		if f.Hidden {
			b.WriteString(" hidden")
		}
		if f.Deprecated != "" {
			fmt.Fprintf(&b, " deprecated=%q", f.Deprecated)
		}
		if f.ShorthandDeprecated != "" {
			fmt.Fprintf(&b, " shorthand-deprecated=%q", f.ShorthandDeprecated)
		}
		if len(f.Annotations) > 0 {
			fmt.Fprintf(&b, " annotations=%s", formatGoldenAnnotations(f.Annotations))
		}
		fmt.Fprintf(&b, " usage=%q", f.Usage)
		lines = append(lines, b.String())
	}
	return lines
}

// implicitNoOptDefault reports whether f's NoOptDefVal is the one pflag gives
// every flag of its type (a bare --bool means true, a bare --count adds one),
// which would only repeat the type on each line.
func implicitNoOptDefault(f *pflag.Flag) bool {
	switch f.Value.Type() {
	case "bool":
		return f.NoOptDefVal == "true"
	case "count":
		return f.NoOptDefVal == "+1"
	}
	return false
}

// formatGoldenAnnotations renders a command's or a flag's annotations sorted
// by key.
func formatGoldenAnnotations[V string | []string](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// renderCommandHelpGolden runs `bd <args> --help` through rootCmd in-process
// and returns its stdout with color escapes stripped. cobra keeps the parsed
// --help value on the shared tree, so every flag is put back afterwards: a
// later in-process `bd create ...` must not print help instead.
//
// internal/ui decides color once, at init, from CLICOLOR_FORCE and whether the
// test binary's stdout is a terminal. CI never gives it one, so there the
// strip removes nothing; it is what keeps a run from a terminal matching the
// goldens.
func renderCommandHelpGolden(t *testing.T, args []string) string {
	t.Helper()
	argv := append(append([]string(nil), args...), "--help")
	primeCommandTreeForGolden()
	var execErr error
	out := captureStdout(t, func() error {
		rootCmd.SetArgs(argv)
		defer rootCmd.SetArgs(nil)
		defer resetCommandFlags(rootCmd)
		_, execErr = rootCmd.ExecuteC()
		return nil
	})
	if execErr != nil {
		t.Fatalf("bd %s: %v", strings.Join(argv, " "), execErr)
	}
	return ansi.Strip(out)
}

// checkCommandGolden compares got with the golden at rel under pkgDir, or
// rewrites the golden when BD_UPDATE_GOLDEN=1 (the package's shared golden
// switch).
func checkCommandGolden(t *testing.T, pkgDir, rel, got string) {
	t.Helper()
	path := filepath.Join(pkgDir, rel)
	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", rel, err)
		}
		t.Logf("updated golden %s", rel)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with %s): %v", rel, commandGoldenRegen, err)
	}
	if bytes.Equal([]byte(got), want) {
		return
	}
	t.Errorf("bd command surface drifted from %s.\n%s\nIf the change is intended, regenerate with\n  %s\nand review the testdata diff.",
		rel, goldenLineDiff(string(want), got), commandGoldenRegen)
}

// goldenLineDiff reports the region where want and got differ: the common
// leading and trailing lines are trimmed and the rest is shown as -/+ lines,
// capped so a large drift stays readable (the reviewed artifact is the
// regenerated testdata diff, not this message).
func goldenLineDiff(want, got string) string {
	const maxLines = 40
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	pre := 0
	for pre < len(w) && pre < len(g) && w[pre] == g[pre] {
		pre++
	}
	suf := 0
	for suf < len(w)-pre && suf < len(g)-pre && w[len(w)-1-suf] == g[len(g)-1-suf] {
		suf++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "first difference at line %d:\n", pre+1)
	write := func(sign string, lines []string) {
		for i, line := range lines {
			if i == maxLines {
				fmt.Fprintf(&b, "%s ... (%d more lines)\n", sign, len(lines)-maxLines)
				return
			}
			fmt.Fprintf(&b, "%s %s\n", sign, line)
		}
	}
	write("-", w[pre:len(w)-suf])
	write("+", g[pre:len(g)-suf])
	return b.String()
}
