package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestRelatesTo verifies relates-to dependencies work via the dependency API.
// Per Decision 004, relates-to links are now stored in the dependencies table.
func TestRelatesTo(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create two issues
	issue1 := &types.Issue{
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue2 := &types.Issue{
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("Failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("Failed to create issue2: %v", err)
	}

	// Add relates-to dependency (bidirectional)
	dep1 := &types.Dependency{
		IssueID:     issue1.ID,
		DependsOnID: issue2.ID,
		Type:        types.DepRelatesTo,
	}
	if err := store.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatalf("Failed to add relates-to dep1: %v", err)
	}
	dep2 := &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepRelatesTo,
	}
	if err := store.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("Failed to add relates-to dep2: %v", err)
	}

	// Verify links via GetDependenciesWithMetadata
	deps1, err := store.GetDependenciesWithMetadata(ctx, issue1.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found1 := false
	for _, d := range deps1 {
		if d.ID == issue2.ID && d.DependencyType == types.DepRelatesTo {
			found1 = true
		}
	}
	if !found1 {
		t.Errorf("issue1 should have relates-to link to issue2")
	}

	deps2, err := store.GetDependenciesWithMetadata(ctx, issue2.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found2 := false
	for _, d := range deps2 {
		if d.ID == issue1.ID && d.DependencyType == types.DepRelatesTo {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("issue2 should have relates-to link to issue1")
	}
}

func TestRelatesTo_MultipleLinks(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create three issues
	issues := make([]*types.Issue, 3)
	for i := range issues {
		issues[i] = &types.Issue{
			Title:     "Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, issues[i], "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
	}

	// Link issue0 to both issue1 and issue2
	for _, targetIssue := range []*types.Issue{issues[1], issues[2]} {
		dep := &types.Dependency{
			IssueID:     issues[0].ID,
			DependsOnID: targetIssue.ID,
			Type:        types.DepRelatesTo,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("Failed to add relates-to: %v", err)
		}
	}

	// Verify
	deps, err := store.GetDependenciesWithMetadata(ctx, issues[0].ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	relatesCount := 0
	for _, d := range deps {
		if d.DependencyType == types.DepRelatesTo {
			relatesCount++
		}
	}
	if relatesCount != 2 {
		t.Errorf("RelatesTo has %d links, want 2", relatesCount)
	}
}

// TestDuplicateOf verifies duplicates dependencies work via the dependency API.
func TestDuplicateOf(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create canonical and duplicate issues
	canonical := &types.Issue{
		Title:     "Canonical Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	duplicate := &types.Issue{
		Title:     "Duplicate Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, canonical, "test"); err != nil {
		t.Fatalf("Failed to create canonical: %v", err)
	}
	if err := store.CreateIssue(ctx, duplicate, "test"); err != nil {
		t.Fatalf("Failed to create duplicate: %v", err)
	}

	// Add duplicates dependency
	dep := &types.Dependency{
		IssueID:     duplicate.ID,
		DependsOnID: canonical.ID,
		Type:        types.DepDuplicates,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add duplicates dep: %v", err)
	}

	// Close the duplicate
	if err := store.CloseIssue(ctx, duplicate.ID, "Closed as duplicate", "test", ""); err != nil {
		t.Fatalf("Failed to close duplicate: %v", err)
	}

	// Verify dependency
	deps, err := store.GetDependenciesWithMetadata(ctx, duplicate.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found := false
	for _, d := range deps {
		if d.ID == canonical.ID && d.DependencyType == types.DepDuplicates {
			found = true
		}
	}
	if !found {
		t.Errorf("duplicate should have duplicates link to canonical")
	}

	// Verify closed status
	updated, err := store.GetIssue(ctx, duplicate.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if updated.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", updated.Status, types.StatusClosed)
	}
}

// TestSupersededBy verifies supersedes dependencies work via the dependency API.
func TestSupersededBy(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create old and new versions
	oldVersion := &types.Issue{
		Title:     "Design Doc v1",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	newVersion := &types.Issue{
		Title:     "Design Doc v2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, oldVersion, "test"); err != nil {
		t.Fatalf("Failed to create old version: %v", err)
	}
	if err := store.CreateIssue(ctx, newVersion, "test"); err != nil {
		t.Fatalf("Failed to create new version: %v", err)
	}

	// Add supersedes dependency (newVersion supersedes oldVersion)
	// Stored as: oldVersion depends on newVersion with type supersedes
	dep := &types.Dependency{
		IssueID:     oldVersion.ID,
		DependsOnID: newVersion.ID,
		Type:        types.DepSupersedes,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add supersedes dep: %v", err)
	}

	// Close old version
	if err := store.CloseIssue(ctx, oldVersion.ID, "Superseded by v2", "test", ""); err != nil {
		t.Fatalf("Failed to close old version: %v", err)
	}

	// Verify dependency
	deps, err := store.GetDependenciesWithMetadata(ctx, oldVersion.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found := false
	for _, d := range deps {
		if d.ID == newVersion.ID && d.DependencyType == types.DepSupersedes {
			found = true
		}
	}
	if !found {
		t.Errorf("oldVersion should have supersedes link to newVersion")
	}

	// Verify closed status
	updated, err := store.GetIssue(ctx, oldVersion.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if updated.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", updated.Status, types.StatusClosed)
	}
}

// TestRepliesTo verifies replies-to dependencies work via the dependency API.
func TestRepliesTo(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create original message and reply
	original := &types.Issue{
		Title:       "Original Message",
		Description: "Original content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "message",
		Sender:      "alice",
		Assignee:    "bob",
		Ephemeral:        true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	reply := &types.Issue{
		Title:       "Re: Original Message",
		Description: "Reply content",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "message",
		Sender:      "bob",
		Assignee:    "alice",
		Ephemeral:        true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, original, "test"); err != nil {
		t.Fatalf("Failed to create original: %v", err)
	}
	if err := store.CreateIssue(ctx, reply, "test"); err != nil {
		t.Fatalf("Failed to create reply: %v", err)
	}

	// Add replies-to dependency
	dep := &types.Dependency{
		IssueID:     reply.ID,
		DependsOnID: original.ID,
		Type:        types.DepRepliesTo,
		ThreadID:    original.ID, // Thread root is the original message
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add replies-to dep: %v", err)
	}

	// Verify thread link
	deps, err := store.GetDependenciesWithMetadata(ctx, reply.ID)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	found := false
	for _, d := range deps {
		if d.ID == original.ID && d.DependencyType == types.DepRepliesTo {
			found = true
		}
	}
	if !found {
		t.Errorf("reply should have replies-to link to original")
	}
}

func TestRepliesTo_Chain(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a chain of replies
	messages := make([]*types.Issue, 3)
	var prevID string

	for i := range messages {
		messages[i] = &types.Issue{
			Title:     "Message",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: "message",
			Sender:    "user",
			Assignee:  "inbox",
			Ephemeral: true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, messages[i], "test"); err != nil {
			t.Fatalf("Failed to create message %d: %v", i, err)
		}

		// Add replies-to dependency for subsequent messages
		if prevID != "" {
			dep := &types.Dependency{
				IssueID:     messages[i].ID,
				DependsOnID: prevID,
				Type:        types.DepRepliesTo,
				ThreadID:    messages[0].ID, // Thread root is the first message
			}
			if err := store.AddDependency(ctx, dep, "test"); err != nil {
				t.Fatalf("Failed to add replies-to dep for message %d: %v", i, err)
			}
		}
		prevID = messages[i].ID
	}

	// Verify chain by checking dependents
	for i := 0; i < len(messages)-1; i++ {
		dependents, err := store.GetDependentsWithMetadata(ctx, messages[i].ID)
		if err != nil {
			t.Fatalf("GetDependentsWithMetadata failed for message %d: %v", i, err)
		}
		found := false
		for _, d := range dependents {
			if d.ID == messages[i+1].ID && d.DependencyType == types.DepRepliesTo {
				found = true
			}
		}
		if !found {
			t.Errorf("Message %d should have reply from message %d", i, i+1)
		}
	}
}

func TestWispField(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create wisp issue
	wisp := &types.Issue{
		Title:     "Wisp Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Ephemeral:      true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Create non-wisp issue
	permanent := &types.Issue{
		Title:     "Permanent Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral:      false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("Failed to create wisp: %v", err)
	}
	if err := store.CreateIssue(ctx, permanent, "test"); err != nil {
		t.Fatalf("Failed to create permanent: %v", err)
	}

	// Verify wisp flag
	savedWisp, err := store.GetIssue(ctx, wisp.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if !savedWisp.Ephemeral {
		t.Error("Wisp issue should have Wisp=true")
	}

	savedPermanent, err := store.GetIssue(ctx, permanent.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if savedPermanent.Ephemeral {
		t.Error("Permanent issue should have Wisp=false")
	}
}

func TestWispFilter(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create mix of wisp and non-wisp issues
	for i := 0; i < 3; i++ {
		wisp := &types.Issue{
			Title:     "Wisp",
			Status:    types.StatusClosed, // Closed for cleanup test
			Priority:  2,
			IssueType: "message",
			Ephemeral:      true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("Failed to create wisp %d: %v", i, err)
		}
	}

	for i := 0; i < 2; i++ {
		permanent := &types.Issue{
			Title:     "Permanent",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral:      false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := store.CreateIssue(ctx, permanent, "test"); err != nil {
			t.Fatalf("Failed to create permanent %d: %v", i, err)
		}
	}

	// Filter for wisp only
	wispTrue := true
	closedStatus := types.StatusClosed
	wispFilter := types.IssueFilter{
		Status: &closedStatus,
		Ephemeral:   &wispTrue,
	}

	wispIssues, err := store.SearchIssues(ctx, "", wispFilter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(wispIssues) != 3 {
		t.Errorf("Expected 3 wisp issues, got %d", len(wispIssues))
	}

	// Filter for non-wisp only
	wispFalse := false
	nonWispFilter := types.IssueFilter{
		Status: &closedStatus,
		Ephemeral:   &wispFalse,
	}

	permanentIssues, err := store.SearchIssues(ctx, "", nonWispFilter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(permanentIssues) != 2 {
		t.Errorf("Expected 2 non-wisp issues, got %d", len(permanentIssues))
	}
}

func TestSenderField(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create issue with sender
	msg := &types.Issue{
		Title:     "Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		Sender:    "alice@example.com",
		Assignee:  "bob@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Verify sender is preserved
	saved, err := store.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if saved.Sender != "alice@example.com" {
		t.Errorf("Sender = %q, want %q", saved.Sender, "alice@example.com")
	}
}

func TestMessageType(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Create a message type issue
	msg := &types.Issue{
		Title:     "Test Message",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "message",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, msg, "test"); err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	// Verify type is preserved
	saved, err := store.GetIssue(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if saved.IssueType != "message" {
		t.Errorf("IssueType = %q, want %q", saved.IssueType, "message")
	}

	// Filter by message type
	messageType := types.IssueType("message")
	filter := types.IssueFilter{
		IssueType: &messageType,
	}

	messages, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}
}
