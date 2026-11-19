package main

import (
	"fmt"
	"io"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// isGitUsageRefusalErr reports git's argument-parse refusal as Dolt's git
// remote transport surfaces it — the usage banner wearing credential hints
// that Dolt's auth wrapper appends to every git failure (GH#6472). See
// dberrors.IsGitUsageRefusal for where the banner comes from and what the
// classifier deliberately leaves unclassified.
func isGitUsageRefusalErr(err error) bool {
	return dberrors.IsGitUsageRefusal(err)
}

// gitUsageRefusalLead is the guidance's first line. Callers that have already
// printed the guidance inside an error message (bootstrap's probe enriches the
// error it reports) test for it to avoid printing the block twice.
const gitUsageRefusalLead = "Git rejected the arguments of a git command Dolt's remote transport ran,"

// printGitUsageRefusalGuidance prints the shared guidance for a Git usage
// refusal: git refused the arguments of an invocation Dolt's remote transport
// made, before any authentication ran, and the credential hints around the
// banner are Dolt's wrapper on every git failure — not evidence of an
// authentication problem. An old Git, or a wrapper or shim first on PATH, is
// the usual cause; bd runs no version probes, mutates nothing and retries
// nothing, it tells the operator what to check.
//
// bd dolt push, bd dolt pull, bd sync and dolt auto-push all print it after
// their own Error/warning line, once, on the classifier's positive match.
func printGitUsageRefusalGuidance(w io.Writer) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, gitUsageRefusalLead)
	fmt.Fprintln(w, "before any authentication ran. The credential hints around the banner are")
	fmt.Fprintln(w, "added by Dolt's wrapper on every git failure; they do not establish an")
	fmt.Fprintln(w, "authentication problem.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "An old Git, or a wrapper or shim first on PATH, can cause this. Check what")
	fmt.Fprintln(w, "the command actually runs:")
	fmt.Fprintln(w, "  git --version")
	fmt.Fprintln(w, "  where.exe git        (Windows)      which -a git      (Unix)")
	fmt.Fprintln(w, "Install a current Git and make sure it is the one found first on PATH.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "In server mode, the git that runs is the one on the server process's PATH;")
	fmt.Fprintln(w, "check that environment, not this one.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "If a current Git still fails, capture a trace and see the troubleshooting")
	fmt.Fprintln(w, "entry:")
	fmt.Fprintln(w, "  GIT_TRACE=1 bd dolt push        (PowerShell: $env:GIT_TRACE='1')")
	fmt.Fprintln(w, "  https://github.com/gastownhall/beads/blob/main/docs/getting-started/sync-setup.md#usage-git-banner-on-push-pull-or-bootstrap")
}
