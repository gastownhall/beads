package main

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
)

// TestPersistentPostRunProxiedHonorsAutoExport reproduces GH#8490: on a
// proxied-server workspace with export.auto=true, a write command's post-run
// must either run the auto-export hook or tell the user auto-export is not
// supported in this mode. Before the fix the proxied arm of PersistentPostRunE
// only pruned the journal, ran auto-backup and closed the provider, so
// issues.jsonl silently stopped tracking the store.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunProxiedHonorsAutoExport(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	initConfigForTest(t)
	config.Set("export.auto", true)

	oldStore, oldProvider := store, uowProvider
	oldReadonly, oldProxied := readonlyMode, proxiedServerMode
	oldRootCtx, oldRootCancel := rootCtx, rootCancel
	oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
	oldBackup, oldExport := runPostRunAutoBackup, runPostRunAutoExport
	t.Cleanup(func() {
		store, uowProvider = oldStore, oldProvider
		readonlyMode, proxiedServerMode = oldReadonly, oldProxied
		rootCtx, rootCancel = oldRootCtx, oldRootCancel
		commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
		runPostRunAutoBackup, runPostRunAutoExport = oldBackup, oldExport
	})

	exportCalls := 0
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = func(context.Context, bool) error { exportCalls++; return nil }
	store, uowProvider = nil, nil
	readonlyMode = false
	proxiedServerMode = true
	rootCtx, rootCancel = context.Background(), nil
	commandSpan, profileFile, traceFile = nil, nil, nil

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = rootCmd.PersistentPostRunE(&cobra.Command{Use: "update"}, nil)
	})
	if runErr != nil {
		t.Fatalf("PersistentPostRunE: %v", runErr)
	}
	if exportCalls == 0 && !strings.Contains(strings.ToLower(stderr), "auto-export") {
		t.Fatalf("proxied write with export.auto=true: auto-export hook calls = 0 and no auto-export notice on stderr (got %q); export.auto is a silent no-op on the proxied path (GH#8490)", stderr)
	}
}
