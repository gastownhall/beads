package beads

import "testing"

// The tests here exercise the portable Windows SEC-003 policy with injected
// facts, so Linux CI verifies the exact production decision logic without a
// Windows host (GH#6101). Host-API behavior (facts gathering, physical path
// resolution) is covered by the windows-tagged tests in
// winboundary_windows_test.go and context_windows_test.go.

// TestWinCleanPath pins the lexical normalization every Windows boundary
// comparison builds on: separator folding, dot-segment cleaning that cannot
// escape the volume, extended \\?\ / \\.\ spellings, and rejection of
// spellings with no comparable plain form.
func TestWinCleanPath(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"drive path kept", `C:\Windows\System32`, `C:\Windows\System32`, true},
		{"separators folded", `c:/windows/system32`, `c:\windows\system32`, true},
		{"case preserved", `C:\WINDOWS`, `C:\WINDOWS`, true},
		{"dot segments collapsed", `C:\a\.\b\..`, `C:\a`, true},
		{"dotdot cannot escape volume", `C:\..\..\beads`, `C:\beads`, true},
		{"trailing separator dropped", `C:/x/y/`, `C:\x\y`, true},
		{"drive root", `C:\`, `C:\`, true},
		{"drive root via extended form", `\\?\C:\`, `C:\`, true},
		{"extended drive form", `\\?\C:\x\y`, `C:\x\y`, true},
		{"device-namespace drive form", `\\.\C:\x\y`, `C:\x\y`, true},
		{"extended UNC form", `\\?\UNC\s\share\y`, `\\s\share\y`, true},
		{"plain UNC", `\\server\share\proj\.beads`, `\\server\share\proj\.beads`, true},
		{"share root", `\\server\share`, `\\server\share`, true},
		{"UNC dotdot stops at share", `\\s\share\..\x`, `\\s\share\x`, true},

		{"empty", ``, ``, false},
		{"relative", `foo\bar`, ``, false},
		{"rooted relative", `\foo`, ``, false},
		{"drive-relative bare", `C:`, ``, false},
		{"drive-relative tail", `C:foo\bar`, ``, false},
		{"UNC missing share", `\\server`, ``, false},
		{"UNC empty", `\\`, ``, false},
		{"extended UNC empty", `\\?\UNC\`, ``, false},
		{"volume GUID", `\\?\Volume{1234-abcd}\x`, ``, false},
		{"globalroot device", `\\?\GLOBALROOT\Device\Harddisk0\x`, ``, false},
		{"physical drive device", `\\.\PhysicalDrive0`, ``, false},
		{"serial device", `\\.\COM1`, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := winCleanPath(tt.in)
			if ok != tt.valid {
				t.Fatalf("winCleanPath(%q) valid = %v, want %v", tt.in, ok, tt.valid)
			}
			if ok && got != tt.want {
				t.Errorf("winCleanPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNewWindowsBoundary pins fact validation: malformed values and volume
// roots are dropped, not trusted — a bad SystemRoot value must not deny an
// entire drive.
func TestNewWindowsBoundary(t *testing.T) {
	t.Run("system roots", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{
				`C:\Windows`,
				`c:\windows`, // duplicate spelling is harmless
				`C:\`,        // volume root: never a deny fact
				``,           // empty: unavailable source
				`relative`,   // malformed: dropped
				`D:\Win`,     // non-default drive install
			},
		})
		if len(b.systemRoots) != 3 {
			t.Fatalf("systemRoots = %v, want 3 entries", b.systemRoots)
		}
		for _, root := range []string{`C:\Windows`, `c:\windows`, `D:\Win`} {
			if !containsWinPath(b.systemRoots, root) {
				t.Errorf("systemRoots %v missing %q", b.systemRoots, root)
			}
		}
	})

	t.Run("profile parents validated", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{
				`C:\Users`,    // default
				`D:\Profiles`, // relocated profile tree
				`E:\`,         // volume root: dropped
				`nope`,        // malformed: dropped
			},
		})
		if len(b.profileParents) != 2 {
			t.Fatalf("profileParents = %v, want 2 entries", b.profileParents)
		}
		for _, pp := range []string{`C:\Users`, `D:\Profiles`} {
			if !containsWinPath(b.profileParents, pp) {
				t.Errorf("profileParents %v missing %q", b.profileParents, pp)
			}
		}
	})

	t.Run("home parent named Users adds a profile parent", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `D:\Users\kim`,
		})
		if b.home != `D:\Users\kim` {
			t.Errorf("home = %q, want D:\\Users\\kim", b.home)
		}
		if !containsWinPath(b.profileParents, `D:\Users`) {
			t.Errorf("profileParents %v missing derived D:\\Users", b.profileParents)
		}
	})

	t.Run("home at a profile parent is unknown", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users`,
		})
		if b.home != "" {
			t.Errorf("home = %q, want unknown (\"\")", b.home)
		}
	})

	t.Run("home above a profile parent is unknown", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\`,
		})
		if b.home != "" {
			t.Errorf("home = %q, want unknown (\"\")", b.home)
		}
	})

	t.Run("volume-root home is unknown", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{homeValue: `D:\`})
		if b.home != "" {
			t.Errorf("home = %q, want unknown (\"\")", b.home)
		}
	})

	t.Run("ordinary home below a parent is known", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users\alice`,
		})
		if b.home != `C:\Users\alice` {
			t.Errorf("home = %q, want C:\\Users\\alice", b.home)
		}
	})

	t.Run("temp carve-out validated", func(t *testing.T) {
		base := windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			resolve: func(p string) (string, bool) {
				if p == `C:\Linked` {
					return `C:\Windows`, true // a temp that physically IS a system root
				}
				return "", false
			},
		}
		kept := base
		kept.tempValue = `C:\Users\kim\AppData\Local\Temp`
		if got := newWindowsBoundary(kept).tempRoot; got != kept.tempValue {
			t.Errorf("tempRoot = %q, want kept %q", got, kept.tempValue)
		}
		dropped := base
		dropped.tempValue = `C:\Windows` // equal to a system root
		if got := newWindowsBoundary(dropped).tempRoot; got != "" {
			t.Errorf("tempRoot = %q, want discarded", got)
		}
		volumeRoot := base
		volumeRoot.tempValue = `C:\`
		if got := newWindowsBoundary(volumeRoot).tempRoot; got != "" {
			t.Errorf("tempRoot = %q, want discarded (volume root)", got)
		}
		physical := base
		physical.tempValue = `C:\Linked`
		if got := newWindowsBoundary(physical).tempRoot; got != "" {
			t.Errorf("tempRoot = %q, want discarded (physically a system root)", got)
		}
		descendant := windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			tempValue:        `C:\Windows\Temp`, // SYSTEM-account temp: a proper descendant
		}
		if got := newWindowsBoundary(descendant).tempRoot; got != `C:\Windows\Temp` {
			t.Errorf("tempRoot = %q, want kept C:\\Windows\\Temp", got)
		}
	})

	t.Run("public roots derived from profile parents", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`, `D:\Profiles`},
		})
		if len(b.publicRoots) != 2 {
			t.Fatalf("publicRoots = %v, want 2 entries", b.publicRoots)
		}
		for _, pub := range []string{`C:\Users\Public`, `D:\Profiles\Public`} {
			if !containsWinPath(b.publicRoots, pub) {
				t.Errorf("publicRoots %v missing %q", b.publicRoots, pub)
			}
		}
	})
}

func containsWinPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// TestWindowsPathInSafeBoundary evaluates the Windows policy end to end with
// injected facts: system roots deny roots and descendants under equivalent
// spellings, while similarly named siblings outside them stay admitted.
func TestWindowsPathInSafeBoundary(t *testing.T) {
	b := newWindowsBoundary(windowsBoundaryInput{
		systemRootValues: []string{
			`C:\Windows`,
			`C:\Program Files`,
			`C:\Program Files (x86)`,
			`D:\Win`, // relocated install on another drive
		},
	})
	tests := []struct {
		name  string
		input string
		abs   string
		safe  bool
	}{
		{"system root itself", `C:\Windows`, `C:\Windows`, false},
		{"system root descendant", `C:\Windows\System32\.beads`, `C:\Windows\System32\.beads`, false},
		{"case-folded spelling", `C:\WINDOWS\system32\drivers`, `C:\WINDOWS\system32\drivers`, false},
		{"slash spelling", `C:/Windows/System32`, `C:\Windows\System32`, false},
		{"extended abs spelling", `C:\Windows\System32`, `\\?\C:\Windows\System32`, false},
		{"program files", `C:\Program Files\beads`, `C:\Program Files\beads`, false},
		{"program files x86 case", `c:\program files (x86)\beads`, `c:\program files (x86)\beads`, false},
		{"other-drive install root", `D:\Win\x\y`, `D:\Win\x\y`, false},

		{"similarly named sibling", `C:\WindowsFoo\beads`, `C:\WindowsFoo\beads`, true},
		{"program files sibling", `C:\Program Files Extra`, `C:\Program Files Extra`, true},
		{"other-drive sibling", `D:\Winning\proj`, `D:\Winning\proj`, true},
		{"ordinary workspace", `D:\beads`, `D:\beads`, true},
		{"drive root workspace", `C:\`, `C:\`, true},
		{"UNC workspace", `\\server\share\proj\.beads`, `\\server\share\proj\.beads`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := b.inSafeBoundary(tt.input, tt.abs); got != tt.safe {
				t.Errorf("inSafeBoundary(%q, %q) = %v, want %v", tt.input, tt.abs, got, tt.safe)
			}
		})
	}

	// An existing alias into a denied system directory is rejected by where
	// it physically lands, not just by its spelling — including a missing
	// descendant reached through the alias. The resolver map stands in for
	// the CreateFile + GetFinalPathNameByHandle adapter (wine-verified in
	// TestWindowsBoundaryRegression).
	t.Run("physical alias into denied system root", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			resolve: func(p string) (string, bool) {
				switch p {
				case `C:\links\sys`:
					return `C:\Windows\System32`, true
				case `C:\links\sys\new\.beads`:
					return `C:\Windows\System32\new\.beads`, true
				}
				return "", false
			},
		})
		if b.inSafeBoundary(`C:\links\sys`, `C:\links\sys`) {
			t.Error("inSafeBoundary(alias into C:\\Windows) = true, want false")
		}
		if b.inSafeBoundary(`C:\links\sys\new\.beads`, `C:\links\sys\new\.beads`) {
			t.Error("inSafeBoundary(missing descendant through alias) = true, want false")
		}
	})

	// A physically aliased root (relocated install named through a link)
	// still denies what it contains.
	t.Run("aliased system root denies its contents", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{`D:\links\winroot`},
			resolve: func(p string) (string, bool) {
				if p == `D:\links\winroot` {
					return `D:\realwin`, true
				}
				return "", false
			},
		})
		if b.inSafeBoundary(`D:\realwin\System32`, `D:\realwin\System32`) {
			t.Error("inSafeBoundary(path inside aliased root) = true, want false")
		}
	})

	// Ordinary paths keep their lexical answer when resolution is
	// unavailable: the boundary is best-effort outside carve-outs.
	t.Run("ordinary path with unresolved physical form stays admitted", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			resolve:          func(string) (string, bool) { return "", false },
		})
		if !b.inSafeBoundary(`C:\work\proj\.beads`, `C:\work\proj\.beads`) {
			t.Error("inSafeBoundary(ordinary unresolved path) = false, want true")
		}
	})

	// Private profiles: ordinary paths in the current home are admitted;
	// peer profiles are rejected, including sibling names that merely share
	// the home's prefix, under every known profile parent (a relocated tree
	// included). An alias into the current home is the user's own directory;
	// an alias into a peer profile is denied on physical location.
	t.Run("private profiles", func(t *testing.T) {
		profiles := func(home string) windowsBoundaryInput {
			return windowsBoundaryInput{
				profileParentValues: []string{`C:\Users`, `D:\Profiles`},
				homeValue:           home,
				resolve: func(p string) (string, bool) {
					switch p {
					case `C:\Users\alice`:
						return `C:\Users\alice`, true
					case `C:\links\home`:
						return `C:\Users\alice`, true
					case `C:\links\evil`:
						return `C:\Users\bob`, true
					}
					return "", false
				},
			}
		}
		rows := []struct {
			name  string
			home  string
			input string
			safe  bool
		}{
			{"current home admitted", `C:\Users\alice`, `C:\Users\alice`, true},
			{"current home descendant admitted", `C:\Users\alice`, `C:\Users\alice\proj\.beads`, true},
			{"peer profile rejected", `C:\Users\alice`, `C:\Users\bob\.beads`, false},
			{"peer profile case-folded", `C:\Users\alice`, `c:\USERS\Bob\x`, false},
			{"relocated peer rejected", `C:\Users\alice`, `D:\Profiles\bob\proj`, false},
			{"profile parent root admitted", `C:\Users\alice`, `C:\Users`, true},
			{"relocated parent root admitted", `C:\Users\alice`, `D:\Profiles`, true},
			{"alias into current home admitted", `C:\Users\alice`, `C:\links\home`, true},
			{"alias into peer rejected", `C:\Users\alice`, `C:\links\evil`, false},
			{"unknown home rejects private profiles", `C:\`, `C:\Users\alice\proj`, false},
			{"unknown home keeps parent root", `C:\`, `C:\Users`, true},
		}
		for _, tt := range rows {
			t.Run(tt.name, func(t *testing.T) {
				b := newWindowsBoundary(profiles(tt.home))
				if got := b.inSafeBoundary(tt.input, tt.input); got != tt.safe {
					t.Errorf("inSafeBoundary(%q) = %v, want %v", tt.input, got, tt.safe)
				}
			})
		}
	})

	// A sibling that shares the home's name prefix is a different user:
	// comparison must happen on component boundaries.
	t.Run("sibling sharing home prefix rejected", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users\al`,
		})
		if b.inSafeBoundary(`C:\Users\alice\.beads`, `C:\Users\alice\.beads`) {
			t.Error("inSafeBoundary(C:\\Users\\alice with home C:\\Users\\al) = true, want false")
		}
		if !b.inSafeBoundary(`C:\Users\al\.beads`, `C:\Users\al\.beads`) {
			t.Error("inSafeBoundary(home itself) = false, want true")
		}
	})

	// The Public carve-out: C:\Users\<parent>\Public is the OS-designated
	// shared directory, not a peer profile. Admission needs physical
	// containment, so escapes through links and unresolved paths inside it
	// are rejected.
	t.Run("public carve-out", func(t *testing.T) {
		const pub = `C:\Users\Public`
		b := newWindowsBoundary(windowsBoundaryInput{
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users\alice`,
			resolve: func(p string) (string, bool) {
				switch p {
				case pub:
					return pub, true
				case pub + `\proj\.beads`:
					return pub + `\proj\.beads`, true
				case pub + `\esc`:
					return `C:\Windows\System32`, true
				}
				return "", false
			},
		})
		if !b.inSafeBoundary(pub+`\proj\.beads`, pub+`\proj\.beads`) {
			t.Error("inSafeBoundary(missing Public descendant) = false, want true")
		}
		if !b.inSafeBoundary(pub, pub) {
			t.Error("inSafeBoundary(Public root) = false, want true")
		}
		if b.inSafeBoundary(pub+`\esc`, pub+`\esc`) {
			t.Error("inSafeBoundary(escape from Public) = true, want false")
		}
		if b.inSafeBoundary(pub+`\unres`, pub+`\unres`) {
			t.Error("inSafeBoundary(unresolved path in Public) = true, want false")
		}
		if b.inSafeBoundary(`C:\Users\bob\.beads`, `C:\Users\bob\.beads`) {
			t.Error("inSafeBoundary(peer profile) = true, want false")
		}
	})

	// The temp carve-out: missing descendants whose longest existing
	// ancestor resolves inside the temp root are admitted; escapes and
	// unresolved paths inside it are rejected (fail closed, the tree is
	// world-writable). Temp runs before system denial so a SYSTEM-account
	// temp under C:\Windows\Temp keeps working without opening C:\Windows
	// itself.
	t.Run("temp carve-out", func(t *testing.T) {
		const temp = `C:\Users\alice\AppData\Local\Temp`
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues:    []string{`C:\Windows`},
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users\alice`,
			tempValue:           temp,
			resolve: func(p string) (string, bool) {
				switch p {
				case temp, temp + `\proj\.beads`:
					return p, true
				case temp + `\esc`:
					return `C:\Windows\System32`, true
				}
				return "", false
			},
		})
		if !b.inSafeBoundary(temp+`\proj\.beads`, temp+`\proj\.beads`) {
			t.Error("inSafeBoundary(missing temp descendant) = false, want true")
		}
		if !b.inSafeBoundary(temp, temp) {
			t.Error("inSafeBoundary(temp root) = false, want true")
		}
		if b.inSafeBoundary(temp+`\esc`, temp+`\esc`) {
			t.Error("inSafeBoundary(escape from temp) = true, want false")
		}
		if b.inSafeBoundary(temp+`\unres`, temp+`\unres`) {
			t.Error("inSafeBoundary(unresolved path in temp) = true, want false")
		}
	})

	t.Run("SYSTEM temp below a system root", func(t *testing.T) {
		const sysTemp = `C:\Windows\Temp`
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			tempValue:        sysTemp,
			resolve: func(p string) (string, bool) {
				if p == sysTemp || p == sysTemp+`\beads` {
					return p, true
				}
				return "", false
			},
		})
		if !b.inSafeBoundary(sysTemp+`\beads`, sysTemp+`\beads`) {
			t.Error("inSafeBoundary(SYSTEM temp workspace) = false, want true")
		}
		if !b.inSafeBoundary(sysTemp, sysTemp) {
			t.Error("inSafeBoundary(SYSTEM temp root) = false, want true")
		}
		if b.inSafeBoundary(sysTemp+`\unres`, sysTemp+`\unres`) {
			t.Error("inSafeBoundary(unresolved in SYSTEM temp) = true, want false")
		}
		if b.inSafeBoundary(`C:\Windows\System32`, `C:\Windows\System32`) {
			t.Error("inSafeBoundary(system dir) = true, want false")
		}
	})

	// A discarded temp value must not blunt the deny rules.
	t.Run("invalid temp does not swallow system denial", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues: []string{`C:\Windows`},
			tempValue:        `C:\Windows`,
		})
		if b.inSafeBoundary(`C:\Windows\beads`, `C:\Windows\beads`) {
			t.Error("inSafeBoundary(C:\\Windows\\beads with discarded temp) = true, want false")
		}
	})

	// Ordinary locations outside the denied roots and the carve-outs stay
	// admitted under the complete policy: drive-root workspaces, UNC
	// workspaces, ProgramData. A relocated install on a drive or a
	// workspace on one share never denies the whole drive or server.
	t.Run("ordinary locations admitted under full policy", func(t *testing.T) {
		b := newWindowsBoundary(windowsBoundaryInput{
			systemRootValues:    []string{`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `D:\Win`},
			profileParentValues: []string{`C:\Users`},
			homeValue:           `C:\Users\alice`,
			tempValue:           `C:\Users\alice\AppData\Local\Temp`,
		})
		rows := []struct {
			name  string
			input string
		}{
			{"drive root", `D:\`},
			{"drive-root workspace", `D:\beads`},
			{"same drive as relocated install", `D:\elsewhere\proj\.beads`},
			{"C drive workspace", `C:\beads`},
			{"ProgramData", `C:\ProgramData\beads`},
			{"UNC share root", `\\server\share`},
			{"UNC workspace", `\\server\share\proj\.beads`},
			{"second share on same server", `\\server\other\proj\.beads`},
			{"extended spelling of ordinary path", `\\?\C:\beads`},
			{"home project", `C:\Users\alice\projects\beads`},
		}
		for _, tt := range rows {
			t.Run(tt.name, func(t *testing.T) {
				if !b.inSafeBoundary(tt.input, tt.input) {
					t.Errorf("inSafeBoundary(%q) = false, want true", tt.input)
				}
			})
		}
	})
}
