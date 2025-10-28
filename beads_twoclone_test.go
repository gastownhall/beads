package beads_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTwoCloneCollision demonstrates that beads does NOT work with the basic workflow
// of two independent clones filing issues simultaneously.
func TestTwoCloneCollision(t *testing.T) {
	tmpDir := t.TempDir()

	// Get path to bd binary
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	if _, err := os.Stat(bdPath); err != nil {
		t.Fatalf("bd binary not found at %s - run 'go build -o bd ./cmd/bd' first", bdPath)
	}

	// Create a bare git repo to act as the remote
	remoteDir := filepath.Join(tmpDir, "remote.git")
	runCmd(t, tmpDir, "git", "init", "--bare", remoteDir)

	// Create clone A
	cloneA := filepath.Join(tmpDir, "clone-a")
	runCmd(t, tmpDir, "git", "clone", remoteDir, cloneA)
	
	// Create clone B
	cloneB := filepath.Join(tmpDir, "clone-b")
	runCmd(t, tmpDir, "git", "clone", remoteDir, cloneB)

	// Copy bd binary to both clones
	copyFile(t, bdPath, filepath.Join(cloneA, "bd"))
	copyFile(t, bdPath, filepath.Join(cloneB, "bd"))

	// Initialize beads in clone A
	t.Log("Initializing beads in clone A")
	runCmd(t, cloneA, "./bd", "init", "--quiet", "--prefix", "test")
	
	// Commit the initial .beads directory from clone A
	runCmd(t, cloneA, "git", "add", ".beads")
	runCmd(t, cloneA, "git", "commit", "-m", "Initialize beads")
	runCmd(t, cloneA, "git", "push", "origin", "master")

	// Pull in clone B to get the beads initialization
	t.Log("Pulling beads init to clone B")
	runCmd(t, cloneB, "git", "pull", "origin", "master")
	
	// Initialize database in clone B from JSONL
	t.Log("Initializing database in clone B")
	runCmd(t, cloneB, "./bd", "init", "--quiet", "--prefix", "test")

	// Install git hooks in both clones
	t.Log("Installing git hooks")
	installGitHooks(t, cloneA)
	installGitHooks(t, cloneB)

	// Start daemons in both clones with auto-commit and auto-push
	t.Log("Starting daemons")
	startDaemon(t, cloneA)
	startDaemon(t, cloneB)
	
	// Ensure cleanup happens even if test fails
	t.Cleanup(func() {
		t.Log("Cleaning up daemons")
		stopAllDaemons(t, cloneA)
		stopAllDaemons(t, cloneB)
	})

	// Wait for daemons to be ready (short timeout)
	waitForDaemon(t, cloneA, 1*time.Second)
	waitForDaemon(t, cloneB, 1*time.Second)

	// Clone A creates an issue
	t.Log("Clone A creating issue")
	runCmd(t, cloneA, "./bd", "create", "Issue from clone A", "-t", "task", "-p", "1", "--json")
	
	// Clone B creates an issue (should get same ID since databases are independent)
	t.Log("Clone B creating issue")
	runCmd(t, cloneB, "./bd", "create", "Issue from clone B", "-t", "task", "-p", "1", "--json")

	// Force sync clone A first
	t.Log("Clone A syncing")
	runCmd(t, cloneA, "./bd", "sync")

	// Wait for push to complete by polling git log
	waitForPush(t, cloneA, 2*time.Second)

	// Clone B will conflict when syncing
	t.Log("Clone B syncing (will conflict)")
	syncBOut := runCmdOutputAllowError(t, cloneB, "./bd", "sync")
	if !strings.Contains(syncBOut, "CONFLICT") && !strings.Contains(syncBOut, "Error") {
		t.Log("Expected conflict during clone B sync, but got success. Output:")
		t.Log(syncBOut)
	}
	
	// Clone B needs to abort the rebase and resolve manually
	t.Log("Clone B aborting rebase")
	runCmdAllowError(t, cloneB, "git", "rebase", "--abort")
	
	// Pull with merge instead
	t.Log("Clone B pulling with merge")
	pullOut := runCmdOutputAllowError(t, cloneB, "git", "pull", "--no-rebase", "origin", "master")
	if !strings.Contains(pullOut, "CONFLICT") {
		t.Logf("Pull output: %s", pullOut)
	}
	
	// Check if we have conflict markers in the JSONL
	jsonlPath := filepath.Join(cloneB, ".beads", "issues.jsonl")
	jsonlContent, _ := os.ReadFile(jsonlPath)
	if strings.Contains(string(jsonlContent), "<<<<<<<") {
		t.Log("JSONL has conflict markers - manually resolving")
		// For this test, just take both issues (keep all non-marker lines)
		var cleanLines []string
		for _, line := range strings.Split(string(jsonlContent), "\n") {
			if !strings.HasPrefix(line, "<<<<<<<") && 
			   !strings.HasPrefix(line, "=======") && 
			   !strings.HasPrefix(line, ">>>>>>>") {
				if strings.TrimSpace(line) != "" {
					cleanLines = append(cleanLines, line)
				}
			}
		}
		cleaned := strings.Join(cleanLines, "\n") + "\n"
		if err := os.WriteFile(jsonlPath, []byte(cleaned), 0644); err != nil {
			t.Fatalf("Failed to write cleaned JSONL: %v", err)
		}
		// Mark as resolved
		runCmd(t, cloneB, "git", "add", ".beads/issues.jsonl")
		runCmd(t, cloneB, "git", "commit", "-m", "Resolve merge conflict")
	}

	// Force import with collision resolution in both
	t.Log("Resolving collisions via import")
	runCmd(t, cloneB, "./bd", "import", "-i", ".beads/issues.jsonl", "--resolve-collisions")

	// Push the resolved state from clone B
	t.Log("Clone B pushing resolved state")
	runCmd(t, cloneB, "git", "push", "origin", "master")
	
	// Clone A now tries to sync - will this work?
	t.Log("Clone A syncing after clone B resolved collision")
	syncAOut := runCmdOutputAllowError(t, cloneA, "./bd", "sync")
	
	// Check if clone A also hit a conflict
	hasConflict := strings.Contains(syncAOut, "CONFLICT") || strings.Contains(syncAOut, "Error pulling")
	
	if hasConflict {
		t.Log("✓ TEST PROVES THE PROBLEM: Clone A also hit a conflict when syncing!")
		t.Log("This demonstrates that the basic two-clone workflow does NOT converge cleanly.")
		t.Errorf("EXPECTED FAILURE: beads cannot handle two clones filing issues simultaneously")
		return
	}
	
	// If we somehow got here, check if things converged
	t.Log("Checking if git status is clean")
	statusA := runCmdOutputAllowError(t, cloneA, "git", "status", "--porcelain")
	statusB := runCmdOutputAllowError(t, cloneB, "git", "status", "--porcelain")
	
	if strings.TrimSpace(statusA) != "" {
		t.Errorf("Clone A git status not clean:\n%s", statusA)
	}
	if strings.TrimSpace(statusB) != "" {
		t.Errorf("Clone B git status not clean:\n%s", statusB)
	}

	// Check if bd ready matches
	readyA := runCmdOutputAllowError(t, cloneA, "./bd", "ready", "--json")
	readyB := runCmdOutputAllowError(t, cloneB, "./bd", "ready", "--json")
	
	if readyA != readyB {
		t.Log("✓ TEST PROVES THE PROBLEM: Databases did not converge!")
		t.Log("Even without conflicts, the two clones have different issue databases.")
		t.Errorf("bd ready output differs:\nClone A:\n%s\n\nClone B:\n%s", readyA, readyB)
	} else {
		t.Log("Unexpected success: beads handled two-clone collision properly!")
	}
}

func installGitHooks(t *testing.T, repoDir string) {
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	
	preCommit := `#!/bin/sh
./bd --no-daemon export -o .beads/issues.jsonl >/dev/null 2>&1 || true
git add .beads/issues.jsonl >/dev/null 2>&1 || true
exit 0
`
	
	postMerge := `#!/bin/sh
./bd --no-daemon import -i .beads/issues.jsonl >/dev/null 2>&1 || true
exit 0
`
	
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(preCommit), 0755); err != nil {
		t.Fatalf("Failed to write pre-commit hook: %v", err)
	}
	
	if err := os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(postMerge), 0755); err != nil {
		t.Fatalf("Failed to write post-merge hook: %v", err)
	}
}

func startDaemon(t *testing.T, repoDir string) {
	cmd := exec.Command("./bd", "daemon", "start", "--auto-commit", "--auto-push")
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Logf("Warning: daemon start failed (may already be running): %v", err)
	}
}

func stopAllDaemons(t *testing.T, repoDir string) {
	t.Helper()
	cmd := exec.Command("./bd", "daemons", "killall", "--force")
	cmd.Dir = repoDir
	
	// Run with timeout to avoid hanging
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("Warning: daemon killall failed (may not be running): %v\nOutput: %s", err, string(out))
		}
	}()
	
	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Logf("Warning: daemon killall timed out, continuing")
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}
}

func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Command failed: %s %v\nError: %v", name, args, err)
	}
}

func runCmdOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command output: %s", string(out))
		t.Fatalf("Command failed: %s %v\nError: %v", name, args, err)
	}
	return string(out)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		t.Fatalf("Failed to write %s: %v", dst, err)
	}
}

func runCmdAllowError(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func runCmdOutputAllowError(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func waitForDaemon(t *testing.T, repoDir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Just check if we can list issues - daemon doesn't have to be running
		cmd := exec.Command("./bd", "list", "--json")
		cmd.Dir = repoDir
		_, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Don't fail - test can continue without daemon
	t.Logf("Warning: daemon not ready within %v, continuing anyway", timeout)
}

func waitForPush(t *testing.T, repoDir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastCommit string
	
	// Get initial commit
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err == nil {
		lastCommit = strings.TrimSpace(string(out))
	}
	
	for time.Now().Before(deadline) {
		// First fetch to update remote tracking
		exec.Command("git", "fetch", "origin").Run()
		
		// Check if remote has our commit
		cmd := exec.Command("git", "log", "origin/master", "--oneline", "-1")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err == nil && strings.Contains(string(out), lastCommit) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Don't fail, just warn - push might complete async
	t.Logf("Warning: push not detected within %v", timeout)
}
