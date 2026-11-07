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
}
