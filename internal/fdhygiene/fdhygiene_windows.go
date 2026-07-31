//go:build windows

package fdhygiene

// markInheritedCloexec is a no-op on Windows. Handles are not inheritable
// unless a spawn explicitly marks them so and passes them in the STARTUPINFO,
// which os/exec does only for Stdin/Stdout/Stderr and Cmd.ExtraFiles, so a
// caller's unrelated handles cannot reach the detached child in the first
// place.
func markInheritedCloexec() []int {
	return nil
}
