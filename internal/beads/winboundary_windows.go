//go:build windows

package beads

import (
	"os"

	"golang.org/x/sys/windows"
)

// hostWindowsFacts gathers the live host inputs to the Windows boundary.
func hostWindowsFacts() windowsFacts {
	f := windowsFacts{Getenv: os.Getenv}
	if dir, err := windows.GetSystemWindowsDirectory(); err == nil {
		f.WindowsDir = dir
	}
	return f
}
