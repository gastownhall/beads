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
	Short:   "Show the formula schema index (every exported struct in types.go)",
	Long: `Show the formula schema index: every exported struct declared
in a .formula.toml/.formula.json, with field names, types, and tags.

The index is generated from internal/formula/types.go via go:generate; the
struct definitions are the source of truth, so this list cannot drift. It is
structural reference, not proof that every declared runtime behavior is wired.

Examples:
  bd formula schema                 # list every declared schema struct
  bd formula schema loop            # show LoopSpec fields
  bd formula primitives gate        # alias; shows Gate fields
  bd formula schema --json          # machine-readable index

Curated smoke-tested fixtures for wired primitives live in
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

	fmt.Printf("Formula schema structs (%d):\n\n", len(formula.Primitives))
	for _, p := range formula.Primitives {
		fmt.Printf("  %-18s %s\n", p.Name, firstDocLine(p.Doc))
	}
	fmt.Printf("\n%s\n", ui.RenderMuted("Show fields:       bd formula schema <name>"))
	fmt.Printf("%s\n", ui.RenderMuted("Wired examples:    examples/formulas/primitives/"))
	fmt.Printf("%s\n", ui.RenderMuted("Note: schema output is structural; smoke-tested examples are the verified authoring surface."))
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
