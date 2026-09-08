package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func TestResolveWhereBeadsDir_FallsBackToFindBeadsDir(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	originalCmdCtx := cmdCtx
	defer func() {
		cmdCtx = originalCmdCtx
	}()

	resetCommandContext()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: fallback\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir(%q): %v", repoDir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	setDBPath("")

	dbFlag := rootCmd.PersistentFlags().Lookup("db")
	originalFlagValue := dbFlag.Value.String()
	originalFlagChanged := dbFlag.Changed
	if err := dbFlag.Value.Set(""); err != nil {
		t.Fatalf("reset db flag: %v", err)
	}
	dbFlag.Changed = false
	t.Cleanup(func() {
		_ = dbFlag.Value.Set(originalFlagValue)
		dbFlag.Changed = originalFlagChanged
	})

	if got := resolveWhereBeadsDir(nil); !utils.PathsEqual(got, beadsDir) {
		t.Fatalf("resolveWhereBeadsDir(nil) = %q, want %q", got, beadsDir)
	}
}

func TestResolveWhereBeadsDir_ReturnsEmptyWithoutWorkspace(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	originalCmdCtx := cmdCtx
	defer func() {
		cmdCtx = originalCmdCtx
	}()

	resetCommandContext()

	workspace := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir(%q): %v", workspace, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	setDBPath("")

	dbFlag := rootCmd.PersistentFlags().Lookup("db")
	originalFlagValue := dbFlag.Value.String()
	originalFlagChanged := dbFlag.Changed
	if err := dbFlag.Value.Set(""); err != nil {
		t.Fatalf("reset db flag: %v", err)
	}
	dbFlag.Changed = false
	t.Cleanup(func() {
		_ = dbFlag.Value.Set(originalFlagValue)
		dbFlag.Changed = originalFlagChanged
	})

	if got := resolveWhereBeadsDir(nil); got != "" {
		t.Fatalf("resolveWhereBeadsDir(nil) = %q, want empty", got)
	}
}

func TestResolveWhereBeadsDir_UsesInitializedDBPath(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)

	originalCmdCtx := cmdCtx
	defer func() {
		cmdCtx = originalCmdCtx
	}()

	resetCommandContext()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	dbDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}

	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	cwd := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	setDBPath(dbDir)

	if got := resolveWhereBeadsDir(nil); !utils.PathsEqual(got, beadsDir) {
		t.Fatalf("resolveWhereBeadsDir(nil) = %q, want %q", got, beadsDir)
	}
}

func TestResolveWhereDatabasePath_PrefersInitializedDBPath(t *testing.T) {
	originalDBPath := dbPath
	originalCmdCtx := cmdCtx
	defer func() {
		dbPath = originalDBPath
		cmdCtx = originalCmdCtx
	}()

	resetCommandContext()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	dbDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}

	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	dbPath = dbDir
	t.Setenv("BEADS_DIR", "")

	prepareSelectedNoDBContext(beadsDir)

	if got := resolveWhereDatabasePath(); !utils.PathsEqual(got, dbDir) {
		t.Fatalf("resolveWhereDatabasePath() = %q, want %q", got, dbDir)
	}
}

func TestIsSelectedNoDBCommand(t *testing.T) {
	doltChild := func(name string) *cobra.Command {
		parent := &cobra.Command{Use: "dolt"}
		child := &cobra.Command{Use: name}
		parent.AddCommand(child)
		return child
	}
	parentedChild := func(parentName, name string) *cobra.Command {
		parent := &cobra.Command{Use: parentName}
		child := &cobra.Command{Use: name}
		parent.AddCommand(child)
		return child
	}

	tests := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{
			name: "nil command",
			cmd:  nil,
			want: false,
		},
		{
			name: "where",
			cmd:  &cobra.Command{Use: "where"},
			want: true,
		},
		{
			name: "context",
			cmd:  &cobra.Command{Use: "context"},
			want: true,
		},
		{
			name: "root command without dolt parent",
			cmd:  &cobra.Command{Use: "status"},
			want: false,
		},
		{
			name: "child with non-dolt parent",
			cmd:  parentedChild("config", "show"),
			want: false,
		},
		{
			name: "dolt status",
			cmd:  doltChild("status"),
			want: true,
		},
		{
			name: "dolt push",
			cmd:  doltChild("push"),
			want: false,
		},
		{
			name: "dolt pull",
			cmd:  doltChild("pull"),
			want: false,
		},
		{
			name: "dolt commit",
			cmd:  doltChild("commit"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelectedNoDBCommand(tt.cmd); got != tt.want {
				t.Fatalf("isSelectedNoDBCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldReadWherePrefixFromStore(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	t.Run("empty beads dir", func(t *testing.T) {
		if got := shouldReadWherePrefixFromStore(""); got {
			t.Fatal("shouldReadWherePrefixFromStore(\"\") = true, want false")
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}
		if got := shouldReadWherePrefixFromStore(beadsDir); !got {
			t.Fatal("shouldReadWherePrefixFromStore(missing metadata) = false, want true")
		}
	})

	t.Run("server mode metadata", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}
		cfg := &configfile.Config{
			Backend:  configfile.BackendDolt,
			DoltMode: configfile.DoltModeServer,
		}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save metadata: %v", err)
		}
		if got := shouldReadWherePrefixFromStore(beadsDir); got {
			t.Fatal("shouldReadWherePrefixFromStore(server mode) = true, want false")
		}
	})

	t.Run("embedded mode metadata", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}
		cfg := &configfile.Config{
			Backend:  configfile.BackendDolt,
			DoltMode: configfile.DoltModeEmbedded,
		}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save metadata: %v", err)
		}
		if got := shouldReadWherePrefixFromStore(beadsDir); !got {
			t.Fatal("shouldReadWherePrefixFromStore(embedded mode) = false, want true")
		}
	})
}

func TestWhereCommand_UsesConfigPrefixFromSelectedDB(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	initConfigForTest(t)

	originalCmdCtx := cmdCtx
	originalJSONOutput := jsonOutput
	originalRootCtx := rootCtx
	defer func() {
		cmdCtx = originalCmdCtx
		jsonOutput = originalJSONOutput
		rootCtx = originalRootCtx
	}()

	resetCommandContext()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	dbDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}

	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: yamlprefix\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", dbDir)
	t.Setenv("BD_DB", "")

	dbFlag := rootCmd.PersistentFlags().Lookup("db")
	originalFlagValue := dbFlag.Value.String()
	originalFlagChanged := dbFlag.Changed
	if err := dbFlag.Value.Set(""); err != nil {
		t.Fatalf("reset db flag: %v", err)
	}
	dbFlag.Changed = false
	t.Cleanup(func() {
		_ = dbFlag.Value.Set(originalFlagValue)
		dbFlag.Changed = originalFlagChanged
	})

	jsonOutput = true
	rootCtx = context.Background()

	output := captureStdout(t, func() error {
		return whereCmd.RunE(whereCmd, nil)
	})

	var result WhereResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", output, err)
	}

	if !utils.PathsEqual(result.Path, beadsDir) {
		t.Fatalf("Path = %q, want %q", result.Path, beadsDir)
	}
	if !utils.PathsEqual(result.DatabasePath, dbDir) {
		t.Fatalf("DatabasePath = %q, want %q", result.DatabasePath, dbDir)
	}
	if result.Prefix != "yamlprefix" {
		t.Fatalf("Prefix = %q, want %q", result.Prefix, "yamlprefix")
	}
}

// TestFindOriginalBeadsDir_BeadsDirEnvWithoutRedirectFallsThrough guards
// against a regression where `bd where` silently dropped its "(via redirect
// from ...)" footnote after #3230 started rebinding BEADS_DIR to the
// post-redirect target. Before the fix, an early `return ""` when BEADS_DIR
// was set but had no redirect file short-circuited the filesystem walk,
// so `bd where` from inside a redirecting worktree never reported the
// redirect source.
//
// Scenario: worktree's .beads/ holds a redirect file pointing to the
// shared .beads/, and BEADS_DIR has been set to the shared target (as
// bd's own startup does for rebound commands). findOriginalBeadsDir must
// still walk up from cwd and discover the worktree's redirecting .beads/.
func TestFindOriginalBeadsDir_BeadsDirEnvWithoutRedirectFallsThrough(t *testing.T) {
	tmp := t.TempDir()

	// Redirect source: worktree/.beads/ with a redirect file.
	worktreeRoot := filepath.Join(tmp, "worktree")
	worktreeBeads := filepath.Join(worktreeRoot, ".beads")
	if err := os.MkdirAll(worktreeBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "main", ".beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	redirectFile := filepath.Join(worktreeBeads, beads.RedirectFileName)
	if err := os.WriteFile(redirectFile, []byte(target), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate bd's post-rebind state: BEADS_DIR points at the resolved
	// target, which does NOT carry a redirect file.
	t.Setenv("BEADS_DIR", target)
	t.Chdir(worktreeRoot)

	got := findOriginalBeadsDir()
	if got == "" {
		t.Fatal("findOriginalBeadsDir returned empty; expected the redirecting worktree .beads/")
	}
	// Compare by base+parent since /var vs /private/var may be resolved either way.
	if !strings.HasSuffix(got, filepath.Join("worktree", ".beads")) {
		t.Errorf("findOriginalBeadsDir = %q, want a path ending in worktree/.beads", got)
	}
}

// TestFindOriginalBeadsDir_BeadsDirEnvWithRedirectReturnsEnv keeps the
// BEADS_DIR fast path working when the env var itself points at a
// redirecting directory (a user-set BEADS_DIR override). The env value
// should win without a filesystem walk.
func TestFindOriginalBeadsDir_BeadsDirEnvWithRedirectReturnsEnv(t *testing.T) {
	tmp := t.TempDir()
	envBeads := filepath.Join(tmp, "alt", ".beads")
	if err := os.MkdirAll(envBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "shared", ".beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envBeads, beads.RedirectFileName), []byte(target), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run cwd from an unrelated directory so the fs walk alone cannot find
	// the redirect — only the BEADS_DIR env check can.
	unrelated := filepath.Join(tmp, "unrelated")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", envBeads)
	t.Chdir(unrelated)

	got := findOriginalBeadsDir()
	// Compare by suffix for macOS /var vs /private/var differences.
	if !strings.HasSuffix(got, filepath.Join("alt", ".beads")) {
		t.Errorf("findOriginalBeadsDir = %q, want a path ending in alt/.beads", got)
	}
}
