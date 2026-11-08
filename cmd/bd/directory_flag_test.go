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
	if err := rootCmd.PersistentPreRunE(contextCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE(context): %v", err)
	}
	jsonOutput = true
	out := captureStdout(t, func() error { return contextCmd.RunE(contextCmd, nil) })
	var got struct {
		Database string `json:"database"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("context --json output %q: %v", out, err)
	}
	return got.Database
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
