//go:build windows

package execgrace

import "testing"

// testPlatformCancellation has no Windows cases: there is no SIGTERM to
// deliver, and the default kill plus the bounded wait is os/exec's own
// behavior.
func testPlatformCancellation(*testing.T) {}
