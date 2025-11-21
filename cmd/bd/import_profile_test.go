package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestImportPerformance profiles import operations at various scales
// This test helps identify bottlenecks in the import pipeline
func TestImportPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	scales := []int{100, 500, 1000}

	for _, numIssues := range scales {
		t.Run(fmt.Sprintf("%d_issues", numIssues), func(t *testing.T) {
			profileImportOperation(t, numIssues)
		})
	}
}

func profileImportOperation(t *testing.T, numIssues int) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "bd-import-profile-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize storage
	ctx := context.Background()
	var store storage.Storage
	store, err = sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Set test config
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Generate test issues
	t.Logf("Generating %d test issues...", numIssues)
	issues := generateTestIssues(numIssues)

	// Convert to JSONL
	var jsonlBuf bytes.Buffer
	for _, issue := range issues {
		data, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("Failed to marshal issue: %v", err)
		}
		jsonlBuf.Write(data)
		jsonlBuf.WriteByte('\n')
	}
	jsonlData := jsonlBuf.Bytes()

	t.Logf("Generated %d KB of JSONL data", len(jsonlData)/1024)

	// Profile CPU usage
	cpuProfile := filepath.Join(tmpDir, fmt.Sprintf("cpu_%d.prof", numIssues))
	f, err := os.Create(cpuProfile)
	if err != nil {
		t.Fatalf("Failed to create CPU profile: %v", err)
	}
	defer f.Close()

	// Get initial memory stats
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Start profiling
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatalf("Failed to start CPU profile: %v", err)
	}

	// Time the import operation
	startTime := time.Now()

	// Simulate import by timing each phase
	phases := make(map[string]time.Duration)

	// Phase 1: Parse JSONL (simulated - already done above)
	parseStart := time.Now()
	var parsedIssues []*types.Issue
	for _, line := range bytes.Split(jsonlData, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatalf("Failed to parse issue: %v", err)
		}
		parsedIssues = append(parsedIssues, &issue)
	}
	phases["parse"] = time.Since(parseStart)

	// Phase 2: Collision detection
	collisionStart := time.Now()
	sqliteStore := store.(*sqlite.SQLiteStorage)
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, parsedIssues)
	if err != nil {
		t.Fatalf("Collision detection failed: %v", err)
	}
	phases["collision_detection"] = time.Since(collisionStart)

	// Phase 3: Create/update issues
	createStart := time.Now()
	for _, issue := range parsedIssues {
		existing, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to check issue: %v", err)
		}

		if existing != nil {
			// Update
			updates := map[string]interface{}{
				"title":       issue.Title,
				"description": issue.Description,
				"priority":    issue.Priority,
				"status":      issue.Status,
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, "test"); err != nil {
				t.Fatalf("Failed to update issue: %v", err)
			}
		} else {
			// Create
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("Failed to create issue: %v", err)
			}
		}
	}
	phases["create_update"] = time.Since(createStart)

	// Phase 4: Sync counters
	// REMOVED (bd-c7af): Counter sync - no longer needed with hash IDs

	totalDuration := time.Since(startTime)

	// Stop profiling
	pprof.StopCPUProfile()

	// Get final memory stats
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	// Calculate metrics
	issuesPerSec := float64(numIssues) / totalDuration.Seconds()
	memUsedMB := float64(memAfter.Alloc-memBefore.Alloc) / 1024 / 1024

	// Report results
	t.Logf("\n=== Import Performance Report ===")
	t.Logf("Issues: %d", numIssues)
	t.Logf("Total time: %v", totalDuration)
	t.Logf("Throughput: %.1f issues/sec", issuesPerSec)
	t.Logf("Memory used: %.2f MB", memUsedMB)
	t.Logf("\nPhase breakdown:")
	t.Logf("  Parse:              %v (%.1f%%)", phases["parse"], 100*phases["parse"].Seconds()/totalDuration.Seconds())
	t.Logf("  Collision detect:   %v (%.1f%%)", phases["collision_detection"], 100*phases["collision_detection"].Seconds()/totalDuration.Seconds())
	t.Logf("  Create/Update:      %v (%.1f%%)", phases["create_update"], 100*phases["create_update"].Seconds()/totalDuration.Seconds())
	t.Logf("  Sync counters:      %v (%.1f%%)", phases["sync_counters"], 100*phases["sync_counters"].Seconds()/totalDuration.Seconds())
	t.Logf("\nCollision detection results:")
	t.Logf("  New issues: %d", len(collisionResult.NewIssues))
	t.Logf("  Exact matches: %d", len(collisionResult.ExactMatches))
	t.Logf("  Collisions: %d", len(collisionResult.Collisions))
	t.Logf("\nCPU profile saved to: %s", cpuProfile)
	t.Logf("To analyze: go tool pprof %s", cpuProfile)

	// Check performance targets from bd-199
	targetTime := 5 * time.Second
	if numIssues >= 1000 {
		targetTime = 30 * time.Second
	}

	if totalDuration > targetTime {
		t.Logf("\n⚠️  WARNING: Import took %v, target was %v", totalDuration, targetTime)
		t.Logf("This exceeds the performance target from bd-199")
	} else {
		t.Logf("\n✓ Performance target met (%v < %v)", totalDuration, targetTime)
	}
}

// generateTestIssues creates realistic test data with varying content sizes
func generateTestIssues(count int) []*types.Issue {
	issues := make([]*types.Issue, count)
	now := time.Now()

	for i := 0; i < count; i++ {
		// Create issues with varying complexity
		descriptionSize := 100 + (i%10)*50 // 100-550 chars
		designSize := 200 + (i%5)*100      // 200-600 chars

		issue := &types.Issue{
			ID:          fmt.Sprintf("test-%d", i+1),
			Title:       fmt.Sprintf("Test Issue %d", i+1),
			Description: generateText("Description", descriptionSize),
			Design:      generateText("Design", designSize),
			Status:      types.StatusOpen,
			Priority:    i % 5, // Mix of priorities
			IssueType:   []types.IssueType{types.TypeBug, types.TypeFeature, types.TypeTask}[i%3],
			CreatedAt:   now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt:   now,
		}

		// Add some cross-references to create a realistic dependency graph
		if i > 0 && i%10 == 0 {
			// Reference a previous issue
			refID := fmt.Sprintf("test-%d", (i/10)*5+1)
			issue.Description += fmt.Sprintf("\n\nRelated to %s", refID)

			// Add a dependency
			if i%20 == 0 && i > 10 {
				issue.Dependencies = []*types.Dependency{
					{
						IssueID:     issue.ID,
						DependsOnID: fmt.Sprintf("test-%d", i-5),
						Type:        types.DepBlocks,
					},
				}
			}
		}

		issues[i] = issue
	}

	return issues
}

// generateText creates filler text of specified length
func generateText(prefix string, length int) string {
	filler := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
	result := prefix + ": "
	for len(result) < length {
		result += filler
	}
	return result[:length]
}

// TestImportWithExistingData tests import performance when data already exists (idempotent case)
func TestImportWithExistingData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	numIssues := 208 // The exact number from bd-199

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bd-import-existing-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()
	var store storage.Storage
	store, err = sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Generate and create initial issues
	t.Logf("Creating %d initial issues...", numIssues)
	issues := generateTestIssues(numIssues)
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Now import the same issues again (idempotent case)
	t.Logf("Testing idempotent import of %d existing issues...", numIssues)

	sqliteStore := store.(*sqlite.SQLiteStorage)

	startTime := time.Now()
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, issues)
	if err != nil {
		t.Fatalf("Collision detection failed: %v", err)
	}
	duration := time.Since(startTime)

	t.Logf("\n=== Idempotent Import Results ===")
	t.Logf("Time: %v", duration)
	t.Logf("Exact matches: %d", len(collisionResult.ExactMatches))
	t.Logf("New issues: %d", len(collisionResult.NewIssues))
	t.Logf("Collisions: %d", len(collisionResult.Collisions))
	t.Logf("Throughput: %.1f issues/sec", float64(numIssues)/duration.Seconds())

	if duration > 5*time.Second {
		t.Logf("\n⚠️  WARNING: Idempotent import took %v, expected < 5s", duration)
		t.Logf("This matches the symptoms described in bd-199")
	}
}
