//go:build !windows

package configfile

import (
	"fmt"
	"os"
)

// warnIfInsecurePermissions checks if the credentials file is accessible by
// users outside the trusted group and prints a warning to stderr if so.
func warnIfInsecurePermissions(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	if perm&0007 != 0 {
		fmt.Fprintf(os.Stderr, "WARNING: credentials file %s has overly permissive permissions (%04o).\n", path, perm)
		fmt.Fprintf(os.Stderr, "Consider running: chmod o-rwx %s\n", path)
	}
}
