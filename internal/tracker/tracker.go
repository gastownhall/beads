package tracker

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// IssueTracker is the plugin interface that all tracker integrations must implement.
// Each external system (Linear, GitLab, Jira, etc.) provides an adapter implementing
// this interface. The SyncEngine uses it to perform bidirectional synchronization.
type IssueTracker interface {
	// Name returns the lowercase identifier for this tracker (e.g., "linear", "gitlab").
	Name() string

	// DisplayName returns the human-readable name (e.g., "Linear", "GitLab").
	DisplayName() string

	// ConfigPrefix returns the config key prefix (e.g., "linear", "gitlab").
	ConfigPrefix() string

	// Init initializes the tracker with configuration from the beads config store.
	// Called once before any sync operations.
	Init(ctx context.Context, store *dolt.DoltStore) error

	// Validate checks that the tracker is properly configured and can connect.
	Validate() error

	// Close releases any resources held by the tracker.
	Close() error

	// FetchIssues retrieves issues from the external tracker.
	FetchIssues(ctx context.Context, opts FetchOptions) ([]TrackerIssue, error)

	// FetchIssue retrieves a single issue by its external identifier.
	// Returns nil, nil if the issue doesn't exist.
	FetchIssue(ctx context.Context, identifier string) (*TrackerIssue, error)

	// CreateIssue creates a new issue in the external tracker.
	// Returns the created issue with its external ID and URL populated.
	CreateIssue(ctx context.Context, issue *types.Issue) (*TrackerIssue, error)

	// UpdateIssue updates an existing issue in the external tracker.
	// The externalID is the tracker's internal ID (not the human-readable identifier).
	UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*TrackerIssue, error)

	// FieldMapper returns the field mapper for this tracker.
	FieldMapper() FieldMapper

	// IsExternalRef checks if an external_ref string belongs to this tracker.
	IsExternalRef(ref string) bool

	// ExtractIdentifier extracts the human-readable identifier from an external_ref.
	ExtractIdentifier(ref string) string

	// BuildExternalRef constructs an external_ref string for a tracker issue.
	BuildExternalRef(issue *TrackerIssue) string
}

// FieldMapper handles bidirectional conversion of issue fields between
// an external tracker and beads. Each tracker provides its own mapper.
type FieldMapper interface {
	// PriorityToBeads converts a tracker priority to beads priority (0-4).
	PriorityToBeads(trackerPriority interface{}) int

	// PriorityToTracker converts a beads priority (0-4) to the tracker's format.
	PriorityToTracker(beadsPriority int) interface{}

	// StatusToBeads converts a tracker state to a beads status.
	StatusToBeads(trackerState interface{}) types.Status

	// StatusToTracker converts a beads status to the tracker's state format.
	StatusToTracker(beadsStatus types.Status) interface{}

	// TypeToBeads converts a tracker issue type to a beads issue type.
	TypeToBeads(trackerType interface{}) types.IssueType

	// TypeToTracker converts a beads issue type to the tracker's format.
	TypeToTracker(beadsType types.IssueType) interface{}

	// IssueToBeads performs a full conversion from a tracker issue to a beads issue.
	// Returns the converted issue and any dependencies to be created.
	IssueToBeads(trackerIssue *TrackerIssue) *IssueConversion

	// IssueToTracker builds update fields from a beads issue for the external tracker.
	// Returns a map of field names to values in the tracker's format.
	IssueToTracker(issue *types.Issue) map[string]interface{}
}
