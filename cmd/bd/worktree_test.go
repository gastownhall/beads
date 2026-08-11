package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTruncateForBox(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{
			name:   "short path no truncate",
			path:   "/home/user",
			maxLen: 20,
			want:   "/home/user",
		},
		{
			name:   "exact length",
			path:   "12345",
			maxLen: 5,
			want:   "12345",
		},
		{
			name:   "needs truncate",
			path:   "/very/long/path/to/somewhere/deep",
			maxLen: 15,
			want:   "...mewhere/deep",
		},
		{
			name:   "truncate to minimum",
			path:   "abcdefghij",
			maxLen: 5,
			want:   "...ij",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForBox(tt.path, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForBox(%q, %d) = %q, want %q", tt.path, tt.maxLen, got, tt.want)
			}
			if len(got) > tt.maxLen {
				t.Errorf("truncateForBox(%q, %d) returned %q with length %d > maxLen %d",
					tt.path, tt.maxLen, got, len(got), tt.maxLen)
			}
		})
	}
}

func TestGitRevParse(t *testing.T) {
	// Basic test - should either return a value or empty string (if not in git repo)
	result := gitRevParse("--git-dir")
	// Just verify it doesn't panic and returns a string
	if result != "" {
		// In a git repo
		t.Logf("Git dir: %s", result)
	} else {
		// Not in a git repo or error
		t.Logf("Not in git repo or error")
	}
}

func TestGitCmdInDirSuppressesHooksViaGitConfig(t *testing.T) {
	t.Setenv("GIT_DIR", "/wrong")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	cmd := gitCmdInDir(t.Context(), "/tmp/example", "status", "--porcelain")
	if cmd.Dir != "/tmp/example" {
		t.Fatalf("cmd.Dir = %q, want /tmp/example", cmd.Dir)
	}
	wantArgs := []string{"-c", "core.hooksPath=", "status", "--porcelain"}
	if len(cmd.Args) != len(wantArgs)+1 {
		t.Fatalf("cmd.Args = %#v, want git plus %#v", cmd.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if got := cmd.Args[i+1]; got != want {
			t.Fatalf("cmd.Args[%d] = %q, want %q; full args: %#v", i+1, got, want, cmd.Args)
		}
	}
	hasTemplateSuppression := false
	hasOptionalLocks := false
	for _, env := range cmd.Env {
		if env == "GIT_HOOKS_PATH=" {
			t.Fatal("cmd.Env contains dead GIT_HOOKS_PATH hook suppression")
		}
		if env == "GIT_TEMPLATE_DIR=" {
			hasTemplateSuppression = true
		}
		if env == "GIT_OPTIONAL_LOCKS=1" {
			hasOptionalLocks = true
		}
		if isWorktreeGitRoutingEnvKeyForOS(worktreeGitEnvKey(env), runtime.GOOS) && env != "GIT_TEMPLATE_DIR=" {
			t.Fatalf("cmd.Env retains Git routing state: %q", env)
		}
	}
	if !hasTemplateSuppression {
		t.Fatal("cmd.Env missing GIT_TEMPLATE_DIR suppression")
	}
	if !hasOptionalLocks {
		t.Fatal("cmd.Env did not preserve GIT_OPTIONAL_LOCKS")
	}
}

func TestClearWorktreeGitRoutingEnvPreservesNonRoutingVariables(t *testing.T) {
	// ClearRouting is deliberately process-lifetime state for the real CLI.
	// Register restorations for every routing key inherited by the test process
	// before exercising that boundary in-process.
	for _, entry := range os.Environ() {
		key := worktreeGitEnvKey(entry)
		if isWorktreeGitRoutingEnvKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, os.Getenv(key))
		}
	}
	t.Setenv("GIT_DIR", "/wrong")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "1")
	t.Setenv("BD_WORKTREE_ENV_SENTINEL", "keep")

	if err := clearWorktreeGitRoutingEnv(worktreeListCmd); err != nil {
		t.Fatalf("clearWorktreeGitRoutingEnv() error = %v", err)
	}
	for _, key := range []string{"GIT_DIR", "GIT_CONFIG_COUNT"} {
		if value, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remains set to %q", key, value)
		}
	}
	for key, want := range map[string]string{
		"GIT_OPTIONAL_LOCKS":       "1",
		"GIT_NO_REPLACE_OBJECTS":   "1",
		"BD_WORKTREE_ENV_SENTINEL": "keep",
	} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestWorktreeCommandsIgnoreInheritedGitRoutingState(t *testing.T) {
	bdBinary := buildBDForInitTests(t)

	decoyRepo := newGitRepo(t)
	commitTestFile(t, decoyRepo, "README.md", "decoy\n", "decoy commit")
	decoyBeadsDir := filepath.Join(decoyRepo, ".beads")
	writeTestConfigYAML(t, decoyBeadsDir, "issue-prefix: decoy\n")
	if err := os.WriteFile(
		filepath.Join(decoyBeadsDir, ".env"),
		[]byte("BEADS_DIR="+filepath.ToSlash(decoyBeadsDir)+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write decoy selector environment: %v", err)
	}
	// An unsanitized post-create status check would see this file and reject an
	// otherwise clean target worktree.
	if err := os.WriteFile(filepath.Join(decoyRepo, "decoy-dirty.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}

	poisonConfig := filepath.Join(t.TempDir(), "poison.gitconfig")
	if err := os.WriteFile(poisonConfig, []byte("[core]\n\tworktree = "+filepath.ToSlash(decoyRepo)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	poisonEnv := []string{
		"GIT_DIR=" + filepath.Join(decoyRepo, ".git"),
		"GIT_WORK_TREE=" + decoyRepo,
		"GIT_COMMON_DIR=" + filepath.Join(decoyRepo, ".git"),
		"GIT_INDEX_FILE=" + filepath.Join(decoyRepo, ".git", "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(decoyRepo, ".git", "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(decoyRepo, ".git", "objects"),
		"GIT_EXEC_PATH=" + filepath.Join(decoyRepo, "missing-git-exec"),
		"GIT_NAMESPACE=poison",
		"GIT_CONFIG_GLOBAL=" + poisonConfig,
		"GIT_CONFIG_SYSTEM=" + poisonConfig,
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_CONFIG_VALUE_0=" + decoyRepo,
		"GIT_OPTIONAL_LOCKS=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"BD_DB=",
	}

	t.Run("create list and remove stay in the target repository", func(t *testing.T) {
		targetRepo := newGitRepo(t)
		commitTestFile(t, targetRepo, "README.md", "target\n", "target commit")
		writeTestConfigYAML(t, filepath.Join(targetRepo, ".beads"), "issue-prefix: target\n")
		decoyRegistryBefore := gitWorktreeRegistry(t, decoyRepo)

		create := runWorktreeCommandProcess(t, bdBinary, targetRepo, "create", poisonEnv, "lane")
		create.requireSuccess(t)
		var created struct {
			Path   string `json:"path"`
			Branch string `json:"branch"`
		}
		decodeWorktreeCommandJSON(t, create.stdout, &created)
		lane := filepath.Join(targetRepo, "lane")
		if !sameWorktreePath(created.Path, lane) || created.Branch != "lane" {
			t.Fatalf("create result = %#v, want target lane %q", created, lane)
		}
		assertGitWorktreePaths(t, targetRepo, targetRepo, lane)
		if got := gitWorktreeRegistry(t, decoyRepo); got != decoyRegistryBefore {
			t.Fatalf("create mutated decoy worktree registry:\n--- before ---\n%s\n--- after ---\n%s", decoyRegistryBefore, got)
		}

		list := runWorktreeCommandProcess(t, bdBinary, targetRepo, "list", poisonEnv)
		list.requireSuccess(t)
		var listed []WorktreeInfo
		decodeWorktreeCommandJSON(t, list.stdout, &listed)
		if len(listed) != 2 ||
			!sameWorktreePath(listed[0].Path, targetRepo) || listed[0].BeadsState != "shared" ||
			!sameWorktreePath(listed[1].Path, lane) || listed[1].BeadsState != "none" {
			t.Fatalf("list resolved outside target repository or beads state: %#v", listed)
		}

		info := runWorktreeCommandProcess(t, bdBinary, lane, "info", poisonEnv)
		info.requireSuccess(t)
		var details struct {
			IsWorktree      bool   `json:"is_worktree"`
			Path            string `json:"path"`
			MainRepo        string `json:"main_repo"`
			Branch          string `json:"branch"`
			BeadsRedirected bool   `json:"beads_redirected"`
		}
		decodeWorktreeCommandJSON(t, info.stdout, &details)
		if !details.IsWorktree ||
			!sameWorktreePath(details.Path, lane) ||
			!sameWorktreePath(details.MainRepo, targetRepo) ||
			details.Branch != "lane" ||
			details.BeadsRedirected {
			t.Fatalf("info resolved outside the target worktree: %#v", details)
		}

		remove := runWorktreeCommandProcess(t, bdBinary, targetRepo, "remove", poisonEnv, lane, "--force")
		remove.requireSuccess(t)
		if _, err := os.Stat(lane); !os.IsNotExist(err) {
			t.Fatalf("remove left target worktree behind: %v", err)
		}
		assertGitWorktreePaths(t, targetRepo, targetRepo)
		if _, err := os.Stat(filepath.Join(decoyRepo, "decoy-dirty.txt")); err != nil {
			t.Fatalf("remove mutated decoy repository: %v", err)
		}
	})

	t.Run("create refuses a target without a beads workspace", func(t *testing.T) {
		targetRepo := newGitRepo(t)
		commitTestFile(t, targetRepo, "README.md", "target without beads\n", "target commit")
		lane := filepath.Join(targetRepo, "lane")
		registryBefore := gitWorktreeRegistry(t, targetRepo)

		create := runWorktreeCommandProcess(t, bdBinary, targetRepo, "create", poisonEnv, "lane")
		if create.exitCode == 0 {
			t.Fatalf("create accepted the decoy beads workspace\nstdout:\n%s\nstderr:\n%s", create.stdout, create.stderr)
		}
		if _, err := os.Stat(lane); !os.IsNotExist(err) {
			t.Fatalf("refused create mutated target path: %v", err)
		}
		if got := gitWorktreeRegistry(t, targetRepo); got != registryBefore {
			t.Fatalf("refused create mutated target worktree registry:\n--- before ---\n%s\n--- after ---\n%s", registryBefore, got)
		}
		if _, err := os.Stat(filepath.Join(targetRepo, ".gitignore")); !os.IsNotExist(err) {
			t.Fatalf("refused create mutated target gitignore: %v", err)
		}
	})

	t.Run("startup ignores malformed config selected by linked-worktree routing", func(t *testing.T) {
		targetRepo := newGitRepo(t)
		commitTestFile(t, targetRepo, "README.md", "target\n", "target commit")

		decoyMain := newGitRepo(t)
		commitTestFile(t, decoyMain, "README.md", "linked decoy\n", "decoy commit")
		writeTestConfigYAML(t, filepath.Join(decoyMain, ".beads"), "json: [\n")
		decoyLane := filepath.Join(t.TempDir(), "linked-decoy")
		command := exec.Command("git", "worktree", "add", "-b", "linked-decoy", decoyLane)
		command.Dir = decoyMain
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create decoy linked worktree: %v\n%s", err, output)
		}
		decoyAdminDir := gitRevParsePath(t, decoyLane, "--absolute-git-dir")
		decoyCommonDir := gitRevParsePath(t, decoyLane, "--git-common-dir")

		linkedPoisonEnv := append(append([]string(nil), poisonEnv...),
			"GIT_DIR="+decoyAdminDir,
			"GIT_WORK_TREE="+decoyLane,
			"GIT_COMMON_DIR="+decoyCommonDir,
			"GIT_INDEX_FILE="+filepath.Join(decoyAdminDir, "index"),
			"GIT_OBJECT_DIRECTORY="+filepath.Join(decoyCommonDir, "objects"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(decoyCommonDir, "objects"),
			"BEADS_TEST_IGNORE_REPO_CONFIG=",
		)
		list := runWorktreeCommandProcess(t, bdBinary, targetRepo, "list", linkedPoisonEnv)
		list.requireSuccess(t)
		if strings.Contains(strings.ToLower(list.stderr), "failed to initialize config") {
			t.Fatalf("malformed decoy config reached startup initialization:\n%s", list.stderr)
		}
		var listed []WorktreeInfo
		decodeWorktreeCommandJSON(t, list.stdout, &listed)
		if len(listed) != 1 || !sameWorktreePath(listed[0].Path, targetRepo) {
			t.Fatalf("list resolved outside target repository: %#v", listed)
		}
	})
}

func runWorktreeCommandProcess(t *testing.T, bdBinary, dir, action string, extraEnv []string, args ...string) worktreeRemoveProcessResult {
	t.Helper()
	commandArgs := append([]string{"--json", "worktree", action}, args...)
	command := exec.Command(bdBinary, commandArgs...)
	command.Dir = dir
	command.Env = overrideWorktreeRemoveEnv(os.Environ(), append([]string{
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_TEST_MODE=1",
	}, extraEnv...))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("launch worktree command helper: %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	return worktreeRemoveProcessResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func decodeWorktreeCommandJSON(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.NewDecoder(strings.NewReader(output)).Decode(target); err != nil {
		t.Fatalf("decode helper JSON: %v\noutput:\n%s", err, output)
	}
}

func gitWorktreeRegistry(t *testing.T, repoDir string) string {
	t.Helper()
	command := exec.Command("git", "worktree", "list", "--porcelain")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list Git worktree registry for %q: %v\n%s", repoDir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertGitWorktreePaths(t *testing.T, repoDir string, want ...string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(gitWorktreeRegistry(t, repoDir), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			got = append(got, filepath.FromSlash(path))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Git worktree paths = %#v, want %#v", got, want)
	}
	matched := make([]bool, len(got))
	for _, wantPath := range want {
		found := false
		for index, gotPath := range got {
			if !matched[index] && sameWorktreePath(gotPath, wantPath) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Git worktree paths = %#v, missing %q", got, wantPath)
		}
	}
}

func gitRevParsePath(t *testing.T, repoDir, selector string) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", selector)
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s in %q: %v\n%s", selector, repoDir, err, output)
	}
	path := filepath.FromSlash(strings.TrimSpace(string(output)))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	return filepath.Clean(path)
}

// TestResolveWorktreePathByName verifies that resolveWorktreePath can find
// worktrees by name (basename) when they're in subdirectories like .worktrees/
func TestResolveWorktreePathByName(t *testing.T) {
	// Create a temp directory for the main repo
	mainDir := newGitRepo(t)

	// Create initial commit (required for worktrees)
	if err := os.WriteFile(filepath.Join(mainDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create initial commit: %v\n%s", err, output)
	}

	// Create .worktrees subdirectory
	worktreesDir := filepath.Join(mainDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("Failed to create .worktrees dir: %v", err)
	}

	// Create a worktree inside .worktrees/
	worktreePath := filepath.Join(worktreesDir, "test-wt")
	cmd = exec.Command("git", "worktree", "add", "-b", "test-wt", worktreePath)
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create worktree: %v\n%s", err, output)
	}
	defer func() {
		// Cleanup worktree
		cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
		cmd.Dir = mainDir
		_ = cmd.Run()
	}()

	ctx := context.Background()

	t.Run("resolves by name when worktree is in subdirectory", func(t *testing.T) {
		// This should find the worktree by consulting git's registry
		resolved, err := resolveWorktreePath(ctx, mainDir, "test-wt")
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, \"test-wt\") failed: %v", err)
			return
		}
		// Compare resolved paths to handle symlinks (e.g., /var -> /private/var on macOS)
		wantResolved, _ := filepath.EvalSymlinks(worktreePath)
		gotResolved, _ := filepath.EvalSymlinks(resolved)
		if gotResolved != wantResolved {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("resolves by relative path", func(t *testing.T) {
		// This should work via the existing relative-to-repo-root logic
		resolved, err := resolveWorktreePath(ctx, mainDir, ".worktrees/test-wt")
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, \".worktrees/test-wt\") failed: %v", err)
			return
		}
		if resolved != worktreePath {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("resolves by absolute path", func(t *testing.T) {
		resolved, err := resolveWorktreePath(ctx, mainDir, worktreePath)
		if err != nil {
			t.Errorf("resolveWorktreePath(repoRoot, absolutePath) failed: %v", err)
			return
		}
		if resolved != worktreePath {
			t.Errorf("resolveWorktreePath returned %q, want %q", resolved, worktreePath)
		}
	})

	t.Run("returns error for non-existent worktree", func(t *testing.T) {
		_, err := resolveWorktreePath(ctx, mainDir, "non-existent")
		if err == nil {
			t.Error("resolveWorktreePath should return error for non-existent worktree")
		}
	})
}
