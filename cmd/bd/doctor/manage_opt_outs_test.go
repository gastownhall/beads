package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hooks.manage and gitignore.manage opt-outs (bd-cxm9r): each doctor
// check that would ask for a hook or .gitignore write reports an OK "not
// managed" result for a repository that sets the key to false, each writer
// leaves the files byte-identical, and a repository that leaves the key unset
// is still managed.

// clearManageEnv unsets the environment overrides of both manage keys.
func clearManageEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BD_HOOKS_MANAGE", "BD_GITIGNORE_MANAGE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// manageRepo is a git repository whose .beads/config.yaml holds configYAML.
func manageRepo(t *testing.T, configYAML string) string {
	t.Helper()
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// treeSnapshot maps every path under each root to its mode and content (or
// "absent"), so a skipped write can be checked to have left them identical.
func treeSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) && path == root {
				snap[root] = "absent"
				return nil
			}
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				snap[path] = "dir " + info.Mode().String()
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap[path] = info.Mode().String() + "\n" + string(data)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", root, err)
		}
	}
	return snap
}

func requireSameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, was := range before {
		if after[path] != was {
			t.Errorf("%s changed:\nbefore: %q\nafter:  %q", path, was, after[path])
		}
	}
	for path, is := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s created: %q", path, is)
		}
	}
}

const (
	hooksOff     = "hooks:\n  manage: false\n"
	gitignoreOff = "gitignore:\n  manage: false\n"
	keysUnset    = "issue-prefix: demo\n"
)

func TestHooksManageFalseDoctorChecksReportNotManaged(t *testing.T) {
	clearManageEnv(t)
	// A legacy inline post-merge hook without the Dolt backend check makes the
	// compatibility check an error, and no hooks dir entry for the others
	// makes Git Hooks a warning, when hooks are managed.
	writeLegacyPostMerge := func(t *testing.T, repo string) {
		t.Helper()
		hooksDir := filepath.Join(repo, ".git", "hooks")
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\n# bd (beads) post-merge hook\nbd sync --import-only\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("not managed", func(t *testing.T) {
		repo := manageRepo(t, hooksOff)
		writeLegacyPostMerge(t, repo)
		runInDir(t, repo, func() {
			for _, check := range []DoctorCheck{CheckGitHooksForRepo(repo, "0.49.6"), CheckGitHooksDoltCompatibility(repo)} {
				if check.Status != StatusOK || !strings.Contains(check.Message, "hooks.manage=false") {
					t.Errorf("%s: got %s %q, want ok and not managed", check.Name, check.Status, check.Message)
				}
				if check.Fix != "" {
					t.Errorf("%s: a not-managed check must offer no fix, got %q", check.Name, check.Fix)
				}
			}
		})
	})

	t.Run("key unset still managed", func(t *testing.T) {
		repo := manageRepo(t, keysUnset)
		writeLegacyPostMerge(t, repo)
		runInDir(t, repo, func() {
			if check := CheckGitHooksForRepo(repo, "0.49.6"); check.Status != StatusWarning {
				t.Errorf("Git Hooks: got %s %q, want a warning for missing hooks", check.Status, check.Message)
			}
			if check := CheckGitHooksDoltCompatibility(repo); check.Status != StatusError {
				t.Errorf("Dolt compatibility: got %s %q, want an error for the legacy hook", check.Status, check.Message)
			}
		})
	})
}

func TestHooksManageFalseReportsNoPendingHookMigration(t *testing.T) {
	clearManageEnv(t)
	legacyShim := "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n"
	pendingHooks := func(t *testing.T, configYAML string) bool {
		t.Helper()
		repo := manageRepo(t, configYAML)
		hooksDir := filepath.Join(repo, ".git", "hooks")
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(legacyShim), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit.old"), []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, m := range DetectPendingMigrations(repo) {
			if m.Name == "hooks" {
				return true
			}
		}
		return false
	}
	if !pendingHooks(t, keysUnset) {
		t.Fatal("with hooks.manage unset a legacy shim must still be a pending hook migration")
	}
	if pendingHooks(t, hooksOff) {
		t.Error("hooks.manage: false must not report a pending hook migration")
	}
}

func TestGitignoreManageFalseLeavesBeadsGitignoreAlone(t *testing.T) {
	clearManageEnv(t)
	const own = "# ours\n*.tmp\n"

	repo := manageRepo(t, gitignoreOff)
	beadsDir := filepath.Join(repo, ".beads")
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	if check := CheckGitignore(repo); check.Status != StatusOK || !strings.Contains(check.Message, "gitignore.manage=false") {
		t.Errorf("missing file: got %s %q, want ok and not managed", check.Status, check.Message)
	}
	before := treeSnapshot(t, gitignorePath)
	if err := EnsureGitignoreForBeadsDir(beadsDir); err != nil {
		t.Fatalf("EnsureGitignoreForBeadsDir: %v", err)
	}
	if err := FixGitignore(repo); err != nil {
		t.Fatalf("FixGitignore: %v", err)
	}
	requireSameTree(t, before, treeSnapshot(t, gitignorePath))

	// An existing file missing every required pattern, with loose permissions,
	// keeps its bytes and its mode.
	if err := os.WriteFile(gitignorePath, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gitignorePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if check := CheckGitignore(repo); check.Status != StatusOK {
		t.Errorf("outdated file: got %s %q, want ok", check.Status, check.Message)
	}
	before = treeSnapshot(t, gitignorePath)
	if err := FixGitignore(repo); err != nil {
		t.Fatalf("FixGitignore: %v", err)
	}
	requireSameTree(t, before, treeSnapshot(t, gitignorePath))

	managed := manageRepo(t, keysUnset)
	if check := CheckGitignore(managed); check.Status != StatusWarning {
		t.Errorf("key unset: got %s %q, want a warning for the missing file", check.Status, check.Message)
	}
	if err := FixGitignore(managed); err != nil {
		t.Fatalf("FixGitignore: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(managed, ".beads", ".gitignore")); err != nil || string(got) != GitignoreTemplate {
		t.Errorf("key unset: FixGitignore must write the template (err %v)", err)
	}
}

func TestGitignoreManageFalseLeavesProjectGitignoreAlone(t *testing.T) {
	clearManageEnv(t)
	const own = "node_modules/\n"

	repo := manageRepo(t, gitignoreOff)
	projectGitignore := filepath.Join(repo, ".gitignore")
	if check := CheckProjectGitignore(repo); check.Status != StatusOK || !strings.Contains(check.Message, "gitignore.manage=false") {
		t.Errorf("missing file: got %s %q, want ok and not managed", check.Status, check.Message)
	}
	before := treeSnapshot(t, projectGitignore)
	if err := EnsureProjectGitignore(repo); err != nil {
		t.Fatalf("EnsureProjectGitignore: %v", err)
	}
	requireSameTree(t, before, treeSnapshot(t, projectGitignore))

	if err := os.WriteFile(projectGitignore, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	before = treeSnapshot(t, projectGitignore)
	if err := FixProjectGitignore(repo); err != nil {
		t.Fatalf("FixProjectGitignore: %v", err)
	}
	requireSameTree(t, before, treeSnapshot(t, projectGitignore))

	managed := manageRepo(t, keysUnset)
	if check := CheckProjectGitignore(managed); check.Status != StatusWarning {
		t.Errorf("key unset: got %s %q, want a warning", check.Status, check.Message)
	}
	if err := EnsureProjectGitignore(managed); err != nil {
		t.Fatalf("EnsureProjectGitignore: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(managed, ".gitignore")); err != nil || !strings.Contains(string(got), ProjectGitignoreHeader) {
		t.Errorf("key unset: EnsureProjectGitignore must add bd's section (err %v):\n%s", err, got)
	}
}
