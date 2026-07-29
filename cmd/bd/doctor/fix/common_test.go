package fix

import (
	"os"
	"path/filepath"
	"testing"
)

func resolvePathForTest(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", path, err)
	}

	return resolved
}

func absPathForTest(t *testing.T, path string) string {
	t.Helper()

	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs(%q) failed: %v", path, err)
	}

	return absPath
}

func TestSafeWorkspacePath(t *testing.T) {
	root := t.TempDir()
	absEscape, _ := filepath.Abs(filepath.Join(root, "..", "escape"))

	tests := []struct {
		name    string
		relPath string
		wantErr bool
	}{
		{
			name:    "normal relative path",
			relPath: ".beads/issues.jsonl",
			wantErr: false,
		},
		{
			name:    "nested relative path",
			relPath: filepath.Join(".beads", "nested", "file.txt"),
			wantErr: false,
		},
		{
			name:    "absolute path rejected",
			relPath: absEscape,
			wantErr: true,
		},
		{
			name:    "path traversal rejected",
			relPath: filepath.Join("..", "escape"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeWorkspacePath(root, tt.relPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("safeWorkspacePath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if !isWithinWorkspace(root, got) {
					t.Fatalf("resolved path %q not within workspace %q", got, root)
				}
				if !filepath.IsAbs(got) {
					t.Fatalf("resolved path is not absolute: %q", got)
				}
			}
		})
	}
}

func TestResolvedWorkspaceBeadsDir(t *testing.T) {
	t.Run("local workspace returns local beads dir", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		got, err := resolvedWorkspaceBeadsDir(dir)
		if err != nil {
			t.Fatalf("resolvedWorkspaceBeadsDir() error = %v", err)
		}

		want := resolvePathForTest(t, filepath.Join(dir, ".beads"))
		if got != want {
			t.Fatalf("resolvedWorkspaceBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("shared worktree falls back to main repo beads dir", func(t *testing.T) {
		mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
		mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
		if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
			t.Fatalf("failed to create main .beads dir: %v", err)
		}

		got, err := resolvedWorkspaceBeadsDir(worktreeDir)
		if err != nil {
			t.Fatalf("resolvedWorkspaceBeadsDir() error = %v", err)
		}

		want := resolvePathForTest(t, mainBeadsDir)
		if got != want {
			t.Fatalf("resolvedWorkspaceBeadsDir() = %q, want %q", got, mainBeadsDir)
		}
	})

	t.Run("worktree local beads dir takes precedence over shared fallback", func(t *testing.T) {
		mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
		mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
			t.Fatalf("failed to create main .beads dir: %v", err)
		}
		if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		got, err := resolvedWorkspaceBeadsDir(worktreeDir)
		if err != nil {
			t.Fatalf("resolvedWorkspaceBeadsDir() error = %v", err)
		}

		want := resolvePathForTest(t, worktreeBeadsDir)
		if got != want {
			t.Fatalf("resolvedWorkspaceBeadsDir() = %q, want %q", got, worktreeBeadsDir)
		}
	})
}

func TestLocalWorkspaceBeadsDir(t *testing.T) {
	t.Run("returns local beads path for local workspace", func(t *testing.T) {
		dir := setupTestWorkspace(t)

		got, err := localWorkspaceBeadsDir(dir)
		if err != nil {
			t.Fatalf("localWorkspaceBeadsDir() error = %v", err)
		}

		want := filepath.Join(absPathForTest(t, dir), ".beads")
		if got != want {
			t.Fatalf("localWorkspaceBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("shared worktree still returns worktree local path", func(t *testing.T) {
		_, worktreeDir := setupSharedWorktreeWorkspace(t)

		got, err := localWorkspaceBeadsDir(worktreeDir)
		if err != nil {
			t.Fatalf("localWorkspaceBeadsDir() error = %v", err)
		}

		want := filepath.Join(absPathForTest(t, worktreeDir), ".beads")
		if got != want {
			t.Fatalf("localWorkspaceBeadsDir() = %q, want %q", got, want)
		}
	})
}
