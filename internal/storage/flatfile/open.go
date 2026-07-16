package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// IsFlatFileBackend checks metadata.json in beadsDir for backend: "flatfile".
func IsFlatFileBackend(beadsDir string) bool {
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return false
	}
	var meta struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Backend == "flatfile"
}

// OpenStore creates a FlatFileStore from a beads directory.
// Returns the store as storage.DoltStorage for use with HookFiringStore.
func OpenStore(_ context.Context, beadsDir string) (storage.DoltStorage, error) {
	abs, err := verifyFlatfileMetadata(beadsDir)
	if err != nil {
		return nil, err
	}
	return NewFlatFileStore(abs)
}

// OpenStoreReadOnly opens a FlatFileStore without creating any directories.
// Read-only cross-repo opens must not mutate foreign projects (GH#3231):
// NewFlatFileStore's MkdirAll would create issues/, comments/, memories/,
// events/ inside the foreign checkout (a fresh clone lacks the empty ones —
// git does not track empty dirs) and fails outright on a read-only
// filesystem. Read paths tolerate the missing subdirectories.
func OpenStoreReadOnly(_ context.Context, beadsDir string) (storage.DoltStorage, error) {
	abs, err := verifyFlatfileMetadata(beadsDir)
	if err != nil {
		return nil, err
	}
	return newFlatFileStoreNoCreate(abs)
}

// verifyFlatfileMetadata resolves beadsDir and verifies metadata.json
// declares the flatfile backend, returning the absolute beads dir.
func verifyFlatfileMetadata(beadsDir string) (string, error) {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		return "", fmt.Errorf("flatfile: resolve path: %w", err)
	}

	metaPath := filepath.Join(abs, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("flatfile: metadata.json not found in %s", abs)
		}
		return "", fmt.Errorf("flatfile: read metadata: %w", err)
	}
	var meta struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("flatfile: parse metadata: %w", err)
	}
	if meta.Backend != "flatfile" {
		return "", fmt.Errorf("flatfile: metadata.json backend is %q, not \"flatfile\"", meta.Backend)
	}
	return abs, nil
}

// InGitRepo reports whether the directory containing beadsDir is inside a
// git work tree. The flat-file backend syncs through git; without a repo,
// issues exist only on the local machine. Returns false when git is not
// installed.
func InGitRepo(beadsDir string) bool {
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = filepath.Dir(beadsDir)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// CheckGitignored returns a non-empty warning message if the .beads/issues/
// directory would be ignored by git. The flat-file backend requires git
// tracking for sync — if issues are gitignored, push/pull won't work.
// Callers must establish that a git repo exists (InGitRepo) before treating
// an empty return as "tracked": 'git check-ignore' exits non-zero both for
// "not ignored" (exit 1) and "not a repo" (exit 128), and this function
// reports "" for both.
func CheckGitignored(beadsDir string) string {
	issuesDir := filepath.Join(beadsDir, "issues")
	repoRoot := filepath.Dir(beadsDir)

	// Use git check-ignore to test whether the issues dir is ignored.
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", issuesDir)
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		// Exit code 1 = not ignored (good). Other errors = no git repo.
		return ""
	}

	// The issues dir IS ignored. Figure out why.
	cmd2 := exec.CommandContext(ctx, "git", "check-ignore", "-v", issuesDir)
	cmd2.Dir = repoRoot
	out, _ := cmd2.Output()
	source := strings.TrimSpace(string(out))

	return fmt.Sprintf(
		"WARNING: .beads/issues/ is gitignored — flat-file issues won't be tracked by git.\n"+
			"The flat-file backend uses git for sync (push/pull). Without git tracking,\n"+
			"your issues exist only on this machine.\n\n"+
			"  Ignored by: %s\n\n"+
			"To fix, remove or narrow the gitignore pattern so .beads/issues/ is tracked.\n"+
			"The flat-file .beads/.gitignore already excludes runtime files (*.tmp, local_metadata.json).",
		source,
	)
}
