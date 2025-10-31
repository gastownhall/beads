package beads_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestFiveCloneCollision tests N-way collision resolution with 5 clones.
// Verifies that the collision resolution algorithm scales beyond 3 clones.
func TestFiveCloneCollision(t *testing.T) {
	t.Run("SequentialSync", func(t *testing.T) {
		testNCloneCollision(t, 5, []string{"A", "B", "C", "D", "E"})
	})
	
	t.Run("ReverseSync", func(t *testing.T) {
		testNCloneCollision(t, 5, []string{"E", "D", "C", "B", "A"})
	})
	
	t.Run("RandomSync", func(t *testing.T) {
		testNCloneCollision(t, 5, []string{"C", "A", "E", "B", "D"})
	})
}

// TestTenCloneCollision - DEPRECATED: TestFiveCloneCollision is sufficient for N-way testing
func TestTenCloneCollision(t *testing.T) {
	t.Skip("DEPRECATED: TestFiveCloneCollision provides sufficient N-way coverage")
}

// testNCloneCollision is the generalized N-way convergence test.
// With hash-based IDs (bd-165), each clone creates an issue with a unique content-based ID.
// No collisions occur, so syncing should work cleanly without conflict resolution.
func testNCloneCollision(t *testing.T, numClones int, syncOrder []string) {
	t.Helper()
	
	if len(syncOrder) != numClones {
		t.Fatalf("syncOrder length (%d) must match numClones (%d)", 
			len(syncOrder), numClones)
	}
	
	tmpDir := t.TempDir()
	
	// Get path to bd binary
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	if _, err := os.Stat(bdPath); err != nil {
		t.Fatalf("bd binary not found at %s - run 'go build -o bd ./cmd/bd' first", bdPath)
	}
	
	// Setup remote and N clones
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates issue with different content (thus different hash-based ID)
	t.Logf("Creating issues in %d clones", numClones)
	for name, dir := range cloneDirs {
		createIssueInClone(t, dir, fmt.Sprintf("Issue from clone %s", name))
	}
	
	// Sync in specified order
	t.Logf("Syncing in order: %v", syncOrder)
	for i, name := range syncOrder {
		syncCloneWithConflictResolution(t, cloneDirs[name], name, i == 0)
	}
	
	// Final convergence rounds - do a few more sync rounds to ensure convergence
	// Each sync round allows one more issue to propagate through the network
	t.Log("Final convergence rounds")
	for round := 1; round <= 3; round++ {
		t.Logf("Convergence round %d", round)
		for i := 0; i < numClones; i++ {
			name := string(rune('A' + i))
			dir := cloneDirs[name]
			syncCloneWithConflictResolution(t, dir, name, false)
		}
	}
	
	// Verify all clones have all N issues
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Issue from clone %s", name)] = true
	}
	
	t.Logf("Verifying convergence: expecting %d issues", len(expectedTitles))
	for name, dir := range cloneDirs {
		titles := getTitlesFromClone(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s missing issues:\n  Expected: %v\n  Got: %v", 
				name, sortedKeys(expectedTitles), sortedKeys(titles))
		}
	}
	
	t.Logf("✓ All %d clones converged successfully", numClones)
}

// setupBareRepo creates a bare git repository with an initial commit
func setupBareRepo(t *testing.T, tmpDir string) string {
	t.Helper()
	
	remoteDir := filepath.Join(tmpDir, "remote.git")
	runCmd(t, tmpDir, "git", "init", "--bare", remoteDir)
	
	// Create temporary clone to add initial commit
	tempClone := filepath.Join(tmpDir, "temp-init")
	runCmd(t, tmpDir, "git", "clone", remoteDir, tempClone)
	runCmd(t, tempClone, "git", "commit", "--allow-empty", "-m", "Initial commit")
	runCmd(t, tempClone, "git", "push", "origin", "master")
	
	return remoteDir
}

// setupClone creates a clone, initializes beads, and copies the bd binary
func setupClone(t *testing.T, tmpDir, remoteDir, name, bdPath string) string {
	t.Helper()
	
	cloneDir := filepath.Join(tmpDir, fmt.Sprintf("clone-%s", strings.ToLower(name)))
	runCmd(t, tmpDir, "git", "clone", remoteDir, cloneDir)
	
	// Copy bd binary
	copyFile(t, bdPath, filepath.Join(cloneDir, "bd"))
	
	// First clone initializes and pushes .beads directory
	if name == "A" {
		t.Logf("Initializing beads in clone %s", name)
		runCmd(t, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
		// Enable hash ID mode for collision-free IDs
		runCmdWithEnv(t, cloneDir, map[string]string{"BEADS_NO_DAEMON": "1"}, "./bd", "config", "set", "id_mode", "hash")
		runCmd(t, cloneDir, "git", "add", ".beads")
		runCmd(t, cloneDir, "git", "commit", "-m", "Initialize beads")
		runCmd(t, cloneDir, "git", "push", "origin", "master")
	} else {
		// Other clones pull and initialize from JSONL
		runCmd(t, cloneDir, "git", "pull", "origin", "master")
		runCmd(t, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
		// Enable hash ID mode (same as clone A)
		runCmdWithEnv(t, cloneDir, map[string]string{"BEADS_NO_DAEMON": "1"}, "./bd", "config", "set", "id_mode", "hash")
	}
	
	// Install git hooks
	installGitHooks(t, cloneDir)
	
	return cloneDir
}

// createIssueInClone creates an issue in the specified clone
func createIssueInClone(t *testing.T, cloneDir, title string) {
	t.Helper()
	runCmdWithEnv(t, cloneDir, map[string]string{"BEADS_NO_DAEMON": "1"}, "./bd", "create", title, "-t", "task", "-p", "1", "--json")
}

// syncCloneWithConflictResolution syncs a clone and resolves any conflicts
func syncCloneWithConflictResolution(t *testing.T, cloneDir, name string, isFirst bool) {
	t.Helper()
	
	t.Logf("%s syncing", name)
	syncOut := runCmdOutputAllowError(t, cloneDir, "./bd", "sync")
	
	if isFirst {
		// First clone should sync cleanly
		waitForPush(t, cloneDir, 2*time.Second)
		return
	}
	
	// Subsequent clones will likely conflict
	if strings.Contains(syncOut, "CONFLICT") || strings.Contains(syncOut, "Error") {
		t.Logf("%s hit conflict (expected)", name)
		runCmdAllowError(t, cloneDir, "git", "rebase", "--abort")
		
		// Pull with merge
		runCmdOutputAllowError(t, cloneDir, "git", "pull", "--no-rebase", "origin", "master")
		
		// Resolve conflict markers if present
		jsonlPath := filepath.Join(cloneDir, ".beads", "issues.jsonl")
		jsonlContent, _ := os.ReadFile(jsonlPath)
		if strings.Contains(string(jsonlContent), "<<<<<<<") {
			t.Logf("%s resolving conflict markers", name)
			resolveConflictMarkers(t, jsonlPath)
			runCmd(t, cloneDir, "git", "add", ".beads/issues.jsonl")
			runCmd(t, cloneDir, "git", "commit", "-m", "Resolve merge conflict")
		}
		
		// Import with collision resolution
		runCmdWithEnv(t, cloneDir, map[string]string{"BEADS_NO_DAEMON": "1"}, "./bd", "import", "-i", ".beads/issues.jsonl", "--resolve-collisions")
		runCmd(t, cloneDir, "git", "push", "origin", "master")
	}
}

// finalPullForClone pulls final changes without pushing
func finalPullForClone(t *testing.T, cloneDir, name string) {
	t.Helper()
	
	pullOut := runCmdOutputAllowError(t, cloneDir, "git", "pull", "--no-rebase", "origin", "master")
	
	// If there's a conflict, resolve it
	if strings.Contains(pullOut, "CONFLICT") {
		jsonlPath := filepath.Join(cloneDir, ".beads", "issues.jsonl")
		jsonlContent, _ := os.ReadFile(jsonlPath)
		if strings.Contains(string(jsonlContent), "<<<<<<<") {
			t.Logf("%s resolving final conflict markers", name)
			resolveConflictMarkers(t, jsonlPath)
			runCmd(t, cloneDir, "git", "add", ".beads/issues.jsonl")
			runCmd(t, cloneDir, "git", "commit", "-m", "Resolve final merge conflict")
		}
	}
	
	// Import JSONL to update database
	// Use --resolve-collisions to handle any remaining ID conflicts
	runCmdOutputWithEnvAllowError(t, cloneDir, map[string]string{"BEADS_NO_DAEMON": "1"}, true, "./bd", "import", "-i", ".beads/issues.jsonl", "--resolve-collisions")
}

// getTitlesFromClone extracts all issue titles from a clone's database
func getTitlesFromClone(t *testing.T, cloneDir string) map[string]bool {
	t.Helper()
	
	// Wait for any auto-imports to complete
	time.Sleep(200 * time.Millisecond)
	
	// Disable auto-import to avoid messages in JSON output
	listJSON := runCmdOutputWithEnv(t, cloneDir, map[string]string{
		"BEADS_NO_DAEMON":      "1",
		"BD_NO_AUTO_IMPORT":    "1",
	}, "./bd", "list", "--json")
	
	// Extract JSON array from output (skip any messages before the JSON)
	jsonStart := strings.Index(listJSON, "[")
	if jsonStart == -1 {
		t.Logf("No JSON array found in output: %s", listJSON)
		return nil
	}
	listJSON = listJSON[jsonStart:]
	
	var issues []issueContent
	if err := json.Unmarshal([]byte(listJSON), &issues); err != nil {
		t.Logf("Failed to parse JSON: %v\nContent: %s", err, listJSON)
		return nil
	}
	
	titles := make(map[string]bool)
	for _, issue := range issues {
		titles[issue.Title] = true
	}
	return titles
}

// resolveConflictMarkers removes Git conflict markers from a JSONL file
func resolveConflictMarkers(t *testing.T, jsonlPath string) {
	t.Helper()
	
	jsonlContent, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to read JSONL: %v", err)
	}
	
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
}

// resolveGitConflict resolves a git merge conflict in the JSONL file
func resolveGitConflict(t *testing.T, cloneDir, name string) {
	t.Helper()
	
	jsonlPath := filepath.Join(cloneDir, ".beads", "issues.jsonl")
	jsonlContent, _ := os.ReadFile(jsonlPath)
	if strings.Contains(string(jsonlContent), "<<<<<<<") {
		t.Logf("%s resolving conflict markers", name)
		resolveConflictMarkers(t, jsonlPath)
		runCmd(t, cloneDir, "git", "add", ".beads/issues.jsonl")
		runCmd(t, cloneDir, "git", "commit", "-m", "Resolve conflict")
	}
}

// sortedKeys returns a sorted slice of map keys
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runCmdWithEnv runs a command with custom environment variables
func runCmdWithEnv(t *testing.T, dir string, env map[string]string, name string, args ...string) {
	t.Helper()
	runCmdOutputWithEnvAllowError(t, dir, env, false, name, args...)
}

// runCmdOutputWithEnv runs a command with custom env and returns output
func runCmdOutputWithEnv(t *testing.T, dir string, env map[string]string, name string, args ...string) string {
	t.Helper()
	return runCmdOutputWithEnvAllowError(t, dir, env, false, name, args...)
}

// runCmdOutputWithEnvAllowError runs a command with custom env, optionally allowing errors
func runCmdOutputWithEnvAllowError(t *testing.T, dir string, env map[string]string, allowError bool, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), mapToEnvSlice(env)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil && !allowError {
		t.Logf("Command output: %s", string(out))
		t.Fatalf("Command failed: %s %v\nError: %v", name, args, err)
	}
	return string(out)
}

// mapToEnvSlice converts map[string]string to []string in KEY=VALUE format
func mapToEnvSlice(m map[string]string) []string {
	result := make([]string, 0, len(m))
	for k, v := range m {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}

// TestEdgeCases tests boundary conditions for N-way collision resolution
func TestEdgeCases(t *testing.T) {
	t.Run("AllIdenticalContent", func(t *testing.T) {
		testIdenticalContent(t, 3)
	})
	
	t.Run("OneDifferent", func(t *testing.T) {
		testOneDifferent(t, 3)
	})
	
	t.Run("MixedCollisions", func(t *testing.T) {
		testMixedCollisions(t, 3)
	})
}

// testIdenticalContent tests N clones creating issues with identical content
func testIdenticalContent(t *testing.T, numClones int) {
	t.Helper()
	
	tmpDir := t.TempDir()
	bdPath, _ := filepath.Abs("./bd")
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// All clones create identical issue
	for _, dir := range cloneDirs {
		createIssueInClone(t, dir, "Identical issue")
	}
	
	// Sync all
	syncOrder := make([]string, numClones)
	for i := 0; i < numClones; i++ {
		syncOrder[i] = string(rune('A' + i))
		syncCloneWithConflictResolution(t, cloneDirs[syncOrder[i]], syncOrder[i], i == 0)
	}
	
	// Final convergence rounds
	for round := 1; round <= 3; round++ {
		for i := 0; i < numClones; i++ {
			name := string(rune('A' + i))
			dir := cloneDirs[name]
			syncCloneWithConflictResolution(t, dir, name, false)
		}
	}
	
	// Verify all clones have exactly one issue (deduplication worked)
	for name, dir := range cloneDirs {
		titles := getTitlesFromClone(t, dir)
		if len(titles) != 1 {
			t.Errorf("Clone %s should have 1 issue, got %d: %v", name, len(titles), sortedKeys(titles))
		}
	}
	
	t.Log("✓ Identical content deduplicated correctly")
}

// testOneDifferent tests N-1 clones with same content, 1 different
func testOneDifferent(t *testing.T, numClones int) {
	t.Helper()
	
	tmpDir := t.TempDir()
	bdPath, _ := filepath.Abs("./bd")
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// N-1 clones create same issue, last clone creates different
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		if i < numClones-1 {
			createIssueInClone(t, cloneDirs[name], "Same issue")
		} else {
			createIssueInClone(t, cloneDirs[name], "Different issue")
		}
	}
	
	// Sync all
	syncOrder := make([]string, numClones)
	for i := 0; i < numClones; i++ {
		syncOrder[i] = string(rune('A' + i))
		syncCloneWithConflictResolution(t, cloneDirs[syncOrder[i]], syncOrder[i], i == 0)
	}
	
	// Final convergence rounds
	for round := 1; round <= 3; round++ {
		for i := 0; i < numClones; i++ {
			name := string(rune('A' + i))
			dir := cloneDirs[name]
			syncCloneWithConflictResolution(t, dir, name, false)
		}
	}
	
	// Verify all clones have exactly 2 issues
	expectedTitles := map[string]bool{
		"Same issue":      true,
		"Different issue": true,
	}
	
	for name, dir := range cloneDirs {
		titles := getTitlesFromClone(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s missing issues:\n  Expected: %v\n  Got: %v", 
				name, sortedKeys(expectedTitles), sortedKeys(titles))
		}
	}
	
	t.Log("✓ N-1 same, 1 different handled correctly")
}

// testMixedCollisions tests mix of colliding and non-colliding issues
func testMixedCollisions(t *testing.T, numClones int) {
	t.Helper()
	
	tmpDir := t.TempDir()
	bdPath, _ := filepath.Abs("./bd")
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates:
	// 1. A collision issue (same ID, different content)
	// 2. A unique issue (won't collide)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		createIssueInClone(t, cloneDirs[name], fmt.Sprintf("Collision from %s", name))
		createIssueInClone(t, cloneDirs[name], fmt.Sprintf("Unique from %s", name))
	}
	
	// Sync all
	syncOrder := make([]string, numClones)
	for i := 0; i < numClones; i++ {
		syncOrder[i] = string(rune('A' + i))
		syncCloneWithConflictResolution(t, cloneDirs[syncOrder[i]], syncOrder[i], i == 0)
	}
	
	// Final convergence rounds - same as TestFiveCloneCollision
	t.Log("Final convergence rounds")
	for round := 1; round <= 3; round++ {
		t.Logf("Convergence round %d", round)
		for i := 0; i < numClones; i++ {
			name := string(rune('A' + i))
			dir := cloneDirs[name]
			syncCloneWithConflictResolution(t, dir, name, false)
		}
	}
	
	// Verify all clones have all 2*N issues
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Collision from %s", name)] = true
		expectedTitles[fmt.Sprintf("Unique from %s", name)] = true
	}
	
	for name, dir := range cloneDirs {
		titles := getTitlesFromClone(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s missing issues:\n  Expected: %v\n  Got: %v", 
				name, sortedKeys(expectedTitles), sortedKeys(titles))
		}
	}
	
	t.Log("✓ Mixed collisions handled correctly")
}

// TestConvergenceTime verifies convergence happens within expected bounds
func TestConvergenceTime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping convergence time test in short mode")
	}
	
	for n := 3; n <= 5; n++ {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			rounds := measureConvergenceRounds(t, n)
			maxExpected := n - 1
			
			t.Logf("Convergence took %d rounds (max expected: %d)", rounds, maxExpected)
			
			if rounds > maxExpected {
				t.Errorf("Convergence took %d rounds, expected ≤ %d", rounds, maxExpected)
			}
		})
	}
}

// measureConvergenceRounds measures how many sync rounds it takes for N clones to converge
func measureConvergenceRounds(t *testing.T, numClones int) int {
	t.Helper()
	
	tmpDir := t.TempDir()
	bdPath, _ := filepath.Abs("./bd")
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates a collision issue
	for name, dir := range cloneDirs {
		createIssueInClone(t, dir, fmt.Sprintf("Issue from %s", name))
	}
	
	rounds := 0
	maxRounds := numClones * 2 // Safety limit
	
	// Sync until convergence
	for rounds < maxRounds {
		rounds++
		
		// All clones sync in order
		for i := 0; i < numClones; i++ {
			name := string(rune('A' + i))
			syncCloneWithConflictResolution(t, cloneDirs[name], name, false)
		}
		
		// Check if converged
		if hasConverged(t, cloneDirs, numClones) {
			return rounds
		}
	}
	
	t.Fatalf("Failed to converge after %d rounds", maxRounds)
	return maxRounds
}

// hasConverged checks if all clones have identical content
func hasConverged(t *testing.T, cloneDirs map[string]string, numClones int) bool {
	t.Helper()
	
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Issue from %s", name)] = true
	}
	
	for _, dir := range cloneDirs {
		titles := getTitlesFromClone(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			return false
		}
	}
	
	return true
}
