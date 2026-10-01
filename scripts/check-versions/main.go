// check-versions validates every released Beads version surface through the
// same Go authority used by bd preflight.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/steveyegge/beads/internal/versioncheck"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr, versioncheck.CheckUVLockFreshness))
}

const (
	red   = "\033[0;31m"
	green = "\033[0;32m"
	reset = "\033[0m"
)

type expectedVersionValue struct {
	value     string
	specified bool
}

func (value *expectedVersionValue) String() string {
	return value.value
}

func (value *expectedVersionValue) Set(candidate string) error {
	if value.specified {
		return fmt.Errorf("--expect may be specified only once")
	}
	value.specified = true
	if candidate == "" {
		return fmt.Errorf("--expect requires a non-empty VERSION")
	}
	value.value = candidate
	return nil
}

func run(
	args []string,
	start string,
	stdout, stderr io.Writer,
	checkUVLock versioncheck.UVLockChecker,
) int {
	flags := flag.NewFlagSet("check-versions", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var expected expectedVersionValue
	flags.Var(
		&expected,
		"expect",
		"require the canonical release version to equal this exact value",
	)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: scripts/check-versions.sh [--expect VERSION]")
		return 2
	}
	root, found, err := versioncheck.FindRoot(start)
	if err != nil {
		fmt.Fprintf(stderr, "Cannot identify Beads release root: %v\n", err)
		return 1
	}
	if !found {
		fmt.Fprintln(stderr, "Cannot identify a Beads source repository from the current directory")
		return 1
	}
	report, err := versioncheck.Check(root)
	printReport(stdout, report)
	if err != nil {
		fmt.Fprintf(stderr, "\n%s❌ %v%s\n", red, err, reset)
		if report.CanonicalVersion != "" {
			fmt.Fprintf(
				stderr,
				"Run: scripts/update-versions.sh %s\nOr manually update the mismatched files.\n",
				report.CanonicalVersion,
			)
		}
		return 1
	}
	if expected.specified && report.CanonicalVersion != expected.value {
		fmt.Fprintf(
			stderr,
			"Canonical release version %s does not match required version %s\n",
			report.CanonicalVersion,
			expected.value,
		)
		// Distinct from status 1: the release surfaces agree with each other,
		// only the required version differs. Callers such as the release-tag
		// pre-push hook must not answer this with the version-drift remedy.
		return 3
	}
	if checkUVLock != nil {
		available, checkErr := checkUVLock(root)
		if available && checkErr != nil {
			fmt.Fprintln(
				stderr,
				red+"❌ MCP uv.lock: stale — run: uv lock --directory integrations/beads-mcp"+reset,
			)
			return 1
		}
		if available {
			fmt.Fprintln(stdout, green+"✓ MCP uv.lock: fresh (uv lock --check)"+reset)
		}
	}
	fmt.Fprintf(
		stdout,
		"\n%s✓ Version files in sync at: %s%s\n",
		green,
		report.CanonicalVersion,
		reset,
	)
	// The checker reads no documentation surface, so it must not imply one.
	fmt.Fprintln(stdout, "  (Version files only — this script does not check docs. The docs")
	fmt.Fprintln(stdout, "   release line and docs/cli-docs.pin are manual release steps; see")
	fmt.Fprintln(stdout, "   RELEASING.md.)")
	return 0
}

func printReport(output io.Writer, report versioncheck.Report) {
	if report.CanonicalVersion == "" {
		return
	}
	fmt.Fprintf(output, "Canonical version (from version.go): %s\n\n", report.CanonicalVersion)
	for _, item := range report.Sources {
		switch {
		case item.Problem == "":
			fmt.Fprintf(output, "%s✓ %s: %s%s\n", green, item.Description, item.Version, reset)
		case item.Version != "":
			fmt.Fprintf(
				output,
				"%s❌ %s: %s (expected %s)%s\n",
				red,
				item.Description,
				item.Version,
				item.Expected,
				reset,
			)
		default:
			fmt.Fprintf(
				output,
				"%s❌ %s: %s (expected %s)%s\n",
				red,
				item.Description,
				item.Problem,
				item.Expected,
				reset,
			)
		}
	}
}
