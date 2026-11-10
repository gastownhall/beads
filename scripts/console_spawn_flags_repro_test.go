package scripts_test

// Repro for bd-l5o8ld (GH#6076): the long-lived Windows children bd starts
// (the shared dolt sql-server and bd db-proxy-child) must be created with
// CREATE_NO_WINDOW so they get a hidden console of their own, and must not
// use DETACHED_PROCESS, which leaves them console-less so every console
// program they spawn (dolt's uname calls) allocates a visible window.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLongLivedWindowsSpawnsHideConsole(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, rel := range []string{
		"internal/doltserver/doltserver_windows.go",
		"internal/storage/dbproxy/proxy/endpoint_windows.go",
	} {
		src := readPolicyFile(t, root, filepath.FromSlash(rel))
		start := strings.Index(src, "func procAttrDetached()")
		if start < 0 {
			t.Fatalf("%s: procAttrDetached not found", rel)
		}
		body := src[start:]
		if end := strings.Index(body, "\n}\n"); end >= 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "CREATE_NO_WINDOW") {
			t.Errorf("%s: procAttrDetached lacks CREATE_NO_WINDOW; a console-less bd makes Windows allocate a visible console for the child:\n%s", rel, body)
		}
		if strings.Contains(body, "DETACHED_PROCESS") {
			t.Errorf("%s: procAttrDetached uses DETACHED_PROCESS; the child's own console programs then each open a visible window:\n%s", rel, body)
		}
	}
}
