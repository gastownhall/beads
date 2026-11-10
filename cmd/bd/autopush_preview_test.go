package main

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

// The preview half of the publish-safeguard fix (GH#7876 AC-7): a preview
// command (--dry-run/--inspect) must not auto-publish after it runs. The
// post-run auto-push would otherwise write push-state.json and push real
// commits for a command that changed nothing — the same suppression the
// maintenance net already applies to its other steps.
func TestPreviewCommandSkipsPostRunAutoPush(t *testing.T) {
	originalStore := store
	originalReadonly := readonlyMode
	originalProxied := proxiedServerMode
	originalRootCtx := rootCtx
	originalRootCancel := rootCancel
	originalCommandSpan := commandSpan
	originalProfileFile := profileFile
	originalTraceFile := traceFile
	storeMutex.Lock()
	originalStoreActive := storeActive
	storeMutex.Unlock()
	originalDoltAutoCommit := doltAutoCommit
	originalDidWrite := commandDidWrite.Load()
	originalDidExplicitCommit := commandDidExplicitDoltCommit
	originalDidWriteTipMetadata := commandDidWriteTipMetadata
	originalTipIDsWereNil := commandTipIDsShown == nil
	originalTipIDsShown := make(map[string]struct{}, len(commandTipIDsShown))
	for id := range commandTipIDsShown {
		originalTipIDsShown[id] = struct{}{}
	}
	originalCommit := runPostRunAutoCommit
	originalBackup := runPostRunAutoBackup
	originalExport := runPostRunAutoExport
	originalPush := runPostRunAutoPush
	t.Cleanup(func() {
		store = originalStore
		readonlyMode = originalReadonly
		proxiedServerMode = originalProxied
		rootCtx = originalRootCtx
		rootCancel = originalRootCancel
		commandSpan = originalCommandSpan
		profileFile = originalProfileFile
		traceFile = originalTraceFile
		storeMutex.Lock()
		storeActive = originalStoreActive
		storeMutex.Unlock()
		doltAutoCommit = originalDoltAutoCommit
		commandDidWrite.Store(originalDidWrite)
		commandDidExplicitDoltCommit = originalDidExplicitCommit
		commandDidWriteTipMetadata = originalDidWriteTipMetadata
		if originalTipIDsWereNil {
			commandTipIDsShown = nil
		} else {
			commandTipIDsShown = originalTipIDsShown
		}
		runPostRunAutoCommit = originalCommit
		runPostRunAutoBackup = originalBackup
		runPostRunAutoExport = originalExport
		runPostRunAutoPush = originalPush
	})

	pushCalls := 0
	runPostRunAutoCommit = func(context.Context, doltAutoCommitParams) error { return nil }
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = func(context.Context, bool) error { return nil }
	runPostRunAutoPush = func(context.Context) { pushCalls++ }

	readonlyMode = false
	proxiedServerMode = false
	rootCtx = context.Background()
	rootCancel = nil
	commandSpan = nil
	profileFile = nil
	traceFile = nil
	commandDidWrite.Store(false)
	commandDidExplicitDoltCommit = false
	commandDidWriteTipMetadata = false
	commandTipIDsShown = nil
	doltAutoCommit = string(doltAutoCommitOn)

	// A preview invocation of an otherwise writing command.
	preview := &cobra.Command{Use: "create"}
	preview.Flags().Bool("dry-run", false, "preview")
	if err := preview.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set dry-run: %v", err)
	}

	store = &strictReadonlyPostRunStore{}
	if err := rootCmd.PersistentPostRunE(preview, nil); err != nil {
		t.Fatalf("PersistentPostRunE (preview): %v", err)
	}
	if pushCalls != 0 {
		t.Fatalf("preview command ran the post-run auto-push %d time(s); a preview must not publish", pushCalls)
	}

	// Control: the same command without the preview flag still auto-pushes,
	// proving the skip above was the preview check and not the plumbing.
	plain := &cobra.Command{Use: "create"}
	plain.Flags().Bool("dry-run", false, "preview")

	store = &strictReadonlyPostRunStore{}
	if err := rootCmd.PersistentPostRunE(plain, nil); err != nil {
		t.Fatalf("PersistentPostRunE (control): %v", err)
	}
	if pushCalls != 1 {
		t.Fatalf("non-preview write command ran the post-run auto-push %d time(s), want 1", pushCalls)
	}
}
