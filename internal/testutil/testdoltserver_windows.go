//go:build windows

package testutil

import (
	"fmt"
	"os"
	"testing"
)

// TestDoltServer represents a running test Dolt server instance.
// On Windows CI, Docker Desktop is not reliably available, so all
// container-based test helpers skip gracefully.
type TestDoltServer struct {
	Port int
}

// StartTestDoltServer is not supported on Windows CI.
func StartTestDoltServer(_ string) (*TestDoltServer, func()) {
	fmt.Fprintln(os.Stderr, "WARN: Docker not available on Windows CI, skipping test server")
	return nil, func() {}
}

// IsCrashed always returns false on Windows (no container to monitor).
func (s *TestDoltServer) IsCrashed() bool { return false }

// CrashError always returns nil on Windows (no container to monitor).
func (s *TestDoltServer) CrashError() error { return nil }

// StartIsolatedDoltContainer is not supported on Windows CI.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	t.Skip("Docker not available on Windows CI")
	return ""
}

// EnsureDoltContainerForTestMain is not supported on Windows CI.
func EnsureDoltContainerForTestMain() error {
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

// TerminateDoltContainer is a no-op on Windows.
func TerminateDoltContainer() {}

