package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// findCommandByPath resolves a command path such as "dep tree" in the real
// command tree, the way Cobra dispatches it (aliases included).
func findCommandByPath(t *testing.T, path string) *cobra.Command {
	t.Helper()
	cmd, rest, err := rootCmd.Find(strings.Fields(path))
	if err != nil || len(rest) != 0 || cmd == rootCmd {
		t.Fatalf("command path %q does not resolve: cmd=%v rest=%v err=%v", path, cmd, rest, err)
	}
	return cmd
}

// TestReadOnlyCommandPathsResolveToReadOnlyCommands pins every entry of the
// path-keyed read-only set: each names a real command whose path is exactly
// that entry, and each is classified read-only.
func TestReadOnlyCommandPathsResolveToReadOnlyCommands(t *testing.T) {
	want := []string{
		"children", "dep tree", "epic status", "history", "info", "kv get",
		"memories", "prime", "stale", "status", "statuses", "types",
	}
	got := make([]string, 0, len(readOnlyCommandPaths))
	for path := range readOnlyCommandPaths {
		got = append(got, path)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("readOnlyCommandPaths = %q, want %q", got, want)
	}
	for _, path := range want {
		cmd := findCommandByPath(t, path)
		if p := commandRegistryPath(cmd); p != path {
			t.Errorf("%q resolves to a command at path %q", path, p)
		}
		if !commandReadsOnly(cmd) {
			t.Errorf("commandReadsOnly(%q) = false, want true", path)
		}
	}
}

// TestCommandReadsOnlyFollowsAliasToCanonicalPath pins that `bd stats`, an
// alias Cobra dispatches to the status command, is classified through the
// canonical path "status" and not through the leaf map's "stats" entry.
func TestCommandReadsOnlyFollowsAliasToCanonicalPath(t *testing.T) {
	cmd := findCommandByPath(t, "stats")
	if p := commandRegistryPath(cmd); p != "status" {
		t.Fatalf("`bd stats` dispatches to path %q, want %q", p, "status")
	}
	if isReadOnlyCommand(cmd.Name()) {
		t.Fatalf("the leaf map already answers for %q; this test no longer reaches the path set", cmd.Name())
	}
	if !commandReadsOnly(cmd) {
		t.Fatal("commandReadsOnly(`bd stats`) = false, want true")
	}
}

// TestCommandReadsOnlyIgnoresRootName pins that the classification does not
// depend on the root command's name, so a binary built under another name
// classifies the same paths.
func TestCommandReadsOnlyIgnoresRootName(t *testing.T) {
	root := &cobra.Command{Use: "beads-fork"}
	dep := &cobra.Command{Use: "dep"}
	tree := &cobra.Command{Use: "tree [issue-id]", Run: func(*cobra.Command, []string) {}}
	federation := &cobra.Command{Use: "federation"}
	fedStatus := &cobra.Command{Use: "status", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(dep, federation)
	dep.AddCommand(tree)
	federation.AddCommand(fedStatus)

	if !commandReadsOnly(tree) {
		t.Error("commandReadsOnly(`beads-fork dep tree`) = false, want true")
	}
	if commandReadsOnly(fedStatus) {
		t.Error("commandReadsOnly(`beads-fork federation status`) = true, want false")
	}
}

// TestCommandReadsOnlyLeavesNestedNamesalikesAlone walks every command and
// checks each one that shares a leaf name with a path entry but sits at
// another path (`federation status`, `mol stale`, `config get`, ...): its
// classification must be what the leaf map answered before the path set
// existed.
func TestCommandReadsOnlyLeavesNestedNamesalikesAlone(t *testing.T) {
	leaves := map[string]bool{}
	for path := range readOnlyCommandPaths {
		fields := strings.Fields(path)
		leaves[fields[len(fields)-1]] = true
	}

	checked := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		path := commandRegistryPath(c)
		if path != "" && !readOnlyCommandPaths[path] && leaves[c.Name()] {
			checked[path] = true
			if got, want := commandReadsOnly(c), isReadOnlyCommand(c.Name()); got != want {
				t.Errorf("commandReadsOnly(%q) = %v, want the leaf map's %v", path, got, want)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	for _, path := range []string{"mol stale", "config get", "backup status"} {
		if !checked[path] {
			t.Errorf("walk did not reach %q; the fixture no longer covers a nested name collision", path)
		}
	}
	if commandReadsOnly(findCommandByPath(t, "mol stale")) {
		t.Error("commandReadsOnly(`mol stale`) = true, want false")
	}
	// federation.go builds only with cgo.
	if checked["federation status"] {
		if commandReadsOnly(findCommandByPath(t, "federation status")) {
			t.Error("commandReadsOnly(`federation status`) = true, want false")
		}
	} else {
		t.Log("federation status is not in this build (cgo off)")
	}
}

// TestPathClassifiedReadReachesEveryConsumer pins that the four places that
// act on the read-only classification (the root store open, auto-export,
// journal prune and auto-push) all consult commandReadsOnly, using a command
// that only the path set classifies.
func TestPathClassifiedReadReachesEveryConsumer(t *testing.T) {
	depTree := findCommandByPath(t, "dep tree")
	depAdd := findCommandByPath(t, "dep add")
	if isReadOnlyCommand(depTree.Name()) {
		t.Fatal("the leaf map already classifies `dep tree`; pick a command only the path set classifies")
	}

	oldReadonly := readonlyMode
	t.Cleanup(func() { readonlyMode = oldReadonly })
	readonlyMode = false

	t.Run("store open", func(t *testing.T) {
		if !effectiveRootStorePolicy(depTree, false).readOnly {
			t.Error("`dep tree` does not open the store read-only")
		}
		if effectiveRootStorePolicy(depAdd, false).readOnly {
			t.Error("control: `dep add` opens the store read-only")
		}
	})
	t.Run("auto-export", func(t *testing.T) {
		if shouldRunPostCommandAutoExport(depTree) {
			t.Error("`dep tree` runs auto-export")
		}
		if !shouldRunPostCommandAutoExport(depAdd) {
			t.Error("control: `dep add` skips auto-export")
		}
	})
	t.Run("journal prune", func(t *testing.T) {
		if shouldAutoPruneEventsJournal(depTree) {
			t.Error("`dep tree` runs events-journal auto-prune")
		}
		if !shouldAutoPruneEventsJournal(depAdd) {
			t.Error("control: `dep add` skips events-journal auto-prune")
		}
	})
}

// TestPersistentPostRunSkipsAutoPushForPathClassifiedRead drives the root
// post-run, where auto-push is decided inline. `events prune` is the control:
// it is not a read, and the events family never reaches the journal prune.
func TestPersistentPostRunSkipsAutoPushForPathClassifiedRead(t *testing.T) {
	snapshotRootContext(t)
	testModeUseGlobals = true
	readonlyMode = false

	oldStore, oldProxied := store, proxiedServerMode
	oldDidWrite := commandDidWrite.Load()
	oldTipMetadata := commandDidWriteTipMetadata
	oldCommit, oldBackup := runPostRunAutoCommit, runPostRunAutoBackup
	oldExport, oldPush := runPostRunAutoExport, runPostRunAutoPush
	t.Cleanup(func() {
		store, proxiedServerMode = oldStore, oldProxied
		commandDidWrite.Store(oldDidWrite)
		commandDidWriteTipMetadata = oldTipMetadata
		runPostRunAutoCommit, runPostRunAutoBackup = oldCommit, oldBackup
		runPostRunAutoExport, runPostRunAutoPush = oldExport, oldPush
	})
	store, proxiedServerMode = nil, false
	commandDidWrite.Store(false)
	commandDidWriteTipMetadata = false

	var exports, pushes int
	runPostRunAutoCommit = func(context.Context, doltAutoCommitParams) error { return nil }
	runPostRunAutoBackup = func(context.Context) {}
	runPostRunAutoExport = func(context.Context, bool) error { exports++; return nil }
	runPostRunAutoPush = func(context.Context) { pushes++ }

	for _, tc := range []struct {
		path        string
		wantPushes  int
		wantExports int
	}{
		{path: "dep tree"},
		{path: "events prune", wantPushes: 1, wantExports: 1},
	} {
		exports, pushes = 0, 0
		setRootContext(context.Background(), nil)
		if err := rootCmd.PersistentPostRunE(findCommandByPath(t, tc.path), nil); err != nil {
			t.Fatalf("PersistentPostRunE(%s): %v", tc.path, err)
		}
		if pushes != tc.wantPushes || exports != tc.wantExports {
			t.Errorf("%s: auto-push ran %d time(s), auto-export %d; want %d and %d",
				tc.path, pushes, exports, tc.wantPushes, tc.wantExports)
		}
	}
}

// setCommandFlagForTest sets one of cmd's flags as if typed on the command
// line and restores its value and Changed bit afterwards.
func setCommandFlagForTest(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("%s has no --%s flag", commandRegistryPath(cmd), name)
	}
	old, oldChanged := f.Value.String(), f.Changed
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = f.Value.Set(old)
		f.Changed = oldChanged
	})
}

// TestPersistentPreRunSkipsStoreForLocalOnlyFlags pins that `info --thanks`,
// `info --whats-new` and `types --sections`, which print text built into the
// binary, run with the database unavailable: the root pre-run does not open a
// store for them. The same commands without those flags, and `--thanks=false`,
// still take the store path and fail on the unreachable server. The fixture is
// the one TestPersistentPreRunSkipsStoreForStoreFreeConfigGet uses.
func TestPersistentPreRunSkipsStoreForLocalOnlyFlags(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltDatabase:   "local_only_flags_test",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: closedPort,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	reset := func(t *testing.T) {
		t.Helper()
		serverMode = false
		cmdCtx = nil
		dbPath = ""
		readonlyMode = false
		doltAutoCommit = ""
		store = nil
		if err := os.Remove(filepath.Join(beadsDir, localVersionFile)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", localVersionFile, err)
		}
	}

	for _, tc := range []struct {
		name      string
		cmd       *cobra.Command
		flag      string
		value     string
		wantStore bool
	}{
		{name: "info --thanks", cmd: infoCmd, flag: "thanks", value: "true"},
		{name: "info --whats-new", cmd: infoCmd, flag: "whats-new", value: "true"},
		{name: "types --sections", cmd: typesCmd, flag: "sections", value: "true"},
		{name: "info --thanks=false", cmd: infoCmd, flag: "thanks", value: "false", wantStore: true},
		{name: "info", cmd: infoCmd, wantStore: true},
		{name: "types", cmd: typesCmd, wantStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			if tc.flag != "" {
				setCommandFlagForTest(t, tc.cmd, tc.flag, tc.value)
			}
			err := rootCmd.PersistentPreRunE(tc.cmd, nil)
			if tc.wantStore {
				if err == nil {
					t.Errorf("%s must still attempt store init and fail on the unreachable server", tc.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s must skip store init and return nil, got: %v", tc.name, err)
			}
			if store != nil {
				t.Errorf("%s opened a store", tc.name)
			}
			if _, err := os.Stat(filepath.Join(beadsDir, localVersionFile)); err == nil {
				t.Errorf("%s wrote .beads/%s", tc.name, localVersionFile)
			}
		})
	}
}
