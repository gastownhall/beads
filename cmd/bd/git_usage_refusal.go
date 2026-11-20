package main

import (
	"fmt"
	"io"
	"runtime"

	"github.com/steveyegge/beads/internal/remedy"
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

// gitUsageRefusalDocs is the troubleshooting entry both guidance blocks end on.
const gitUsageRefusalDocs = "https://github.com/gastownhall/beads/blob/main/docs/getting-started/sync-setup.md#usage-git-banner-on-push-pull-or-bootstrap"

// printGitUsageRefusalGuidance prints the guidance for a Git usage refusal
// inside Dolt's remote transport: git refused the arguments of an invocation
// Dolt made, before any authentication ran, and the credential hints around
// the banner are Dolt's wrapper on every git failure — not evidence of an
// authentication problem. An old Git, or a wrapper or shim first on PATH, is
// the usual cause; bd runs no version probes, mutates nothing and retries
// nothing, it tells the operator what to check. command is the bd command
// that failed and remote the named remote it ran against ("" for the
// default), which the trace recipe re-runs.
//
// bd dolt push, bd dolt pull, bd sync, dolt auto-push and bootstrap's clone
// print it after their own Error/warning line, once, on the classifier's
// positive match. Bootstrap's remote probe is bd's own git, not Dolt's, and
// prints printGitProbeUsageRefusalGuidance instead.
func printGitUsageRefusalGuidance(w io.Writer, command, remote string) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Git rejected the arguments of a git command Dolt's remote transport ran,")
	fmt.Fprintln(w, "before any authentication ran. The credential hints around the banner are")
	fmt.Fprintln(w, "added by Dolt's wrapper on every git failure; they do not establish an")
	fmt.Fprintln(w, "authentication problem.")
	printGitOnPathCheck(w)
	// A server keeps the environment it was started with
	// (doltserver.ServerSpawnEnv for one bd starts), so a PATH fixed here
	// only reaches it after a restart.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "In server mode, the Dolt server runs git with the PATH it started with;")
	fmt.Fprintln(w, "check that environment, not this one, and restart the server after")
	fmt.Fprintln(w, "fixing it (bd dolt stop; the next bd command starts it again).")
	// gittraceenv removes stderr-directed GIT_TRACE values (1, true, 2-9)
	// around every remote call Dolt makes — embedded, the CLI route and the
	// server bd spawns — and keeps an absolute file path, so only a file
	// target reaches the git that failed.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "If a current Git still fails, trace git to a file and see the")
	fmt.Fprintln(w, "troubleshooting entry (bd removes GIT_TRACE=1 before Dolt runs git):")
	printGitTraceRecipe(w, gitUsageRerun(command, remote))
	fmt.Fprintln(w, "In server mode, run bd dolt stop first so the server restarts with the")
	fmt.Fprintln(w, "variable set; afterwards, unset it and run bd dolt stop again.")
	fmt.Fprintln(w, "  "+gitUsageRefusalDocs)
}

// printGitProbeUsageRefusalGuidance prints the guidance for a Git usage
// refusal of bootstrap's own remote probe: the `git ls-remote -- <url>` bd
// runs itself, from this process's PATH and environment. Dolt is not
// involved, so there are no credential hints to explain and no server whose
// PATH applies.
func printGitProbeUsageRefusalGuidance(w io.Writer) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Git rejected the arguments of the git ls-remote bd ran to check the")
	fmt.Fprintln(w, "remote, before any authentication ran.")
	printGitOnPathCheck(w)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "If a current Git still fails, trace git to a file and see the")
	fmt.Fprintln(w, "troubleshooting entry:")
	printGitTraceRecipe(w, "bd bootstrap")
	fmt.Fprintln(w, "  "+gitUsageRefusalDocs)
}

// printGitOnPathCheck prints the old-Git-on-PATH check both guidance blocks
// share.
func printGitOnPathCheck(w io.Writer) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "An old Git, or a wrapper or shim first on PATH, can cause this. Check what")
	fmt.Fprintln(w, "the command actually runs:")
	fmt.Fprintln(w, "  git --version")
	fmt.Fprintln(w, "  where.exe git        (Windows)      which -a git      (Unix)")
	fmt.Fprintln(w, "Install a current Git and make sure it is the one found first on PATH.")
}

// gitUsageRerun spells the failed command for the trace recipe: command, plus
// --remote <name> for a named remote, pasted the way every remedy pastes a
// value (remedy.CommandArg: one shell word, or a placeholder with the name
// escaped after it).
func gitUsageRerun(command, remote string) string {
	if remote == "" {
		return command
	}
	name, note := remedy.CommandArg(runtime.GOOS, remote, "<remote>")
	return command + " --remote " + name + note
}

// printGitTraceRecipe prints the file-target GIT_TRACE recipe for rerun, in
// Unix shell and PowerShell form; the trace lands in that file.
func printGitTraceRecipe(w io.Writer, rerun string) {
	fmt.Fprintf(w, "  GIT_TRACE=/tmp/bd-git-trace.log %s\n", rerun)
	fmt.Fprintf(w, "  PowerShell: $env:GIT_TRACE=\"$env:TEMP\\bd-git-trace.log\"; %s\n", rerun)
}
