package main

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// TestProxiedAutoExportBackendSelection pins which proxied runs auto-export
// may read through. Unlike proxied auto-backup there is no topology gate —
// export only reads SQL and writes a client-local file — so the refusals are
// exactly: no provider this run (nothing to read through), a provider without
// the non-transactional maintenance seam, and a --database override (the
// JSONL belongs to the workspace's configured database). Resolving the
// backend must not touch the server: the reads happen later, inside the
// pipeline, or never.
//
// Cannot be parallel: mutates the uowProvider and commandDatabaseOverride
// globals.
func TestProxiedAutoExportBackendSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noProvider bool
		plainDuck  bool // provider type lacking the MaintenanceProvider seam
		override   string
		wantNil    bool
	}{
		{name: "provider with the maintenance seam", wantNil: false},
		{name: "no provider this run", noProvider: true, wantNil: true},
		{name: "provider without a maintenance seam", plainDuck: true, wantNil: true},
		{name: "database override", override: "otherdb", wantNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldProvider, oldOverride := uowProvider, commandDatabaseOverride
			t.Cleanup(func() { uowProvider, commandDatabaseOverride = oldProvider, oldOverride })

			var fake *fakeMaintenanceProvider
			switch {
			case tc.noProvider:
				uowProvider = nil
			case tc.plainDuck:
				uowProvider = plainProviderDuck{}
			default:
				fake = &fakeMaintenanceProvider{}
				uowProvider = fake
			}
			commandDatabaseOverride = tc.override

			backend := proxiedAutoExportBackendForCommand()
			if tc.wantNil {
				if backend != nil {
					t.Fatalf("backend = %T, want nil", backend)
				}
				return
			}
			if _, ok := backend.(proxiedAutoExportBackend); !ok {
				t.Fatalf("backend = %T, want proxiedAutoExportBackend", backend)
			}
			if fake != nil && fake.runs != 0 {
				t.Fatalf("resolving the backend ran %d maintenance call(s); it must not touch the server", fake.runs)
			}
		})
	}
}

// plainProviderDuck satisfies uow.UnitOfWorkProvider (and little else): a
// provider decorator that dropped RunNonTx, the case the seam check refuses.
type plainProviderDuck struct {
	uow.UnitOfWorkProvider
}

// TestPersistentPostRunProxiedRunsAutoExportGate pins the proxied arm's gate:
// auto-export runs after an ordinary mutating command and nowhere the direct
// arm would skip it — read-only commands (`bd list`), strict --readonly,
// `bd serve`, a --dry-run preview, and a migration freeze. A failing export
// surfaces as the command's own error.
//
// Before the hook existed, export.auto was a silent no-op on every proxied
// workspace (GH#8490): the pipeline read a process-global store proxied
// commands never set.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunProxiedRunsAutoExportGate(t *testing.T) {
	preview := func() *cobra.Command {
		c := &cobra.Command{Use: "create"}
		c.Flags().Bool("dry-run", false, "")
		if err := c.Flags().Set("dry-run", "true"); err != nil {
			t.Fatalf("set --dry-run: %v", err)
		}
		return c
	}
	for _, tc := range []struct {
		name      string
		cmd       func() *cobra.Command
		readonly  bool
		frozen    bool
		stubError error
		wantCalls int
		wantErr   bool
	}{
		{name: "mutating command", cmd: func() *cobra.Command { return &cobra.Command{Use: "create"} }, wantCalls: 1},
		{name: "read-only command", cmd: func() *cobra.Command { return &cobra.Command{Use: "list"} }},
		{name: "strict readonly", cmd: func() *cobra.Command { return &cobra.Command{Use: "create"} }, readonly: true},
		{name: "bd serve", cmd: func() *cobra.Command { return &cobra.Command{Use: serveCmdName} }},
		{name: "preview", cmd: preview},
		{name: "migration freeze", cmd: func() *cobra.Command { return &cobra.Command{Use: "create"} }, frozen: true},
		{name: "export error surfaces", cmd: func() *cobra.Command { return &cobra.Command{Use: "create"} }, stubError: errors.New("boom"), wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")

			oldStore, oldProvider := store, uowProvider
			oldReadonly, oldProxied := readonlyMode, proxiedServerMode
			oldRootCtx, oldRootCancel := rootCtx, rootCancel
			oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
			oldBackup, oldExport := runPostRunAutoBackup, runPostRunAutoExport
			oldFreeze := commandFreeze
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				readonlyMode, proxiedServerMode = oldReadonly, oldProxied
				rootCtx, rootCancel = oldRootCtx, oldRootCancel
				commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
				runPostRunAutoBackup, runPostRunAutoExport = oldBackup, oldExport
				commandFreeze = oldFreeze
			})

			calls := 0
			runPostRunAutoBackup = func(context.Context) {}
			runPostRunAutoExport = func(context.Context, bool) error {
				calls++
				return tc.stubError
			}
			store, uowProvider = nil, nil
			readonlyMode = tc.readonly
			proxiedServerMode = true
			rootCtx, rootCancel = context.Background(), nil
			commandSpan, profileFile, traceFile = nil, nil, nil
			if tc.frozen {
				commandFreeze = migration.Result{Path: "some/freeze/marker"}
			} else {
				commandFreeze = migration.Result{}
			}

			err := rootCmd.PersistentPostRunE(tc.cmd(), nil)
			if tc.wantErr && err == nil {
				t.Fatal("PersistentPostRunE = nil error, want the export error surfaced")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("PersistentPostRunE: %v", err)
			}
			if calls != tc.wantCalls {
				t.Fatalf("auto-export hook calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
