package beads

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// The Windows SEC-003 policy (GH#6101) runs on Windows spellings independently
// of the host, so these tests inject host facts and run on every platform.

func winEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func winBoundaryCheck(t *testing.T, b *windowsBoundary, cases map[string]bool) {
	t.Helper()
	for path, want := range cases {
		if got := b.allows(path, path); got != want {
			t.Errorf("allows(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWinCleanPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Windows`:                  `C:\Windows`,
		`c:\windows\`:                 `C:\windows`,
		`C:/Windows/System32`:         `C:\Windows\System32`,
		`C:\\Windows\\\System32`:      `C:\Windows\System32`,
		`C:\Windows\.\System32\..\x`:  `C:\Windows\x`,
		`C:\..\..\Windows`:            `C:\Windows`,
		`C:\`:                         `C:\`,
		`\\?\C:\Windows\System32`:     `C:\Windows\System32`,
		`\\.\c:\Windows`:              `C:\Windows`,
		`//?/C:/Program Files/beads`:  `C:\Program Files\beads`,
		`C:\Program Files (x86)\b\..`: `C:\Program Files (x86)`,
		`\\server\share`:              `\\server\share\`,
		`\\server\share\proj\..\..\x`: `\\server\share\x`,
		`//server/share/proj`:         `\\server\share\proj`,
		`\\?\UNC\server\share\proj`:   `\\server\share\proj`,
		`\\.\unc\server\share\proj`:   `\\server\share\proj`,
	} {
		got, ok := winCleanPath(in)
		if !ok || got.String() != want {
			t.Errorf("winCleanPath(%q) = %q, %v; want %q, true", in, got.String(), ok, want)
		}
	}
	for _, in := range []string{
		``, `beads`, `.\beads`, `C:`, `C:beads`, `\Windows`, `\\?\C:`, `\\.\C:`,
		`\\server`, `\\server\`, `\\\share`, `\\?\UNC\server`, `\\..\share\x`,
	} {
		if got, ok := winCleanPath(in); ok {
			t.Errorf("winCleanPath(%q) = %q, true; want rejection", in, got.String())
		}
	}
}

func TestNewWindowsBoundary(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		WindowsDir: `E:\WINNT`,
		Getenv: winEnv(map[string]string{
			"SystemRoot":        `D:\Windows`,
			"windir":            `D:\Windows`,
			"ProgramFiles":      `D:\Apps`,
			"ProgramFiles(x86)": `relative\path`, // malformed: discarded
			"ProgramW6432":      `F:\`,           // volume root: discarded
			"ProgramFiles(Arm)": "",
		}),
	})
	var got []string
	for _, r := range b.system {
		got = append(got, r.String())
	}
	want := []string{
		`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`,
		`E:\WINNT`, `D:\Windows`, `D:\Windows`, `D:\Apps`,
	}
	if len(got) != len(want) {
		t.Fatalf("system roots = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("system roots = %q, want %q", got, want)
		}
	}
}

// AC-1: fixed and host-reported system roots deny themselves and their
// descendants in every supported spelling, but not similarly named siblings.
func TestWindowsPathInSafeBoundary_SystemRoots(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		WindowsDir: `E:\WINNT`,
		Getenv: winEnv(map[string]string{
			"SystemRoot":   `E:\WINNT`,
			"ProgramFiles": `D:\Programs`,
		}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		// Fixed roots, in any case and separator spelling.
		`C:\Windows`:                         false,
		`C:\Windows\System32\.beads`:         false,
		`c:\WINDOWS\beads`:                   false,
		`C:/Windows/beads`:                   false,
		`C:\Program Files\beads`:             false,
		`C:\Program Files (x86)\beads`:       false,
		`\\?\C:\Windows\System32\.beads`:     false,
		`\\.\C:\Program Files\beads`:         false,
		`C:\Users\me\..\..\Windows\.beads`:   false,
		`C:\Windows\.\System32\..\..\beads2`: true, // cleans to C:\beads2
		// Host-reported roots on a non-default drive.
		`E:\WINNT\System32\.beads`: false,
		`e:\winnt`:                 false,
		`D:\Programs\Tool\.beads`:  false,
		// Siblings sharing a prefix are not inside the root.
		`C:\Windows2\.beads`:          true,
		`C:\WindowsApps-mine\.beads`:  true,
		`C:\Program Files Mine\beads`: true,
		`E:\WINNT-old\.beads`:         true,
		`D:\Programs2\.beads`:         true,
	})
}

// AC-8: spellings Windows does not name unambiguously are rejected, whatever
// they would resolve to.
func TestWindowsPathInSafeBoundary_Spelling(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{})
	winBoundaryCheck(t, b, map[string]bool{
		// Device, Volume GUID and GLOBALROOT forms.
		`\\.\C:`:             false,
		`\\.\pipe\beads`:     false,
		`\\.\PhysicalDrive0`: false,
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\beads`: false,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\beads`:            false,
		// Alternate data streams: a directory's $INDEX_ALLOCATION stream opens
		// the directory itself.
		`C:\Windows::$INDEX_ALLOCATION\System32`: false,
		`D:\work\.beads:stream`:                  false,
		`D:\work::$DATA`:                         false,
		// Invalid file-name and control characters.
		`D:\work\be<ads`:      false,
		`D:\work\be>ads`:      false,
		`D:\work\be"ads`:      false,
		`D:\work\be|ads`:      false,
		`D:\work\be*ads`:      false,
		`D:\work\be?ads`:      false,
		"D:\\work\\be\x01ads": false,
		"D:\\work\\be\xffads": false,
		// Trailing dots and spaces, which Win32 strips when opening.
		`C:\Windows.\System32`: false,
		`C:\Windows \System32`: false,
		`D:\work.\.beads`:      false,
		`D:\work\.beads `:      false,
		// Dot segments and ordinary names stay valid.
		`D:\work\.\.beads`:          true,
		`D:\work\sub\..\.beads`:     true,
		`D:\work\my.project\.beads`: true,
		`D:\work\with space\.beads`: true,
		"D:\\work\\café\\.beads":    true,
	})

	// The input as the caller gave it is checked too: filepath.Abs on Windows
	// strips the trailing dot that the absolute form no longer shows.
	for _, tc := range []struct{ input, abs string }{
		{`D:\work.\.beads`, `D:\work\.beads`},
		{`work \.beads`, `D:\cwd\work\.beads`},
		{`D:work.\x`, `D:\cwd\work\x`},
		{`.beads:evil`, `D:\cwd\.beads:evil`},
	} {
		if b.allows(tc.input, tc.abs) {
			t.Errorf("allows(%q, %q) = true, want false (ambiguous input spelling)", tc.input, tc.abs)
		}
	}
	if !b.allows(`work\.beads`, `D:\cwd\work\.beads`) {
		t.Errorf("allows(relative work\\.beads) = false, want true")
	}
}

// AC-5: ordinary locations outside the denied roots stay admitted. Neither a
// whole drive nor a whole share is denied, and ProgramData is not a system
// root.
func TestWindowsPathInSafeBoundary_OrdinaryLocations(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		WindowsDir: `C:\Windows`,
		Getenv: winEnv(map[string]string{
			"SystemRoot":   `C:\Windows`,
			"ProgramFiles": `C:\Program Files`,
		}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		`C:\`:                                 true,
		`C:\beads`:                            true,
		`C:\beads\.beads`:                     true,
		`D:\`:                                 true,
		`D:\beads`:                            true,
		`D:\src\project\.beads`:               true,
		`C:\ProgramData\beads\.beads`:         true,
		`\\fileserver\team`:                   true,
		`\\fileserver\team\proj\.beads`:       true,
		`//fileserver/team/proj/.beads`:       true,
		`\\?\UNC\fileserver\team\proj\.beads`: true,
		`\\?\D:\src\project\.beads`:           true,
		// The system roots are still denied alongside.
		`C:\Windows\.beads`: false,
	})
}

// AC-5 under the full policy: with system, profile and temp facts all in
// force, ordinary locations stay admitted. A relocated install on a drive
// denies neither the drive nor its other directories, and a workspace on one
// share leaves the server's other shares alone.
func TestWindowsPathInSafeBoundary_OrdinaryLocationsFullPolicy(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		WindowsDir:  `D:\Win`,
		ProfilesDir: `C:\Users`,
		Home:        `C:\Users\alice`,
		TempDir:     `C:\Users\alice\AppData\Local\Temp`,
	})
	winBoundaryCheck(t, b, map[string]bool{
		`D:\`:                           true,
		`D:\beads`:                      true,
		`D:\elsewhere\proj\.beads`:      true,
		`C:\beads`:                      true,
		`C:\ProgramData\beads`:          true,
		`\\server\share`:                true,
		`\\server\share\proj\.beads`:    true,
		`\\server\other\proj\.beads`:    true,
		`\\?\C:\beads`:                  true,
		`C:\Users\alice\projects\beads`: true,
		// The denials hold alongside.
		`D:\Win\.beads`:          false,
		`C:\Windows\.beads`:      false,
		`C:\Users\bob\.beads`:    false,
		`C:\Program Files\beads`: false,
	})
}

func TestNewWindowsBoundary_Profiles(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		ProfilesDir: `E:\Profiles`,
		Home:        `F:\Users\alice`,
		Getenv:      winEnv(map[string]string{"SystemDrive": `D:`}),
	})
	var got []string
	for _, r := range b.profiles {
		got = append(got, r.String())
	}
	want := `[C:\Users E:\Profiles D:\Users F:\Users]`
	if fmt.Sprint(got) != want {
		t.Errorf("profile parents = %v, want %s", got, want)
	}
	if b.home == nil || b.home.String() != `F:\Users\alice` {
		t.Errorf("home = %v, want F:\\Users\\alice", b.home)
	}

	// Malformed SystemDrive values add nothing; a home whose parent is not
	// named Users does not make that parent a profile parent.
	for _, drive := range []string{`D:\`, `DD`, `1:`, `\\server\share`} {
		b := newWindowsBoundary(windowsFacts{
			Home:   `D:\home\alice`,
			Getenv: winEnv(map[string]string{"SystemDrive": drive}),
		})
		if len(b.profiles) != 1 || b.profiles[0].String() != `C:\Users` {
			t.Errorf("SystemDrive %q: profile parents = %v, want only C:\\Users", drive, b.profiles)
		}
	}

	// A home that is invalid, a volume root, or at or above a profile parent
	// is unknown.
	for _, home := range []string{"", `relative\alice`, `C:\`, `C:\Users`, `C:\Users\.`, `E:\`, `E:\Profiles`} {
		b := newWindowsBoundary(windowsFacts{ProfilesDir: `E:\Profiles\All`, Home: home})
		if b.home != nil {
			t.Errorf("home %q: got known home %v, want unknown", home, b.home)
		}
	}
}

// AC-3: the current user's profile is admitted; peer profiles, including
// siblings whose names extend the home's name, are rejected.
func TestWindowsPathInSafeBoundary_Profiles(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		ProfilesDir: `C:\Users`,
		Home:        `C:\Users\alice`,
		Getenv:      winEnv(map[string]string{"SystemDrive": `C:`}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		`C:\Users`:                    true, // the parent itself, like /home
		`C:\Users\alice`:              true,
		`C:\Users\alice\src\.beads`:   true,
		`c:\users\ALICE\src\.beads`:   true,
		`\\?\C:\Users\alice\.beads`:   true,
		`C:\Users\bob`:                false,
		`C:\Users\bob\.beads`:         false,
		`c:/users/BOB/src/.beads`:     false,
		`C:\Users\alice2\.beads`:      false,
		`C:\Users\alice.old\.beads`:   false,
		`C:\Users\alice-work\.beads`:  false,
		`C:\Users\alic\.beads`:        false,
		`C:\Users\alice\..\bob\x`:     false,
		`D:\Users\bob\.beads`:         true, // not a known profile parent
		`D:\src\alice-and-bob\.beads`: true,
	})

	// Relocated profiles: the OS-reported parent and the home's Users parent
	// both count, and the C:\Users floor stays.
	b = newWindowsBoundary(windowsFacts{
		ProfilesDir: `E:\Profiles`,
		Home:        `E:\Profiles\alice`,
	})
	winBoundaryCheck(t, b, map[string]bool{
		`E:\Profiles\alice\.beads`: true,
		`E:\Profiles\bob\.beads`:   false,
		`C:\Users\bob\.beads`:      false,
		`C:\Users\alice\.beads`:    false, // not this user's home here
	})
	b = newWindowsBoundary(windowsFacts{Home: `F:\Users\alice`})
	winBoundaryCheck(t, b, map[string]bool{
		`F:\Users\alice\.beads`: true,
		`F:\Users\bob\.beads`:   false,
	})

	// An unknown home admits no private profile.
	b = newWindowsBoundary(windowsFacts{ProfilesDir: `C:\Users`})
	winBoundaryCheck(t, b, map[string]bool{
		`C:\Users`:              true,
		`C:\Users\alice\.beads`: false,
		`D:\beads`:              true,
	})
}

// winFakeEntry is one existing entry of a fake Windows filesystem: its final
// physical path ("" for itself), whether it is a file, and an error to report
// instead (a dangling link, access denied).
type winFakeEntry struct {
	final string
	file  bool
	err   error
}

// winFakeFS is an injected winResolver over a fixed set of entries, keyed
// case-insensitively. Entries not listed do not exist.
func winFakeFS(entries map[string]winFakeEntry) winResolver {
	byKey := map[string]winFakeEntry{}
	for k, e := range entries {
		byKey[strings.ToLower(k)] = e
	}
	return func(path string) (string, bool, error) {
		e, ok := byKey[strings.ToLower(path)]
		if !ok {
			return "", false, fs.ErrNotExist
		}
		if e.err != nil {
			return "", false, e.err
		}
		if e.final == "" {
			return `\\?\` + path, !e.file, nil
		}
		return e.final, !e.file, nil
	}
}

var (
	errWinFakeDangling = errors.New("fake dangling link")
	errWinFakeAccess   = errors.New("fake access denied")
)

func TestWindowsBoundaryPhysical(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{Resolve: winFakeFS(map[string]winFakeEntry{
		`C:\`:                {},
		`D:\`:                {},
		`D:\work`:            {},
		`D:\work\link`:       {final: `\\?\C:\Windows`},
		`D:\work\file.txt`:   {file: true},
		`D:\work\dangling`:   {err: errWinFakeDangling},
		`D:\work\locked`:     {err: errWinFakeAccess},
		`D:\work\unprovable`: {err: winOpenError(fs.ErrNotExist, errWinFakeAccess)},
		`C:\PROGRA~1`:        {final: `\\?\C:\Program Files`},
		`X:\`:                {final: `\\?\C:\Users\bob`},
		`\\srv\share`:        {final: `\\?\UNC\srv\share`},
		`\\srv\share\linked`: {final: `\\?\UNC\other\data\linked`},
	})})
	for in, want := range map[string]string{
		`D:\work`:                          `D:\work`,
		`d:\WORK\new\.beads`:               `D:\WORK\new\.beads`, // missing tail kept as named
		`D:\work\link`:                     `C:\Windows`,
		`D:\work\link\System32\new\.beads`: `C:\Windows\System32\new\.beads`,
		`C:\PROGRA~1\tool`:                 `C:\Program Files\tool`,
		`X:\proj\.beads`:                   `C:\Users\bob\proj\.beads`,
		`\\srv\share\linked\.beads`:        `\\other\data\linked\.beads`,
		`D:\work\file.txt`:                 `D:\work\file.txt`,
	} {
		p, _ := winCleanPath(in)
		got, ok := b.physical(p)
		if !ok || got.String() != want {
			t.Errorf("physical(%q) = %q, %v; want %q, true", in, got.String(), ok, want)
		}
	}
	// Failures are never walked past: a dangling link, an access error, a
	// file with a tail below it, and a volume that does not exist.
	for _, in := range []string{
		`D:\work\dangling`, `D:\work\dangling\new\.beads`,
		`D:\work\locked\.beads`,
		`D:\work\unprovable\.beads`,
		`D:\work\file.txt\.beads`,
		`Q:\work\.beads`, `\\nosuch\share\.beads`,
	} {
		p, _ := winCleanPath(in)
		if got, ok := b.physical(p); ok {
			t.Errorf("physical(%q) = %q, true; want unresolved", in, got.String())
		}
	}
	// Without a resolver nothing resolves.
	p, _ := winCleanPath(`D:\work`)
	if _, ok := newWindowsBoundary(windowsFacts{}).physical(p); ok {
		t.Errorf("physical without a resolver resolved")
	}
}

// A not-found open proves absence only when the no-follow Lstat agrees: an
// entry Lstat sees is a dangling link, and an Lstat failing otherwise leaves
// the entry unresolved rather than absent.
func TestWinOpenError(t *testing.T) {
	notFound := fs.ErrNotExist
	if err := winOpenError(notFound, fs.ErrNotExist); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("both not found: %v, want fs.ErrNotExist", err)
	}
	if err := winOpenError(notFound, nil); !errors.Is(err, errWinDanglingLink) {
		t.Errorf("Lstat found the entry: %v, want errWinDanglingLink", err)
	}
	err := winOpenError(notFound, errWinFakeAccess)
	if errors.Is(err, fs.ErrNotExist) || !errors.Is(err, errWinFakeAccess) {
		t.Errorf("Lstat access error: %v, want the access error and not fs.ErrNotExist", err)
	}
	if err := winOpenError(errWinFakeAccess, nil); !errors.Is(err, errWinFakeAccess) {
		t.Errorf("open access error: %v, want it unchanged", err)
	}
}

// AC-2: an existing alias into a denied system directory or another user's
// profile (junction, symlink, short name, subst drive) is denied by its
// physical location, also for a not-yet-created descendant.
func TestWindowsPathInSafeBoundary_PhysicalAlias(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		ProfilesDir: `C:\Users`,
		Home:        `C:\Users\alice`,
		Getenv:      winEnv(map[string]string{"ProgramFiles": `C:\PF`}),
		Resolve: winFakeFS(map[string]winFakeEntry{
			`C:\`:                       {},
			`D:\`:                       {},
			`C:\Windows`:                {},
			`C:\Users`:                  {},
			`C:\Users\alice`:            {},
			`C:\Users\bob`:              {},
			`D:\work`:                   {},
			`D:\work\winlink`:           {final: `\\?\C:\Windows`},
			`D:\work\bobjunction`:       {final: `\\?\C:\Users\bob`},
			`C:\PROGRA~1`:               {final: `\\?\C:\Program Files`},
			`C:\PF`:                     {final: `\\?\E:\RealPF`}, // relocated system root
			`C:\Users\ALICE~1`:          {final: `\\?\C:\Users\alice`},
			`C:\Users\BOB~1`:            {final: `\\?\C:\Users\bob`},
			`C:\Users\carol`:            {final: `\\?\E:\carol-data`}, // relocated peer profile
			`C:\Users\alice\to-bob`:     {final: `\\?\C:\Users\bob\proj`},
			`C:\Users\alice\deps`:       {final: `\\?\D:\deps`},
			`X:\`:                       {final: `\\?\C:\Users\bob`},
			`Y:\`:                       {final: `\\?\C:\Windows\System32`},
			`\\srv\share`:               {},
			`\\srv\share\sys`:           {final: `\\?\C:\Windows`},
			`D:\work\dangling`:          {err: errWinFakeDangling},
			`C:\Windows\dangling`:       {err: errWinFakeDangling},
			`C:\Users\alice\locked`:     {err: errWinFakeAccess},
			`C:\Users\dave`:             {err: errWinFakeAccess},
			`D:\work\aliceAlias`:        {final: `\\?\C:\Users\alice`},
			`D:\work\aliceAlias\nested`: {final: `\\?\C:\Users\alice\nested`},
		}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		// Aliases into system roots.
		`D:\work\winlink`:                     false,
		`D:\work\winlink\System32\new\.beads`: false,
		`C:\PROGRA~1\beads`:                   false,
		`C:\PROGRA~1\new\.beads`:              false,
		`Y:\.beads`:                           false,
		`\\srv\share\sys\new\.beads`:          false,
		`E:\RealPF\tool\.beads`:               false, // physical form of a system root
		`C:\PF\tool`:                          false,
		`C:\Windows\dangling\.beads`:          false, // unresolved: named form decides
		// Aliases into another user's profile.
		`D:\work\bobjunction`:            false,
		`D:\work\bobjunction\new\.beads`: false,
		`X:\.beads`:                      false,
		`C:\Users\BOB~1\.beads`:          false,
		`C:\Users\alice\to-bob\.beads`:   false,
		`C:\Users\carol\.beads`:          false, // a relocated peer is still a peer
		`C:\Users\dave\.beads`:           false, // unresolved peer
		// Aliases of the current home resolve into it and stay admitted.
		`C:\Users\ALICE~1\src\.beads`:   true,
		`D:\work\aliceAlias\src\.beads`: true,
		`C:\Users\alice\deps\.beads`:    true,
		`C:\Users\alice\locked\.beads`:  true, // unresolved: named form decides
		// Ordinary paths: physical or (unresolved) named form.
		`D:\work\.beads`:          true,
		`D:\work\dangling\.beads`: true,
		`Q:\offline\.beads`:       true,
	})
}

func TestNewWindowsBoundary_Temp(t *testing.T) {
	resolve := winFakeFS(map[string]winFakeEntry{
		`C:\`:       {},
		`T:\tmp`:    {final: `\\?\D:\`},
		`T:\users`:  {final: `\\?\C:\Users`},
		`T:\pf`:     {final: `\\?\C:\Program Files\x\..`},
		`T:\winsub`: {final: `\\?\C:\Windows\Temp`},
	})
	// A temp directory that is invalid, a volume root, or at or above a
	// system root or profile parent (in either form) is no carve-out.
	for _, temp := range []string{
		"", `relative\tmp`, `C:\`, `C:\Users`, `c:\users\`, `C:\Windows`,
		`C:\Program Files`, `E:\Profiles`, `E:\`, `T:\tmp`, `T:\users`, `T:\pf`,
	} {
		b := newWindowsBoundary(windowsFacts{ProfilesDir: `E:\Profiles\All`, TempDir: temp, Resolve: resolve})
		if b.temp != nil {
			t.Errorf("temp %q: got carve-out %v, want none", temp, b.temp)
		}
	}
	// Proper descendants stay carve-outs, including the SYSTEM account's.
	for _, temp := range []string{`C:\Windows\Temp`, `C:\Users\alice\AppData\Local\Temp`, `D:\Temp`, `T:\winsub`} {
		b := newWindowsBoundary(windowsFacts{TempDir: temp, Resolve: resolve})
		if b.temp == nil {
			t.Errorf("temp %q: got no carve-out", temp)
		}
	}

	// Public is <profile parent>\Public for each parent, never $PUBLIC.
	b := newWindowsBoundary(windowsFacts{
		ProfilesDir: `E:\Profiles`,
		Getenv:      winEnv(map[string]string{"PUBLIC": `C:\Users\bob`}),
	})
	var got []string
	for _, r := range b.public {
		got = append(got, r.String())
	}
	if want := `[C:\Users\Public E:\Profiles\Public]`; fmt.Sprint(got) != want {
		t.Errorf("public roots = %v, want %s", got, want)
	}
}

// AC-4: inside the temp and Public carve-outs, a path whose longest existing
// ancestor resolves inside the carve-out is admitted, missing descendants
// included; escapes and unresolved paths are rejected.
func TestWindowsPathInSafeBoundary_CarveOuts(t *testing.T) {
	b := newWindowsBoundary(windowsFacts{
		ProfilesDir: `C:\Users`,
		Home:        `C:\Users\alice`,
		TempDir:     `D:\Temp`,
		Getenv:      winEnv(map[string]string{"PUBLIC": `C:\Users\bob`}),
		Resolve: winFakeFS(map[string]winFakeEntry{
			`C:\`:                     {},
			`D:\`:                     {},
			`C:\Users`:                {},
			`C:\Users\alice`:          {},
			`C:\Users\bob`:            {},
			`C:\Users\Public`:         {},
			`C:\Users\Public\proj`:    {},
			`C:\Users\Public\to-bob`:  {final: `\\?\C:\Users\bob`},
			`C:\Users\Public\to-win`:  {final: `\\?\C:\Windows`},
			`C:\Users\Public\to-temp`: {final: `\\?\D:\Temp`},
			`C:\Users\Public\locked`:  {err: errWinFakeAccess},
			`D:\Temp`:                 {},
			`D:\Temp\proj`:            {},
			`D:\Temp\to-bob`:          {final: `\\?\C:\Users\bob\x`},
			`D:\Temp\to-home`:         {final: `\\?\C:\Users\alice\src`},
			`D:\Temp\to-public`:       {final: `\\?\C:\Users\Public`},
			`D:\Temp\dangling`:        {err: errWinFakeDangling},
			`D:\work`:                 {},
			`D:\work\to-temp`:         {final: `\\?\D:\Temp\proj`},
			`D:\work\to-public`:       {final: `\\?\C:\Users\Public\proj`},
			`C:\Users\alice\to-pub`:   {final: `\\?\C:\Users\Public\proj`},
		}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		// Temp.
		`D:\Temp`:                    true,
		`D:\Temp\proj\.beads`:        true,
		`d:\temp\new\deep\.beads`:    true, // missing descendants
		`D:\work\to-temp\new\.beads`: true, // physically inside
		`D:\Temp\to-bob\.beads`:      false,
		`D:\Temp\to-home\.beads`:     false, // any escape, even to the home
		`D:\Temp\to-public\.beads`:   false,
		`D:\Temp\dangling\.beads`:    false, // unresolved
		`D:\TempX\.beads`:            true,  // a sibling, not the carve-out
		// Public.
		`C:\Users\Public`:                true,
		`C:\Users\Public\proj\.beads`:    true,
		`c:\users\public\new\.beads`:     true,
		`D:\work\to-public\.beads`:       true,
		`C:\Users\alice\to-pub\.beads`:   true,
		`C:\Users\Public\to-bob\.beads`:  false,
		`C:\Users\Public\to-win\.beads`:  false,
		`C:\Users\Public\to-temp\.beads`: true, // physically in temp
		`C:\Users\Public\locked\.beads`:  false,
		`C:\Users\Publicity\.beads`:      false, // a peer profile
		`C:\Users\bob\.beads`:            false, // $PUBLIC is not trusted
	})

	// Without physical resolution nothing inside a carve-out is admitted.
	b = newWindowsBoundary(windowsFacts{ProfilesDir: `C:\Users`, Home: `C:\Users\alice`, TempDir: `D:\Temp`})
	winBoundaryCheck(t, b, map[string]bool{
		`D:\Temp\proj\.beads`:         false,
		`C:\Users\Public\proj\.beads`: false,
		`D:\work\.beads`:              true,
	})

	// The SYSTEM account's temp directory sits under C:\Windows: the temp
	// carve-out precedes the system denial, and only for physical paths.
	b = newWindowsBoundary(windowsFacts{
		TempDir: `C:\Windows\Temp`,
		Resolve: winFakeFS(map[string]winFakeEntry{
			`C:\`:                {},
			`C:\Windows`:         {},
			`C:\Windows\Temp`:    {},
			`C:\Windows\Temp\up`: {final: `\\?\C:\Windows\System32`},
		}),
	})
	winBoundaryCheck(t, b, map[string]bool{
		`C:\Windows\Temp\proj\.beads`: true,
		`C:\Windows\Temp\up\.beads`:   false,
		`C:\Windows\TempX\.beads`:     false,
		`C:\Windows\System32`:         false,
	})
}
