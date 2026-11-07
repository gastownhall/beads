package beads

import (
	"errors"
	"io/fs"
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
	// TempDir is os.TempDir(), "" when unknown.
	TempDir string
	// Resolve finds physical locations; nil resolves nothing.
	Resolve winResolver
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

// winResolver opens an existing path, following links, and returns its final
// physical path and whether it is a directory. An error matching
// fs.ErrNotExist proves the entry absent; any other error (a dangling link,
// access denied, an unavailable volume) leaves the path unresolved.
type winResolver func(path string) (final string, isDir bool, err error)

// winRoot is a policy root in both spellings: as named, and its physical
// location when that resolved.
type winRoot struct {
	lex      winPath
	phys     winPath
	resolved bool
}

func (r winRoot) String() string {
	return r.lex.String()
}

// contains reports whether p is within either spelling of the root.
func (r winRoot) contains(p winPath) bool {
	return p.within(r.lex) || (r.resolved && p.within(r.phys))
}

// strictlyContains is contains without the root itself.
func (r winRoot) strictlyContains(p winPath) bool {
	return (p.within(r.lex) && len(p.comps) > len(r.lex.comps)) ||
		(r.resolved && p.within(r.phys) && len(p.comps) > len(r.phys.comps))
}

// windowsBoundary is the Windows policy built from one set of host facts.
type windowsBoundary struct {
	resolve  winResolver
	system   []winRoot
	profiles []winRoot
	// home is the current user's home, nil when unknown. An unknown home
	// admits no private profile at all.
	home *winRoot
	// public holds <profile parent>\Public for each profile parent, the
	// shared profile (the /Users/Shared analog). The PUBLIC variable is never
	// trusted: pointing it at a peer profile would exempt that profile.
	public []winRoot
	// temp is the temp-directory carve-out, nil when unusable.
	temp *winRoot
}

func newWindowsBoundary(f windowsFacts) *windowsBoundary {
	getenv := f.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	b := &windowsBoundary{resolve: f.Resolve}
	addSystem := func(s string) {
		// Malformed values and volume roots are discarded: a root of C:\
		// would deny the whole drive.
		if p, ok := winCleanPath(s); ok && !p.isVolumeRoot() {
			b.system = append(b.system, b.root(p))
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
			b.profiles = append(b.profiles, b.root(p))
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
	// A home that is a volume root or sits at or above a profile parent, in
	// either spelling, would exempt every profile below it, so it counts as
	// unknown.
	if homeOK && !home.isVolumeRoot() {
		h := b.root(home)
		for _, r := range b.profiles {
			if h.contains(r.lex) || (r.resolved && h.contains(r.phys)) {
				homeOK = false
				break
			}
		}
		if homeOK {
			b.home = &h
		}
	}

	for _, r := range b.profiles {
		pub := winPath{vol: r.lex.vol, comps: append(r.lex.comps[:len(r.lex.comps):len(r.lex.comps)], "Public")}
		b.public = append(b.public, b.root(pub))
	}

	// A temp directory that is a volume root or sits at or above a system
	// root or profile parent, in either spelling, would admit what those
	// deny, so it is no carve-out. A proper descendant such as the SYSTEM
	// account's C:\Windows\Temp stays one.
	if t, ok := winCleanPath(f.TempDir); ok && !t.isVolumeRoot() {
		tr := b.root(t)
		usable := !(tr.resolved && tr.phys.isVolumeRoot())
		for _, r := range append(append([]winRoot{}, b.system...), b.profiles...) {
			if tr.contains(r.lex) || (r.resolved && tr.contains(r.phys)) {
				usable = false
				break
			}
		}
		if usable {
			b.temp = &tr
		}
	}
	return b
}

func (b *windowsBoundary) root(p winPath) winRoot {
	phys, ok := b.physical(p)
	return winRoot{lex: p, phys: phys, resolved: ok}
}

// physical resolves p to its physical location. Trailing components proven
// absent are carried over onto the longest existing ancestor, which must be a
// directory, so a not-yet-created BEADS_DIR still resolves through a link
// above it. Any other failure leaves p unresolved: walking above an entry
// that exists but cannot be resolved would erase exactly the link that
// matters.
func (b *windowsBoundary) physical(p winPath) (winPath, bool) {
	if b.resolve == nil {
		return winPath{}, false
	}
	cur := p
	var missing []string
	for {
		final, isDir, err := b.resolve(cur.String())
		if err == nil {
			if len(missing) > 0 && !isDir {
				return winPath{}, false
			}
			fp, ok := winCleanPath(final)
			if !ok {
				return winPath{}, false
			}
			fp.comps = append(fp.comps[:len(fp.comps):len(fp.comps)], missing...)
			return fp, true
		}
		if !errors.Is(err, fs.ErrNotExist) || cur.isVolumeRoot() {
			// A missing volume root means an unavailable volume.
			return winPath{}, false
		}
		last := len(cur.comps) - 1
		missing = append([]string{cur.comps[last]}, missing...)
		cur = winPath{vol: cur.vol, comps: cur.comps[:last]}
	}
}

// inPeerProfile reports whether p lies inside a user profile other than the
// current user's: strictly below a profile parent (the parent itself stays
// admitted, as /home is on POSIX) and outside the current home.
func (b *windowsBoundary) inPeerProfile(p winPath) bool {
	for _, r := range b.profiles {
		if r.strictlyContains(p) && (b.home == nil || !b.home.contains(p)) && !b.inPublic(p) {
			return true
		}
	}
	return false
}

func (b *windowsBoundary) inPublic(p winPath) bool {
	for _, r := range b.public {
		if r.contains(p) {
			return true
		}
	}
	return false
}

// allows reports whether a guard input (as given, and its absolute form) lies
// outside every denied Windows location. Both the path as named and its
// physical location count; when resolution fails, the named form alone
// decides (the boundary is best effort, as on POSIX).
func (b *windowsBoundary) allows(input, abs string) bool {
	if !winInputSpellingOK(input) {
		return false
	}
	p, ok := winCleanPath(abs)
	if !ok {
		return false
	}
	phys, resolved := b.physical(p)
	forms := []winPath{p}
	if resolved {
		forms = append(forms, phys)
	}
	in := func(r winRoot) bool {
		for _, q := range forms {
			if r.contains(q) {
				return true
			}
		}
		return false
	}
	// The temp and Public carve-outs are world-writable: a path in either
	// spelling is admitted only when it and the root both resolved and it
	// lies physically inside, so a planted link cannot escape through them.
	// An unresolved path inside a carve-out is denied. Temp comes first so
	// the SYSTEM account's temp directory under C:\Windows works.
	carvedIn := func(r winRoot) bool {
		return resolved && r.resolved && phys.within(r.phys)
	}
	if b.temp != nil && in(*b.temp) {
		return carvedIn(*b.temp)
	}
	for _, r := range b.system {
		if in(r) {
			return false
		}
	}
	for _, r := range b.public {
		if in(r) && !carvedIn(r) {
			return false
		}
	}
	// A path physically inside another user's profile is denied however it
	// is named. A path named inside another user's profile is denied unless
	// it is an alias (a short name, say) that resolves into the current home:
	// a peer profile relocated elsewhere is still a peer profile.
	if resolved && b.inPeerProfile(phys) {
		return false
	}
	if b.inPeerProfile(p) {
		return resolved && b.home != nil && b.home.contains(phys)
	}
	return true
}

// windowsPathInSafeBoundary is isPathInSafeBoundary on Windows.
func windowsPathInSafeBoundary(input, abs string) bool {
	return newWindowsBoundary(hostWindowsFacts()).allows(input, abs)
}
