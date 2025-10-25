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

	// For tracking
	dirty map[string]bool // IssueIDs that have been modified

	jsonlPath string // Path to source JSONL file (for reference)
	closed    bool
}

// New creates a new in-memory storage backend
func New(jsonlPath string) *MemoryStorage {
	return &MemoryStorage{
		issues:       make(map[string]*types.Issue),
		dependencies: make(map[string][]*types.Dependency),
		labels:       make(map[string][]string),
		events:       make(map[string][]*types.Event),
		comments:     make(map[string][]*types.Comment),
		config:       make(map[string]string),
		metadata:     make(map[string]string),
		counters:     make(map[string]int),
		dirty:        make(map[string]bool),
		jsonlPath:    jsonlPath,
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
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		return "", 0
	}
	var num int
	_, err := fmt.Sscanf(parts[1], "%d", &num)
	if err != nil {
		return "", 0
	}
	return parts[0], num
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

	// Generate IDs for issues that need them
	for _, issue := range issues {
		issue.CreatedAt = now
		issue.UpdatedAt = now

		if issue.ID == "" {
			m.counters[prefix]++
			issue.ID = fmt.Sprintf("%s-%d", prefix, m.counters[prefix])
		}

		// Check for duplicates
		if _, exists := m.issues[issue.ID]; exists {
			return fmt.Errorf("issue %s already exists", issue.ID)
		}
	}

	// Store all issues
	for _, issue := range issues {
		m.issues[issue.ID] = issue
		m.dirty[issue.ID] = true

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
			if v, ok := value.(string); ok {
				issue.ExternalRef = &v
			} else if value == nil {
				issue.ExternalRef = nil
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

// CloseIssue closes an issue with a reason
func (m *MemoryStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	return m.UpdateIssue(ctx, id, map[string]interface{}{
		"status": string(types.StatusClosed),
	}, actor)
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

// GetDependencyTree gets the dependency tree for an issue
func (m *MemoryStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool) ([]*types.TreeNode, error) {
	// Simplified implementation - just return direct dependencies
	deps, err := m.GetDependencies(ctx, issueID)
	if err != nil {
		return nil, err
	}

	var nodes []*types.TreeNode
	for _, dep := range deps {
		node := &types.TreeNode{
			Depth: 1,
		}
		// Copy issue fields
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

// Stub implementations for other required methods
func (m *MemoryStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	// Simplified: return open issues with no blocking dependencies
	return m.SearchIssues(ctx, "", types.IssueFilter{
		Status: func() *types.Status { s := types.StatusOpen; return &s }(),
	})
}

func (m *MemoryStorage) GetBlockedIssues(ctx context.Context) ([]*types.BlockedIssue, error) {
	return nil, nil
}

func (m *MemoryStorage) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	return nil, nil
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

func (m *MemoryStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &types.Statistics{
		TotalIssues: len(m.issues),
	}

	for _, issue := range m.issues {
		switch issue.Status {
		case types.StatusOpen:
			stats.OpenIssues++
		case types.StatusInProgress:
			stats.InProgressIssues++
		case types.StatusBlocked:
			stats.BlockedIssues++
		case types.StatusClosed:
			stats.ClosedIssues++
		}
	}

	return stats, nil
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

func (m *MemoryStorage) ClearDirtyIssues(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dirty = make(map[string]bool)
	return nil
}

func (m *MemoryStorage) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range issueIDs {
		delete(m.dirty, id)
	}

	return nil
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

// UnderlyingDB returns nil for memory storage (no SQL database)
func (m *MemoryStorage) UnderlyingDB() *sql.DB {
	return nil
}

// UnderlyingConn returns error for memory storage (no SQL database)
func (m *MemoryStorage) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return nil, fmt.Errorf("UnderlyingConn not available in memory storage")
}

// SyncAllCounters synchronizes ID counters based on existing issues
func (m *MemoryStorage) SyncAllCounters(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset counters
	m.counters = make(map[string]int)

	// Recompute from issues
	for _, issue := range m.issues {
		prefix, num := extractPrefixAndNumber(issue.ID)
		if prefix != "" && num > 0 {
			if m.counters[prefix] < num {
				m.counters[prefix] = num
			}
		}
	}

	return nil
}

// MarkIssueDirty marks an issue as dirty for export
func (m *MemoryStorage) MarkIssueDirty(ctx context.Context, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dirty[issueID] = true
	return nil
}
