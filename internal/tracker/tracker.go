package tracker

import (
	"context"
	"time"

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
	Init(ctx context.Context, store Store) error

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

// BatchPushTracker is an optional capability for trackers that can export
// multiple issues in one remote call.
type BatchPushTracker interface {
	BatchPush(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error)
}

// BatchPushDryRunner is an optional capability for trackers that can preview
// batch push decisions without mutating the remote system.
type BatchPushDryRunner interface {
	BatchPushDryRun(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error)
}

// PullStatsProvider is an optional capability for trackers that can report
// raw fetch counts separately from incremental pull candidates.
type PullStatsProvider interface {
	LastPullStats() (queried int, candidates int)
}

// SinceGranularityProvider is implemented by trackers whose incremental
// FetchIssues returns every issue updated at or after FetchOptions.Since
// truncated (in UTC) to SinceGranularity, not only those updated at or after
// Since itself: Jira's JQL and Notion's minute-precision page times match to
// the minute, Azure DevOps' date-only WIQL to the day. The engine uses it to
// tell the rows a fetch from the stored last_sync already returned from those
// only its widened overlap window returns.
type SinceGranularityProvider interface {
	SinceGranularity() time.Duration
}

// MatchKeyer is implemented by trackers whose ExtractIdentifier values are
// not unique across the namespace a single pull can span (e.g. GitLab IIDs
// are project-scoped). MatchKey returns the fallback-dedup key for an
// external ref; "" means the ref takes no fallback match.
type MatchKeyer interface {
	MatchKey(ref string) string
}

// IdentityKeyer is implemented by trackers that record a rename-stable
// identity on the issues a pull imports (GitLab: project ID and IID, which a
// project rename or transfer does not change, unlike the URL MatchKey reads).
// When the exact external_ref lookup misses, a pulled issue is matched to the
// local issue with the same identity key; a local issue that has one takes no
// MatchKey fallback. "" means no identity. A key that two local issues hold
// matches neither. The engine asks for local keys after fetching the pull's
// issues, so LocalIdentityKey may judge a recorded identity stale by what
// they showed (GitLab: the ref's project path belongs to another project).
type IdentityKeyer interface {
	LocalIdentityKey(issue *types.Issue) string
	RemoteIdentityKey(issue *TrackerIssue) string
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
