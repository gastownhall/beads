package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

func TestResolveChangeDirBeadsDirDoesNotChangeCWD(t *testing.T) {
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	startDir := t.TempDir()
	t.Chdir(startDir)

	projectDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := resolveChangeDirBeadsDir(projectDir)
	if err != nil {
		t.Fatalf("resolveChangeDirBeadsDir: %v", err)
	}
	if got != beadsDir {
		t.Fatalf("resolveChangeDirBeadsDir() = %q, want %q", got, beadsDir)
	}

	afterWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd after resolve: %v", err)
	}
	if afterWD != startDir {
		t.Fatalf("working directory changed to %q, want %q", afterWD, startDir)
	}
}

func TestResolveChangeDirBeadsDirRejectsFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := resolveChangeDirBeadsDir(filePath); err == nil {
		t.Fatal("expected non-directory -C target to fail")
	}
}

func TestResolveChangeDirBeadsDirRejectsDirectoryWithoutProject(t *testing.T) {
	if _, err := resolveChangeDirBeadsDir(t.TempDir()); err == nil {
		t.Fatal("expected -C target without a beads project to fail")
	}
}

const primeTestMetadata = `{"backend":"dolt"}`

// primeTestWorkspace creates a workspace root (symlinks resolved, so paths
// compare equal to what -C resolution yields) holding an empty .beads/ plus
// the given files, keyed by path relative to the root.
func primeTestWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestPrimeWorkspaceDir(t *testing.T) {
	t.Cleanup(func() {
		changeDir = ""
	})
	parent := primeTestWorkspace(t, map[string]string{"target/.beads/metadata.json": primeTestMetadata})
	target := filepath.Join(parent, "target")
	t.Chdir(parent)

	for _, tc := range []struct{ changeDir, want string }{
		{"", ""},           // no -C: workspace-relative reads stay on the cwd
		{"  ", ""},         // blank -C is treated as unset, as applyChangeDirSelection does
		{target, target},   // absolute -C
		{"target", target}, // relative -C, like `bd -C target prime`
	} {
		changeDir = tc.changeDir
		if got := primeWorkspaceDir(); got != tc.want {
			t.Errorf("changeDir=%q: primeWorkspaceDir() = %q, want %q", tc.changeDir, got, tc.want)
		}
	}
}

func TestReadCustomPrimeContent_ChangeDirPrefersTarget(t *testing.T) {
	const cwdPrime = "# CWD local PRIME\ncwd-local-custom\n"
	const targetPrime = "# Target workspace PRIME\ntarget-workspace-custom\n"
	cwd := primeTestWorkspace(t, map[string]string{".beads/PRIME.md": cwdPrime})
	target := primeTestWorkspace(t, map[string]string{".beads/PRIME.md": targetPrime})
	t.Chdir(cwd)

	// bd -C target prime: the workspace is the -C target, so its clone-local
	// PRIME.md wins over the cwd's.
	got, ok := readCustomPrimeContent(target, filepath.Join(target, ".beads"))
	if !ok {
		t.Fatal("expected a custom PRIME.md, got none")
	}
	if got != targetPrime {
		t.Fatalf("with -C target, readCustomPrimeContent = %q, want target %q", got, targetPrime)
	}
}

// Without -C the workspace is the cwd even when BEADS_DIR points elsewhere: the
// cwd's clone-local PRIME.md keeps winning (GH#876), and the redirected
// workspace's file is only the second tier.
func TestReadCustomPrimeContent_EnvRedirectStillLocalFirst(t *testing.T) {
	const cwdPrime = "# CWD local PRIME\nenv-redirect-local-wins\n"
	const redirectedPrime = "# Redirected workspace PRIME\nenv-redirect-shared\n"
	cwd := primeTestWorkspace(t, map[string]string{".beads/PRIME.md": cwdPrime})
	redirected := primeTestWorkspace(t, map[string]string{".beads/PRIME.md": redirectedPrime})
	t.Chdir(cwd)

	got, ok := readCustomPrimeContent("", filepath.Join(redirected, ".beads"))
	if !ok {
		t.Fatal("expected a custom PRIME.md, got none")
	}
	if got != cwdPrime {
		t.Fatalf("env-only redirect must keep local-first: got %q, want local %q", got, cwdPrime)
	}
}

// -C target is a redirect clone (.beads/redirect -> external store) that also
// carries its own clone-local .beads/PRIME.md. `cd target && bd prime` reads the
// clone-local file ahead of the shared one; `bd -C target prime` must match it,
// not fall through to the external store's PRIME.md and not read the cwd's.
func TestReadCustomPrimeContent_ChangeDirRedirectClonePrefersCloneLocal(t *testing.T) {
	const cwdPrime = "# CWD-LOCAL\n"
	const clonePrime = "# TARGET-CLONE-LOCAL\n"
	const sharedPrime = "# EXTERNAL-SHARED\n"
	cwd := primeTestWorkspace(t, map[string]string{".beads/PRIME.md": cwdPrime})
	external := primeTestWorkspace(t, map[string]string{
		".beads/metadata.json": primeTestMetadata,
		".beads/PRIME.md":      sharedPrime,
	})
	externalBeads := filepath.Join(external, ".beads")
	target := primeTestWorkspace(t, map[string]string{
		".beads/redirect": externalBeads + "\n",
		".beads/PRIME.md": clonePrime,
	})

	resolvedBeads, err := resolveChangeDirBeadsDir(target)
	if err != nil {
		t.Fatalf("resolveChangeDirBeadsDir: %v", err)
	}
	if filepath.Clean(resolvedBeads) != filepath.Clean(externalBeads) {
		t.Fatalf("resolved beads = %q, want external %q", resolvedBeads, externalBeads)
	}

	// Reference: cd target && bd prime.
	t.Chdir(target)
	want, ok := readCustomPrimeContent("", resolvedBeads)
	if !ok || want != clonePrime {
		t.Fatalf("reference (cd target): got %q, %v; want clone-local %q", want, ok, clonePrime)
	}

	// Under test: bd -C target prime from an unrelated cwd with its own PRIME.md.
	t.Chdir(cwd)
	got, ok := readCustomPrimeContent(target, resolvedBeads)
	if !ok {
		t.Fatal("expected a custom PRIME.md, got none")
	}
	if got != want {
		t.Fatalf("bd -C target must match cd target: got %q, want %q", got, want)
	}
}

func TestIsPreviewCommand(t *testing.T) {
	tests := []struct {
		name string
		flag string
		set  string
		want bool
	}{
		{name: "dry run", flag: "dry-run", set: "true", want: true},
		{name: "inspect", flag: "inspect", set: "true", want: true},
		{name: "false preview flag", flag: "dry-run", set: "false", want: false},
		{name: "no preview flag", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			if tt.flag != "" {
				cmd.Flags().Bool(tt.flag, false, "")
				if err := cmd.Flags().Set(tt.flag, tt.set); err != nil {
					t.Fatalf("set %s: %v", tt.flag, err)
				}
			}
			if got := isPreviewCommand(cmd); got != tt.want {
				t.Fatalf("isPreviewCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

// selectedWorkspaceEnvForTest clears every inherited workspace selector and
// database override, so only the fixture chooses the database.
func selectedWorkspaceEnvForTest(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BEADS_DIR", "BEADS_DB", "BD_DB",
		"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT",
	} {
		t.Setenv(key, "")
	}
	setChangeDirForTest(t, "")
	resetRepoContextCachesForTest(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)
	t.Cleanup(restoreChangeDirSelection)
}

// newServerWorkspaceForTest creates <dir>/.beads as a server-mode workspace
// naming database and returns the .beads path.
func newServerWorkspaceForTest(t *testing.T, dir, database string) string {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, database)
	return beadsDir
}

// newRedirectedCallerForTest builds the GH#5756 caller: a git repository whose
// .beads names caller_db and redirects to shared storage. It returns the
// repository and the caller's .beads.
func newRedirectedCallerForTest(t *testing.T) (callerDir, callerBeadsDir string) {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sharedBeadsDir := newServerWorkspaceForTest(t, filepath.Join(root, "shared"), "shared_db")
	callerDir = filepath.Join(root, "caller")
	initGitRepoAt(t, mkdirForTest(t, callerDir))
	callerBeadsDir = filepath.Join(callerDir, ".beads")
	writeRedirectForTest(t, callerBeadsDir, sharedBeadsDir)
	writeMetadataConfig(t, callerBeadsDir, configfile.DoltModeServer, "caller_db")
	return callerDir, callerBeadsDir
}

func mkdirForTest(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// runContextJSONForTest runs the context command's startup and body in
// process and returns the database it reports.
func runContextJSONForTest(t *testing.T) string {
	t.Helper()
	return runContextInfoForTest(t).Database
}

// runContextInfoForTest runs the context command's startup and body in
// process and returns its JSON result.
func runContextInfoForTest(t *testing.T) ContextInfo {
	t.Helper()
	if err := rootCmd.PersistentPreRunE(contextCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE(context): %v", err)
	}
	jsonOutput = true
	out := captureStdout(t, func() error { return contextCmd.RunE(contextCmd, nil) })
	var got ContextInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("context --json output %q: %v", out, err)
	}
	return got
}

// TestSelectedWorkspaceDatabaseIsolation pins GH#5756 for the no-store
// commands: a workspace selected by -C or an exported BEADS_DIR reports its
// own database, never the redirect-source database of the caller's cwd repo.
func TestSelectedWorkspaceDatabaseIsolation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector string
		cmd      *cobra.Command
	}{
		{name: "context via -C", selector: "-C", cmd: contextCmd},
		{name: "context via BEADS_DIR", selector: "BEADS_DIR", cmd: contextCmd},
		{name: "doctor startup via -C", selector: "-C", cmd: doctorCmd},
		{name: "doctor startup via BEADS_DIR", selector: "BEADS_DIR", cmd: doctorCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectedWorkspaceEnvForTest(t)
			callerDir, _ := newRedirectedCallerForTest(t)
			targetDir := filepath.Join(t.TempDir(), "target")
			targetBeadsDir := newServerWorkspaceForTest(t, targetDir, "target_db")
			t.Chdir(callerDir)
			if tc.selector == "-C" {
				changeDir = targetDir
			} else {
				t.Setenv("BEADS_DIR", targetBeadsDir)
			}

			if tc.cmd == contextCmd {
				if got := runContextJSONForTest(t); got != "target_db" {
					t.Fatalf("context database = %q, want target_db (the caller's redirect-source database leaked into the selected workspace)", got)
				}
			} else if err := rootCmd.PersistentPreRunE(tc.cmd, nil); err != nil {
				t.Fatalf("PersistentPreRunE(%s): %v", tc.cmd.Name(), err)
			}
			if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
				t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset for a selected workspace without a redirect", got)
			}
		})
	}
}

// TestSelectedWorkspaceStorelessIsolation pins GH#5756 for the store
// commands: -C onto a workspace with no database keeps the existing
// no-database refusal and never exports the caller's redirect-source database
// on the way there.
func TestSelectedWorkspaceStorelessIsolation(t *testing.T) {
	for _, cmd := range []*cobra.Command{listCmd, createCmd, updateCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			selectedWorkspaceEnvForTest(t)
			callerDir, _ := newRedirectedCallerForTest(t)
			targetDir := filepath.Join(t.TempDir(), "target")
			writeTestConfigYAML(t, filepath.Join(targetDir, ".beads"), "prefix: zz\n")
			t.Chdir(callerDir)
			changeDir = targetDir

			var err error
			stderr := captureStderr(t, func() { err = rootCmd.PersistentPreRunE(cmd, nil) })
			if err == nil || !strings.Contains(stderr, "no beads database found") {
				t.Fatalf("PersistentPreRunE(%s) = %v, stderr %q; want the no-database refusal", cmd.Name(), err, stderr)
			}
			if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
				t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset: the caller's redirect-source database must not follow -C", got)
			}
		})
	}
}

// TestSelectedWorkspaceBackendAdmission pins GH#5756 for workspaces admitted
// without local database files: -C onto a registered-backend workspace from a
// redirected caller still reaches that backend, without the caller's
// redirect-source database exported for the open.
func TestSelectedWorkspaceBackendAdmission(t *testing.T) {
	selectedWorkspaceEnvForTest(t)
	const name = "selected-workspace-admission"
	var opened []string
	open := func(_ context.Context, beadsDir string) (storage.DoltStorage, error) {
		opened = append(opened, beadsDir)
		return nil, errRegistryReadWrite
	}
	backends.Register(name, backends.Backend{Open: open, OpenReadOnly: open, WorkspaceIsBeadsDir: true, Remote: true})
	t.Cleanup(func() { backends.Deregister(name) })

	callerDir, _ := newRedirectedCallerForTest(t)
	targetDir := filepath.Join(t.TempDir(), "target")
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", targetBeadsDir, err)
	}
	if err := (&configfile.Config{Backend: name}).Save(targetBeadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	t.Chdir(callerDir)
	changeDir = targetDir

	if err := rootCmd.PersistentPreRunE(listCmd, nil); err == nil {
		t.Fatal("PersistentPreRunE(list) succeeded against a backend that refuses every open")
	}
	if len(opened) == 0 {
		t.Fatal("the registered backend was never opened: admission fell through to the no-database refusal")
	}
	for _, dir := range opened {
		if !sameDirPath(dir, targetBeadsDir) {
			t.Fatalf("backend opened %q, want the selected workspace %q", dir, targetBeadsDir)
		}
	}
	if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset: the caller's redirect-source database must not follow -C", got)
	}
}

// TestSelectedWorkspaceRedirectDatabase pins GH#5756 for two clones sharing
// one redirected storage under distinct source databases: selecting clone2
// from clone1 (or from outside any repository) uses clone2's own redirect, so
// context reports clone2's database and where names clone2 as the source.
func TestSelectedWorkspaceRedirectDatabase(t *testing.T) {
	for _, tc := range []struct {
		name       string
		selector   string
		fromClone1 bool
		cmd        *cobra.Command
	}{
		{name: "context via -C from clone1", selector: "-C", fromClone1: true, cmd: contextCmd},
		{name: "context via BEADS_DIR from clone1", selector: "BEADS_DIR", fromClone1: true, cmd: contextCmd},
		{name: "context via -C from outside", selector: "-C", cmd: contextCmd},
		{name: "where via -C from clone1", selector: "-C", fromClone1: true, cmd: whereCmd},
		{name: "where via BEADS_DIR from clone1", selector: "BEADS_DIR", fromClone1: true, cmd: whereCmd},
		{name: "where via -C from outside", selector: "-C", cmd: whereCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectedWorkspaceEnvForTest(t)
			clone1Dir, clone1BeadsDir := newRedirectedCallerForTest(t)
			sharedBeadsDir := beads.FollowRedirect(clone1BeadsDir)
			clone2Dir := filepath.Join(filepath.Dir(clone1Dir), "clone2")
			initGitRepoAt(t, mkdirForTest(t, clone2Dir))
			clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
			writeRedirectForTest(t, clone2BeadsDir, sharedBeadsDir)
			writeMetadataConfig(t, clone2BeadsDir, configfile.DoltModeServer, "clone2_db")
			if tc.fromClone1 {
				t.Chdir(clone1Dir)
			} else {
				t.Chdir(t.TempDir())
			}
			if tc.selector == "-C" {
				changeDir = clone2Dir
			} else {
				t.Setenv("BEADS_DIR", clone2BeadsDir)
			}

			if tc.cmd == contextCmd {
				if got := runContextJSONForTest(t); got != "clone2_db" {
					t.Fatalf("context database = %q, want clone2_db", got)
				}
				return
			}
			got := runWhereJSONForTest(t)
			if !sameDirPath(got.Path, sharedBeadsDir) || !sameDirPath(got.RedirectedFrom, clone2BeadsDir) {
				t.Fatalf("where = %q via %q, want %q via %q", got.Path, got.RedirectedFrom, sharedBeadsDir, clone2BeadsDir)
			}
		})
	}
}

// TestSelectedWorkspaceRedirectPrecedence pins the redirect-source shapes
// GH#5756 must keep: plain discovery from the redirected caller, BEADS_DIR
// naming the caller's own redirect target (bd-wayc3) or its redirecting
// .beads, and a user-exported BEADS_DOLT_SERVER_DATABASE, which still wins.
func TestSelectedWorkspaceRedirectPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		beadsDir  func(callerBeadsDir string) string
		exported  string
		wantDBase string
	}{
		{name: "plain discovery", wantDBase: "caller_db"},
		{name: "BEADS_DIR names the redirect target", beadsDir: beads.FollowRedirect, wantDBase: "caller_db"},
		{name: "BEADS_DIR names the redirecting workspace", beadsDir: func(dir string) string { return dir }, wantDBase: "caller_db"},
		{name: "user-exported database", exported: "user_db", wantDBase: "user_db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectedWorkspaceEnvForTest(t)
			callerDir, callerBeadsDir := newRedirectedCallerForTest(t)
			t.Chdir(callerDir)
			if tc.beadsDir != nil {
				t.Setenv("BEADS_DIR", tc.beadsDir(callerBeadsDir))
			}
			if tc.exported != "" {
				t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.exported)
			}

			if got := runContextJSONForTest(t); got != tc.wantDBase {
				t.Fatalf("context database = %q, want %q", got, tc.wantDBase)
			}
		})
	}
}

// TestSelectedWorkspaceSelectionEdges pins the selection edges GH#5756 must
// keep: an explicit --db target suppresses ambient preservation, a .beads/.env
// selector routes to a foreign target, a nested direct workspace under an
// ancestor redirect stays foreign, the retained source does not survive into
// the next command, BEADS_DIR naming a redirecting .beads keeps its redirect
// from outside any repository, BEADS_DIR naming the caller's redirect target
// keeps the caller's provenance (bd-wayc3), and plain discovery still passes
// over a redirect at the temp root.
func TestSelectedWorkspaceSelectionEdges(t *testing.T) {
	t.Run("explicit --db target suppresses ambient preservation", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		callerDir, _ := newRedirectedCallerForTest(t)
		targetBeadsDir := newServerWorkspaceForTest(t, filepath.Join(t.TempDir(), "target"), "target_db")
		t.Chdir(callerDir)
		dbPath = filepath.Join(targetBeadsDir, "dolt")
		flag := rootCmd.PersistentFlags().Lookup("db")
		flag.Changed = true
		t.Cleanup(func() { flag.Changed = false })

		if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(doctor): %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
			t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset: an explicit --db target wins over the caller's redirect", got)
		}
	})

	t.Run(".env selector routes to a foreign target", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		callerDir, callerBeadsDir := newRedirectedCallerForTest(t)
		targetBeadsDir := newServerWorkspaceForTest(t, filepath.Join(t.TempDir(), "target"), "target_db")
		envFile := filepath.Join(beads.FollowRedirect(callerBeadsDir), ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_DIR="+targetBeadsDir+"\n"), 0o600); err != nil {
			t.Fatalf("write .env selector: %v", err)
		}
		t.Chdir(callerDir)

		info := runContextInfoForTest(t)
		if !sameDirPath(info.BeadsDir, targetBeadsDir) || info.Database != "target_db" {
			t.Fatalf("context = %q (%q), want %q (target_db)", info.BeadsDir, info.Database, targetBeadsDir)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
			t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset for a foreign .env selection", got)
		}
	})

	t.Run("nested direct workspace under an ancestor redirect", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		callerDir, _ := newRedirectedCallerForTest(t)
		nestedDir := filepath.Join(callerDir, "nested")
		nestedBeadsDir := newServerWorkspaceForTest(t, nestedDir, "nested_db")
		t.Chdir(callerDir)
		changeDir = nestedDir

		info := runContextInfoForTest(t)
		if !sameDirPath(info.BeadsDir, nestedBeadsDir) || info.Database != "nested_db" {
			t.Fatalf("context = %q (%q), want %q (nested_db)", info.BeadsDir, info.Database, nestedBeadsDir)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
			t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset: the ancestor repo's redirect is not the nested workspace's", got)
		}
	})

	t.Run("retained source does not survive into the next command", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		callerDir, _ := newRedirectedCallerForTest(t)
		targetDir := filepath.Join(t.TempDir(), "target")
		newServerWorkspaceForTest(t, targetDir, "target_db")
		t.Chdir(callerDir)

		if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(doctor), first command: %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "caller_db" {
			t.Fatalf("first command preserved %q, want caller_db", got)
		}

		// A fresh process starts with the preserved variable unset.
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		changeDir = targetDir
		if got := runContextJSONForTest(t); got != "target_db" {
			t.Fatalf("second command context database = %q, want target_db", got)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
			t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want unset: the first command's source leaked into the second", got)
		}
	})

	t.Run("BEADS_DIR naming a redirecting .beads from outside any repository", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		_, callerBeadsDir := newRedirectedCallerForTest(t)
		t.Chdir(t.TempDir())
		t.Setenv("BEADS_DIR", callerBeadsDir)

		got := runWhereJSONForTest(t)
		if !sameDirPath(got.Path, beads.FollowRedirect(callerBeadsDir)) || !sameDirPath(got.RedirectedFrom, callerBeadsDir) {
			t.Fatalf("where = %q via %q, want %q via %q", got.Path, got.RedirectedFrom, beads.FollowRedirect(callerBeadsDir), callerBeadsDir)
		}
	})

	t.Run("BEADS_DIR naming the redirect target keeps the caller's provenance", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		callerDir, callerBeadsDir := newRedirectedCallerForTest(t)
		sharedBeadsDir := beads.FollowRedirect(callerBeadsDir)
		t.Chdir(callerDir)
		t.Setenv("BEADS_DIR", sharedBeadsDir)

		got := runWhereJSONForTest(t)
		if !sameDirPath(got.Path, sharedBeadsDir) || !sameDirPath(got.RedirectedFrom, callerBeadsDir) {
			t.Fatalf("where = %q via %q, want %q via %q", got.Path, got.RedirectedFrom, sharedBeadsDir, callerBeadsDir)
		}
	})

	t.Run("plain discovery passes over a redirect at the temp root", func(t *testing.T) {
		selectedWorkspaceEnvForTest(t)
		tempRoot := filepath.Join(t.TempDir(), "tmp")
		writeRedirectForTest(t, filepath.Join(tempRoot, ".beads"), filepath.Join(t.TempDir(), ".beads"))
		child := mkdirForTest(t, filepath.Join(tempRoot, "fixture"))
		t.Setenv("TMPDIR", tempRoot)
		// With no workspace discovered, the no-store guard also considers
		// the user-global ~/.beads, which belongs to whoever runs the test.
		t.Setenv("HOME", t.TempDir())
		t.Chdir(child)

		if err := rootCmd.PersistentPreRunE(whereCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		jsonOutput = true
		out, err := captureStdoutWithError(t, func() error { return whereCmd.RunE(whereCmd, nil) })
		var exitErr *exitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("where RunE = %v, want the silent no-workspace exit", err)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil || payload.Error != "no_beads_directory" {
			t.Fatalf("where --json = %q (%v), want the no_beads_directory error", out, err)
		}
	})
}
