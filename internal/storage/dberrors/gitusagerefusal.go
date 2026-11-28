package dberrors

import "strings"

// IsGitUsageRefusal reports whether err carries git's argument-parse refusal —
// a "usage: git …" banner — as Dolt's git remote transport surfaces it
// (GH#6472).
//
// Dolt's embedded git transport spawns the system git it finds on PATH
// (`git --git-dir <cache> ls-remote --heads -- <url>`, and pushes carrying
// `--force-with-lease=<dst>:<expected>`), and wraps every failure in
// gitauth.NonInteractiveAuthError, which appends credential hints to the
// captured output. Over SQL that arrives as Error 1105, so a git too old to
// parse the separator or the lease argument looks like an authentication
// problem: the banner is evidence of an invocation refusal, the hints are not
// evidence of anything. bd's own ls-remote probe (cmd/bd/sync_git.go) reports
// the same banner through its sanitized error, which this classifier also
// recognizes.
//
// The match is on the lowercased message, like IsRemoteDanglingReference, so
// it survives every tier that flattens errors to text (SQL, http, proxied,
// CLI CombinedOutput). Authentication failures — with or without Dolt's hints —
// and the existing merge, push and table refusals never carry git's usage
// banner and stay unclassified. nil is not a refusal.
func IsGitUsageRefusal(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "usage: git ")
}
