package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// TestPersistentPostRunProxiedHonorsAutoExport is the GH#8490 reproduction: a
// proxied-server write with export.auto=true must reach the auto-export hook
// (or at least say why it did not). Before the fix the proxied arm of
// PersistentPostRunE never called it, so issues.jsonl silently drifted.
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
	stderr := captureStderr(t, func() { runErr = rootCmd.PersistentPostRunE(&cobra.Command{Use: "update"}, nil) })
	if runErr != nil {
		t.Fatalf("PersistentPostRunE: %v", runErr)
	}
	if exportCalls == 0 && !strings.Contains(strings.ToLower(stderr), "auto-export") {
		t.Fatalf("proxied write with export.auto=true: auto-export hook calls = 0 and no auto-export notice on stderr (got %q); export.auto is a silent no-op on the proxied path (GH#8490)", stderr)
	}
}

// closeRecordingProvider is a proxied provider stand-in that records, into a
// shared event log, when PersistentPostRunE closes it.
type closeRecordingProvider struct {
	uow.UnitOfWorkProvider
	events *[]string
}

func (p *closeRecordingProvider) Close(context.Context) error {
	*p.events = append(*p.events, "close")
	return nil
}

// TestPersistentPostRunProxiedRunsAutoExport pins the wiring of GH#8490: the
// proxied arm of PersistentPostRunE calls the auto-export hook through the
// provider the command opened — before that provider closes — under the
// auto-backup gate (strict --readonly, `bd serve`, previews and a migration
// freeze skip it) plus the direct arm's read-only classification. purge
// forwards its may-empty verdict exactly as the direct arm does, and a hook
// error fails the command while the provider is still closed exactly once.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunProxiedRunsAutoExport(t *testing.T) {
	dryRun := func(name string) func() *cobra.Command {
		return func() *cobra.Command {
			c := &cobra.Command{Use: name}
			c.Flags().Bool("dry-run", false, "")
			if err := c.Flags().Set("dry-run", "true"); err != nil {
				t.Fatalf("set --dry-run: %v", err)
			}
			return c
		}
	}
	named := func(name string) func() *cobra.Command {
		return func() *cobra.Command { return &cobra.Command{Use: name} }
	}
	hookErr := errors.New("auto-export: git add failed: index.lock exists")

	for _, tc := range []struct {
		name         string
		cmd          func() *cobra.Command
		readonly     bool
		frozen       bool
		mayEmpty     bool
		hookErr      error
		wantEvents   []string
		wantAllow    bool
		wantErr      bool
		wantStderrIn string
	}{
		{name: "create exports before the provider closes", cmd: named("create"), wantEvents: []string{"export", "close"}},
		{name: "purge forwards may-empty", cmd: named("purge"), mayEmpty: true, wantEvents: []string{"export", "close"}, wantAllow: true},
		{name: "read-only list", cmd: named("list"), wantEvents: []string{"close"}},
		{name: "strict readonly", cmd: named("create"), readonly: true, wantEvents: []string{"close"}},
		{name: "bd serve", cmd: named(serveCmdName), wantEvents: []string{"close"}},
		{name: "dry-run preview", cmd: dryRun("create"), wantEvents: []string{"close"}},
		{name: "migration freeze", cmd: named("create"), frozen: true, wantEvents: []string{"close"}},
		{name: "hook error fails the command", cmd: named("create"), hookErr: hookErr, wantEvents: []string{"export", "close"}, wantErr: true, wantStderrIn: "index.lock exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")

			oldStore, oldProvider := store, uowProvider
			oldReadonly, oldProxied := readonlyMode, proxiedServerMode
			oldRootCtx, oldRootCancel := rootCtx, rootCancel
			oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
			oldBackup, oldExport := runPostRunAutoBackup, runPostRunAutoExport
			oldFreeze, oldMayEmpty := commandFreeze, commandMayEmptyJSONLExport.Load()
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				readonlyMode, proxiedServerMode = oldReadonly, oldProxied
				rootCtx, rootCancel = oldRootCtx, oldRootCancel
				commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
				runPostRunAutoBackup, runPostRunAutoExport = oldBackup, oldExport
				commandFreeze = oldFreeze
				commandMayEmptyJSONLExport.Store(oldMayEmpty)
			})

			var events []string
			gotAllow := false
			runPostRunAutoBackup = func(context.Context) {}
			runPostRunAutoExport = func(_ context.Context, allowEmpty bool) error {
				events = append(events, "export")
				gotAllow = allowEmpty
				return tc.hookErr
			}
			store = nil
			uowProvider = &closeRecordingProvider{events: &events}
			readonlyMode = tc.readonly
			proxiedServerMode = true
			rootCtx, rootCancel = context.Background(), nil
			commandSpan, profileFile, traceFile = nil, nil, nil
			commandFreeze = migration.Result{}
			if tc.frozen {
				commandFreeze = migration.Result{Path: filepath.Join(t.TempDir(), migration.FileName)}
			}
			commandMayEmptyJSONLExport.Store(tc.mayEmpty)

			var runErr error
			stderr := captureStderr(t, func() { runErr = rootCmd.PersistentPostRunE(tc.cmd(), nil) })
			if (runErr != nil) != tc.wantErr {
				t.Fatalf("PersistentPostRunE err = %v, want error %v", runErr, tc.wantErr)
			}
			if strings.Join(events, ",") != strings.Join(tc.wantEvents, ",") {
				t.Fatalf("events = %v, want %v (export must run once, before the provider closes)", events, tc.wantEvents)
			}
			if gotAllow != tc.wantAllow {
				t.Fatalf("allowEmptyOverwrite = %v, want %v", gotAllow, tc.wantAllow)
			}
			if tc.wantStderrIn != "" && !strings.Contains(stderr, tc.wantStderrIn) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.wantStderrIn)
			}
			if uowProvider != nil {
				t.Fatalf("uowProvider left open after PersistentPostRunE")
			}
		})
	}
}
