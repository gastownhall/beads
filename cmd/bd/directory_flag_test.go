package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
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
