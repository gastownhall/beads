//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

func TestBackupRestoreMissingDir(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStoreWithPrefix(t, testDBPath, "dn")
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	err := runBackupRestore(ctx, s, "/nonexistent/path", false)
	if err == nil {
		t.Error("expected error for nonexistent backup dir")
	}
}
