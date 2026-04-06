package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// BatchContext holds per-batch state read once and reused for every issue.
type BatchContext struct {
	CustomStatuses  []string
	CustomTypes     []string
	ConfigPrefix    string
	AllowedPrefixes string
	Opts            storage.BatchCreateOptions
}

// NewBatchContext reads config from the database and returns a BatchContext.
func NewBatchContext(ctx context.Context, tx *sql.Tx, opts storage.BatchCreateOptions) (*BatchContext, error) {
	customStatuses, err := GetCustomStatusesTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom types: %w", err)
	}
	configPrefix, err := ReadConfigPrefix(ctx, tx)
	if err != nil {
		return nil, err
	}
	var allowedPrefixes string
	_ = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "allowed_prefixes").Scan(&allowedPrefixes)

	return &BatchContext{
		CustomStatuses:  customStatuses,
		CustomTypes:     customTypes,
		ConfigPrefix:    configPrefix,
		AllowedPrefixes: allowedPrefixes,
		Opts:            opts,
	}, nil
}

// CreateIssueInTx handles a single issue within a transaction:
// prepare, resolve prefix, generate ID, validate prefix, check orphans,
// insert, record event, persist labels/comments.
// Returns nil if the issue was skipped (e.g., orphan skip mode).
func CreateIssueInTx(ctx context.Context, tx *sql.Tx, bc *BatchContext, issue *types.Issue, actor string) error {
	if err := PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
		return err
	}

	issueTable, eventTable := TableRouting(issue)

	// Resolve prefix and generate ID if needed.
	if issue.ID == "" {
		prefix := bc.ConfigPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		} else if issue.IDPrefix != "" {
			prefix = bc.ConfigPrefix + "-" + issue.IDPrefix
		} else if IsWisp(issue) {
			prefix = bc.ConfigPrefix + "-wisp"
		}
		var err error
		issue.ID, err = GenerateIssueIDInTable(ctx, tx, issueTable, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
	} else if !bc.Opts.SkipPrefixValidation {
		if err := ValidateIssueIDPrefix(issue.ID, bc.ConfigPrefix, bc.AllowedPrefixes); err != nil {
			return fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
		}
	}

	if skip, err := CheckOrphan(ctx, tx, issue, issueTable, bc.Opts.OrphanHandling); err != nil {
		return err
	} else if skip {
		return nil
	}

	isNew, err := InsertIssueIfNew(ctx, tx, issueTable, issue)
	if err != nil {
		return err
	}

	if isNew {
		if err := RecordEventInTable(ctx, tx, eventTable, issue.ID, types.EventCreated, actor, ""); err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}

	if err := PersistLabels(ctx, tx, issue); err != nil {
		return err
	}
	return PersistComments(ctx, tx, issue)
}

// CreateIssuesInTx creates multiple issues within a single transaction.
// Handles the first pass (insert each issue), second pass (dependencies),
// and child counter reconciliation. Does NOT handle dolt versioning.
func CreateIssuesInTx(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	bc, err := NewBatchContext(ctx, tx, opts)
	if err != nil {
		return err
	}

	for _, issue := range issues {
		if err := CreateIssueInTx(ctx, tx, bc, issue, actor); err != nil {
			return err
		}
	}

	if err := PersistDependencies(ctx, tx, issues, actor); err != nil {
		return err
	}

	return ReconcileChildCounters(ctx, tx, issues)
}

// PrepareIssueForInsert normalizes timestamps, validates, and computes the content hash.
func PrepareIssueForInsert(issue *types.Issue, customStatuses, customTypes []string) error {
	if err := ValidateMetadataIfConfigured(issue.Metadata); err != nil {
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

// ValidateIssueIDPrefix validates that the issue ID matches the configured prefix
// or any of the allowed_prefixes.
func ValidateIssueIDPrefix(id, prefix, allowedPrefixes string) error {
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

// ParseHierarchicalID checks if an ID is hierarchical (e.g., "bd-abc.1")
// and returns the parent ID and child number.
func ParseHierarchicalID(id string) (parentID string, childNum int, ok bool) {
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

// AllWisps returns true if every issue in the slice should be routed to the
// wisps table (i.e., is ephemeral or no-history). Used to gate the fast path
// that skips Dolt versioning in batch creates.
func AllWisps(issues []*types.Issue) bool {
	for _, issue := range issues {
		if !issue.Ephemeral && !issue.NoHistory {
			return false
		}
	}
	return true
}

// CheckOrphan handles orphan detection for hierarchical IDs.
// Returns (skip=true, nil) if the issue should be skipped.
//
//nolint:gosec // G201: table is a hardcoded constant
func CheckOrphan(ctx context.Context, tx *sql.Tx, issue *types.Issue, issueTable string, handling storage.OrphanHandling) (skip bool, err error) {
	if issue.ID == "" {
		return false, nil
	}
	parentID, _, ok := ParseHierarchicalID(issue.ID)
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

// InsertIssueIfNew inserts the issue and returns whether it was genuinely new.
//
//nolint:gosec // G201: table is a hardcoded constant
func InsertIssueIfNew(ctx context.Context, tx *sql.Tx, issueTable string, issue *types.Issue) (isNew bool, err error) {
	var existingCount int
	if issue.ID != "" {
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), issue.ID).Scan(&existingCount); err != nil {
			return false, fmt.Errorf("failed to check issue existence for %s: %w", issue.ID, err)
		}
	}
	if err := InsertIssueIntoTable(ctx, tx, issueTable, issue); err != nil {
		return false, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
	}
	return existingCount == 0, nil
}

// PersistLabels writes issue.Labels into the appropriate labels table.
func PersistLabels(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	if len(issue.Labels) == 0 {
		return nil
	}
	labelTable := "labels"
	if IsWisp(issue) {
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

// PersistComments writes issue.Comments into the appropriate comments table.
// The comments table uses a UUID PK (DEFAULT UUID()), so ON DUPLICATE KEY UPDATE
// would never match. Instead, we check for an existing identical comment
// (same issue_id, author, and created_at) before inserting to prevent
// duplicates on re-import.
func PersistComments(ctx context.Context, tx *sql.Tx, issue *types.Issue) error {
	if len(issue.Comments) == 0 {
		return nil
	}
	commentTable := "comments"
	if IsWisp(issue) {
		commentTable = "wisp_comments"
	}
	for _, comment := range issue.Comments {
		createdAt := comment.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		// Check for existing identical comment to prevent duplicates on re-import.
		// The UUID PK means ON DUPLICATE KEY UPDATE would never fire,
		// so we do an explicit existence check instead.
		var exists int
		//nolint:gosec // G201: table is determined by ephemeral flag
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COUNT(*) FROM %s
			WHERE issue_id = ? AND author = ? AND created_at = ? AND text = ?
		`, commentTable), issue.ID, comment.Author, createdAt, comment.Text).Scan(&exists); err != nil {
			return fmt.Errorf("failed to check comment existence for %s: %w", issue.ID, err)
		}
		if exists > 0 {
			continue
		}
		//nolint:gosec // G201: table is determined by ephemeral flag
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?)
		`, commentTable), issue.ID, comment.Author, comment.Text, createdAt)
		if err != nil {
			return fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// PersistDependencies inserts dependencies for all issues (second pass).
func PersistDependencies(ctx context.Context, tx *sql.Tx, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		depTable := "dependencies"
		lookupTable := "issues"
		if IsWisp(issue) {
			depTable = "wisp_dependencies"
			lookupTable = "wisps"
		}
		for _, dep := range issue.Dependencies {
			// Default IssueID to the owning issue when not pre-set (e.g.,
			// markdown bulk create where the ID is auto-generated).
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}
			// Skip if target doesn't exist.
			var exists int
			//nolint:gosec // G201: table is determined by isWisp flag
			if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", lookupTable), dep.DependsOnID).Scan(&exists); err != nil {
				continue
			}
			createdAt := dep.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			//nolint:gosec // G201: table is determined by isWisp flag
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`
				INSERT INTO %s (issue_id, depends_on_id, type, created_by, created_at)
				VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE type = type
			`, depTable), dep.IssueID, dep.DependsOnID, dep.Type, actor, createdAt)
			if err != nil {
				return fmt.Errorf("failed to insert dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
		}
	}
	return nil
}

// ReconcileChildCounters updates child_counters so that subsequent
// bd create --parent doesn't collide with imported hierarchical IDs.
func ReconcileChildCounters(ctx context.Context, tx *sql.Tx, issues []*types.Issue) error {
	childMaxMap := make(map[string]int)
	for _, issue := range issues {
		if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok {
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
