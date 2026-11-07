package beads

import "strings"

// This file implements the Windows arm of the SEC-003 safe-boundary check
// (isPathInSafeBoundary, GH#6101). The policy is deliberately portable: it
// operates only on injected facts, so Linux CI exercises the exact production
// decision logic. Host facts (environment, OS directories, physical path
// resolution) are gathered in winboundary_windows.go; winboundary_other.go
// holds the non-Windows stub.

// winCleanPath normalizes a Windows path spelling into the boundary's
// comparison form. It folds separators, collapses "." and ".." components
// without escaping the volume, and maps the extended \\?\ and \\.\ spellings
// of DOS and UNC paths onto their plain forms. Relative paths, drive-relative
// "C:foo" spellings, and extended forms with no plain equivalent (Volume
// GUIDs, GLOBALROOT, device-only \\.\ names) are rejected. Components keep
// their original case; comparisons use strings.EqualFold.
func winCleanPath(p string) (string, bool) {
	s := strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(s, `\\?\UNC\`):
		s = `\\` + s[len(`\\?\UNC\`):]
	case strings.HasPrefix(s, `\\?\`):
		s = s[len(`\\?\`):]
	case strings.HasPrefix(s, `\\.\`):
		rest := s[len(`\\.\`):]
		if !isWinDrivePath(rest) {
			return "", false // device-only name (\\.\PhysicalDrive0, \\.\COM1, …)
		}
		s = rest
	}
	switch {
	case isWinDrivePath(s):
		vol := s[:2]
		tail := s[2:]
		if tail == "" {
			return "", false // "C:" is drive-relative, not an absolute spelling
		}
		if tail[0] != '\\' {
			return "", false // "C:foo" resolves against the drive's CWD
		}
		comps := cleanWinComps(strings.Split(tail[1:], `\`))
		if len(comps) == 0 {
			return vol + `\`, true // drive root
		}
		return vol + `\` + strings.Join(comps, `\`), true
	case strings.HasPrefix(s, `\\`):
		parts := strings.Split(s[2:], `\`)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", false // UNC needs both a server and a share
		}
		comps := cleanWinComps(parts[2:])
		vol := `\\` + parts[0] + `\` + parts[1]
		if len(comps) == 0 {
			return vol, true // share root
		}
		return vol + `\` + strings.Join(comps, `\`), true
	default:
		return "", false // relative or otherwise unsupported
	}
}

func isWinDrivePath(s string) bool {
	return len(s) >= 2 && isWinDriveLetter(s[0]) && s[1] == ':'
}

func isWinDriveLetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// cleanWinComps collapses "." and ".." components without escaping the
// volume: ".." with nothing below it is dropped, mirroring filepath.Clean
// applied above a volume root.
func cleanWinComps(comps []string) []string {
	out := make([]string, 0, len(comps))
	for _, c := range comps {
		switch c {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, c)
		}
	}
	return out
}

// winWithin reports whether path is root or lies below it, case-insensitively
// and on component boundaries (so C:\WindowsFoo is not inside C:\Windows).
// Both must be winCleanPath forms.
func winWithin(root, path string) bool {
	root = strings.TrimSuffix(root, `\`)
	if root == "" || path == "" {
		return false
	}
	if strings.EqualFold(root, path) {
		return true
	}
	return len(path) > len(root) &&
		strings.EqualFold(path[:len(root)], root) &&
		path[len(root)] == '\\'
}

// winIsVolumeRoot reports whether a winCleanPath form names a drive root
// (C:\) or a UNC share root (\\server\share) with nothing below it.
func winIsVolumeRoot(p string) bool {
	if strings.HasSuffix(p, `\`) {
		return true // canonical drive roots are "C:\"
	}
	return strings.HasPrefix(p, `\\`) && strings.Count(p, `\`) == 3
}

// windowsBoundaryInput carries the host-reported facts the Windows boundary
// policy needs. The Windows build fills it from the OS
// (winboundary_windows.go); tests inject synthetic values so the policy
// itself stays verifiable on Linux. Values are raw spellings; normalization
// and validation happen in newWindowsBoundary.
type windowsBoundaryInput struct {
	systemRootValues []string
	// resolve physically resolves a path (following junctions and symlinks)
	// to its final spelling; ok=false means unresolved. nil means no
	// resolution is available.
	resolve func(path string) (string, bool)
}

// windowsBoundary is the validated, normalized form of windowsBoundaryInput.
// Fields hold winCleanPath forms; an empty optional field means the fact is
// unavailable and the corresponding rule contributes nothing.
type windowsBoundary struct {
	systemRoots []string
	resolve     func(path string) (string, bool)
}

// newWindowsBoundary validates and normalizes the host facts. Malformed and
// volume-root values are dropped rather than trusted: system sources only
// ever add denials, so a bad value must not deny a whole drive.
func newWindowsBoundary(in windowsBoundaryInput) windowsBoundary {
	var b windowsBoundary
	for _, v := range in.systemRootValues {
		if canon, ok := winCleanPath(v); ok && !winIsVolumeRoot(canon) {
			b.systemRoots = append(b.systemRoots, canon)
		}
	}
	b.resolve = in.resolve
	return b
}

// resolvePath returns the physical form of a canonical path, or "" when it
// is unresolved. Physical answers matter exactly where a spelling can lie:
// carve-out containment, and aliases into denied roots.
func (b windowsBoundary) resolvePath(p string) string {
	if b.resolve == nil || p == "" {
		return ""
	}
	out, ok := b.resolve(p)
	if !ok || out == "" {
		return ""
	}
	canon, ok := winCleanPath(out)
	if !ok {
		return ""
	}
	return canon
}

// inSafeBoundary evaluates the Windows SEC-003 policy for a path in both its
// caller-supplied and absolute spellings. A spelling the policy cannot
// normalize is denied rather than admitted.
func (b windowsBoundary) inSafeBoundary(input, abs string) bool {
	lexInput, okIn := winCleanPath(input)
	lexAbs, okAbs := winCleanPath(abs)
	if !okIn || !okAbs {
		return false
	}
	phys := b.resolvePath(lexAbs)
	for _, root := range b.systemRoots {
		if winWithin(root, lexInput) || winWithin(root, lexAbs) {
			return false
		}
	}
	if phys != "" {
		for _, root := range b.systemRoots {
			if winWithin(root, phys) {
				return false
			}
		}
	}
	// A root spelled through an alias must still deny what it physically
	// contains (a relocated install named by a link).
	for _, root := range b.systemRoots {
		rootPhys := b.resolvePath(root)
		if rootPhys == "" || rootPhys == root {
			continue
		}
		if winWithin(rootPhys, lexInput) || winWithin(rootPhys, lexAbs) ||
			(phys != "" && winWithin(rootPhys, phys)) {
			return false
		}
	}
	return true
}
