//go:build windows

package proxy

import (
	"testing"

	"golang.org/x/sys/windows"
)

// The db-proxy child is DETACHED_PROCESS and must stay so: CREATE_NO_WINDOW
// is ignored next to it, and execwin.Hide is not applied to it (GH#6076).
func TestProcAttrDetachedKeepsTheProxyChildDetached(t *testing.T) {
	attr := procAttrDetached()
	if attr == nil {
		t.Fatal("procAttrDetached returned nil")
	}
	want := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	if got := attr.CreationFlags; got != want {
		t.Fatalf("CreationFlags = %#x, want DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP (%#x)", got, want)
	}
}
