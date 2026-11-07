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
// "C:foo" spellings, extended forms with no plain equivalent (Volume GUIDs,
// GLOBALROOT, device-only \\.\ names), and spellings Win32 would silently
// normalize away (alternate data stream colons, reserved characters, control
// bytes, trailing dot/space components) are rejected. Components keep their
// original case; comparisons use strings.EqualFold.
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
		if !winValidComps(comps) {
			return "", false
		}
		if len(comps) == 0 {
			return vol + `\`, true // drive root
		}
		return vol + `\` + strings.Join(comps, `\`), true
	case strings.HasPrefix(s, `\\`):
		parts := strings.Split(s[2:], `\`)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", false // UNC needs both a server and a share
		}
		if !winValidComp(parts[0]) || !winValidComp(parts[1]) {
			return "", false
		}
		comps := cleanWinComps(parts[2:])
		if !winValidComps(comps) {
			return "", false
		}
		vol := `\\` + parts[0] + `\` + parts[1]
		if len(comps) == 0 {
			return vol, true // share root
		}
		return vol + `\` + strings.Join(comps, `\`), true
	default:
		return "", false // relative or otherwise unsupported
	}
}

// winValidComps reports whether every component of a cleaned path has a
// stable plain form. Bare "." and ".." are consumed by cleaning first, so
// "C:\.." still names the drive root.
func winValidComps(comps []string) bool {
	for _, c := range comps {
		if !winValidComp(c) {
			return false
		}
	}
	return true
}

// winValidComp reports whether a path component can be compared meaningfully.
// A colon past the volume is an alternate data stream; < > " | ? * and bytes
// below 0x20 are reserved; Win32 strips a trailing dot or space — so none of
// those spellings name a file the boundary could locate. They are rejected
// rather than guessed at (c is never empty: cleaning drops empty parts).
func winValidComp(c string) bool {
	for i := 0; i < len(c); i++ {
		switch b := c[i]; b {
		case ':', '<', '>', '"', '|', '?', '*':
			return false
		default:
			if b < 0x20 {
				return false
			}
		}
	}
	last := c[len(c)-1]
	return last != '.' && last != ' '
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

// splitWinPath splits a canonical path into its parent and final component.
// Volume roots are their own parent, which stops walks above a volume.
func splitWinPath(p string) (parent, base string) {
	if winIsVolumeRoot(p) {
		return p, ""
	}
	i := strings.LastIndexByte(p, '\\')
	if i < 0 {
		return p, p
	}
	parent = p[:i]
	if len(parent) == 2 && isWinDrivePath(parent) {
		parent += `\`
	}
	return parent, p[i+1:]
}

// windowsBoundaryInput carries the host-reported facts the Windows boundary
// policy needs. The Windows build fills it from the OS
// (winboundary_windows.go); tests inject synthetic values so the policy
// itself stays verifiable on Linux. Values are raw spellings; normalization
// and validation happen in newWindowsBoundary.
type windowsBoundaryInput struct {
	systemRootValues    []string
	profileParentValues []string
	// homeValue is the current user's home directory; invalid, volume-root,
	// or profile-parent-level values leave the home unknown.
	homeValue string
	// tempValue is the OS temp directory; invalid values, volume roots, and
	// temp locations at or above a system or profile root are discarded as
	// carve-outs (proper descendants such as C:\Windows\Temp are kept).
	tempValue string
	// resolve physically resolves a path (following junctions and symlinks)
	// to its final spelling; ok=false means unresolved. nil means no
	// resolution is available.
	resolve func(path string) (string, bool)
}

// windowsBoundary is the validated, normalized form of windowsBoundaryInput.
// Fields hold winCleanPath forms; an empty optional field means the fact is
// unavailable and the corresponding rule contributes nothing.
type windowsBoundary struct {
	systemRoots    []string
	profileParents []string
	publicRoots    []string
	home           string
	tempRoot       string
	resolve        func(path string) (string, bool)
}

// newWindowsBoundary validates and normalizes the host facts. Malformed and
// volume-root values are dropped rather than trusted: system sources only
// ever add denials, so a bad value must not deny a whole drive.
func newWindowsBoundary(in windowsBoundaryInput) windowsBoundary {
	b := windowsBoundary{resolve: in.resolve}
	for _, v := range in.systemRootValues {
		if canon, ok := winCleanPath(v); ok && !winIsVolumeRoot(canon) {
			b.systemRoots = append(b.systemRoots, canon)
		}
	}
	for _, v := range in.profileParentValues {
		if canon, ok := winCleanPath(v); ok && !winIsVolumeRoot(canon) {
			b.profileParents = append(b.profileParents, canon)
		}
	}
	if home, ok := winCleanPath(in.homeValue); ok && !winIsVolumeRoot(home) {
		// A home at or above a profile parent cannot separate self from
		// peers, so it stays unknown and admits no private-profile
		// exception.
		aboveParent := false
		for _, pp := range b.profileParents {
			if winWithin(home, pp) {
				aboveParent = true
				break
			}
		}
		if !aboveParent {
			b.home = home
			// A home directly under a "Users" directory names one more
			// profile parent (a relocated or second profile tree on
			// another drive).
			if parent, _ := splitWinPath(home); !winIsVolumeRoot(parent) {
				if _, pbase := splitWinPath(parent); strings.EqualFold(pbase, "Users") {
					b.profileParents = append(b.profileParents, parent)
				}
			}
		}
	}
	// The temp carve-out must not swallow a deny rule: a temp location at or
	// above a system or profile root — lexically or physically — is
	// discarded. Proper descendants such as C:\Windows\Temp are kept, and
	// the temp rule runs before system denial so a SYSTEM-account temp keeps
	// working.
	if temp, ok := winCleanPath(in.tempValue); ok && !winIsVolumeRoot(temp) {
		if !b.winAtOrAboveAnyRoot(temp) {
			b.tempRoot = temp
		}
	}
	// Public is derived from the profile parents — never from the mutable
	// PUBLIC variable — mirroring /Users/Shared on macOS.
	for _, pp := range b.profileParents {
		if pub, ok := winCleanPath(pp + `\Public`); ok {
			b.publicRoots = append(b.publicRoots, pub)
		}
	}
	return b
}

// winAtOrAboveAnyRoot reports whether p is equal to or above any system or
// profile root, considering the successfully resolved physical forms on both
// sides.
func (b windowsBoundary) winAtOrAboveAnyRoot(p string) bool {
	phys := b.resolvePath(p)
	for _, group := range [][]string{b.systemRoots, b.profileParents} {
		for _, root := range group {
			rootPhys := b.resolvePath(root)
			if winWithin(p, root) {
				return true
			}
			if phys != "" && winWithin(phys, root) {
				return true
			}
			if rootPhys != "" && (winWithin(p, rootPhys) || (phys != "" && winWithin(phys, rootPhys))) {
				return true
			}
		}
	}
	return false
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

	// Temp carve-out, ahead of system denial so a SYSTEM-account temp under
	// C:\Windows\Temp still works. The temp tree is world-writable, so
	// admission requires physical containment within the resolved temp
	// root: a link planted under it that escapes is rejected, and an
	// unresolved path inside it fails closed.
	if b.tempRoot != "" {
		if winWithin(b.tempRoot, lexInput) || winWithin(b.tempRoot, lexAbs) ||
			(phys != "" && winWithin(b.tempRoot, phys)) {
			if phys == "" {
				return false
			}
			tempPhys := b.resolvePath(b.tempRoot)
			if tempPhys == "" {
				return false
			}
			return winWithin(tempPhys, phys)
		}
	}

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

	// Public carve-out: the OS-designated shared directory below a profile
	// parent, not a peer profile. Same fail-closed containment as temp.
	for _, pub := range b.publicRoots {
		if lexInput == pub || lexAbs == pub || winWithin(pub, lexInput) || winWithin(pub, lexAbs) {
			if phys == "" {
				return false
			}
			pubPhys := b.resolvePath(pub)
			if pubPhys == "" {
				return false
			}
			return winWithin(pubPhys, phys)
		}
	}

	// Private profiles: another user's home directory is off limits the way
	// /home/<other> is on POSIX. The profile parents themselves stay
	// admitted, as /home is today. Below a parent, only the current home is
	// allowed — spelled directly, or reached through an alias that resolves
	// into it. An unknown home admits no exception.
	for _, pp := range b.profileParents {
		if lexInput == pp || lexAbs == pp {
			continue // the parent root itself
		}
		if !winWithin(pp, lexInput) && !winWithin(pp, lexAbs) &&
			!(phys != "" && winWithin(pp, phys)) {
			continue
		}
		selfLexical := b.home != "" &&
			(winWithin(b.home, lexInput) || winWithin(b.home, lexAbs))
		selfPhysical := false
		if b.home != "" && phys != "" {
			if homePhys := b.resolvePath(b.home); homePhys != "" {
				selfPhysical = winWithin(homePhys, phys)
			}
		}
		if !selfLexical && !selfPhysical {
			return false
		}
	}
	return true
}
