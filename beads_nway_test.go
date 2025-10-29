package beads_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFiveCloneCollision tests 5-way collision resolution with different sync orders
func TestFiveCloneCollision(t *testing.T) {
	t.Run("SequentialSync", func(t *testing.T) {
		testNCloneCollision(t, 5, "A", "B", "C", "D", "E")
	})
	
	t.Run("ReverseSync", func(t *testing.T) {
		testNCloneCollision(t, 5, "E", "D", "C", "B", "A")
	})
	
	t.Run("RandomSync", func(t *testing.T) {
		testNCloneCollision(t, 5, "C", "A", "E", "B", "D")
	})
}

// TestTenCloneCollision tests scalability to larger collision groups
func TestTenCloneCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping 10-clone test in short mode")
	}
	
	// Generate sync order: A, B, C, ..., J
	syncOrder := make([]string, 10)
	for i := 0; i < 10; i++ {
		syncOrder[i] = string(rune('A' + i))
	}
	
	testNCloneCollision(t, 10, syncOrder...)
}

// testNCloneCollision is a generalized N-way collision test
// It creates N clones, has each create an issue with the same ID but different content,
// syncs them in the specified order, and verifies all clones converge
func testNCloneCollision(t *testing.T, numClones int, syncOrder ...string) {
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
	t.Logf("Setting up %d clones", numClones)
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates issue with same ID but different content
	t.Logf("Each clone creating unique issue")
	for name, dir := range cloneDirs {
		createIssue(t, dir, fmt.Sprintf("Issue from clone %s", name))
	}
	
	// Sync in specified order
	t.Logf("Syncing in order: %v", syncOrder)
	for i, name := range syncOrder {
		syncClone(t, cloneDirs[name], name, i == 0)
	}
	
	// Final pull for convergence
	t.Log("Final pull for all clones to converge")
	for name, dir := range cloneDirs {
		finalPull(t, dir, name)
	}
	
	// Verify all clones have all N issues
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Issue from clone %s", name)] = true
	}
	
	t.Logf("Verifying all %d clones have all %d issues", numClones, numClones)
	for name, dir := range cloneDirs {
		titles := getTitles(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s missing issues:\nExpected: %v\nGot: %v", 
				name, expectedTitles, titles)
		}
	}
	
	t.Logf("✓ All %d clones converged successfully", numClones)
}

// TestEdgeCases tests boundary conditions for N-way collisions
func TestEdgeCases(t *testing.T) {
	t.Run("AllIdenticalContent", func(t *testing.T) {
		testNCloneIdenticalContent(t, 5)
	})
	
	t.Run("OneDifferent", func(t *testing.T) {
		testNCloneOneDifferent(t, 5)
	})
	
	t.Run("MixedCollisions", func(t *testing.T) {
		testMixedCollisions(t, 5)
	})
}

// testNCloneIdenticalContent tests deduplication when all clones create identical issues
func testNCloneIdenticalContent(t *testing.T, numClones int) {
	t.Helper()
	tmpDir := t.TempDir()
	
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// All clones create identical issue
	identicalTitle := "Identical issue from all clones"
	for _, dir := range cloneDirs {
		createIssue(t, dir, identicalTitle)
	}
	
	// Sync all clones
	syncOrder := make([]string, numClones)
	for i := 0; i < numClones; i++ {
		syncOrder[i] = string(rune('A' + i))
		syncClone(t, cloneDirs[syncOrder[i]], syncOrder[i], i == 0)
	}
	
	// Final pull
	for name, dir := range cloneDirs {
		finalPull(t, dir, name)
	}
	
	// Should have exactly 1 issue (deduplicated)
	for name, dir := range cloneDirs {
		titles := getTitles(t, dir)
		if len(titles) != 1 {
			t.Errorf("Clone %s: expected 1 issue, got %d: %v", name, len(titles), titles)
		}
		if !titles[identicalTitle] {
			t.Errorf("Clone %s: missing expected title %q", name, identicalTitle)
		}
	}
	
	t.Logf("✓ All %d clones deduplicated to 1 issue", numClones)
}

// testNCloneOneDifferent tests N-1 clones with same content, 1 different
func testNCloneOneDifferent(t *testing.T, numClones int) {
	t.Helper()
	tmpDir := t.TempDir()
	
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// First N-1 clones create identical issue, last one different
	commonTitle := "Common issue"
	differentTitle := "Different issue from last clone"
	
	for i := 0; i < numClones-1; i++ {
		name := string(rune('A' + i))
		createIssue(t, cloneDirs[name], commonTitle)
	}
	lastClone := string(rune('A' + numClones - 1))
	createIssue(t, cloneDirs[lastClone], differentTitle)
	
	// Sync all
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		syncClone(t, cloneDirs[name], name, i == 0)
	}
	
	// Final pull
	for name, dir := range cloneDirs {
		finalPull(t, dir, name)
	}
	
	// Should have exactly 2 issues
	expectedTitles := map[string]bool{
		commonTitle:    true,
		differentTitle: true,
	}
	
	for name, dir := range cloneDirs {
		titles := getTitles(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s:\nExpected: %v\nGot: %v", name, expectedTitles, titles)
		}
	}
	
	t.Logf("✓ All %d clones converged to 2 issues", numClones)
}

// testMixedCollisions tests mix of collisions and non-collisions
func testMixedCollisions(t *testing.T, numClones int) {
	t.Helper()
	tmpDir := t.TempDir()
	
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates 2 issues:
	// - One unique issue
	// - One colliding issue (same ID across all clones)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		dir := cloneDirs[name]
		
		// Unique issue
		createIssue(t, dir, fmt.Sprintf("Unique issue from clone %s", name))
		
		// Colliding issue (same ID, different content)
		createIssue(t, dir, fmt.Sprintf("Colliding issue from clone %s", name))
	}
	
	// Sync all
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		syncClone(t, cloneDirs[name], name, i == 0)
	}
	
	// Final pull
	for name, dir := range cloneDirs {
		finalPull(t, dir, name)
	}
	
	// Should have 2*N issues (N unique + N from collision)
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Unique issue from clone %s", name)] = true
		expectedTitles[fmt.Sprintf("Colliding issue from clone %s", name)] = true
	}
	
	for name, dir := range cloneDirs {
		titles := getTitles(t, dir)
		if !compareTitleSets(titles, expectedTitles) {
			t.Errorf("Clone %s:\nExpected: %v\nGot: %v", name, expectedTitles, titles)
		}
	}
	
	t.Logf("✓ All %d clones converged to %d issues", numClones, 2*numClones)
}

// Helper functions

func setupBareRepo(t *testing.T, tmpDir string) string {
	t.Helper()
	remoteDir := filepath.Join(tmpDir, "remote.git")
	runCmdQuiet(t, tmpDir, "git", "init", "--bare", remoteDir)
	
	// Create initial commit
	tempClone := filepath.Join(tmpDir, "temp-init")
	runCmdQuiet(t, tmpDir, "git", "clone", remoteDir, tempClone)
	runCmdQuiet(t, tempClone, "git", "commit", "--allow-empty", "-m", "Initial commit")
	runCmdQuiet(t, tempClone, "git", "push", "origin", "master")
	
	return remoteDir
}

// runCmdQuiet runs a command suppressing all output
func runCmdQuiet(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Command failed: %s %v\nError: %v", name, args, err)
	}
}

func setupClone(t *testing.T, tmpDir, remoteDir, name, bdPath string) string {
	t.Helper()
	cloneDir := filepath.Join(tmpDir, fmt.Sprintf("clone-%s", strings.ToLower(name)))
	
	runCmd(t, tmpDir, "git", "clone", "--quiet", remoteDir, cloneDir)
	copyFile(t, bdPath, filepath.Join(cloneDir, "bd"))
	
	// Initialize beads only in first clone
	if name == "A" {
		runCmd(t, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
		runCmd(t, cloneDir, "git", "add", ".beads")
		runCmd(t, cloneDir, "git", "commit", "--quiet", "-m", "Initialize beads")
		runCmd(t, cloneDir, "git", "push", "--quiet", "origin", "master")
	} else {
		// Pull beads initialization
		runCmd(t, cloneDir, "git", "pull", "--quiet", "origin", "master")
		runCmd(t, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
	}
	
	installGitHooks(t, cloneDir)
	
	return cloneDir
}

func createIssue(t *testing.T, dir, title string) {
	t.Helper()
	runCmd(t, dir, "./bd", "create", title, "-t", "task", "-p", "1", "--json")
}

func syncClone(t *testing.T, dir, name string, isFirst bool) {
	t.Helper()
	
	if isFirst {
		t.Logf("%s syncing (first, clean push)", name)
		runCmd(t, dir, "./bd", "sync")
		waitForPush(t, dir, 2*time.Second)
		return
	}
	
	t.Logf("%s syncing (may conflict)", name)
	syncOut := runCmdOutputAllowError(t, dir, "./bd", "sync")
	
	if strings.Contains(syncOut, "CONFLICT") || strings.Contains(syncOut, "Error") {
		t.Logf("%s hit conflict, resolving", name)
		runCmdAllowError(t, dir, "git", "rebase", "--abort")
		
		// Pull with merge
		runCmdOutputAllowError(t, dir, "git", "pull", "--no-rebase", "origin", "master")
		
		// Resolve conflict markers
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		jsonlContent, _ := os.ReadFile(jsonlPath)
		if strings.Contains(string(jsonlContent), "<<<<<<<") {
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
			os.WriteFile(jsonlPath, []byte(cleaned), 0644)
			runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
			runCmd(t, dir, "git", "commit", "-m", "Resolve merge conflict")
		}
		
		// Import with collision resolution
		runCmd(t, dir, "./bd", "import", "-i", ".beads/issues.jsonl", "--resolve-collisions")
		runCmd(t, dir, "git", "push", "origin", "master")
	}
}

func finalPull(t *testing.T, dir, name string) {
	t.Helper()
	
	pullOut := runCmdOutputAllowError(t, dir, "git", "pull", "--no-rebase", "origin", "master")
	
	if strings.Contains(pullOut, "CONFLICT") {
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		jsonlContent, _ := os.ReadFile(jsonlPath)
		if strings.Contains(string(jsonlContent), "<<<<<<<") {
			t.Logf("%s resolving final conflict", name)
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
			os.WriteFile(jsonlPath, []byte(cleaned), 0644)
			runCmd(t, dir, "git", "add", ".beads/issues.jsonl")
			runCmd(t, dir, "git", "commit", "-m", "Resolve final merge conflict")
		}
	}
	
	// Import to sync database
	runCmdOutputAllowError(t, dir, "./bd", "import", "-i", ".beads/issues.jsonl")
	time.Sleep(500 * time.Millisecond)
}

func getTitles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	
	// Get clean JSON output
	listOut := runCmdOutput(t, dir, "./bd", "list", "--json")
	
	// Find the JSON array in the output (skip any prefix messages)
	start := strings.Index(listOut, "[")
	if start == -1 {
		t.Logf("No JSON array found in output: %s", listOut)
		return make(map[string]bool)
	}
	jsonData := listOut[start:]
	
	var issues []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(jsonData), &issues); err != nil {
		t.Logf("Failed to parse JSON: %v\nContent: %s", err, jsonData)
		return make(map[string]bool)
	}
	
	titles := make(map[string]bool)
	for _, issue := range issues {
		titles[issue.Title] = true
	}
	return titles
}

// BenchmarkNWayCollision benchmarks N-way collision resolution performance
func BenchmarkNWayCollision(b *testing.B) {
	for _, n := range []int{3, 5, 10} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				benchNCloneCollision(b, n)
			}
		})
	}
}

func benchNCloneCollision(b *testing.B, numClones int) {
	b.Helper()
	
	tmpDir := b.TempDir()
	
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		b.Fatalf("Failed to get bd path: %v", err)
	}
	
	remoteDir := setupBareRepoBench(b, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupCloneBench(b, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates issue
	for name, dir := range cloneDirs {
		createIssueBench(b, dir, fmt.Sprintf("Issue from clone %s", name))
	}
	
	// Sync in order
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		syncCloneBench(b, cloneDirs[name], name, i == 0)
	}
	
	// Final pull
	for _, dir := range cloneDirs {
		finalPullBench(b, dir)
	}
}

// TestConvergenceTime verifies bounded convergence
func TestConvergenceTime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping convergence time test in short mode")
	}
	
	for _, n := range []int{3, 5, 7} {
		n := n
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			rounds := measureConvergenceRounds(t, n)
			maxExpected := n
			
			t.Logf("Convergence took %d rounds for %d clones", rounds, n)
			
			if rounds > maxExpected {
				t.Errorf("Convergence took %d rounds, expected ≤ %d", 
					rounds, maxExpected)
			}
		})
	}
}

func measureConvergenceRounds(t *testing.T, numClones int) int {
	t.Helper()
	
	tmpDir := t.TempDir()
	
	bdPath, err := filepath.Abs("./bd")
	if err != nil {
		t.Fatalf("Failed to get bd path: %v", err)
	}
	
	remoteDir := setupBareRepo(t, tmpDir)
	cloneDirs := make(map[string]string)
	
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		cloneDirs[name] = setupClone(t, tmpDir, remoteDir, name, bdPath)
	}
	
	// Each clone creates issue
	for name, dir := range cloneDirs {
		createIssue(t, dir, fmt.Sprintf("Issue from clone %s", name))
	}
	
	// Initial sync round (first clone pushes, others pull and resolve)
	rounds := 1
	
	// First clone syncs
	firstClone := "A"
	syncClone(t, cloneDirs[firstClone], firstClone, true)
	
	// Other clones sync
	for i := 1; i < numClones; i++ {
		name := string(rune('A' + i))
		syncClone(t, cloneDirs[name], name, false)
	}
	
	// Additional convergence rounds
	expectedTitles := make(map[string]bool)
	for i := 0; i < numClones; i++ {
		name := string(rune('A' + i))
		expectedTitles[fmt.Sprintf("Issue from clone %s", name)] = true
	}
	
	maxRounds := numClones * 2
	for round := 2; round <= maxRounds; round++ {
		allConverged := true
		
		// Each clone pulls
		for name, dir := range cloneDirs {
			finalPull(t, dir, name)
			
			titles := getTitles(t, dir)
			if !compareTitleSets(titles, expectedTitles) {
				allConverged = false
			}
		}
		
		if allConverged {
			return round
		}
		
		rounds = round
	}
	
	return rounds
}

// Benchmark helper functions

func setupBareRepoBench(b *testing.B, tmpDir string) string {
	b.Helper()
	remoteDir := filepath.Join(tmpDir, "remote.git")
	runCmdBench(b, tmpDir, "git", "init", "--bare", "--quiet", remoteDir)
	
	tempClone := filepath.Join(tmpDir, "temp-init")
	runCmdBench(b, tmpDir, "git", "clone", "--quiet", remoteDir, tempClone)
	runCmdBench(b, tempClone, "git", "commit", "--allow-empty", "-m", "Initial commit")
	runCmdBench(b, tempClone, "git", "push", "--quiet", "origin", "master")
	
	return remoteDir
}

func setupCloneBench(b *testing.B, tmpDir, remoteDir, name, bdPath string) string {
	b.Helper()
	cloneDir := filepath.Join(tmpDir, fmt.Sprintf("clone-%s", strings.ToLower(name)))
	
	runCmdBench(b, tmpDir, "git", "clone", "--quiet", remoteDir, cloneDir)
	copyFileBench(b, bdPath, filepath.Join(cloneDir, "bd"))
	
	if name == "A" {
		runCmdBench(b, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
		runCmdBench(b, cloneDir, "git", "add", ".beads")
		runCmdBench(b, cloneDir, "git", "commit", "--quiet", "-m", "Initialize beads")
		runCmdBench(b, cloneDir, "git", "push", "--quiet", "origin", "master")
	} else {
		runCmdBench(b, cloneDir, "git", "pull", "--quiet", "origin", "master")
		runCmdBench(b, cloneDir, "./bd", "init", "--quiet", "--prefix", "test")
	}
	
	installGitHooksBench(b, cloneDir)
	
	return cloneDir
}

func createIssueBench(b *testing.B, dir, title string) {
	b.Helper()
	runCmdBench(b, dir, "./bd", "create", title, "-t", "task", "-p", "1", "--json")
}

func syncCloneBench(b *testing.B, dir, name string, isFirst bool) {
	b.Helper()
	
	if isFirst {
		runCmdBench(b, dir, "./bd", "sync")
		return
	}
	
	runCmdAllowErrorBench(b, dir, "./bd", "sync")
	runCmdAllowErrorBench(b, dir, "git", "rebase", "--abort")
	runCmdAllowErrorBench(b, dir, "git", "pull", "--no-rebase", "origin", "master")
	
	jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
	jsonlContent, _ := os.ReadFile(jsonlPath)
	if strings.Contains(string(jsonlContent), "<<<<<<<") {
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
		os.WriteFile(jsonlPath, []byte(cleaned), 0644)
		runCmdBench(b, dir, "git", "add", ".beads/issues.jsonl")
		runCmdBench(b, dir, "git", "commit", "-m", "Resolve merge conflict")
	}
	
	runCmdBench(b, dir, "./bd", "import", "-i", ".beads/issues.jsonl", "--resolve-collisions")
	runCmdBench(b, dir, "git", "push", "origin", "master")
}

func finalPullBench(b *testing.B, dir string) {
	b.Helper()
	runCmdAllowErrorBench(b, dir, "git", "pull", "--no-rebase", "origin", "master")
	runCmdAllowErrorBench(b, dir, "./bd", "import", "-i", ".beads/issues.jsonl")
}

func installGitHooksBench(b *testing.B, repoDir string) {
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
		b.Fatalf("Failed to write pre-commit hook: %v", err)
	}
	
	if err := os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(postMerge), 0755); err != nil {
		b.Fatalf("Failed to write post-merge hook: %v", err)
	}
}

func runCmdBench(b *testing.B, dir string, name string, args ...string) {
	b.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		b.Fatalf("Command failed: %s %v\nError: %v", name, args, err)
	}
}

func runCmdAllowErrorBench(b *testing.B, dir string, name string, args ...string) {
	b.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	_ = cmd.Run()
}

func copyFileBench(b *testing.B, src, dst string) {
	b.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		b.Fatalf("Failed to read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		b.Fatalf("Failed to write %s: %v", dst, err)
	}
}
