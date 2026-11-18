//go:build windows

package proxy

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestProcAttrDetachedStaysDetachedProcess pins the db-proxy child's creation
// flags (gastownhall/beads#6076): the child keeps
// DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP and is deliberately NOT hidden —
// CREATE_NO_WINDOW is ignored next to DETACHED_PROCESS, and a detached child
// has no console to allocate a window from; the console children the proxy
// child starts (dolt config/init, the managed sql-server) are hidden by the
// execwin policy instead. This package has no native Windows CI lane; run
// `go test -tags gms_pure_go -run TestProcAttrDetached ./internal/storage/dbproxy/proxy/`
// on a Windows host for this one.
func TestProcAttrDetachedStaysDetachedProcess(t *testing.T) {
	attr := procAttrDetached()
	if got, want := attr.CreationFlags, uint32(windows.DETACHED_PROCESS|windows.CREATE_NEW_PROCESS_GROUP); got != want {
		t.Fatalf("CreationFlags = %#x, want %#x (DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP)", got, want)
	}
}
