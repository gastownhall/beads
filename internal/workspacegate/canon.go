package workspacegate

import (
	"fmt"
	"strings"
)

// This file holds the platform-neutral half of gate-parent
// canonicalization: pure string handling that the Windows resolver
// (canon_windows.go) composes and that the Linux lane table-tests.

// win32FilePrefix is the prefix GetFinalPathNameByHandle puts on every
// path it returns.
const win32FilePrefix = `\\?\`

// finalPathToGateParent converts a GetFinalPathNameByHandle result into
// the spelling a gate parent takes:
//
//   - \\?\C:\x            -> C:\x (and \\?\C:\ -> C:\)
//   - \\?\UNC\srv\share\x -> \\srv\share\x
//   - \\?\Volume{guid}\x  -> kept verbatim (a volume with no DOS name;
//     the bare root keeps its trailing separator)
//
// Any other shape is refused rather than guessed at. Dropping the
// \\?\ prefix from a long result is safe: Go's file operations re-apply
// it (fixLongPath) when a path needs it.
func finalPathToGateParent(s string) (string, error) {
	rest, ok := strings.CutPrefix(s, win32FilePrefix)
	if !ok {
		return "", fmt.Errorf("workspacegate: final path %q lacks the %s prefix", s, win32FilePrefix)
	}
	switch {
	case isDriveAbs(rest):
		return rest, nil
	case strings.HasPrefix(rest, `UNC\`):
		unc := strings.TrimPrefix(rest, `UNC\`)
		server, after, _ := strings.Cut(unc, `\`)
		share, _, _ := strings.Cut(after, `\`)
		if server == "" || share == "" {
			return "", fmt.Errorf("workspacegate: final path %q names no UNC server and share", s)
		}
		return `\\` + unc, nil
	case strings.HasPrefix(rest, "Volume{"):
		end := strings.IndexByte(rest, '}')
		if end < 0 || !strings.HasPrefix(rest[end+1:], `\`) {
			return "", fmt.Errorf("workspacegate: final path %q is not a volume GUID path", s)
		}
		return s, nil
	}
	return "", fmt.Errorf("workspacegate: final path %q has an unsupported shape", s)
}

// isDriveAbs reports whether s starts with a drive-absolute root such as
// C:\ .
func isDriveAbs(s string) bool {
	if len(s) < 3 || s[1] != ':' || s[2] != '\\' {
		return false
	}
	c := s[0] | 0x20 // ASCII lower-case
	return 'a' <= c && c <= 'z'
}
