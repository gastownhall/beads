// Command schemagen regenerates internal/formula/schema_gen.go from
// internal/formula/types.go. Driven by the //go:generate directive in
// internal/formula/schema.go; not invoked at runtime.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/formula/schemagen"
)

func main() {
	var (
		typesPath = flag.String("types", "types.go", "path to types.go")
		outPath   = flag.String("out", "schema_gen.go", "output path")
	)
	flag.Parse()

	src, err := schemagen.Generate(*typesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemagen: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, src, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "schemagen: write %s: %v\n", *outPath, err)
		os.Exit(1)
	}
}
