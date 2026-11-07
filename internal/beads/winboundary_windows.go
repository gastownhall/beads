//go:build windows

package beads

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// windowsIsPathInSafeBoundary is the Windows arm of isPathInSafeBoundary
// (SEC-003). input is the caller-supplied path and abs its filepath.Abs form;
// both keep their original spelling so the policy can reject names the OS
// would silently normalize away.
func windowsIsPathInSafeBoundary(input, abs string) bool {
	return newWindowsBoundary(collectWindowsBoundaryInput()).inSafeBoundary(input, abs)
}

// fixedWindowsSystemRoots are denied even when the environment disagrees: a
// machine with a relocated Windows directory still denies the default
// locations, the way the POSIX prefixes are unconditional literals.
var fixedWindowsSystemRoots = []string{
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
}

// windowsSystemRootEnvNames lists environment values that may report system
// roots; some installations relocate these directories to other drives.
// Values are only ever added to the deny set; malformed values are discarded
// by newWindowsBoundary.
var windowsSystemRootEnvNames = []string{
	"SystemRoot", "windir",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramFiles(Arm)",
}

// collectWindowsBoundaryInput gathers the per-call host facts. It performs no
// caching: facts are read once per boundary check, so an environment change
// mid-process cannot observe a stale policy.
func collectWindowsBoundaryInput() windowsBoundaryInput {
	values := append([]string(nil), fixedWindowsSystemRoots...)
	// The OS-reported Windows directory covers profiles whose SystemRoot is
	// not C:\Windows; errors leave the fixed and env-derived roots in force.
	if sysDir, err := windows.GetSystemWindowsDirectory(); err == nil && sysDir != "" {
		values = append(values, sysDir)
	}
	for _, name := range windowsSystemRootEnvNames {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			values = append(values, v)
		}
	}
	return windowsBoundaryInput{systemRootValues: values}
}
