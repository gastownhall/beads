//go:build !linux

package git

// discoverGitInProcess is linux-only: elsewhere path case-folding (darwin,
// windows) and ownership semantics (windows) make an exact in-process answer
// harder to prove, so discovery always runs git.
func discoverGitInProcess() (revParseResult, bool) {
	return revParseResult{}, false
}

// CommonDirInProcess is linux-only; see discoverGitInProcess.
func CommonDirInProcess(string) (commonDir string, isRepo, ok bool) {
	return "", false, false
}

// HasRemoteInProcess is linux-only; see discoverGitInProcess.
func HasRemoteInProcess(string) (has, ok bool) {
	return false, false
}
