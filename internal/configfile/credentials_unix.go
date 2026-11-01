//go:build !windows

package configfile

import (
	"fmt"
	"os"
	"runtime"

	"github.com/steveyegge/beads/internal/remedy"
)

// warnIfInsecurePermissions checks if the credentials file is readable by
// group or others, and prints a warning to stderr if so. Mirrors ssh behavior.
func warnIfInsecurePermissions(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		arg, note := remedy.CommandArg(runtime.GOOS, path, "<path>")
		fmt.Fprintf(os.Stderr, "WARNING: credentials file %s has overly permissive permissions (%04o).\n", remedy.PrintedValue(path), perm)
		fmt.Fprintf(os.Stderr, "Consider running: chmod 600 %s%s\n", arg, note)
	}
}
