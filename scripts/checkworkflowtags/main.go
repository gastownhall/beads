package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/scripts/workflowtags"
)

func main() {
	violations, err := workflowtags.CheckDir(filepath.Join(".github", "workflows"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, violation := range violations {
		fmt.Fprintf(os.Stderr, "%s job %q step %q run line %d: %s\n", violation.Path, violation.Job, violation.Step, violation.Line, violation.Message)
	}
	if len(violations) != 0 {
		os.Exit(1)
	}
	fmt.Println("workflow build-tag declarations: all direct commands clear")
}
