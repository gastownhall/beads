package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHuskyMigrationSkipsSymlinks: hook preservation and the husky v9 shim
// rewrite never write through — or materialize — a symlinked destination
// (bd-1tmym4 / #7888).
func TestHuskyMigrationSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	writeHook := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("preservation live link skips", func(t *testing.T) {
		tmp := t.TempDir()
		source := filepath.Join(tmp, "source-hooks")
		target := filepath.Join(tmp, "beads-hooks")
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
		writeHook(t, filepath.Join(source, "pre-commit"), "#!/bin/sh\necho custom\n")

		victim := filepath.Join(tmp, "victim.sh")
		writeHook(t, victim, "#!/bin/sh\noriginal\n")
		if err := os.Symlink(victim, filepath.Join(target, "pre-commit")); err != nil {
			t.Fatal(err)
		}

		preservePreexistingHooksAt(target, source, func() string { return "" })

		got, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "#!/bin/sh\noriginal\n" {
			t.Errorf("preservation wrote through the symlink: %q", got)
		}
	})

	t.Run("preservation dangling link skips", func(t *testing.T) {
		tmp := t.TempDir()
		source := filepath.Join(tmp, "source-hooks")
		target := filepath.Join(tmp, "beads-hooks")
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
		writeHook(t, filepath.Join(source, "pre-commit"), "#!/bin/sh\necho custom\n")

		// A dangling link at the destination: os.Stat reports it absent, so
		// the write path used to create the link's target.
		victim := filepath.Join(tmp, "victim.sh")
		if err := os.Symlink(victim, filepath.Join(target, "pre-commit")); err != nil {
			t.Fatal(err)
		}

		preservePreexistingHooksAt(target, source, func() string { return "" })

		if _, err := os.Lstat(victim); !os.IsNotExist(err) {
			t.Errorf("preservation materialized the dangling link's target: %v", err)
		}
		if fi, err := os.Lstat(filepath.Join(target, "pre-commit")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling link was replaced: %v (%v)", fi, err)
		}
	})

	t.Run("husky v9 rewrite skips symlinked destination", func(t *testing.T) {
		tmp := t.TempDir()
		husky := filepath.Join(tmp, ".husky")
		source := filepath.Join(husky, "_") // husky v9 hooksPath
		target := filepath.Join(tmp, ".beads", "hooks")
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
		// v9 dispatcher + shim in the source dir, real user hook in .husky/.
		writeHook(t, filepath.Join(source, "h"), `#!/usr/bin/env sh
[ -f "$HOME/.huskyrc" ] && . "$HOME/.huskyrc"
"$(dirname "$(dirname "$0")")/h" "$@"
`)
		writeHook(t, filepath.Join(source, "pre-commit"), ". \"$(dirname \"$0\")/h\"\n")
		writeHook(t, filepath.Join(husky, "pre-commit"), "npm run lint\n")

		victim := filepath.Join(tmp, "victim.sh")
		writeHook(t, victim, "#!/bin/sh\noriginal\n")
		if err := os.Symlink(victim, filepath.Join(target, "pre-commit")); err != nil {
			t.Fatal(err)
		}

		fixHuskyHookLayout(source, target)

		got, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "#!/bin/sh\noriginal\n" {
			t.Errorf("husky v9 rewrite wrote through the symlink: %q", got)
		}
		if fi, lerr := os.Lstat(filepath.Join(target, "pre-commit")); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlinked destination was replaced: %v (%v)", fi, lerr)
		}
	})
}

// TestHookInstallRefusesSymlinkBeforePreservation: a symlink at a managed
// hook destination refuses the whole install before any preservation copy
// runs — the guard runs first, so nothing else is written either
// (bd-1tmym4 / #7888).
func TestHookInstallRefusesSymlinkBeforePreservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	repo := newGitRepo(t)
	runInDir(t, repo, func() {
		// A foreign hook in the source hooks dir: preservation would copy it
		// into the target on a shared install.
		sourceHooks := filepath.Join(repo, ".git", "hooks")
		if err := os.MkdirAll(sourceHooks, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceHooks, "commit-msg"), []byte("#!/bin/sh\necho user hook\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		// A symlink at the managed hook destination: the install must refuse.
		victim := filepath.Join(repo, "victim.sh")
		if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(repo, ".beads-hooks")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(target, "pre-commit")); err != nil {
			t.Fatal(err)
		}

		err := installHooksAt([]string{"pre-commit"}, false, true /*shared*/, false, false, nil)
		if err == nil {
			t.Fatal("installHooksAt accepted a symlinked managed hook destination")
		}
		if !strings.Contains(err.Error(), "refusing to install") {
			t.Errorf("refusal does not read as a managed-hook refusal: %v", err)
		}
		if got, rerr := os.ReadFile(victim); rerr != nil || string(got) != "original\n" {
			t.Errorf("victim was modified: %q (%v)", got, rerr)
		}
		// The guard must run before preservation: nothing may have been
		// copied into the target on the way to the refusal.
		if _, serr := os.Lstat(filepath.Join(target, "commit-msg")); !os.IsNotExist(serr) {
			t.Errorf("preservation ran before the managed-hook guard refused")
		}
	})
}