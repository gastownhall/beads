// Package dolt provides cross-project data isolation tests for shared Dolt servers.
//
// These tests validate that when multiple projects share a single Dolt server
// (whether by design in shared-server mode, or by accident via port collision),
// each project's data remains fully isolated. This is the core concern raised
// in GH#2372: cross-project data leakage when two projects connect to the same
// Dolt server.
//
// The test matrix covers:
//   - Different issue prefixes (the common case)
//   - Same issue prefix (worst case for collision detection)
//   - Concurrent writes from both projects simultaneously
//   - Read isolation (project A never sees project B's issues)
//
// Based on tests by @PabloLION (PR #2472).
package dolt

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/beads/internal/types"
)

// setupTwoProjectStores creates two DoltStore instances on the same server,
// each with its own database and issue prefix — simulating two beads projects
// sharing a Dolt server (either intentionally or via port collision).
func setupTwoProjectStores(t *testing.T, prefixA, prefixB string) (storeA, storeB *DoltStore, cleanup func()) {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	tmpDirA, err := os.MkdirTemp("", "dolt-project-a-*")
	if err != nil {
		t.Fatalf("failed to create temp dir for project A: %v", err)
	}
	tmpDirB, err := os.MkdirTemp("", "dolt-project-b-*")
	if err != nil {
		os.RemoveAll(tmpDirA)
		t.Fatalf("failed to create temp dir for project B: %v", err)
	}

	dbNameA := uniqueTestDBName(t) + "_a"
	dbNameB := uniqueTestDBName(t) + "_b"

	cfgA := &Config{
		Path:            tmpDirA,
		CommitterName:   "project-a",
		CommitterEmail:  "a@test.com",
		Database:        dbNameA,
		CreateIfMissing: true,
	}
	cfgB := &Config{
		Path:            tmpDirB,
		CommitterName:   "project-b",
		CommitterEmail:  "b@test.com",
		Database:        dbNameB,
		CreateIfMissing: true,
	}

	storeA, err = New(ctx, cfgA)
	if err != nil {
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to create store A: %v", err)
	}

	if err := storeA.SetConfig(ctx, "issue_prefix", prefixA); err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to set prefix for project A: %v", err)
	}

	storeB, err = New(ctx, cfgB)
	if err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to create store B: %v", err)
	}

	if err := storeB.SetConfig(ctx, "issue_prefix", prefixB); err != nil {
		storeA.Close()
		storeB.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to set prefix for project B: %v", err)
	}

	cleanup = func() {
		// Skip DROP DATABASE — rapid CREATE/DROP cycles crash the Dolt container.
		// Orphan databases are cleaned up when the container terminates.
		storeA.Close()
		storeB.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
	}

	return storeA, storeB, cleanup
}

// =============================================================================
// Test 1: Basic Read Isolation — Different Prefixes
// =============================================================================

func TestCrossProject_ReadIsolation_DifferentPrefixes(t *testing.T) {
	storeA, storeB, cleanup := setupTwoProjectStores(t, "alpha", "beta")
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const numIssuesPerProject = 5
	aIDs := make([]string, numIssuesPerProject)
	for i := 0; i < numIssuesPerProject; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Alpha Issue %d", i),
			Description: fmt.Sprintf("Created by project A, issue %d", i),
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := storeA.CreateIssue(ctx, issue, "project-a"); err != nil {
			t.Fatalf("project A failed to create issue %d: %v", i, err)
		}
		aIDs[i] = issue.ID
	}

	bIDs := make([]string, numIssuesPerProject)
	for i := 0; i < numIssuesPerProject; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Beta Issue %d", i),
			Description: fmt.Sprintf("Created by project B, issue %d", i),
			Status:      types.StatusOpen,
			Priority:    3,
			IssueType:   types.TypeTask,
		}
		if err := storeB.CreateIssue(ctx, issue, "project-b"); err != nil {
			t.Fatalf("project B failed to create issue %d: %v", i, err)
		}
		bIDs[i] = issue.ID
	}

	// Verify: project A sees only its issues
	allA, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project A SearchIssues failed: %v", err)
	}
	if len(allA) != numIssuesPerProject {
		t.Errorf("project A: expected %d issues, got %d", numIssuesPerProject, len(allA))
	}

	// Verify: project B sees only its issues
	allB, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project B SearchIssues failed: %v", err)
	}
	if len(allB) != numIssuesPerProject {
		t.Errorf("project B: expected %d issues, got %d", numIssuesPerProject, len(allB))
	}

	// Cross-check: no leakage
	aIDSet := make(map[string]bool)
	for _, id := range aIDs {
		aIDSet[id] = true
	}
	bIDSet := make(map[string]bool)
	for _, id := range bIDs {
		bIDSet[id] = true
	}

	for _, issue := range allA {
		if bIDSet[issue.ID] {
			t.Errorf("DATA LEAKAGE: project A contains project B's issue %s", issue.ID)
		}
	}
	for _, issue := range allB {
		if aIDSet[issue.ID] {
			t.Errorf("DATA LEAKAGE: project B contains project A's issue %s", issue.ID)
		}
	}

	t.Logf("Isolation verified: A has %d issues, B has %d issues, zero cross-contamination", len(allA), len(allB))
}

// =============================================================================
// Test 2: Port Collision — Same Database (the real #2372 scenario)
// When two projects share the SAME database (e.g., both defaulting to "beads"),
// data leakage occurs. This test documents the expected behavior.
// =============================================================================

func TestCrossProject_PortCollision_SameDatabase(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	sharedDB := uniqueTestDBName(t) + "_shared"

	tmpDirA, err := os.MkdirTemp("", "dolt-collision-a-*")
	if err != nil {
		t.Fatalf("temp dir A: %v", err)
	}
	tmpDirB, err := os.MkdirTemp("", "dolt-collision-b-*")
	if err != nil {
		os.RemoveAll(tmpDirA)
		t.Fatalf("temp dir B: %v", err)
	}

	cfgA := &Config{
		Path:            tmpDirA,
		CommitterName:   "project-a",
		CommitterEmail:  "a@test.com",
		Database:        sharedDB,
		CreateIfMissing: true,
	}
	cfgB := &Config{
		Path:            tmpDirB,
		CommitterName:   "project-b",
		CommitterEmail:  "b@test.com",
		Database:        sharedDB,
		CreateIfMissing: true,
	}

	storeA, err := New(ctx, cfgA)
	if err != nil {
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("store A: %v", err)
	}
	if err := storeA.SetConfig(ctx, "issue_prefix", "proj-a"); err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("set prefix A: %v", err)
	}

	storeB, err := New(ctx, cfgB)
	if err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("store B: %v", err)
	}
	if err := storeB.SetConfig(ctx, "issue_prefix", "proj-b"); err != nil {
		storeA.Close()
		storeB.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("set prefix B: %v", err)
	}

	defer func() {
		// Skip DROP DATABASE — rapid CREATE/DROP cycles crash the Dolt container.
		storeA.Close()
		storeB.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
	}()

	issueA := &types.Issue{
		Title:       "Project A's secret issue",
		Description: "This should only be visible to project A",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
	}
	if err := storeA.CreateIssue(ctx, issueA, "project-a"); err != nil {
		t.Fatalf("project A create: %v", err)
	}

	issueB := &types.Issue{
		Title:       "Project B's secret issue",
		Description: "This should only be visible to project B",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
	}
	if err := storeB.CreateIssue(ctx, issueB, "project-b"); err != nil {
		t.Fatalf("project B create: %v", err)
	}

	allA, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project A search: %v", err)
	}
	allB, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project B search: %v", err)
	}

	// When two projects share the same database, both see all issues.
	// This is expected — the fix is preventing shared databases (via
	// prefix-based DB naming in shared-server mode).
	if len(allA) > 1 {
		t.Logf("Expected: shared database means project A sees %d issues (both projects' data)", len(allA))
	}
	if len(allB) > 1 {
		t.Logf("Expected: shared database means project B sees %d issues (both projects' data)", len(allB))
	}

	// Cross-fetch by ID — documents leakage in shared-DB scenario
	ghostB, err := storeA.GetIssue(ctx, issueB.ID)
	if err == nil && ghostB != nil {
		t.Logf("Expected: project A can fetch project B's issue %s in shared-DB mode", issueB.ID)
	}
	ghostA, err := storeB.GetIssue(ctx, issueA.ID)
	if err == nil && ghostA != nil {
		t.Logf("Expected: project B can fetch project A's issue %s in shared-DB mode", issueA.ID)
	}

	if len(allA) == 1 && len(allB) == 1 {
		t.Log("Shared-DB isolation achieved (unexpected but good)")
	}
}

// =============================================================================
// Test 3: Read Isolation — Same Prefix (separate databases)
// =============================================================================

func TestCrossProject_ReadIsolation_SamePrefix(t *testing.T) {
	storeA, storeB, cleanup := setupTwoProjectStores(t, "beads", "beads")
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	issueA := &types.Issue{
		Title:       "Project A Issue",
		Description: "Belongs to project A only",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
	}
	if err := storeA.CreateIssue(ctx, issueA, "project-a"); err != nil {
		t.Fatalf("project A create failed: %v", err)
	}

	issueB := &types.Issue{
		Title:       "Project B Issue",
		Description: "Belongs to project B only",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
	}
	if err := storeB.CreateIssue(ctx, issueB, "project-b"); err != nil {
		t.Fatalf("project B create failed: %v", err)
	}

	allA, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project A SearchIssues failed: %v", err)
	}
	if len(allA) != 1 {
		t.Errorf("project A: expected 1 issue, got %d", len(allA))
	}

	allB, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project B SearchIssues failed: %v", err)
	}
	if len(allB) != 1 {
		t.Errorf("project B: expected 1 issue, got %d", len(allB))
	}

	if issueA.ID == issueB.ID {
		t.Errorf("COLLISION: both projects generated the same issue ID: %s", issueA.ID)
	}

	ghostB, err := storeA.GetIssue(ctx, issueB.ID)
	if err == nil && ghostB != nil {
		t.Errorf("DATA LEAKAGE: project A can fetch project B's issue %s", issueB.ID)
	}
	ghostA, err := storeB.GetIssue(ctx, issueA.ID)
	if err == nil && ghostA != nil {
		t.Errorf("DATA LEAKAGE: project B can fetch project A's issue %s", issueA.ID)
	}

	t.Logf("Same-prefix isolation verified: A=%s, B=%s", issueA.ID, issueB.ID)
}

// =============================================================================
// Test 4: Concurrent Cross-Project Writes (separate databases)
// =============================================================================

func TestCrossProject_ConcurrentWrites(t *testing.T) {
	storeA, storeB, cleanup := setupTwoProjectStores(t, "proj-a", "proj-b")
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const numIssuesPerProject = 10
	const maxRetries = 5

	var wg sync.WaitGroup
	aIDs := make(chan string, numIssuesPerProject)
	bIDs := make(chan string, numIssuesPerProject)
	errs := make(chan error, numIssuesPerProject*2)

	for i := 0; i < numIssuesPerProject; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for attempt := 0; attempt <= maxRetries; attempt++ {
				issue := &types.Issue{
					Title:       fmt.Sprintf("A-concurrent-%d", n),
					Description: fmt.Sprintf("Project A goroutine %d", n),
					Status:      types.StatusOpen,
					Priority:    2,
					IssueType:   types.TypeTask,
				}
				err := storeA.CreateIssue(ctx, issue, fmt.Sprintf("a-worker-%d", n))
				if err == nil {
					aIDs <- issue.ID
					return
				}
				if isSerializationError(err) && attempt < maxRetries {
					time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
					continue
				}
				errs <- fmt.Errorf("project A goroutine %d: %w", n, err)
				return
			}
		}(i)
	}

	for i := 0; i < numIssuesPerProject; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for attempt := 0; attempt <= maxRetries; attempt++ {
				issue := &types.Issue{
					Title:       fmt.Sprintf("B-concurrent-%d", n),
					Description: fmt.Sprintf("Project B goroutine %d", n),
					Status:      types.StatusOpen,
					Priority:    3,
					IssueType:   types.TypeTask,
				}
				err := storeB.CreateIssue(ctx, issue, fmt.Sprintf("b-worker-%d", n))
				if err == nil {
					bIDs <- issue.ID
					return
				}
				if isSerializationError(err) && attempt < maxRetries {
					time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
					continue
				}
				errs <- fmt.Errorf("project B goroutine %d: %w", n, err)
				return
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timeout — possible deadlock during concurrent cross-project writes")
	}

	close(aIDs)
	close(bIDs)
	close(errs)

	var errCount int
	for err := range errs {
		t.Errorf("write error: %v", err)
		errCount++
	}
	if errCount > 0 {
		t.Fatalf("%d goroutines failed", errCount)
	}

	aIDSet := make(map[string]bool)
	for id := range aIDs {
		aIDSet[id] = true
	}
	bIDSet := make(map[string]bool)
	for id := range bIDs {
		bIDSet[id] = true
	}

	t.Logf("Created: %d issues in A, %d issues in B", len(aIDSet), len(bIDSet))

	allA, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project A SearchIssues failed: %v", err)
	}
	allB, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("project B SearchIssues failed: %v", err)
	}

	if len(allA) != numIssuesPerProject {
		t.Errorf("project A: expected %d issues, got %d", numIssuesPerProject, len(allA))
	}
	if len(allB) != numIssuesPerProject {
		t.Errorf("project B: expected %d issues, got %d", numIssuesPerProject, len(allB))
	}

	var leakCount int
	for _, issue := range allA {
		if bIDSet[issue.ID] {
			t.Errorf("DATA LEAKAGE: project A contains project B's issue %s", issue.ID)
			leakCount++
		}
	}
	for _, issue := range allB {
		if aIDSet[issue.ID] {
			t.Errorf("DATA LEAKAGE: project B contains project A's issue %s", issue.ID)
			leakCount++
		}
	}

	if leakCount == 0 {
		t.Logf("Concurrent isolation verified: %d+%d issues, zero leakage", len(allA), len(allB))
	}
}

// =============================================================================
// Test 5: Concurrent Read-Write Mix Across Projects (separate databases)
// =============================================================================

func TestCrossProject_ConcurrentReadWriteMix(t *testing.T) {
	storeA, storeB, cleanup := setupTwoProjectStores(t, "reader", "writer")
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const seedIssues = 3
	for i := 0; i < seedIssues; i++ {
		issueA := &types.Issue{
			Title:       fmt.Sprintf("A-seed-%d", i),
			Description: "Seeded by project A",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := storeA.CreateIssue(ctx, issueA, "seed-a"); err != nil {
			t.Fatalf("seed A issue %d: %v", i, err)
		}
		issueB := &types.Issue{
			Title:       fmt.Sprintf("B-seed-%d", i),
			Description: "Seeded by project B",
			Status:      types.StatusOpen,
			Priority:    3,
			IssueType:   types.TypeTask,
		}
		if err := storeB.CreateIssue(ctx, issueB, "seed-b"); err != nil {
			t.Fatalf("seed B issue %d: %v", i, err)
		}
	}

	const iterations = 20
	var wg sync.WaitGroup
	var leakDetected atomic.Int32

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			issue := &types.Issue{
				Title:       fmt.Sprintf("A-concurrent-write-%d", i),
				Description: "Written by A during concurrent read-write",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
			}
			if err := storeA.CreateIssue(ctx, issue, "a-writer"); err != nil {
				if !isSerializationError(err) {
					t.Logf("A write error (non-serialization): %v", err)
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			issues, err := storeB.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				continue
			}
			for _, issue := range issues {
				if issue.Description == "Written by A during concurrent read-write" ||
					issue.Description == "Seeded by project A" {
					leakDetected.Add(1)
					t.Errorf("DATA LEAKAGE: project B read contains project A's issue: %s %q",
						issue.ID, issue.Title)
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			issue := &types.Issue{
				Title:       fmt.Sprintf("B-concurrent-write-%d", i),
				Description: "Written by B during concurrent read-write",
				Status:      types.StatusOpen,
				Priority:    3,
				IssueType:   types.TypeTask,
			}
			if err := storeB.CreateIssue(ctx, issue, "b-writer"); err != nil {
				if !isSerializationError(err) {
					t.Logf("B write error (non-serialization): %v", err)
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			issues, err := storeA.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				continue
			}
			for _, issue := range issues {
				if issue.Description == "Written by B during concurrent read-write" ||
					issue.Description == "Seeded by project B" {
					leakDetected.Add(1)
					t.Errorf("DATA LEAKAGE: project A read contains project B's issue: %s %q",
						issue.ID, issue.Title)
				}
			}
		}
	}()

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-ctx.Done():
		t.Fatal("timeout — possible deadlock during concurrent read-write mix")
	}

	if leakDetected.Load() == 0 {
		t.Log("Concurrent read-write isolation verified: zero cross-project leakage")
	} else {
		t.Errorf("TOTAL LEAKAGE EVENTS: %d", leakDetected.Load())
	}
}
