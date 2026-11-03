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
//   - Project-identity verification on connect (GH#4637): a CreateIfMissing:true
//     open reconnecting to an already-existing database with a mismatched local
//     project_id must fail, not silently share the database
//
// Based on tests by @PabloLION (PR #2472).
package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// setupTwoProjectStores creates two DoltStore instances on the same server,
// each with its own database and issue prefix — simulating two beads projects
// sharing a Dolt server (either intentionally or via port collision).
func setupTwoProjectStores(t *testing.T, prefixA, prefixB string) (storeA, storeB *DoltStore, cleanup func()) {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

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

	// Every call here gets its own budget. A shared context would not work even
	// though only the opens are expensive: deadlines are absolute, so the two
	// cold opens would drain a context created here and the cheap SetConfig
	// calls would be the ones to report "context deadline exceeded".
	storeA, err = openStoreWithOwnBudget(t, cfgA)
	if err != nil {
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to create store A: %v", err)
	}

	if err := setConfigWithOwnBudget(t, storeA, "issue_prefix", prefixA); err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to set prefix for project A: %v", err)
	}

	storeB, err = openStoreWithOwnBudget(t, cfgB)
	if err != nil {
		storeA.Close()
		os.RemoveAll(tmpDirA)
		os.RemoveAll(tmpDirB)
		t.Fatalf("failed to create store B: %v", err)
	}

	if err := setConfigWithOwnBudget(t, storeB, "issue_prefix", prefixB); err != nil {
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

	storeA, err := openStoreWithOwnBudget(t, cfgA)
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

	storeB, err := openStoreWithOwnBudget(t, cfgB)
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

// =============================================================================
// Test 6: Project-Identity Verification on Connect (GH#4637 Part A)
// =============================================================================
//
// newServerMode's identity gate previously only ran when CreateIfMissing was
// false. `bd init` always sets CreateIfMissing:true, so it never verified
// identity at all — reconnecting to an already-existing database with a
// mismatched local project_id (the shape of `bd init` accidentally targeting
// another project's shared-server database) was silently accepted. These
// tests cover the fixed gate and the cases that must keep working unchanged:
// a genuinely new database, and each of verifyProjectIdentity's soft-skip
// paths (missing local metadata.json, empty project_id on either side).

// identityTestFixture holds a temp beadsDir wired up for identity-check tests:
// a Path for the Dolt client data dir, and a BeadsDir so verifyProjectIdentity
// can find metadata.json.
type identityTestFixture struct {
	tmpDir   string
	beadsDir string
	dbName   string
}

func newIdentityTestFixture(t *testing.T, namePrefix string) *identityTestFixture {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "dolt-identity-"+namePrefix+"-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beadsDir: %v", err)
	}
	return &identityTestFixture{
		tmpDir:   tmpDir,
		beadsDir: beadsDir,
		dbName:   uniqueTestDBName(t),
	}
}

func (f *identityTestFixture) saveLocalProjectID(t *testing.T, projectID string) {
	t.Helper()
	if err := (&configfile.Config{ProjectID: projectID}).Save(f.beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
}

func (f *identityTestFixture) config() *Config {
	return &Config{
		Path:            filepath.Join(f.tmpDir, "dolt"),
		BeadsDir:        f.beadsDir,
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		Database:        f.dbName,
		CreateIfMissing: true,
	}
}

func TestCrossProject_IdentityCheck_ExistingDatabase_ForeignRejected(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "foreign")
	ownerID := "11111111-1111-1111-1111-111111111111"
	f.saveLocalProjectID(t, ownerID)

	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	if err := ownerStore.SetMetadata(ctx, "_project_id", ownerID); err != nil {
		ownerStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	ownerStore.Close()

	// A different project's local metadata.json (foreign project id)
	// reconnects to the SAME already-existing database with
	// CreateIfMissing:true — the exact shape of `bd init` against a shared
	// server. This must fail, not silently adopt the database.
	foreignID := "22222222-2222-2222-2222-222222222222"
	f.saveLocalProjectID(t, foreignID)

	foreignStore, err := openStoreWithOwnBudget(t, f.config())
	if err == nil {
		foreignStore.Close()
		t.Fatalf("expected identity mismatch error, got nil (silently connected to foreign project's database)")
	}
	if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
		t.Fatalf("expected PROJECT IDENTITY MISMATCH error, got: %v", err)
	}
	// CreateIfMissing alone is not bd init: the library API, bd doctor --fix
	// and bd bootstrap open this way too, and keep the ordinary-open advice
	// (GH#5558). Only Config.OpenedByInit selects the init wording.
	if strings.Contains(err.Error(), "refusing to initialize") {
		t.Errorf("a non-init CreateIfMissing open got the bd init wording: %v", err)
	}

	initCfg := f.config()
	initCfg.OpenedByInit = true
	initStore, err := openStoreWithOwnBudget(t, initCfg)
	if err == nil {
		initStore.Close()
		t.Fatalf("expected identity mismatch error for the bd init open, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to initialize") || strings.Contains(err.Error(), "Do NOT run 'bd init'") {
		t.Errorf("the bd init open did not get the init wording: %v", err)
	}
}

// TestCrossProject_IdentityCheck_ExistingDatabase_MatchingSucceeds is the
// positive control for the rejection test above: the same
// already-existing-database + CreateIfMissing:true shape, but with local
// metadata.json carrying the SAME project id the database already records.
// This must keep succeeding — the fix must not turn ordinary reconnects
// (e.g. a second `bd` invocation against the same project) into failures.
func TestCrossProject_IdentityCheck_ExistingDatabase_MatchingSucceeds(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "matching")
	projectID := "55555555-5555-5555-5555-555555555555"
	f.saveLocalProjectID(t, projectID)

	firstStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create first store: %v", err)
	}
	if err := firstStore.SetMetadata(ctx, "_project_id", projectID); err != nil {
		firstStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	firstStore.Close()

	secondStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("reconnect with matching project id must succeed, got: %v", err)
	}
	secondStore.Close()
}

func TestCrossProject_IdentityCheck_NewDatabase_Succeeds(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "new")
	f.saveLocalProjectID(t, "33333333-3333-3333-3333-333333333333")

	store, err := New(ctx, f.config())
	if err != nil {
		t.Fatalf("creating a brand-new database must still succeed: %v", err)
	}
	store.Close()
}

// TestCrossProject_IdentityCheck_SoftSkip_NoLocalMetadata covers
// verifyProjectIdentity's soft-skip when the reopening side has no local
// metadata.json at all (localID == ""): this must not be turned into a hard
// failure by the CreateIfMissing:true/dbAlreadyExisted gate.
func TestCrossProject_IdentityCheck_SoftSkip_NoLocalMetadata(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "nolocal")

	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	if err := ownerStore.SetMetadata(ctx, "_project_id", "44444444-4444-4444-4444-444444444444"); err != nil {
		ownerStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	ownerStore.Close()

	// No metadata.json written for this reopen — configfile.Load returns nil.
	reopenStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("expected soft-skip (no local metadata.json), got error: %v", err)
	}
	reopenStore.Close()
}

// TestCrossProject_IdentityCheck_SoftSkip_EmptyDBProjectID covers
// verifyProjectIdentity's other soft-skip: a local project_id is set, but the
// database itself has no _project_id recorded (an old-style database that
// predates the identity field). This must not be turned into a hard failure
// either — only a database that records a DIFFERENT non-empty _project_id is
// rejected.
func TestCrossProject_IdentityCheck_SoftSkip_EmptyDBProjectID(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	f := newIdentityTestFixture(t, "emptydb")

	// Create the database but never seed _project_id — simulating an
	// old-style database from before GH#2372.
	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	ownerStore.Close()

	f.saveLocalProjectID(t, "66666666-6666-6666-6666-666666666666")
	reopenStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("expected soft-skip (database has no _project_id), got error: %v", err)
	}
	reopenStore.Close()
}

// TestCrossProject_IdentityCheck_Gateway_SkipsVerification covers the one
// branch newServerMode's identity gate deliberately does NOT enforce:
// Gateway mode. openServerConnection never probes existence for a gateway
// database (see its Gateway branch), so it always reports dbAlreadyExisted
// == false and the CreateIfMissing:true/dbAlreadyExisted check never fires
// for Gateway — even though the database plainly does already exist
// server-side. This is intentional: cmd/bd/init.go's resolveInitProjectID
// reconciles a stale or mismatched local project_id onto the
// server-authoritative one on every gateway init instead of failing at open.
// A future cleanup that "closes" the Gateway exemption here would break that
// reconciliation flow — this test exists so that change has to update this
// assertion, not just delete a condition.
func TestCrossProject_IdentityCheck_Gateway_SkipsVerification(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "gateway")
	ownerID := "77777777-7777-7777-7777-777777777777"
	f.saveLocalProjectID(t, ownerID)

	// Seed schema and _project_id with a normal (non-gateway) open first.
	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	if err := ownerStore.SetMetadata(ctx, "_project_id", ownerID); err != nil {
		ownerStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	ownerStore.Close()

	// Reopen the SAME already-existing database in Gateway mode, with a
	// mismatched local project_id. A non-gateway CreateIfMissing:true open in
	// this shape is rejected (see the ForeignRejected test above) — Gateway
	// must NOT be, because identity is reconciled by resolveInitProjectID,
	// not enforced here.
	mismatchedID := "88888888-8888-8888-8888-888888888888"
	f.saveLocalProjectID(t, mismatchedID)

	gatewayCfg := f.config()
	gatewayCfg.Gateway = true
	gatewayStore, err := openStoreWithOwnBudget(t, gatewayCfg)
	if err != nil {
		t.Fatalf("gateway open must skip identity verification (reconciled by resolveInitProjectID instead), got error: %v", err)
	}
	gatewayStore.Close()
}

// --- verifyProjectIdentity hermetic coverage (sqlmock) ---
//
// The Dolt-backed tests above exercise the verifier through a real open;
// these call verifyProjectIdentity itself against a sqlmock db, so read
// failures that are hard to stage against a live server — an access denial,
// a corrupted metadata table, a warming-window connection error — each pin
// the verifier's own decision.

// newIdentityUnitStore builds the fields verifyProjectIdentity reads: a
// sqlmock db, the database name its refusals name, and a temp beadsDir whose
// metadata.json records localID (saved even when empty — an empty local
// project_id is the old-style skip, a distinct case from no metadata.json).
func newIdentityUnitStore(t *testing.T, localID, database string) (*DoltStore, sqlmock.Sqlmock, string) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	beadsDir := t.TempDir()
	if err := (&configfile.Config{ProjectID: localID}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	return &DoltStore{db: db, database: database}, mock, beadsDir
}

// expectIdentityRead stages one GetMetadata("_project_id") read: the read
// transaction's Begin, the query, and the Rollback that always follows. A
// nil value with a nil err answers with no rows (the key is absent); a
// non-nil err fails the query itself.
func expectIdentityRead(mock sqlmock.Sqlmock, value *string, err error) {
	mock.ExpectBegin()
	q := mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM metadata WHERE `key` = ?")).
		WithArgs("_project_id")
	switch {
	case err != nil:
		q.WillReturnError(err)
	case value == nil:
		q.WillReturnRows(sqlmock.NewRows([]string{"value"}))
	default:
		q.WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(*value))
	}
	mock.ExpectRollback()
}

// TestVerifyProjectIdentity_ReadErrors pins the verifier's decision on a
// failed identity read (bd-8b6e39 / PR #7565): a database whose _project_id
// cannot be read offers a failed proof, not an empty (old-style) one, so an
// applicable check must refuse the open with a named, greppable error that
// still wraps the underlying cause — not silently skip.
func TestVerifyProjectIdentity_ReadErrors(t *testing.T) {
	const localID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const database = "shared_db"

	expectRefusal := func(t *testing.T, err error, wantCause error) {
		t.Helper()
		if err == nil {
			t.Fatalf("verifyProjectIdentity() = nil; a failed identity read must refuse, not silently skip")
		}
		for _, want := range []string{
			"cannot verify project identity",
			`"` + database + `"`,
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal missing %q:\n%s", want, err)
			}
		}
		if wantCause != nil && !errors.Is(err, wantCause) {
			t.Errorf("refusal does not wrap the underlying cause:\n%v", err)
		}
	}

	t.Run("access denial refuses", func(t *testing.T) {
		denied := &mysql.MySQLError{
			Number:  1142,
			Message: "SELECT command denied to user 'bd'@'%' for table 'metadata'",
		}
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, denied)
		expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, false), denied)
	})

	t.Run("generic read error refuses", func(t *testing.T) {
		cause := errors.New("metadata unreadable: disk error")
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, cause)
		expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, false), cause)
	})

	t.Run("refusal covers init and normal opens", func(t *testing.T) {
		for _, openedByInit := range []bool{false, true} {
			cause := errors.New("metadata unreadable: disk error")
			store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
			expectIdentityRead(mock, nil, cause)
			expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, openedByInit), cause)
		}
	})

	// The one read failure that keeps skipping: a database with no bd schema
	// yet has no metadata table at all (a pre-created database, or a
	// CREATE-race loser), and only that specifically classified absence is
	// compatibility — every other missing table, an unidentifiable 1146, and
	// a missing column still refuse.
	expectSkip := func(t *testing.T, err error) {
		t.Helper()
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, err)
		if got := store.verifyProjectIdentity(context.Background(), beadsDir, false); got != nil {
			t.Errorf("verifyProjectIdentity() = %v; an absent metadata table is the pre-created-database skip", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	}

	t.Run("absent metadata table skips, every classified spelling", func(t *testing.T) {
		for name, err := range map[string]error{
			"stock MySQL qualified": &mysql.MySQLError{
				Number:  1146,
				Message: "Table 'shared_db.metadata' doesn't exist",
			},
			"backticked": &mysql.MySQLError{
				Number:  1146,
				Message: "Table `metadata` doesn't exist",
			},
			"go-mysql-server": &mysql.MySQLError{
				Number:  1146,
				Message: "table not found: metadata",
			},
		} {
			t.Run(name, func(t *testing.T) {
				expectSkip(t, err)
			})
		}
	})

	t.Run("missing other table refuses", func(t *testing.T) {
		cause := &mysql.MySQLError{
			Number:  1146,
			Message: "Table 'shared_db.issues' doesn't exist",
		}
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, cause)
		expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, false), cause)
	})

	t.Run("unidentifiable 1146 refuses", func(t *testing.T) {
		cause := &mysql.MySQLError{
			Number:  1146,
			Message: "Table doesn't exist",
		}
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, cause)
		expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, false), cause)
	})

	t.Run("missing column refuses", func(t *testing.T) {
		cause := &mysql.MySQLError{
			Number:  1054,
			Message: "Unknown column 'value' in 'field list'",
		}
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, cause)
		expectRefusal(t, store.verifyProjectIdentity(context.Background(), beadsDir, false), cause)
	})

	// Existing outcomes the read-error refusal must leave unchanged: a
	// usable identity read decides as before, and the local soft-skips
	// (no metadata.json at all, an empty local project id) must perform no
	// SQL — an unexpected query would come back from sqlmock as an error
	// and fail these.
	value := func(s string) *string { return &s }

	t.Run("absent key skips", func(t *testing.T) {
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, nil, nil)
		if err := store.verifyProjectIdentity(context.Background(), beadsDir, false); err != nil {
			t.Errorf("verifyProjectIdentity() = %v; want the absent-key skip", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	})

	t.Run("empty database identity skips", func(t *testing.T) {
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, value(""), nil)
		if err := store.verifyProjectIdentity(context.Background(), beadsDir, false); err != nil {
			t.Errorf("verifyProjectIdentity() = %v; want the old-style-database skip", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	})

	t.Run("matching identity passes", func(t *testing.T) {
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, value(localID), nil)
		if err := store.verifyProjectIdentity(context.Background(), beadsDir, false); err != nil {
			t.Errorf("verifyProjectIdentity() = %v; want the matching identity to pass", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	})

	t.Run("mismatched identity refuses with the mismatch error", func(t *testing.T) {
		store, mock, beadsDir := newIdentityUnitStore(t, localID, database)
		expectIdentityRead(mock, value("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), nil)
		err := store.verifyProjectIdentity(context.Background(), beadsDir, false)
		if err == nil {
			t.Fatal("verifyProjectIdentity() = nil; want the existing mismatch refusal")
		}
		if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
			t.Errorf("refusal missing the existing mismatch headline:\n%s", err)
		}
	})

	t.Run("no local metadata skips without SQL", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store := &DoltStore{db: db, database: database}
		beadsDir := t.TempDir() // no metadata.json at all
		if err := store.verifyProjectIdentity(context.Background(), beadsDir, false); err != nil {
			t.Errorf("verifyProjectIdentity() = %v; want the no-local-metadata skip", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	})

	t.Run("empty local id skips without SQL", func(t *testing.T) {
		store, mock, beadsDir := newIdentityUnitStore(t, "", database)
		if err := store.verifyProjectIdentity(context.Background(), beadsDir, false); err != nil {
			t.Errorf("verifyProjectIdentity() = %v; want the empty-local-id skip", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled sqlmock expectations: %v", err)
		}
	})
}

// TestCrossProject_IdentityCheck_ReadErrorRefuses covers the verifier's
// refusal on a database whose _project_id cannot be READ (bd-8b6e39 /
// PR #7565). The metadata table exists but cannot yield the identity — here
// it lost the `value` column the verifier selects — so unlike an absent
// metadata table (the preserved pre-created-database compatibility skip,
// pinned by TestCrossProject_IdentityCheck_SoftSkip_NoBdSchema), this is a
// damaged read, not a schema-less database, and the open must refuse with
// the named error instead of treating the failure like an empty (old-style)
// _project_id. The damaged table is also proven unchanged after the refused
// opens: the refusal fires before any schema write. (The table is dropped
// and recreated within this disposable per-test database; DROP DATABASE is
// never run against the shared container.)
func TestCrossProject_IdentityCheck_ReadErrorRefuses(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "readerr")
	ownerID := "77777777-7777-7777-7777-777777777777"
	f.saveLocalProjectID(t, ownerID)

	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	if err := ownerStore.SetMetadata(ctx, "_project_id", ownerID); err != nil {
		ownerStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	if _, err := ownerStore.db.ExecContext(ctx, "DROP TABLE metadata"); err != nil {
		ownerStore.Close()
		t.Fatalf("drop metadata table: %v", err)
	}
	if _, err := ownerStore.db.ExecContext(ctx,
		"CREATE TABLE metadata (`key` varchar(255) primary key)"); err != nil {
		ownerStore.Close()
		t.Fatalf("recreate damaged metadata table: %v", err)
	}
	ownerStore.Close()

	for _, openedByInit := range []bool{false, true} {
		cfg := f.config()
		cfg.OpenedByInit = openedByInit
		reopenStore, reopenErr := openStoreWithOwnBudget(t, cfg)
		if reopenErr == nil {
			reopenStore.Close()
			t.Fatalf("open with OpenedByInit=%v: expected a 'cannot verify project identity' refusal, got nil (identity check silently skipped)", openedByInit)
		}
		if !strings.Contains(reopenErr.Error(), "cannot verify project identity") {
			t.Errorf("open with OpenedByInit=%v: expected a 'cannot verify project identity' refusal, got: %v", openedByInit, reopenErr)
		}
	}

	// Refusal before schema writes: the damaged table is exactly as we left
	// it — still present, and still unreadable the way the verifier reads it.
	raw := rawTestConn(t, testServerPort)
	defer raw.Close()
	var rows int
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+f.dbName+"`.metadata").Scan(&rows); err != nil {
		t.Fatalf("the damaged metadata table must still exist after the refused opens: %v", err)
	}
	var value string
	if err := raw.QueryRowContext(ctx,
		"SELECT value FROM `"+f.dbName+"`.metadata WHERE `key` = '_project_id'").Scan(&value); err == nil {
		t.Fatalf("the damaged metadata table was repaired during the refused open; the refusal must fire before any schema write")
	}
}

// TestCrossProject_IdentityCheck_SoftSkip_NoBdSchema pins the preserved
// compatibility exception the read-error refusal (bd-8b6e39 / PR #7565) must
// not break: a pre-created database (docs/cli-reference/init.md) has no bd
// schema at all, so identity verification — which runs before
// initialization, and which the CREATE-race path can also reach — reads no
// metadata table. That absence must keep skipping even with a non-empty
// local project id and the database already existing, for both values of
// CreateIfMissing and OpenedByInit, and the open must then initialize the
// schema so metadata queries work afterwards. Each variant gets its own bare
// database and its own local metadata, since the first open initializes.
func TestCrossProject_IdentityCheck_SoftSkip_NoBdSchema(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	const localID = "99999999-9999-9999-9999-999999999999"
	variants := []struct {
		name           string
		createIfMissing bool
		openedByInit   bool
	}{
		{"connect existing", false, false},
		{"init existing", false, true},
		{"create existing", true, false},
		{"init create existing", true, true},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			f := newIdentityTestFixture(t, "noschema")
			f.saveLocalProjectID(t, localID)
			// A bare database: created on the server, no bd schema inside.
			createTestDatabase(t, testServerPort, f.dbName)

			cfg := f.config()
			cfg.CreateIfMissing = v.createIfMissing
			cfg.OpenedByInit = v.openedByInit
			store, err := openStoreWithOwnBudget(t, cfg)
			if err != nil {
				t.Fatalf("open against a pre-created schema-less database must keep succeeding, got: %v", err)
			}
			defer store.Close()

			// The open initialized the schema: metadata queries work.
			if got, err := store.GetMetadata(ctx, "_project_id"); err != nil || got != "" {
				t.Errorf("GetMetadata(_project_id) = %q, %v; want the absent key after initialization", got, err)
			}
			if err := store.SetMetadata(ctx, "_project_id", localID); err != nil {
				t.Errorf("SetMetadata(_project_id) after initialization: %v", err)
			}
			if got, err := store.GetMetadata(ctx, "_project_id"); err != nil || got != localID {
				t.Errorf("GetMetadata(_project_id) = %q, %v; want %q", got, err, localID)
			}
		})
	}
}
