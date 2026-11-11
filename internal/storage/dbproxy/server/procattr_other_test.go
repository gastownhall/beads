//go:build !windows

package server

import "testing"

// TestManagedServerSysProcAttrNilOffWindows: the managed sql-server's process
// attributes off Windows stay what they were before the Windows console fix.
func TestManagedServerSysProcAttrNilOffWindows(t *testing.T) {
	if attr := managedServerSysProcAttr(); attr != nil {
		t.Fatalf("managedServerSysProcAttr() = %+v off windows, want nil", attr)
	}
}
