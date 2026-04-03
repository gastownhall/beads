//go:build windows

package configfile

// warnIfInsecurePermissions is a no-op on Windows.
// Windows ACLs don't map to unix permission bits.
func warnIfInsecurePermissions(path string) {}
