//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		bc, err := newBatchContext(ctx, tx, storage.BatchCreateOptions{})
		if err != nil {
			return err
		}
		return createIssueInBatch(ctx, tx, bc, issue, actor)
	})
}

func (s *EmbeddedDoltStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: false,
	})
}

func (s *EmbeddedDoltStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}

	// All-ephemeral fast path: create each wisp individually.
	if allEphemeral(issues) {
		for _, issue := range issues {
			issue.Ephemeral = true
			if err := s.CreateIssue(ctx, issue, actor); err != nil {
				return err
			}
		}
		return nil
	}

	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		batchCtx, err := newBatchContext(ctx, tx, opts)
		if err != nil {
			return err
		}

		// First pass: insert each issue with its event, labels, and comments.
		for _, issue := range issues {
			if err := createIssueInBatch(ctx, tx, batchCtx, issue, actor); err != nil {
				return err
			}
		}

		// Second pass: dependencies (all issues must exist first).
		if err := persistDependencies(ctx, tx, issues, actor); err != nil {
			return err
		}

		// Reconcile child counters for hierarchical IDs.
		if err := reconcileChildCounters(ctx, tx, issues); err != nil {
			return err
		}

		return nil
	})
}

// isWisp returns true if the issue is ephemeral or has a wisp-style ID.
func isWisp(issue *types.Issue) bool {
	return issue.Ephemeral || strings.Contains(issue.ID, "-wisp-")
}

// tableRouting returns the issue and event table names for an issue,
// routing ephemeral issues to the wisps tables.
func tableRouting(issue *types.Issue) (issueTable, eventTable string) {
	if isWisp(issue) {
		return "wisps", "wisp_events"
	}
	return "issues", "events"
}

// readConfigPrefix reads and normalizes issue_prefix from the config table.
func readConfigPrefix(ctx context.Context, tx *sql.Tx) (string, error) {
	var configPrefix string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return "", fmt.Errorf("%w: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)", storage.ErrNotInitialized)
	} else if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}
	return strings.TrimSuffix(configPrefix, "-"), nil
}

// prepareIssueForInsert normalizes timestamps, validates, and computes the content hash.
func prepareIssueForInsert(issue *types.Issue, customStatuses, customTypes []string) error {
	if err := validateMetadataIfConfigured(issue.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed for issue %s: %w", issue.ID, err)
	}

	// Normalize timestamps to UTC, defaulting to now.
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}

	// Ensure closed issues have a closed_at timestamp.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
	}
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}
	return nil
}

// validateIssueIDPrefix validates that the issue ID matches the configured prefix
// or any of the allowed_prefixes.
func validateIssueIDPrefix(id, prefix, allowedPrefixes string) error {
	if strings.HasPrefix(id, prefix+"-") {
		return nil
	}
	if allowedPrefixes != "" {
		for _, allowed := range strings.Split(allowedPrefixes, ",") {
			allowed = strings.TrimSpace(allowed)
			if allowed != "" && strings.HasPrefix(id, allowed+"-") {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: issue ID %s does not match configured prefix %s", storage.ErrPrefixMismatch, id, prefix)
}

// parseHierarchicalID checks if an ID is hierarchical (e.g., "bd-abc.1")
// and returns the parent ID and child number.
func parseHierarchicalID(id string) (parentID string, childNum int, ok bool) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return "", 0, false
	}
	parentID = id[:lastDot]
	var num int
	if _, err := fmt.Sscanf(id[lastDot+1:], "%d", &num); err != nil {
		return "", 0, false
	}
	return parentID, num, true
}

// batchContext holds per-batch state read once and reused for every issue.
type batchContext struct {
	customStatuses  []string
	customTypes     []string
	configPrefix    string
	allowedPrefixes string
	opts            storage.BatchCreateOptions
}

func newBatchContext(ctx context.Context, tx *sql.Tx, opts storage.BatchCreateOptions) (*batchContext, error) {
	customStatuses, err := getCustomStatusesTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := getCustomTypesTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom types: %w", err)
	}
	configPrefix, err := readConfigPrefix(ctx, tx)
	if err != nil {
		return nil, err
	}
	var allowedPrefixes string
	_ = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "allowed_prefixes").Scan(&allowedPrefixes)

	return &batchContext{
		customStatuses:  customStatuses,
		customTypes:     customTypes,
		configPrefix:    configPrefix,
		allowedPrefixes: allowedPrefixes,
		opts:            opts,
	}, nil
}

// createIssueInBatch handles a single issue within a transaction:
// prepare, resolve prefix, generate ID, validate prefix, check orphans,
// insert, record event, persist labels/comments.
// Returns nil if the issue was skipped (e.g., orphan skip mode).
func createIssueInBatch(ctx context.Context, tx *sql.Tx, bc *batchContext, issue *types.Issue, actor string) error {
	if err := prepareIssueForInsert(issue, bc.customStatuses, bc.customTypes); err != nil {
		return err
	}

	issueTable, eventTable := tableRouting(issue)

	// Resolve prefix and generate ID if needed.
	if issue.ID == "" {
		prefix := bc.configPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		} else if issue.IDPrefix != "" {
			prefix = bc.configPrefix + "-" + issue.IDPrefix
		} else if isWisp(issue) {
			prefix = bc.configPrefix + "-wisp"
		}
		var err error
		issue.ID, err = generateIssueIDInTable(ctx, tx, issueTable, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
	} else if !bc.opts.SkipPrefixValidation {
		if err := validateIssueIDPrefix(issue.ID, bc.configPrefix, bc.allowedPrefixes); err != nil {
			return fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
		}
	}

	if skip, err := checkOrphan(ctx, tx, issue, issueTable, bc.opts.OrphanHandling); err != nil {
		return err
	} else if skip {
		return nil
	}

	isNew, err := insertIssueIfNew(ctx, tx, issueTable, issue)
	if err != nil {
		return err
	}

	if isNew {
		if err := recordEventInTable(ctx, tx, eventTable, issue.ID, types.EventCreated, actor, ""); err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}

	if err := persistLabels(ctx, tx, issue); err != nil {
		return err
	}
	return persistComments(ctx, tx, issue)
}

// allEphemeral returns true if every issue in the slice is ephemeral.
func allEphemeral(issues []*types.Issue) bool {
	for _, issue := range issues {
		if !issue.Ephemeral {
			return false
		}
	}
	return true
}

// checkOrphan handles orphan detection for hierarchical IDs.
// Returns (skip=true, nil) if the issue should be skipped.
//
//nolint:gosec // G201: table is a hardcoded constant
func checkOrphan(ctx context.Context, tx *sql.Tx, issue *types.Issue, issueTable string, handling storage.OrphanHandling) (skip bool, err error) {
	if issue.ID == "" {
		return false, nil
	}
	parentID, _, ok := parseHierarchicalID(issue.ID)
	if !ok {
		return false, nil
	}

	var parentCount int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), parentID).Scan(&parentCount); err != nil {
		return false, fmt.Errorf("failed to check parent existence: %w", err)
	}
	if parentCount > 0 {
		return false, nil
	}

	switch handling {
	case storage.OrphanStrict:
		return false, fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
	case storage.OrphanSkip:
		return true, nil
	default: // OrphanAllow, OrphanResurrect
		return false, nil
	}
}

// insertIssueIfNew inserts the issue and returns whether it was genuinely new.
//
//nolint:gosec // G201: table is a hardcoded constant
func insertIssueIfNew(ctx context.Context, tx *sql.Tx, issueTable string, issue *types.Issue) (isNew bool, err error) {
	var existingCount int
	if issue.ID != "" {
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), issue.ID).Scan(&existingCount); err != nil {
			return false, fmt.Errorf("failed to check issue existence for %s: %w", issue.ID, err)
		}
	}
	if err := insertIssueIntoTable(ctx, tx, issueTable, issue); err != nil {
		return false, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
	}
	return existingCount == 0, nil
}

// persistLabels writes issue.Labels into the appropriate labels table.
func persistLabels(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	if len(issue.Labels) == 0 {
		return nil
	}
	labelTable := "labels"
	if isWisp(issue) {
		labelTable = "wisp_labels"
	}
	for _, label := range issue.Labels {
		//nolint:gosec // G201: table is determined by ephemeral flag
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (issue_id, label)
			VALUES (?, ?)
			ON DUPLICATE KEY UPDATE label = label
		`, labelTable), issue.ID, label)
		if err != nil {
			return fmt.Errorf("failed to insert label %q for %s: %w", label, issue.ID, err)
		}
	}
	return nil
}

// persistComments writes issue.Comments into the appropriate comments table.
func persistComments(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	if len(issue.Comments) == 0 {
		return nil
	}
	commentTable := "comments"
	if isWisp(issue) {
		commentTable = "wisp_comments"
	}
	for _, comment := range issue.Comments {
		createdAt := comment.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		//nolint:gosec // G201: table is determined by ephemeral flag
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE text = VALUES(text)
		`, commentTable), issue.ID, comment.Author, comment.Text, createdAt)
		if err != nil {
			return fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// persistDependencies inserts dependencies for all issues (second pass).
func persistDependencies(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		depTable := "dependencies"
		lookupTable := "issues"
		if isWisp(issue) {
			depTable = "wisp_dependencies"
			lookupTable = "wisps"
		}
		for _, dep := range issue.Dependencies {
			// Skip if target doesn't exist.
			var exists int
			//nolint:gosec // G201: table is determined by isWisp flag
			if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", lookupTable), dep.DependsOnID).Scan(&exists); err != nil {
				continue
			}
			//nolint:gosec // G201: table is determined by isWisp flag
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`
				INSERT INTO %s (issue_id, depends_on_id, type, created_by, created_at)
				VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE type = type
			`, depTable), dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.CreatedAt)
			if err != nil {
				return fmt.Errorf("failed to insert dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
		}
	}
	return nil
}

// reconcileChildCounters updates child_counters so that subsequent
// bd create --parent doesn't collide with imported hierarchical IDs.
func reconcileChildCounters(ctx context.Context, tx *sql.Tx, issues []*types.Issue) error {
	childMaxMap := make(map[string]int)
	for _, issue := range issues {
		if parentID, childNum, ok := parseHierarchicalID(issue.ID); ok {
			if childNum > childMaxMap[parentID] {
				childMaxMap[parentID] = childNum
			}
		}
	}
	for parentID, maxChild := range childMaxMap {
		var parentExists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ?", parentID).Scan(&parentExists); err != nil {
			continue // parent not in issues table — skip counter
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, ?)
		`, parentID, maxChild, maxChild)
		if err != nil {
			return fmt.Errorf("failed to reconcile child counter for %s: %w", parentID, err)
		}
	}
	return nil
}
