package main

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// fakeMaintenanceProvider is a proxied provider stand-in exposing the
// non-transactional seam proxied auto-backup runs on. It never opens a
// connection; the tests here only assert whether it would be used.
type fakeMaintenanceProvider struct {
	uow.UnitOfWorkProvider
	runs int
}

func (f *fakeMaintenanceProvider) RunNonTx(context.Context, func(context.Context, *sql.Conn) error) error {
	f.runs++
	return nil
}

func (f *fakeMaintenanceProvider) Close(context.Context) error { return nil }

// TestProxiedAutoBackupBackendFollowsLocality pins where auto-backup may run on
// a proxied workspace: exactly where `bd backup sync` is honored. Auto-backup
// registers a server-side backup remote, which on a server bd does not own is
// global to every client of it — the storm the explicit verbs refuse by design
// — so an implicit trigger must not get further than the explicit one. An
// unreadable sidecar resolves to the unknown topology and fails closed.
//
// Cannot be parallel: mutates the proxiedServerMode and uowProvider globals.
func TestProxiedAutoBackupBackendFollowsLocality(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sidecar     string
		noProvider  bool
		wantBackend bool
	}{
		{name: "managed-local with an open provider", sidecar: `{}`, wantBackend: true},
		{name: "managed-local with no provider open", sidecar: `{}`, noProvider: true},
		{name: "external tcp", sidecar: `{"external":{"host":"db.example.com","port":3306}}`},
		{name: "external unix socket", sidecar: `{"external":{"socket":"/tmp/dolt.sock"}}`},
		{name: "unreadable sidecar", sidecar: `{not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := prepareBackupStatusTest(t)
			proxiedServerMode = true
			if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte(tc.sidecar), 0o600); err != nil {
				t.Fatalf("write proxied sidecar: %v", err)
			}

			oldProvider := uowProvider
			t.Cleanup(func() { uowProvider = oldProvider })
			fake := &fakeMaintenanceProvider{}
			uowProvider = fake
			if tc.noProvider {
				uowProvider = nil
			}

			backend, ok := autoBackupBackendForCommand()
			if ok != tc.wantBackend {
				t.Fatalf("autoBackupBackendForCommand() ok = %v, want %v", ok, tc.wantBackend)
			}
			if !ok {
				return
			}
			if _, isProxied := backend.(proxiedLocalBackup); !isProxied {
				t.Fatalf("backend = %T, want proxiedLocalBackup", backend)
			}
			if fake.runs != 0 {
				t.Fatalf("resolving the backend ran %d maintenance call(s); it must not touch the server", fake.runs)
			}
		})
	}
}

// The direct arm has the same rule as the proxied arm: a --dry-run or --inspect
// must not run the post-command backup, export or push. A preview's embedded
// store refuses the backup, and the failed attempt still records its time, which
// holds off the next real backup for the whole interval. Export and push write
// their own files and state.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunDirectMaintenanceSkipsPreview(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
		want int
	}{
		{name: "ordinary command", want: 1},
		{name: "dry run", flag: "dry-run"},
		{name: "inspect", flag: "inspect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")

			oldStore, oldProvider := store, uowProvider
			oldReadonly, oldProxied := readonlyMode, proxiedServerMode
			oldRootCtx, oldRootCancel := rootCtx, rootCancel
			oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
			oldBackup, oldExport, oldPush := runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush
			oldDidWrite := commandDidWrite.Load()
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				readonlyMode, proxiedServerMode = oldReadonly, oldProxied
				rootCtx, rootCancel = oldRootCtx, oldRootCancel
				commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
				runPostRunAutoBackup, runPostRunAutoExport, runPostRunAutoPush = oldBackup, oldExport, oldPush
				commandDidWrite.Store(oldDidWrite)
			})

			var backups, exports, pushes int
			runPostRunAutoBackup = func(context.Context) { backups++ }
			runPostRunAutoExport = func(context.Context, bool) error { exports++; return nil }
			runPostRunAutoPush = func(context.Context) { pushes++ }
			commandDidWrite.Store(false)
			store, uowProvider = &strictReadonlyPostRunStore{}, nil
			readonlyMode, proxiedServerMode = false, false
			rootCtx, rootCancel = context.Background(), nil
			commandSpan, profileFile, traceFile = nil, nil, nil

			// A write command: export and push skip read-only ones on their own.
			cmd := &cobra.Command{Use: "create"}
			if tc.flag != "" {
				cmd.Flags().Bool(tc.flag, false, "")
				if err := cmd.Flags().Set(tc.flag, "true"); err != nil {
					t.Fatalf("set --%s: %v", tc.flag, err)
				}
			}
			if err := rootCmd.PersistentPostRunE(cmd, nil); err != nil {
				t.Fatalf("PersistentPostRunE: %v", err)
			}
			if backups != tc.want || exports != tc.want || pushes != tc.want {
				t.Fatalf("backups = %d, exports = %d, pushes = %d, want %d of each", backups, exports, pushes, tc.want)
			}
		})
	}
}

// TestPersistentPostRunProxiedRunsAutoBackup pins the wiring: the proxied arm
// of PersistentPostRunE calls the auto-backup hook, under the same gate the
// direct arm's maintenance net uses, plus previews.
//
// Before this hook existed the proxied arm only pruned the journal and closed
// the provider, so backup.enabled=true was inert on every proxied workspace.
//
// Cannot be parallel: mutates PersistentPostRunE's package globals.
func TestPersistentPostRunProxiedRunsAutoBackup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cmd      func() *cobra.Command
		readonly bool
		want     int
	}{
		{name: "ordinary command", cmd: func() *cobra.Command { return &cobra.Command{Use: "list"} }, want: 1},
		{name: "strict readonly", cmd: func() *cobra.Command { return &cobra.Command{Use: "list"} }, readonly: true},
		{name: "bd serve", cmd: func() *cobra.Command { return &cobra.Command{Use: serveCmdName} }},
		{name: "preview", cmd: func() *cobra.Command {
			c := &cobra.Command{Use: "list"}
			c.Flags().Bool("dry-run", false, "")
			if err := c.Flags().Set("dry-run", "true"); err != nil {
				t.Fatalf("set --dry-run: %v", err)
			}
			return c
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")

			oldStore, oldProvider := store, uowProvider
			oldReadonly, oldProxied := readonlyMode, proxiedServerMode
			oldRootCtx, oldRootCancel := rootCtx, rootCancel
			oldSpan, oldProfile, oldTrace := commandSpan, profileFile, traceFile
			oldBackup := runPostRunAutoBackup
			t.Cleanup(func() {
				store, uowProvider = oldStore, oldProvider
				readonlyMode, proxiedServerMode = oldReadonly, oldProxied
				rootCtx, rootCancel = oldRootCtx, oldRootCancel
				commandSpan, profileFile, traceFile = oldSpan, oldProfile, oldTrace
				runPostRunAutoBackup = oldBackup
			})

			calls := 0
			runPostRunAutoBackup = func(context.Context) { calls++ }
			store, uowProvider = nil, nil
			readonlyMode = tc.readonly
			proxiedServerMode = true
			rootCtx, rootCancel = context.Background(), nil
			commandSpan, profileFile, traceFile = nil, nil, nil

			if err := rootCmd.PersistentPostRunE(tc.cmd(), nil); err != nil {
				t.Fatalf("PersistentPostRunE: %v", err)
			}
			if calls != tc.want {
				t.Fatalf("auto-backup hook calls = %d, want %d", calls, tc.want)
			}
		})
	}
}
