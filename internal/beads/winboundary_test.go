package beads

import (
	"fmt"
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
