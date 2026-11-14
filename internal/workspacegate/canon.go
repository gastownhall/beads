package workspacegate

import (
	"errors"
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

// resolveParent runs the gate-parent resolvers in preference order and
// returns the first accepted result:
//
//  1. dos: the handle's final path in drive-letter (or UNC) spelling;
//  2. guid: the handle's final path in volume-GUID spelling, for a volume
//     with no DOS name;
//  3. eval: filepath.EvalSymlinks, for filesystems that do not implement
//     the final-path query, so nothing that resolved before stops
//     resolving.
//
// Resolvers run lazily: a later one never runs once an earlier one is
// accepted. A resolver is accepted when it returns a non-empty path and a
// nil error (dos and guid have already been filtered through
// finalPathToGateParent). When all three fail the result is the joined
// errors, so errors.Is still finds fs.ErrNotExist; the unresolved input
// path is never returned as a fallback.
func resolveParent(dos, guid, eval func() (string, error)) (string, error) {
	steps := []struct {
		name    string
		resolve func() (string, error)
	}{
		{"final path (DOS)", dos},
		{"final path (volume GUID)", guid},
		{"EvalSymlinks", eval},
	}
	errs := make([]error, 0, len(steps))
	for _, s := range steps {
		p, err := s.resolve()
		if err == nil && p != "" {
			return p, nil
		}
		if err == nil {
			err = errors.New("empty path")
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
	}
	return "", errors.Join(errs...)
}
