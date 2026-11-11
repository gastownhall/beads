//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDoltPushRecordsPushState_ServerStore is TestDoltPushRecordsPushState
// against a real server-mode store and file:// remotes (GH#6289): a
// successful bd dolt push to origin rewrites push-state.json with the HEAD
// it published, where server mode used to leave the file untouched, and a
// push to a backup remote does not.
func TestDoltPushRecordsPushState_ServerStore(t *testing.T) {
	// Cannot be parallel: mutates env vars, config, the store global and
	// doltPushCmd's flags.
	saveAndRestoreGlobals(t)
	resetCommandContext()
	resetCommandFlags(doltPushCmd)
	t.Cleanup(func() { resetCommandFlags(doltPushCmd) })

	st := newTestStoreIsolatedDB(t, filepath.Join(t.TempDir(), "dolt"), "ps")
	ctx := context.Background()
	originDir, backupDir := t.TempDir(), t.TempDir()
	if err := st.AddRemote(ctx, "origin", "file://"+originDir); err != nil {
		t.Fatalf("add origin: %v", err)
	}
	if err := st.AddRemote(ctx, "backup", "file://"+backupDir); err != nil {
		t.Fatalf("add backup: %v", err)
	}

	seed := staleFailedPushState()
	beadsDir := pushStateWorkspace(t, "", seed)
	store = st

	// A backup push succeeds on the server but is not origin's success.
	if _, stderr := captureStdio(t, func() {
		if err := doltPushCmd.RunE(doltPushCmd, []string{"backup"}); err != nil {
			t.Fatalf("bd dolt push backup: %v", err)
		}
	}); t.Failed() {
		t.Logf("stderr: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "manifest")); err != nil {
		t.Fatalf("backup push wrote no Dolt store at %s: %v", backupDir, err)
	}
	assertPushStateUnchanged(t, beadsDir, seed)

	before := time.Now().UTC()
	_, stderr := captureStdio(t, func() {
		if err := doltPushCmd.RunE(doltPushCmd, nil); err != nil {
			t.Errorf("bd dolt push: %v", err)
		}
	})
	after := time.Now().UTC()
	if t.Failed() {
		t.Fatalf("stderr: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(originDir, "manifest")); err != nil {
		t.Fatalf("origin push wrote no Dolt store at %s: %v", originDir, err)
	}
	head, err := st.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	assertPushStateRecordedSuccess(t, beadsDir, before, after, head)
}
