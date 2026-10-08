//go:build !linux

package git

// discoverGitInProcess is linux-only: elsewhere path case-folding (darwin,
// windows) and ownership semantics (windows) make an exact in-process answer
// harder to prove, so discovery always runs git.
func discoverGitInProcess() (revParseResult, bool) {
	return revParseResult{}, false
}
