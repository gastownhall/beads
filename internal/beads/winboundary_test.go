package beads

import (
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
		if got := b.allows(path); got != want {
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
	} {
		got, ok := winCleanPath(in)
		if !ok || got.String() != want {
			t.Errorf("winCleanPath(%q) = %q, %v; want %q, true", in, got.String(), ok, want)
		}
	}
	for _, in := range []string{
		``, `beads`, `.\beads`, `C:`, `C:beads`, `\Windows`, `\\?\C:`, `\\.\C:`,
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
