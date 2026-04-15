// Package telemetry is a no-op stub kept for API compatibility.
//
// The OpenTelemetry dependency tree has been removed. Init/Shutdown are
// retained as no-ops so callers need not be changed, and WrapStorage is an
// identity pass-through.
package telemetry

import "context"

// Enabled always returns false now that telemetry has been removed.
func Enabled() bool {
	return false
}

// Init is a no-op. It is retained so the CLI startup path keeps compiling.
func Init(_ context.Context, _, _ string) error {
	return nil
}

// Shutdown is a no-op.
func Shutdown(_ context.Context) {}
