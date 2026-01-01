// Package memory implements the storage interface using in-memory data structures.
// This is designed for --no-db mode where the database is loaded from JSONL at startup
// and written back to JSONL after each command.
package memory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// MemoryStorage implements the Storage interface using in-memory data structures
type MemoryStorage struct {
	mu sync.RWMutex // Protects all maps

	// Core data
	issues       map[string]*types.Issue       // ID -> Issue
	dependencies map[string][]*types.Dependency // IssueID -> Dependencies
	labels       map[string][]string           // IssueID -> Labels
	events       map[string][]*types.Event     // IssueID -> Events
	comments     map[string][]*types.Comment   // IssueID -> Comments
	config       map[string]string             // Config key-value pairs
	metadata     map[string]string             // Metadata key-value pairs
	counters     map[string]int                // Prefix -> Last ID

	// Indexes for O(1) lookups
	externalRefToID map[string]string // ExternalRef -> IssueID

	// For tracking
	dirty map[string]bool // IssueIDs that have been modified

	jsonlPath string // Path to source JSONL file (for reference)
	closed    bool
}

// New creates a new in-memory storage backend
func New(jsonlPath string) *MemoryStorage {
	return &MemoryStorage{
		issues:          make(map[string]*types.Issue),
		dependencies:    make(map[string][]*types.Dependency),
		labels:          make(map[string][]string),
		events:          make(map[string][]*types.Event),
		comments:        make(map[string][]*types.Comment),
		config:          make(map[string]string),
		metadata:        make(map[string]string),
		counters:        make(map[string]int),
		externalRefToID: make(map[string]string),
		dirty:           make(map[string]bool),
		jsonlPath:       jsonlPath,
	}
}

// LoadFromIssues populates the in-memory storage from a slice of issues
// This is used when loading from JSONL at startup
func (m *MemoryStorage) LoadFromIssues(issues []*types.Issue) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, issue := range issues {
		if issue == nil {
			continue
		}

		// Store the issue
		m.issues[issue.ID] = issue

		// Index external ref for O(1) lookup
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			m.externalRefToID[*issue.ExternalRef] = issue.ID
		}

		// Store dependencies
		if len(issue.Dependencies) > 0 {
			m.dependencies[issue.ID] = issue.Dependencies
		}

		// Store labels
		if len(issue.Labels) > 0 {
			m.labels[issue.ID] = issue.Labels
		}

		// Store comments
		if len(issue.Comments) > 0 {
			m.comments[issue.ID] = issue.Comments
		}

		// Update counter based on issue ID
		prefix, num := extractPrefixAndNumber(issue.ID)
		if prefix != "" && num > 0 {
			if m.counters[prefix] < num {
				m.counters[prefix] = num
			}
		}

		// Update hierarchical child counters based on issue ID
		// e.g. "bd-a3f8e9.2" -> parent "bd-a3f8e9" counter 2
		if parentID, childNum, ok := extractParentAndChildNumber(issue.ID); ok {
			if m.counters[parentID] < childNum {
				m.counters[parentID] = childNum
			}
		}
	}

	return nil
}

// GetAllIssues returns all issues in memory (for export to JSONL)
func (m *MemoryStorage) GetAllIssues() []*types.Issue {
	m.mu.RLock()
	defer m.mu.RUnlock()

	issues := make([]*types.Issue, 0, len(m.issues))
	for _, issue := range m.issues {
		// Deep copy to avoid mutations
		issueCopy := *issue

		// Attach dependencies
		if deps, ok := m.dependencies[issue.ID]; ok {
			issueCopy.Dependencies = deps
		}

		// Attach labels
		if labels, ok := m.labels[issue.ID]; ok {
			issueCopy.Labels = labels
		}

		// Attach comments
		if comments, ok := m.comments[issue.ID]; ok {
			issueCopy.Comments = comments
		}

		issues = append(issues, &issueCopy)
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	return issues
}

// extractPrefixAndNumber extracts prefix and number from issue ID like "bd-123" -> ("bd", 123)
func extractPrefixAndNumber(id string) (string, int) {
	lastDash := strings.LastIndex(id, "-")
	if lastDash == -1 {
		return "", 0
	}

	prefix := id[:lastDash]
	suffix := id[lastDash+1:]

	var num int
	_, err := fmt.Sscanf(suffix, "%d", &num)
	if err != nil {
		return "", 0
	}
	return prefix, num
}

// extractParentAndChildNumber extracts the parent ID and numeric child counter from an issue ID like
// "bd-a3f8e9.2" -> ("bd-a3f8e9", 2, true).
func extractParentAndChildNumber(id string) (string, int, bool) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return "", 0, false
	}

	parentID := id[:lastDot]
	suffix := id[lastDot+1:]

	var num int
	if _, err := fmt.Sscanf(suffix, "%d", &num); err != nil {
		return "", 0, false
	}

	return parentID, num, true
}

// CreateIssue creates a new issue
func (m *MemoryStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Set timestamps
	now := time.Now()
	issue.CreatedAt = now
	issue.UpdatedAt = now

	// Generate ID if not set
	if issue.ID == "" {
		prefix := m.config["issue_prefix"]
		if prefix == "" {
			prefix = "bd" // Default fallback
		}

		// Get next ID
		m.counters[prefix]++
		issue.ID = fmt.Sprintf("%s-%d", prefix, m.counters[prefix])
	}

	// Check for duplicate
	if _, exists := m.issues[issue.ID]; exists {
		return fmt.Errorf("issue %s already exists", issue.ID)
	}

	// Store issue
	m.issues[issue.ID] = issue
	m.dirty[issue.ID] = true

	// Index external ref for O(1) lookup
	if issue.ExternalRef != nil && *issue.ExternalRef != "" {
		m.externalRefToID[*issue.ExternalRef] = issue.ID
	}

	// Record event
	event := &types.Event{
		IssueID:   issue.ID,
		EventType: types.EventCreated,
		Actor:     actor,
		CreatedAt: now,
	}
	m.events[issue.ID] = append(m.events[issue.ID], event)

	return nil
}

// CreateIssues creates multiple issues atomically
func (m *MemoryStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate all first
	for i, issue := range issues {
		if err := issue.Validate(); err != nil {
			return fmt.Errorf("validation failed for issue %d: %w", i, err)
		}
	}

	now := time.Now()
	prefix := m.config["issue_prefix"]
	if prefix == "" {
		prefix = "bd"
	}

	// Track IDs in this batch to detect duplicates within batch
	batchIDs := make(map[string]bool)

	// Generate IDs for issues that need them
	for _, issue := range issues {
		issue.CreatedAt = now
		issue.UpdatedAt = now

		if issue.ID == "" {
			m.counters[prefix]++
			issue.ID = fmt.Sprintf("%s-%d", prefix, m.counters[prefix])
		}

		// Check for duplicates in existing issues
		if _, exists := m.issues[issue.ID]; exists {
			return fmt.Errorf("issue %s already exists", issue.ID)
		}

		// Check for duplicates within this batch
		if batchIDs[issue.ID] {
			return fmt.Errorf("duplicate ID within batch: %s", issue.ID)
		}
		batchIDs[issue.ID] = true
	}

	// Store all issues
	for _, issue := range issues {
		m.issues[issue.ID] = issue
		m.dirty[issue.ID] = true

		// Index external ref for O(1) lookup
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			m.externalRefToID[*issue.ExternalRef] = issue.ID
		}

		// Record event
		event := &types.Event{
			IssueID:   issue.ID,
			EventType: types.EventCreated,
			Actor:     actor,
			CreatedAt: now,
		}
		m.events[issue.ID] = append(m.events[issue.ID], event)
	}

	return nil
}

// GetIssue retrieves an issue by ID
func (m *MemoryStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	issue, exists := m.issues[id]
	if !exists {
		return nil, nil
	}

	// Return a copy to avoid mutations
	issueCopy := *issue

	// Attach dependencies
	if deps, ok := m.dependencies[id]; ok {
		issueCopy.Dependencies = deps
	}

	// Attach labels
	if labels, ok := m.labels[id]; ok {
		issueCopy.Labels = labels
	}

	return &issueCopy, nil
}

// GetIssueByExternalRef retrieves an issue by external reference
func (m *MemoryStorage) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// O(1) lookup using index
	issueID, exists := m.externalRefToID[externalRef]
	if !exists {
		return nil, nil
	}

	issue, exists := m.issues[issueID]
	if !exists {
		return nil, nil
	}

	// Return a copy to avoid mutations
	issueCopy := *issue

	// Attach dependencies
	if deps, ok := m.dependencies[issue.ID]; ok {
		issueCopy.Dependencies = deps
	}

	// Attach labels
	if labels, ok := m.labels[issue.ID]; ok {
		issueCopy.Labels = labels
	}

	return &issueCopy, nil
}

// UpdateIssue updates fields on an issue
func (m *MemoryStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, exists := m.issues[id]
	if !exists {
		return fmt.Errorf("issue %s not found", id)
	}

	now := time.Now()
	issue.UpdatedAt = now

	// Apply updates
	for key, value := range updates {
		switch key {
		case "title":
			if v, ok := value.(string); ok {
				issue.Title = v
			}
		case "description":
			if v, ok := value.(string); ok {
				issue.Description = v
			}
		case "design":
			if v, ok := value.(string); ok {
				issue.Design = v
			}
		case "acceptance_criteria":
			if v, ok := value.(string); ok {
				issue.AcceptanceCriteria = v
			}
		case "notes":
			if v, ok := value.(string); ok {
				issue.Notes = v
			}
		case "status":
			if v, ok := value.(string); ok {
				oldStatus := issue.Status
				issue.Status = types.Status(v)

				// Manage closed_at
				if issue.Status == types.StatusClosed && oldStatus != types.StatusClosed {
					issue.ClosedAt = &now
				} else if issue.Status != types.StatusClosed && oldStatus == types.StatusClosed {
					issue.ClosedAt = nil
				}
			}
		case "priority":
			if v, ok := value.(int); ok {
				issue.Priority = v
			}
		case "issue_type":
			if v, ok := value.(string); ok {
				issue.IssueType = types.IssueType(v)
			}
		case "assignee":
			if v, ok := value.(string); ok {
				issue.Assignee = v
			} else if value == nil {
				issue.Assignee = ""
			}
		case "external_ref":
			// Update external ref index
			oldRef := issue.ExternalRef
			if v, ok := value.(string); ok {
				// Remove old index entry if exists
				if oldRef != nil && *oldRef != "" {
					delete(m.externalRefToID, *oldRef)
				}
				// Add new index entry
				if v != "" {
					m.externalRefToID[v] = id
				}
				issue.ExternalRef = &v
			} else if value == nil {
				// Remove old index entry if exists
				if oldRef != nil && *oldRef != "" {
					delete(m.externalRefToID, *oldRef)
				}
				issue.ExternalRef = nil
			}
		case "close_reason":
			if v, ok := value.(string); ok {
				issue.CloseReason = v
			}
		case "closed_by_session":
			if v, ok := value.(string); ok {
				issue.ClosedBySession = v
			}
		}
	}

	m.dirty[id] = true

	// Record event
	eventType := types.EventUpdated
	if status, hasStatus := updates["status"]; hasStatus {
		if status == string(types.StatusClosed) {
			eventType = types.EventClosed
		}
	}

	event := &types.Event{
		IssueID:   id,
		EventType: eventType,
		Actor:     actor,
		CreatedAt: now,
	}
	m.events[id] = append(m.events[id], event)

	return nil
}

// CloseIssue closes an issue with a reason.
// The session parameter tracks which Claude Code session closed the issue (can be empty).
func (m *MemoryStorage) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	updates := map[string]interface{}{
		"status":       string(types.StatusClosed),
		"close_reason": reason,
	}
	if session != "" {
		updates["closed_by_session"] = session
	}
	return m.UpdateIssue(ctx, id, updates, actor)
}

// CreateTombstone converts an existing issue to a tombstone record.
// This is a soft-delete that preserves the issue with status="tombstone".
func (m *MemoryStorage) CreateTombstone(ctx context.Context, id string, actor string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[id]
	if !ok {
		return fmt.Errorf("issue not found: %s", id)
	}

	now := time.Now()
	issue.OriginalType = string(issue.IssueType)
	issue.Status = types.StatusTombstone
	issue.DeletedAt = &now
	issue.DeletedBy = actor
	issue.DeleteReason = reason
	issue.UpdatedAt = now

	// Mark as dirty for export
	m.dirty[id] = true

	// Record tombstone creation event
	event := &types.Event{
		IssueID:   id,
		EventType: "deleted",
		Actor:     actor,
		Comment:   &reason,
		CreatedAt: now,
	}
	m.events[id] = append(m.events[id], event)

	return nil
}

// DeleteIssue permanently deletes an issue and all associated data
func (m *MemoryStorage) DeleteIssue(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if issue exists
	issue, ok := m.issues[id]
	if !ok {
		return fmt.Errorf("issue not found: %s", id)
	}

	// Remove external ref index entry
	if issue.ExternalRef != nil && *issue.ExternalRef != "" {
		delete(m.externalRefToID, *issue.ExternalRef)
	}

	// Delete the issue
	delete(m.issues, id)

	// Delete associated data
	delete(m.dependencies, id)
	delete(m.labels, id)
	delete(m.events, id)
	delete(m.comments, id)
	delete(m.dirty, id)

	return nil
}

// SearchIssues finds issues matching query and filters
func (m *MemoryStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.Issue

	for _, issue := range m.issues {
		// Apply filters
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Priority != nil && issue.Priority != *filter.Priority {
			continue
		}
		if filter.IssueType != nil && issue.IssueType != *filter.IssueType {
			continue
		}
		if filter.Assignee != nil && issue.Assignee != *filter.Assignee {
			continue
		}

		// Query search (title, description, or ID)
		if query != "" {
			query = strings.ToLower(query)
			if !strings.Contains(strings.ToLower(issue.Title), query) &&
				!strings.Contains(strings.ToLower(issue.Description), query) &&
				!strings.Contains(strings.ToLower(issue.ID), query) {
				continue
			}
		}

		// Label filtering: must have ALL specified labels
		if len(filter.Labels) > 0 {
			issueLabels := m.labels[issue.ID]
			hasAllLabels := true
			for _, reqLabel := range filter.Labels {
				found := false
				for _, label := range issueLabels {
					if label == reqLabel {
						found = true
						break
					}
				}
				if !found {
					hasAllLabels = false
					break
				}
			}
			if !hasAllLabels {
				continue
			}
		}

		// ID filtering
		if len(filter.IDs) > 0 {
			found := false
			for _, filterID := range filter.IDs {
				if issue.ID == filterID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Parent filtering (bd-yqhh): filter children by parent issue
		if filter.ParentID != nil {
			isChild := false
			for _, dep := range m.dependencies[issue.ID] {
				if dep.Type == types.DepParentChild && dep.DependsOnID == *filter.ParentID {
					isChild = true
					break
				}
			}
			if !isChild {
				continue
			}
		}

		// Copy issue and attach metadata
		issueCopy := *issue
		if deps, ok := m.dependencies[issue.ID]; ok {
			issueCopy.Dependencies = deps
		}
		if labels, ok := m.labels[issue.ID]; ok {
			issueCopy.Labels = labels
		}

		results = append(results, &issueCopy)
	}

	// Sort by priority, then by created_at
	sort.Slice(results, func(i, j int) bool {
		if results[i].Priority != results[j].Priority {
			return results[i].Priority < results[j].Priority
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	// Apply limit
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}

	return results, nil
}

// AddDependency adds a dependency between issues
func (m *MemoryStorage) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check that both issues exist
	if _, exists := m.issues[dep.IssueID]; !exists {
		return fmt.Errorf("issue %s not found", dep.IssueID)
	}
	if _, exists := m.issues[dep.DependsOnID]; !exists {
		return fmt.Errorf("issue %s not found", dep.DependsOnID)
	}

	// Check for duplicates
	for _, existing := range m.dependencies[dep.IssueID] {
		if existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
			return fmt.Errorf("dependency already exists")
		}
	}

	m.dependencies[dep.IssueID] = append(m.dependencies[dep.IssueID], dep)
	m.dirty[dep.IssueID] = true

	return nil
}

// RemoveDependency removes a dependency
func (m *MemoryStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	deps := m.dependencies[issueID]
	newDeps := make([]*types.Dependency, 0)

	for _, dep := range deps {
		if dep.DependsOnID != dependsOnID {
			newDeps = append(newDeps, dep)
		}
	}

	m.dependencies[issueID] = newDeps
	m.dirty[issueID] = true

	return nil
}

// GetDependencies gets issues that this issue depends on
func (m *MemoryStorage) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.Issue
	for _, dep := range m.dependencies[issueID] {
		if issue, exists := m.issues[dep.DependsOnID]; exists {
			issueCopy := *issue
			results = append(results, &issueCopy)
		}
	}

	return results, nil
}

// GetDependents gets issues that depend on this issue
func (m *MemoryStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.Issue
	for id, deps := range m.dependencies {
		for _, dep := range deps {
			if dep.DependsOnID == issueID {
				if issue, exists := m.issues[id]; exists {
					results = append(results, issue)
				}
				break
			}
		}
	}

	return results, nil
}

// GetDependenciesWithMetadata gets issues that this issue depends on, with dependency type
func (m *MemoryStorage) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.IssueWithDependencyMetadata
	for _, dep := range m.dependencies[issueID] {
		if issue, exists := m.issues[dep.DependsOnID]; exists {
			issueCopy := *issue
			results = append(results, &types.IssueWithDependencyMetadata{
				Issue:          issueCopy,
				DependencyType: dep.Type,
			})
		}
	}

	return results, nil
}

// GetDependentsWithMetadata gets issues that depend on this issue, with dependency type
func (m *MemoryStorage) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.IssueWithDependencyMetadata
	for id, deps := range m.dependencies {
		for _, dep := range deps {
			if dep.DependsOnID == issueID {
				if issue, exists := m.issues[id]; exists {
					issueCopy := *issue
					results = append(results, &types.IssueWithDependencyMetadata{
						Issue:          issueCopy,
						DependencyType: dep.Type,
					})
				}
				break
			}
		}
	}

	return results, nil
}

// GetDependencyCounts returns dependency and dependent counts for multiple issues
func (m *MemoryStorage) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*types.DependencyCounts)

	// Initialize all requested IDs with zero counts
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{
			DependencyCount: 0,
			DependentCount:  0,
		}
	}

	// Build a set for quick lookup
	idSet := make(map[string]bool)
	for _, id := range issueIDs {
		idSet[id] = true
	}

	// Count dependencies (issues that this issue depends on)
	for _, id := range issueIDs {
		if deps, exists := m.dependencies[id]; exists {
			result[id].DependencyCount = len(deps)
		}
	}

	// Count dependents (issues that depend on this issue)
	for _, deps := range m.dependencies {
		for _, dep := range deps {
			if idSet[dep.DependsOnID] {
				result[dep.DependsOnID].DependentCount++
			}
		}
	}

	return result, nil
}

// GetDependencyRecords gets dependency records for an issue
func (m *MemoryStorage) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.dependencies[issueID], nil
}

// GetAllDependencyRecords gets all dependency records
func (m *MemoryStorage) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy
	result := make(map[string][]*types.Dependency)
	for k, v := range m.dependencies {
		result[k] = v
	}

	return result, nil
}

// GetDirtyIssueHash returns the hash for dirty issue tracking
func (m *MemoryStorage) GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) {
	// Memory storage doesn't track dirty hashes, return empty string
	return "", nil
}

// GetExportHash returns the hash for export tracking
func (m *MemoryStorage) GetExportHash(ctx context.Context, issueID string) (string, error) {
	// Memory storage doesn't track export hashes, return empty string
	return "", nil
}

// SetExportHash sets the hash for export tracking
func (m *MemoryStorage) SetExportHash(ctx context.Context, issueID, hash string) error {
	// Memory storage doesn't track export hashes, no-op
	return nil
}

// ClearAllExportHashes clears all export hashes
func (m *MemoryStorage) ClearAllExportHashes(ctx context.Context) error {
	// Memory storage doesn't track export hashes, no-op
	return nil
}

// GetJSONLFileHash gets the JSONL file hash
func (m *MemoryStorage) GetJSONLFileHash(ctx context.Context) (string, error) {
	// Memory storage doesn't track JSONL file hashes, return empty string
	return "", nil
}

// SetJSONLFileHash sets the JSONL file hash
func (m *MemoryStorage) SetJSONLFileHash(ctx context.Context, fileHash string) error {
	// Memory storage doesn't track JSONL file hashes, no-op
	return nil
}

// GetDependencyTree gets the dependency tree for an issue
func (m *MemoryStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	// Get the root issue first
	root, err := m.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}

	var nodes []*types.TreeNode

	// Add root node at depth 0
	rootNode := &types.TreeNode{
		Depth:    0,
		ParentID: issueID, // Root's parent is itself
	}
	rootNode.ID = root.ID
	rootNode.Title = root.Title
	rootNode.Description = root.Description
	rootNode.Status = root.Status
	rootNode.Priority = root.Priority
	rootNode.IssueType = root.IssueType
	nodes = append(nodes, rootNode)

	// Get dependencies (or dependents if reverse)
	// Note: reverse mode not fully implemented - uses same logic for now
	deps, err := m.GetDependencies(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Add dependencies at depth 1
	for _, dep := range deps {
		node := &types.TreeNode{
			Depth:    1,
			ParentID: issueID, // Parent is the root
		}
		node.ID = dep.ID
		node.Title = dep.Title
		node.Description = dep.Description
		node.Status = dep.Status
		node.Priority = dep.Priority
		node.IssueType = dep.IssueType
		nodes = append(nodes, node)
	}

	return nodes, nil
}

// DetectCycles detects dependency cycles
func (m *MemoryStorage) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	// Simplified - return empty (no cycles detected)
	return nil, nil
}

// Add label methods
func (m *MemoryStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if issue exists
	if _, exists := m.issues[issueID]; !exists {
		return fmt.Errorf("issue %s not found", issueID)
	}

	// Check for duplicate
	for _, l := range m.labels[issueID] {
		if l == label {
			return nil // Already exists
		}
	}

	m.labels[issueID] = append(m.labels[issueID], label)
	m.dirty[issueID] = true

	return nil
}

func (m *MemoryStorage) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	labels := m.labels[issueID]
	newLabels := make([]string, 0)

	for _, l := range labels {
		if l != label {
			newLabels = append(newLabels, l)
		}
	}

	m.labels[issueID] = newLabels
	m.dirty[issueID] = true

	return nil
}

func (m *MemoryStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.labels[issueID], nil
}

func (m *MemoryStorage) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]string)
	for _, issueID := range issueIDs {
		if labels, exists := m.labels[issueID]; exists {
			result[issueID] = labels
		}
	}
	return result, nil
}

func (m *MemoryStorage) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.Issue
	for issueID, labels := range m.labels {
		for _, l := range labels {
			if l == label {
				if issue, exists := m.issues[issueID]; exists {
					issueCopy := *issue
					results = append(results, &issueCopy)
				}
				break
			}
		}
	}

	return results, nil
}

// GetReadyWork returns issues that are ready to work on (no open blockers)
func (m *MemoryStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*types.Issue

	for _, issue := range m.issues {
		// Skip pinned issues - they are context markers, not actionable work (bd-o9o)
		if issue.Pinned {
			continue
		}

		// Status filtering: default to open OR in_progress if not specified
		if filter.Status == "" {
			if issue.Status != types.StatusOpen && issue.Status != types.StatusInProgress {
				continue
			}
		} else if issue.Status != filter.Status {
			continue
		}

		// Priority filtering
		if filter.Priority != nil && issue.Priority != *filter.Priority {
			continue
		}

		// Type filtering (gt-7xtn)
		if filter.Type != "" {
			if string(issue.IssueType) != filter.Type {
				continue
			}
		} else {
			// Exclude workflow types from ready work by default
			// These are internal workflow items, not work for polecats to claim
			switch issue.IssueType {
			case types.TypeMergeRequest, types.TypeGate, types.TypeMolecule, types.TypeMessage:
				continue
			}
		}

		// Unassigned takes precedence over Assignee filter
		if filter.Unassigned {
			if issue.Assignee != "" {
				continue
			}
		} else if filter.Assignee != nil {
			if issue.Assignee != *filter.Assignee {
				continue
			}
		}

		// Label filtering (AND semantics)
		if len(filter.Labels) > 0 {
			issueLabels := m.labels[issue.ID]
			hasAllLabels := true
			for _, reqLabel := range filter.Labels {
				found := false
				for _, label := range issueLabels {
					if label == reqLabel {
						found = true
						break
					}
				}
				if !found {
					hasAllLabels = false
					break
				}
			}
			if !hasAllLabels {
				continue
			}
		}

		// Label filtering (OR semantics)
		if len(filter.LabelsAny) > 0 {
			issueLabels := m.labels[issue.ID]
			hasAnyLabel := false
			for _, reqLabel := range filter.LabelsAny {
				for _, label := range issueLabels {
					if label == reqLabel {
						hasAnyLabel = true
						break
					}
				}
				if hasAnyLabel {
					break
				}
			}
			if !hasAnyLabel {
				continue
			}
		}

		// Skip issues with any open 'blocks' dependencies
		if len(m.getOpenBlockers(issue.ID)) > 0 {
			continue
		}

		issueCopy := *issue
		if deps, ok := m.dependencies[issue.ID]; ok {
			issueCopy.Dependencies = deps
		}
		if labels, ok := m.labels[issue.ID]; ok {
			issueCopy.Labels = labels
		}
		if comments, ok := m.comments[issue.ID]; ok {
			issueCopy.Comments = comments
		}

		results = append(results, &issueCopy)
	}

	// Default to hybrid sort for backwards compatibility
	sortPolicy := filter.SortPolicy
	if sortPolicy == "" {
		sortPolicy = types.SortPolicyHybrid
	}

	switch sortPolicy {
	case types.SortPolicyOldest:
		sort.Slice(results, func(i, j int) bool {
			return results[i].CreatedAt.Before(results[j].CreatedAt)
		})
	case types.SortPolicyPriority:
		sort.Slice(results, func(i, j int) bool {
			if results[i].Priority != results[j].Priority {
				return results[i].Priority < results[j].Priority
			}
			return results[i].CreatedAt.Before(results[j].CreatedAt)
		})
	case types.SortPolicyHybrid:
		fallthrough
	default:
		cutoff := time.Now().Add(-48 * time.Hour)
		sort.Slice(results, func(i, j int) bool {
			iRecent := results[i].CreatedAt.After(cutoff)
			jRecent := results[j].CreatedAt.After(cutoff)
			if iRecent != jRecent {
				return iRecent // recent first
			}
			if iRecent {
				if results[i].Priority != results[j].Priority {
					return results[i].Priority < results[j].Priority
				}
			}
			return results[i].CreatedAt.Before(results[j].CreatedAt)
		})
	}

	// Apply limit
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}

	return results, nil
}

// getOpenBlockers returns the IDs of blockers that are currently open/in_progress/blocked/deferred/hooked.
// The caller must hold at least a read lock.
func (m *MemoryStorage) getOpenBlockers(issueID string) []string {
	deps := m.dependencies[issueID]
	if len(deps) == 0 {
		return nil
	}

	blockers := make([]string, 0)
	for _, dep := range deps {
		if dep.Type != types.DepBlocks {
			continue
		}
		blocker, ok := m.issues[dep.DependsOnID]
		if !ok {
			// If the blocker is missing, treat it as still blocking (data is incomplete)
			blockers = append(blockers, dep.DependsOnID)
			continue
		}
		switch blocker.Status {
		case types.StatusOpen, types.StatusInProgress, types.StatusBlocked, types.StatusDeferred, types.StatusHooked:
			blockers = append(blockers, blocker.ID)
		}
	}

	sort.Strings(blockers)
	return blockers
}

// GetBlockedIssues returns issues that are blocked by other issues
// Note: Pinned issues are excluded from the output (beads-ei4)
func (m *MemoryStorage) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Build set of descendant IDs if parent filter is specified
	var descendantIDs map[string]bool
	if filter.ParentID != nil {
		descendantIDs = m.getAllDescendants(*filter.ParentID)
	}

	var results []*types.BlockedIssue

	for _, issue := range m.issues {
		// Only consider non-closed, non-tombstone issues
		if issue.Status == types.StatusClosed || issue.Status == types.StatusTombstone {
			continue
		}

		// Exclude pinned issues (beads-ei4)
		if issue.Pinned {
			continue
		}

		// Parent filtering: only include descendants of specified parent
		if descendantIDs != nil && !descendantIDs[issue.ID] {
			continue
		}

		blockers := m.getOpenBlockers(issue.ID)
		// Issue is "blocked" if: status is blocked, status is deferred, or has open blockers
		if issue.Status != types.StatusBlocked && issue.Status != types.StatusDeferred && len(blockers) == 0 {
			continue
		}

		issueCopy := *issue
		if deps, ok := m.dependencies[issue.ID]; ok {
			issueCopy.Dependencies = deps
		}
		if labels, ok := m.labels[issue.ID]; ok {
			issueCopy.Labels = labels
		}
		if comments, ok := m.comments[issue.ID]; ok {
			issueCopy.Comments = comments
		}

		results = append(results, &types.BlockedIssue{
			Issue:          issueCopy,
			BlockedByCount: len(blockers),
			BlockedBy:      blockers,
		})
	}

	// Match SQLite behavior: order by priority ascending
	sort.Slice(results, func(i, j int) bool {
		if results[i].Priority != results[j].Priority {
			return results[i].Priority < results[j].Priority
		}
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})

	return results, nil
}

// getAllDescendants returns all descendant IDs of a parent issue recursively
func (m *MemoryStorage) getAllDescendants(parentID string) map[string]bool {
	descendants := make(map[string]bool)
	m.collectDescendants(parentID, descendants)
	return descendants
}

// collectDescendants recursively collects all descendants of a parent
func (m *MemoryStorage) collectDescendants(parentID string, descendants map[string]bool) {
	for issueID, deps := range m.dependencies {
		for _, dep := range deps {
			if dep.Type == types.DepParentChild && dep.DependsOnID == parentID {
				if !descendants[issueID] {
					descendants[issueID] = true
					m.collectDescendants(issueID, descendants)
				}
			}
		}
	}
}

func (m *MemoryStorage) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	return nil, nil
}

func (m *MemoryStorage) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, -filter.Days)
	var stale []*types.Issue

	for _, issue := range m.issues {
		if issue.Status == types.StatusClosed {
			continue
		}
		if filter.Status != "" && string(issue.Status) != filter.Status {
			continue
		}
		if issue.UpdatedAt.Before(cutoff) {
			stale = append(stale, issue)
		}
	}

	// Sort by updated_at ascending (oldest first)
	sort.Slice(stale, func(i, j int) bool {
		return stale[i].UpdatedAt.Before(stale[j].UpdatedAt)
	})

	if filter.Limit > 0 && len(stale) > filter.Limit {
		stale = stale[:filter.Limit]
	}

	return stale, nil
}

// GetNewlyUnblockedByClose returns issues that became unblocked when the given issue was closed.
// This is used by the --suggest-next flag on bd close (GH#679).
func (m *MemoryStorage) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var unblocked []*types.Issue

	// Find issues that depend on the closed issue
	for issueID, deps := range m.dependencies {
		issue, exists := m.issues[issueID]
		if !exists {
			continue
		}

		// Only consider open/in_progress, non-pinned issues
		if issue.Status != types.StatusOpen && issue.Status != types.StatusInProgress {
			continue
		}
		if issue.Pinned {
			continue
		}

		// Check if this issue depended on the closed issue
		dependedOnClosed := false
		for _, dep := range deps {
			if dep.DependsOnID == closedIssueID && dep.Type == types.DepBlocks {
				dependedOnClosed = true
				break
			}
		}

		if !dependedOnClosed {
			continue
		}

		// Check if now unblocked (no remaining open blockers)
		blockers := m.getOpenBlockers(issueID)
		if len(blockers) == 0 {
			issueCopy := *issue
			unblocked = append(unblocked, &issueCopy)
		}
	}

	// Sort by priority ascending
	sort.Slice(unblocked, func(i, j int) bool {
		return unblocked[i].Priority < unblocked[j].Priority
	})

	return unblocked, nil
}

func (m *MemoryStorage) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return nil
}

func (m *MemoryStorage) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	events := m.events[issueID]
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	return events, nil
}

func (m *MemoryStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	comment := &types.Comment{
		ID:        int64(len(m.comments[issueID]) + 1),
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: time.Now(),
	}

	m.comments[issueID] = append(m.comments[issueID], comment)
	m.dirty[issueID] = true

	return comment, nil
}

func (m *MemoryStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.comments[issueID], nil
}

func (m *MemoryStorage) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*types.Comment)
	for _, issueID := range issueIDs {
		if comments, exists := m.comments[issueID]; exists {
			result[issueID] = comments
		}
	}
	return result, nil
}

func (m *MemoryStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &types.Statistics{}

	// First pass: count by status
	for _, issue := range m.issues {
		switch issue.Status {
		case types.StatusOpen:
			stats.OpenIssues++
		case types.StatusInProgress:
			stats.InProgressIssues++
		case types.StatusClosed:
			stats.ClosedIssues++
		case types.StatusDeferred:
			stats.DeferredIssues++
		case types.StatusTombstone:
			stats.TombstoneIssues++
		case types.StatusPinned:
			stats.PinnedIssues++
		}
	}

	// TotalIssues excludes tombstones (matches SQLite behavior)
	stats.TotalIssues = stats.OpenIssues + stats.InProgressIssues + stats.ClosedIssues + stats.DeferredIssues + stats.PinnedIssues

	// Second pass: calculate blocked and ready issues based on dependencies
	// An issue is blocked if it has open blockers (uses same logic as GetBlockedIssues)
	for id, issue := range m.issues {
		// Only consider non-closed, non-tombstone issues for blocking
		if issue.Status == types.StatusClosed || issue.Status == types.StatusTombstone {
			continue
		}

		blockers := m.getOpenBlockers(id)
		if len(blockers) > 0 {
			stats.BlockedIssues++
		} else if issue.Status == types.StatusOpen {
			// Ready = open issues with no open blockers
			stats.ReadyIssues++
		}
	}

	// Calculate average lead time (hours from created to closed)
	var totalLeadTime float64
	var closedCount int
	for _, issue := range m.issues {
		if issue.Status == types.StatusClosed && issue.ClosedAt != nil {
			leadTime := issue.ClosedAt.Sub(issue.CreatedAt).Hours()
			totalLeadTime += leadTime
			closedCount++
		}
	}
	if closedCount > 0 {
		stats.AverageLeadTime = totalLeadTime / float64(closedCount)
	}

	// Calculate epics eligible for closure
	stats.EpicsEligibleForClosure = m.countEpicsEligibleForClosure()

	return stats, nil
}

// countEpicsEligibleForClosure returns the count of non-closed epics where all children are closed
func (m *MemoryStorage) countEpicsEligibleForClosure() int {
	// Build a map of epic -> children using parent-child dependencies
	epicChildren := make(map[string][]string)
	for _, deps := range m.dependencies {
		for _, dep := range deps {
			if dep.Type == types.DepParentChild {
				// dep.IssueID is the child, dep.DependsOnID is the parent
				epicChildren[dep.DependsOnID] = append(epicChildren[dep.DependsOnID], dep.IssueID)
			}
		}
	}

	count := 0
	for epicID, children := range epicChildren {
		epic, exists := m.issues[epicID]
		if !exists {
			continue
		}
		// Only consider non-closed epics
		if epic.IssueType != types.TypeEpic || epic.Status == types.StatusClosed {
			continue
		}
		// Check if all children are closed
		if len(children) == 0 {
			continue
		}
		allClosed := true
		for _, childID := range children {
			child, exists := m.issues[childID]
			if !exists || child.Status != types.StatusClosed {
				allClosed = false
				break
			}
		}
		if allClosed {
			count++
		}
	}
	return count
}

// Dirty tracking
func (m *MemoryStorage) GetDirtyIssues(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var dirtyIDs []string
	for id := range m.dirty {
		dirtyIDs = append(dirtyIDs, id)
	}

	return dirtyIDs, nil
}

func (m *MemoryStorage) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range issueIDs {
		delete(m.dirty, id)
	}

	return nil
}

// ID Generation
func (m *MemoryStorage) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate parent exists
	if _, exists := m.issues[parentID]; !exists {
		return "", fmt.Errorf("parent issue %s does not exist", parentID)
	}

	// Calculate depth (count dots)
	depth := strings.Count(parentID, ".")
	if depth >= 3 {
		return "", fmt.Errorf("maximum hierarchy depth (3) exceeded for parent %s", parentID)
	}

	// Get or initialize counter for this parent
	counter := m.counters[parentID]
	counter++
	m.counters[parentID] = counter

	// Format as parentID.counter
	childID := fmt.Sprintf("%s.%d", parentID, counter)
	return childID, nil
}

// Config
func (m *MemoryStorage) SetConfig(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.config[key] = value
	return nil
}

func (m *MemoryStorage) GetConfig(ctx context.Context, key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.config[key], nil
}

func (m *MemoryStorage) DeleteConfig(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.config, key)
	return nil
}

func (m *MemoryStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy to avoid mutations
	result := make(map[string]string)
	for k, v := range m.config {
		result[k] = v
	}

	return result, nil
}

// GetCustomStatuses retrieves the list of custom status states from config.
func (m *MemoryStorage) GetCustomStatuses(ctx context.Context) ([]string, error) {
	value, err := m.GetConfig(ctx, "status.custom")
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCustomStatuses(value), nil
}

// parseCustomStatuses splits a comma-separated string into a slice of trimmed status names.
func parseCustomStatuses(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// Metadata
func (m *MemoryStorage) SetMetadata(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metadata[key] = value
	return nil
}

func (m *MemoryStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.metadata[key], nil
}

// Prefix rename operations (no-ops for memory storage)
func (m *MemoryStorage) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return fmt.Errorf("UpdateIssueID not supported in --no-db mode")
}

func (m *MemoryStorage) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}

func (m *MemoryStorage) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}

// Lifecycle
func (m *MemoryStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	return nil
}

func (m *MemoryStorage) Path() string {
	return m.jsonlPath
}

// GetMoleculeProgress returns progress stats for a molecule.
// For memory storage, this iterates through dependencies.
func (m *MemoryStorage) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	issue, exists := m.issues[moleculeID]
	if !exists {
		return nil, fmt.Errorf("molecule not found: %s", moleculeID)
	}

	stats := &types.MoleculeProgressStats{
		MoleculeID:    moleculeID,
		MoleculeTitle: issue.Title,
	}

	// Find all parent-child dependencies where moleculeID is the parent
	for _, deps := range m.dependencies {
		for _, dep := range deps {
			if dep.Type == types.DepParentChild && dep.DependsOnID == moleculeID {
				child, exists := m.issues[dep.IssueID]
				if !exists {
					continue
				}
				stats.Total++
				switch child.Status {
				case types.StatusClosed:
					stats.Completed++
					if child.ClosedAt != nil {
						if stats.FirstClosed == nil || child.ClosedAt.Before(*stats.FirstClosed) {
							stats.FirstClosed = child.ClosedAt
						}
						if stats.LastClosed == nil || child.ClosedAt.After(*stats.LastClosed) {
							stats.LastClosed = child.ClosedAt
						}
					}
				case types.StatusInProgress:
					stats.InProgress++
					if stats.CurrentStepID == "" {
						stats.CurrentStepID = child.ID
					}
				}
			}
		}
	}

	return stats, nil
}

// UnderlyingDB returns nil for memory storage (no SQL database)
func (m *MemoryStorage) UnderlyingDB() *sql.DB {
	return nil
}

// UnderlyingConn returns error for memory storage (no SQL database)
func (m *MemoryStorage) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return nil, fmt.Errorf("UnderlyingConn not available in memory storage")
}

// RunInTransaction executes a function within a transaction context.
// For MemoryStorage, this provides basic atomicity via mutex locking.
// If the function returns an error, changes are NOT automatically rolled back
// since MemoryStorage doesn't support true transaction rollback.
//
// Note: For full rollback support, callers should use SQLite storage.
func (m *MemoryStorage) RunInTransaction(ctx context.Context, fn func(tx storage.Transaction) error) error {
	return fmt.Errorf("RunInTransaction not supported in --no-db mode: use SQLite storage for transaction support")
}

// REMOVED (bd-c7af): SyncAllCounters - no longer needed with hash IDs

// MarkIssueDirty marks an issue as dirty for export
func (m *MemoryStorage) MarkIssueDirty(ctx context.Context, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dirty[issueID] = true
	return nil
}
