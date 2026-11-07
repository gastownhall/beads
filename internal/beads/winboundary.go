package beads

import (
	"strings"
	"unicode/utf8"
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
	// ProfilesDir is the OS-reported parent of user profiles, "" when
	// unavailable.
	ProfilesDir string
	// Home is the current user's home directory, "" when unknown.
	Home string
}

// winPath is a cleaned absolute Windows path in comparison form: vol is a
// drive ("C:") or a UNC share (\\server\share), and comps are its components
// below the volume root, with dot segments removed.
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

// winValidComponent reports whether c is a path component Windows names
// unambiguously. A colon names an alternate data stream (or is invalid), and
// the other reserved characters, control characters and invalid UTF-8 are not
// valid file-name characters. Win32 silently strips trailing dots and spaces,
// so "Windows." and "Windows " can open C:\Windows: such a component is
// rejected rather than guessed at. "." and ".." are segments, not names.
func winValidComponent(c string) bool {
	if c == "." || c == ".." {
		return true
	}
	if !utf8.ValidString(c) || strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") {
		return false
	}
	for _, r := range c {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
			return false
		}
	}
	return true
}

// winInputSpellingOK validates the guard input as the caller gave it, before
// filepath.Abs (GetFullPathName) can strip trailing dots and spaces from it.
// Relative inputs are checked component by component.
func winInputSpellingOK(input string) bool {
	s := strings.ReplaceAll(input, "/", `\`)
	if strings.HasPrefix(s, `\\`) {
		_, ok := winCleanPath(s)
		return ok
	}
	if len(s) >= 2 && isDriveLetter(s[0]) && s[1] == ':' {
		s = s[2:]
	}
	for _, c := range strings.Split(s, `\`) {
		if c != "" && !winValidComponent(c) {
			return false
		}
	}
	return true
}

// winCleanPath parses an absolute Windows path into comparison form. Both
// separators are accepted, repeated separators collapse, and "." / ".." are
// removed without escaping the volume root (as Windows itself does). The
// extended forms \\?\C:\ and \\.\C:\ map to the same representation as C:\,
// and \\?\UNC\server\share (or \\.\UNC\...) to \\server\share.
// Anything else is rejected: relative or drive-relative paths, device paths
// (\\.\pipe\x, \\.\C: itself), Volume GUID and GLOBALROOT forms, and any
// component winValidComponent refuses.
func winCleanPath(s string) (winPath, bool) {
	s = strings.ReplaceAll(s, "/", `\`)
	var vol, rest string
	switch {
	case strings.HasPrefix(s, `\\?\`) || strings.HasPrefix(s, `\\.\`):
		first, tail, found := strings.Cut(s[4:], `\`)
		switch {
		case found && strings.EqualFold(first, "UNC"):
			var ok bool
			if vol, rest, ok = winUNCVolume(tail); !ok {
				return winPath{}, false
			}
		case found && len(first) == 2 && isDriveLetter(first[0]) && first[1] == ':':
			vol, rest = strings.ToUpper(first), tail
		default:
			return winPath{}, false
		}
	case strings.HasPrefix(s, `\\`):
		var ok bool
		if vol, rest, ok = winUNCVolume(s[2:]); !ok {
			return winPath{}, false
		}
	case len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && s[2] == '\\':
		vol, rest = strings.ToUpper(s[:2]), s[3:]
	default:
		return winPath{}, false
	}
	p := winPath{vol: vol}
	for _, c := range strings.Split(rest, `\`) {
		if !winValidComponent(c) {
			return winPath{}, false
		}
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

// winUNCVolume splits "server\share\rest" into the UNC volume
// \\server\share and the rest. Both names must be present and valid.
func winUNCVolume(s string) (vol, rest string, ok bool) {
	server, tail, _ := strings.Cut(s, `\`)
	share, rest, _ := strings.Cut(tail, `\`)
	if server == "" || share == "" || server == "." || server == ".." ||
		share == "." || share == ".." || !winValidComponent(server) || !winValidComponent(share) {
		return "", "", false
	}
	return `\\` + server + `\` + share, rest, true
}

// windowsBoundary is the Windows policy built from one set of host facts.
type windowsBoundary struct {
	system   []winPath
	profiles []winPath
	// home is the current user's home, nil when unknown. An unknown home
	// admits no private profile at all.
	home *winPath
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

	// Profile parents hold every user's private profile. The fixed floor and
	// the OS report are joined by SystemDrive\Users and by the home's parent,
	// the latter only when it is named Users: inferring a parent from an
	// arbitrary home would turn sibling projects into "peer profiles".
	addProfile := func(s string) {
		if p, ok := winCleanPath(s); ok && !p.isVolumeRoot() {
			b.profiles = append(b.profiles, p)
		}
	}
	addProfile(`C:\Users`)
	addProfile(f.ProfilesDir)
	if d := getenv("SystemDrive"); len(d) == 2 && isDriveLetter(d[0]) && d[1] == ':' {
		addProfile(d + `\Users`)
	}
	home, homeOK := winCleanPath(f.Home)
	if homeOK && len(home.comps) >= 2 && strings.EqualFold(home.comps[len(home.comps)-2], "Users") {
		addProfile(winPath{vol: home.vol, comps: home.comps[:len(home.comps)-1]}.String())
	}
	// A home that is a volume root or sits at or above a profile parent
	// would exempt every profile below it, so it counts as unknown.
	if homeOK && !home.isVolumeRoot() {
		for _, r := range b.profiles {
			if r.within(home) {
				homeOK = false
				break
			}
		}
		if homeOK {
			b.home = &home
		}
	}
	return b
}

// inPeerProfile reports whether p lies inside a user profile other than the
// current user's: strictly below a profile parent (the parent itself stays
// admitted, as /home is on POSIX) and outside the current home.
func (b *windowsBoundary) inPeerProfile(p winPath) bool {
	for _, r := range b.profiles {
		if p.within(r) && len(p.comps) > len(r.comps) && (b.home == nil || !p.within(*b.home)) {
			return true
		}
	}
	return false
}

// allows reports whether a guard input (as given, and its absolute form) lies
// outside every denied Windows location.
func (b *windowsBoundary) allows(input, abs string) bool {
	if !winInputSpellingOK(input) {
		return false
	}
	p, ok := winCleanPath(abs)
	if !ok {
		return false
	}
	for _, r := range b.system {
		if p.within(r) {
			return false
		}
	}
	return !b.inPeerProfile(p)
}

// windowsPathInSafeBoundary is isPathInSafeBoundary on Windows.
func windowsPathInSafeBoundary(input, abs string) bool {
	return newWindowsBoundary(hostWindowsFacts()).allows(input, abs)
}
