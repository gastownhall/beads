package workspacegate

import (
	"errors"
	"fmt"
	"strings"
)

// resolveParent picks the first accepted canonical parent spelling from
// three injected resolvers, in order: the DOS-volume final path, the
// volume-GUID final path, and filepath.EvalSymlinks (today's behavior,
// kept as the last resort so a filesystem whose final-path query is
// unsupported still resolves exactly as before). Resolvers are called
// lazily, so the GUID query and EvalSymlinks never run when the DOS query
// succeeds.
//
// An accepted result is a resolver that returned no error — the final-path
// resolvers fold their query through finalPathToGateParent, so a rejected
// shape surfaces here as an error and the chain moves on. When every
// resolver fails, the causes are returned joined: errors.Is chains survive
// errors.Join, so fs.ErrNotExist (a missing parent) stays detectable. The
// unresolved input is never returned in place of a resolution.
func resolveParent(dos, guid, eval func() (string, error)) (string, error) {
	var errs []error
	for _, res := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"dos volume name", dos},
		{"volume GUID name", guid},
		{"evalsymlinks", eval},
	} {
		s, err := res.fn()
		if err == nil {
			return s, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", res.name, err))
	}
	return "", errors.Join(errs...)
}

// finalPathToGateParent converts the string a Windows
// GetFinalPathNameByHandle query returns into the spelling a gate path
// uses. The query result always carries the extended-length \\?\ prefix;
// gate paths keep the plain spelling for volumes that have one (C:\x\y,
// \\srv\share\x) — dropping the prefix is safe because Go's own file
// operations re-apply extended-length handling when they need it — and
// keep a \\?\Volume{...} spelling verbatim for volumes that have no drive
// letter, since there is no plainer spelling to use.
//
// The function is pure string handling (no Windows API), so its contract
// is table-tested on Linux. Any shape it does not recognize is an error,
// never a guess: the caller falls back to the next resolver (see
// resolveParent) instead of gating on a path it cannot name.
func finalPathToGateParent(s string) (string, error) {
	const (
		prefix       = `\\?\`
		uncPrefix    = `\\?\UNC\`
		volumePrefix = `\\?\Volume{`
	)
	switch {
	case s == "":
		return "", errors.New("workspacegate: final path is empty")
	case strings.HasPrefix(s, uncPrefix):
		// \\?\UNC\srv\share\x -> \\srv\share\x. Both a server and a share
		// are required: a bare server spelling names no directory a
		// workspace could sit in.
		rest := s[len(uncPrefix):]
		server, share, ok := strings.Cut(rest, `\`)
		if !ok || server == "" || share == "" {
			return "", fmt.Errorf("workspacegate: final path %q has no server\\share", s)
		}
		return `\\` + rest, nil
	case strings.HasPrefix(s, volumePrefix):
		// \\?\Volume{guid}\... — kept verbatim, including the bare volume
		// root's trailing separator, so a guarded directory directly at a
		// letterless volume root resolves rather than becoming a new hard
		// error.
		rest := s[len(volumePrefix):]
		end := strings.IndexByte(rest, '}')
		if end <= 0 || end+1 >= len(rest) || rest[end+1] != '\\' {
			return "", fmt.Errorf("workspacegate: malformed volume GUID final path %q", s)
		}
		return s, nil
	case strings.HasPrefix(s, prefix):
		// \\?\C:\x\y -> C:\x\y (drive-letter volume, uppercase drive
		// letter and rooted, the only shape the query returns).
		rest := s[len(prefix):]
		if len(rest) < 3 || rest[0] < 'A' || rest[0] > 'Z' || rest[1] != ':' || rest[2] != '\\' {
			return "", fmt.Errorf("workspacegate: unrecognized final path %q", s)
		}
		return rest, nil
	default:
		return "", fmt.Errorf("workspacegate: final path %q lacks the %s prefix", s, prefix)
	}
}
