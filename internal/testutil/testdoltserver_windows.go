//go:build windows

package testutil

import (
	"fmt"
	"os"
	"testing"
)

// StartIsolatedDoltContainer is not supported on Windows CI.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	t.Skip("Docker not available on Windows CI")
	return ""
}

// EnsureDoltContainerForTestMain is not supported on Windows CI.
func EnsureDoltContainerForTestMain() error {
	fmt.Fprintln(os.Stderr, "WARN: Docker not available on Windows CI, skipping test server")
	return fmt.Errorf("Docker not available on Windows CI")
}

// RequireDoltContainer is not supported on Windows CI.
func RequireDoltContainer(t *testing.T) {
	t.Helper()
	t.Skip("Docker not available on Windows CI")
}

// DoltContainerAddr returns empty string on Windows.
func DoltContainerAddr() string { return "" }

// DoltContainerPort returns empty string on Windows.
func DoltContainerPort() string { return "" }

// DoltContainerPortInt returns 0 on Windows.
func DoltContainerPortInt() int { return 0 }

// TerminateDoltContainer is a no-op on Windows.
func TerminateDoltContainer() {}

// DoltContainerCrashed always returns false on Windows (no container to monitor).
func DoltContainerCrashed() bool { return false }

// DoltContainerCrashError always returns nil on Windows (no container to monitor).
func DoltContainerCrashError() error { return nil }
