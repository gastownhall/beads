package beads

import (
	"strings"
)

// Windows SEC-003 boundary (GH#6101).
//
// The POSIX boundary in isPathInSafeBoundary compares forward-slash prefixes,
// so on Windows (backslashed, drive- or UNC-rooted, case-insensitive paths)
// nothing matched and every path was admitted. The Windows policy lives here,
// separate from the POSIX body, and works on Windows spellings independently
// of the host OS: host facts (system directories, environment) are gathered by
// hostWindowsFacts on Windows and injected by tests elsewhere, so the policy
// itself is exercised on every platform. Host filepath functions are never
// applied to these synthetic Windows paths.

// winFixedSystemRoots are denied even when the OS and environment report
// nothing; host-reported roots only add to them.
var winFixedSystemRoots = []string{
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
}

// winSystemRootEnv names the environment variables whose absolute values add
// system roots. They can only add denials, never remove one, so a spoofed or
// empty value cannot open a hole.
var winSystemRootEnv = []string{
	"SystemRoot", "windir",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramFiles(Arm)",
}

// windowsFacts are the host inputs to the Windows boundary, gathered fresh for
// every check (no global cache).
type windowsFacts struct {
	// Getenv reads the environment; nil reads nothing.
	Getenv func(string) string
	// WindowsDir is the OS-reported Windows directory, "" when unavailable.
	WindowsDir string
}

// winPath is a cleaned absolute Windows path in comparison form: vol is a
// drive ("C:") and comps are its components below the volume root, with dot
// segments removed.
type winPath struct {
	vol   string
	comps []string
}

func (p winPath) String() string {
	return p.vol + `\` + strings.Join(p.comps, `\`)
}

func (p winPath) isVolumeRoot() bool {
	return len(p.comps) == 0
}

// within reports whether p is root or a descendant of it. Volumes and
// components compare case-insensitively and only on whole components, so
// C:\Windows2 is not within C:\Windows.
func (p winPath) within(root winPath) bool {
	if !strings.EqualFold(p.vol, root.vol) || len(p.comps) < len(root.comps) {
		return false
	}
	for i, c := range root.comps {
		if !strings.EqualFold(p.comps[i], c) {
			return false
		}
	}
	return true
}

func isDriveLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// winCleanPath parses an absolute Windows path into comparison form. Both
// separators are accepted, repeated separators collapse, and "." / ".." are
// removed without escaping the volume root (as Windows itself does). The
// extended forms \\?\C:\ and \\.\C:\ map to the same representation as C:\.
// Anything else (relative or drive-relative paths) is rejected.
func winCleanPath(s string) (winPath, bool) {
	s = strings.ReplaceAll(s, "/", `\`)
	var vol, rest string
	switch {
	case strings.HasPrefix(s, `\\?\`) || strings.HasPrefix(s, `\\.\`):
		first, tail, found := strings.Cut(s[4:], `\`)
		if !found || len(first) != 2 || !isDriveLetter(first[0]) || first[1] != ':' {
			return winPath{}, false
		}
		vol, rest = first, tail
	case len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && s[2] == '\\':
		vol, rest = s[:2], s[3:]
	default:
		return winPath{}, false
	}
	p := winPath{vol: strings.ToUpper(vol)}
	for _, c := range strings.Split(rest, `\`) {
		switch c {
		case "", ".":
		case "..":
			if len(p.comps) > 0 {
				p.comps = p.comps[:len(p.comps)-1]
			}
		default:
			p.comps = append(p.comps, c)
		}
	}
	return p, true
}

// windowsBoundary is the Windows policy built from one set of host facts.
type windowsBoundary struct {
	system []winPath
}

func newWindowsBoundary(f windowsFacts) *windowsBoundary {
	getenv := f.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	b := &windowsBoundary{}
	addSystem := func(s string) {
		// Malformed values and volume roots are discarded: a root of C:\
		// would deny the whole drive.
		if p, ok := winCleanPath(s); ok && !p.isVolumeRoot() {
			b.system = append(b.system, p)
		}
	}
	for _, s := range winFixedSystemRoots {
		addSystem(s)
	}
	addSystem(f.WindowsDir)
	for _, k := range winSystemRootEnv {
		addSystem(getenv(k))
	}
	return b
}

// allows reports whether an absolute guard input lies outside every denied
// Windows location.
func (b *windowsBoundary) allows(abs string) bool {
	p, ok := winCleanPath(abs)
	if !ok {
		return false
	}
	for _, r := range b.system {
		if p.within(r) {
			return false
		}
	}
	return true
}

// windowsPathInSafeBoundary is isPathInSafeBoundary on Windows.
func windowsPathInSafeBoundary(abs string) bool {
	return newWindowsBoundary(hostWindowsFacts()).allows(abs)
}
