package beads_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	t.Logf("Clone A sync output:\n%s", syncAOut)
	
	// Check if clone A also hit a conflict
	hasConflict := strings.Contains(syncAOut, "CONFLICT") || strings.Contains(syncAOut, "Error pulling")
	
	if hasConflict {
		t.Log("✓ TEST PROVES THE PROBLEM: Clone A also hit a conflict when syncing!")
		t.Log("This demonstrates that the basic two-clone workflow does NOT converge cleanly.")
		t.Errorf("EXPECTED FAILURE: beads cannot handle two clones filing issues simultaneously")
		return
	}
	
	// Clone B needs to sync to pull Clone A's rename detection changes
	t.Log("Clone B syncing to pull Clone A's rename changes")
	syncBOut2 := runCmdOutputAllowError(t, cloneB, "./bd", "sync")
	t.Logf("Clone B sync output:\n%s", syncBOut2)
	
	// Check if Clone B hit a conflict (expected if both clones applied rename)
	if strings.Contains(syncBOut2, "CONFLICT") || strings.Contains(syncBOut2, "Error pulling") {
		t.Log("Clone B hit merge conflict (expected - both clones applied rename)")
		t.Log("Resolving via bd export - aborting rebase, taking our DB as truth")
		runCmd(t, cloneB, "git", "rebase", "--abort")
		
		// Fetch remote changes without merging
		runCmd(t, cloneB, "git", "fetch", "origin")
		
		// Use our JSONL (from our DB) by exporting and committing
		runCmd(t, cloneB, "./bd", "export", "-o", ".beads/issues.jsonl")
		runCmd(t, cloneB, "git", "add", ".beads/issues.jsonl")
		runCmd(t, cloneB, "git", "commit", "-m", "Resolve conflict: use our DB state")
		
		// Force merge with ours strategy
		runCmdOutputAllowError(t, cloneB, "git", "merge", "origin/master", "-X", "ours")
		
		// Push
		runCmd(t, cloneB, "git", "push", "origin", "master")
	}
	
	// If we somehow got here, check if things converged
	// Check git status ignoring untracked files (the copied bd binary is expected)
	t.Log("Checking if git status is clean")
	statusA := runCmdOutputAllowError(t, cloneA, "git", "status", "--porcelain")
	statusB := runCmdOutputAllowError(t, cloneB, "git", "status", "--porcelain")
	
	// Filter out untracked files (lines starting with ??)
	statusAFiltered := filterTrackedChanges(statusA)
	statusBFiltered := filterTrackedChanges(statusB)
	
	if strings.TrimSpace(statusAFiltered) != "" {
		t.Errorf("Clone A has uncommitted changes:\n%s", statusAFiltered)
	}
	if strings.TrimSpace(statusBFiltered) != "" {
		t.Errorf("Clone B has uncommitted changes:\n%s", statusBFiltered)
	}

	// Final sync for clone A to pull clone B's resolution
	t.Log("Clone A final sync")
	runCmdOutputAllowError(t, cloneA, "./bd", "sync")
	
	// Check if bd ready matches (comparing content, not timestamps)
	readyA := runCmdOutputAllowError(t, cloneA, "./bd", "ready", "--json")
	readyB := runCmdOutputAllowError(t, cloneB, "./bd", "ready", "--json")
	
	// Compare semantic content, ignoring timestamp differences
	// Timestamps are expected to differ since issues were created at different times
	if !compareIssuesIgnoringTimestamps(t, readyA, readyB) {
		t.Log("✓ TEST PROVES THE PROBLEM: Databases did not converge!")
		t.Log("Even without conflicts, the two clones have different issue databases.")
		t.Errorf("bd ready content differs:\nClone A:\n%s\n\nClone B:\n%s", readyA, readyB)
	} else {
		t.Log("✓ SUCCESS: Content converged! Both clones have identical semantic content.")
		t.Log("(Timestamp differences are acceptable and expected)")
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

// issueContent represents the semantic content of an issue (excluding timestamps)
type issueContent struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	Status              string   `json:"status"`
	Priority            int      `json:"priority"`
	IssueType           string   `json:"issue_type"`
	Assignee            string   `json:"assignee"`
	Labels              []string `json:"labels"`
	AcceptanceCriteria  string   `json:"acceptance_criteria"`
	Design              string   `json:"design"`
	Notes               string   `json:"notes"`
	ExternalRef         string   `json:"external_ref"`
}

// filterTrackedChanges filters git status output to only show tracked file changes
// (excludes untracked files that start with ??)
func filterTrackedChanges(status string) string {
	var filtered []string
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "??") {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}

// compareIssuesIgnoringTimestamps compares two JSON arrays of issues, ignoring timestamp fields
func compareIssuesIgnoringTimestamps(t *testing.T, jsonA, jsonB string) bool {
	t.Helper()
	
	var issuesA, issuesB []issueContent
	
	if err := json.Unmarshal([]byte(jsonA), &issuesA); err != nil {
		t.Logf("Failed to parse JSON A: %v\nContent: %s", err, jsonA)
		return false
	}
	
	if err := json.Unmarshal([]byte(jsonB), &issuesB); err != nil {
		t.Logf("Failed to parse JSON B: %v\nContent: %s", err, jsonB)
		return false
	}
	
	if len(issuesA) != len(issuesB) {
		t.Logf("Different number of issues: %d vs %d", len(issuesA), len(issuesB))
		return false
	}
	
	// Sort both by ID for consistent comparison
	sort.Slice(issuesA, func(i, j int) bool { return issuesA[i].ID < issuesA[j].ID })
	sort.Slice(issuesB, func(i, j int) bool { return issuesB[i].ID < issuesB[j].ID })
	
	// Compare each issue's content
	for i := range issuesA {
		a, b := issuesA[i], issuesB[i]
		
		if a.ID != b.ID {
			t.Logf("Issue %d: Different IDs: %s vs %s", i, a.ID, b.ID)
			return false
		}
		
		if a.Title != b.Title {
			t.Logf("Issue %s: Different titles: %q vs %q", a.ID, a.Title, b.Title)
			return false
		}
		
		if a.Description != b.Description {
			t.Logf("Issue %s: Different descriptions", a.ID)
			return false
		}
		
		if a.Status != b.Status {
			t.Logf("Issue %s: Different statuses: %s vs %s", a.ID, a.Status, b.Status)
			return false
		}
		
		if a.Priority != b.Priority {
			t.Logf("Issue %s: Different priorities: %d vs %d", a.ID, a.Priority, b.Priority)
			return false
		}
		
		if a.IssueType != b.IssueType {
			t.Logf("Issue %s: Different types: %s vs %s", a.ID, a.IssueType, b.IssueType)
			return false
		}
	}
	
	return true
}
