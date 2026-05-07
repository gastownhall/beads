package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/ui"
)

var formulaSchemaCmd = &cobra.Command{
	Use:     "schema [primitive]",
	Aliases: []string{"primitives"},
	Short:   "Show the formula primitive index (every exported struct in types.go)",
	Long: `Show the formula primitive index — every exported struct an agent can write
in a .formula.toml/.formula.json, with field names, types, and tags.

The index is generated from internal/formula/types.go via go:generate; the
struct definitions are the source of truth, so this list cannot drift.

Examples:
  bd formula schema                 # list every primitive
  bd formula schema loop            # show LoopSpec fields
  bd formula primitives on_complete # alias; shows OnCompleteSpec
  bd formula schema --json          # machine-readable index

Curated example fixtures for each wired primitive live in
examples/formulas/primitives/ (with a smoke harness that proves they work).`,
	Args: cobra.MaximumNArgs(1),
	Run:  runFormulaSchema,
}

func runFormulaSchema(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		runFormulaSchemaList()
		return
	}
	runFormulaSchemaShow(args[0])
}

func runFormulaSchemaList() {
	if jsonOutput {
		outputJSON(formula.Primitives)
		return
	}

	fmt.Printf("Formula primitives (%d):\n\n", len(formula.Primitives))
	for _, p := range formula.Primitives {
		fmt.Printf("  %-18s %s\n", p.Name, firstDocLine(p.Doc))
	}
	fmt.Printf("\n%s\n", ui.RenderMuted("Show fields:  bd formula schema <name>"))
	fmt.Printf("%s\n", ui.RenderMuted("Examples:     examples/formulas/primitives/"))
}

func runFormulaSchemaShow(name string) {
	p := formula.PrimitiveByName(name)
	if p == nil {
		fmt.Fprintf(os.Stderr, "Error: unknown primitive %q\n\n", name)
		fmt.Fprintf(os.Stderr, "Available primitives:\n")
		for _, prim := range formula.Primitives {
			fmt.Fprintf(os.Stderr, "  %s\n", prim.Name)
		}
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(p)
		return
	}

	fmt.Printf("%s\n", ui.RenderAccent(p.Name))
	if p.Doc != "" {
		for _, line := range strings.Split(p.Doc, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}

	if len(p.Fields) == 0 {
		fmt.Printf("\n  %s\n", ui.RenderMuted("(no exposed fields)"))
		return
	}

	fmt.Printf("\nFields:\n")
	maxName, maxType := 0, 0
	for _, f := range p.Fields {
		if n := len(f.JSONName); n > maxName {
			maxName = n
		}
		if n := len(f.Type); n > maxType {
			maxType = n
		}
	}
	if maxName < 8 {
		maxName = 8
	}
	if maxType < 8 {
		maxType = 8
	}

	for _, f := range p.Fields {
		req := ""
		if f.Required {
			req = " " + ui.RenderFail("required")
		}
		fmt.Printf("  %-*s  %-*s%s\n", maxName, f.JSONName, maxType, f.Type, req)
		if f.Doc != "" {
			for _, line := range strings.Split(f.Doc, "\n") {
				fmt.Printf("    %s\n", ui.RenderMuted(line))
			}
		}
		if f.TOMLName != "" && f.TOMLName != f.JSONName {
			fmt.Printf("    %s\n", ui.RenderMuted(fmt.Sprintf("toml: %s", f.TOMLName)))
		}
		fmt.Println()
	}
}

func firstDocLine(doc string) string {
	if doc == "" {
		return ""
	}
	if i := strings.IndexByte(doc, '\n'); i >= 0 {
		return doc[:i]
	}
	return doc
}

func init() {
	formulaCmd.AddCommand(formulaSchemaCmd)
}
