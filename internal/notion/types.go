package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultBinaryPath is the default executable used for Notion bridge operations.
const DefaultBinaryPath = "bdnotion"

// Client executes bdnotion beads commands for Notion sync.
type Client struct {
	binaryPath string
	runner     CommandRunner
}

// StatusRequest describes the inputs for `bdnotion beads status`.
type StatusRequest struct {
	DatabaseID string
	ViewURL    string
}

func (r StatusRequest) args() []string {
	args := []string{"--json"}
	if strings.TrimSpace(r.DatabaseID) != "" {
		args = append(args, "--database-id", r.DatabaseID)
	}
	if strings.TrimSpace(r.ViewURL) != "" {
		args = append(args, "--view-url", r.ViewURL)
	}
	return args
}

// PullRequest describes the inputs for `bdnotion beads pull`.
//
// The current bridge CLI does not accept override flags for pull; it always reads
// the saved beads config and local managed-page manifest.
type PullRequest struct {
	CacheMaxAge time.Duration
}

func (r PullRequest) args() []string {
	args := []string{"--json"}
	if r.CacheMaxAge > 0 {
		args = append(args, "--cache-max-age", r.CacheMaxAge.String())
	}
	return args
}

// PushRequest describes the inputs for `bdnotion beads push`.
type PushRequest struct {
	DatabaseID  string
	ViewURL     string
	Payload     []byte
	CacheMaxAge time.Duration
}

func (r PushRequest) args() []string {
	args := []string{"--json", "--input", "-"}
	if strings.TrimSpace(r.DatabaseID) != "" {
		args = append(args, "--database-id", r.DatabaseID)
	}
	if strings.TrimSpace(r.ViewURL) != "" {
		args = append(args, "--view-url", r.ViewURL)
	}
	if r.CacheMaxAge > 0 {
		args = append(args, "--cache-max-age", r.CacheMaxAge.String())
	}
	return args
}

// StatusResponse is the machine-readable output from `bdnotion beads status --json`.
type StatusResponse struct {
	Ready         bool               `json:"ready"`
	DataSourceID  string             `json:"data_source_id,omitempty"`
	ViewURL       string             `json:"view_url,omitempty"`
	SchemaVersion string             `json:"schema_version,omitempty"`
	Configured    bool               `json:"configured,omitempty"`
	SavedConfig   bool               `json:"saved_config_present,omitempty"`
	ConfigSource  string             `json:"config_source,omitempty"`
	Auth          *StatusAuth        `json:"auth,omitempty"`
	Database      *StatusDatabase    `json:"database,omitempty"`
	Views         []StatusView       `json:"views,omitempty"`
	Schema        *StatusSchema      `json:"schema,omitempty"`
	Archive       *ArchiveCapability `json:"archive,omitempty"`
	State         *StatusState       `json:"state,omitempty"`
}

// StatusAuth describes authentication state.
type StatusAuth struct {
	OK   bool        `json:"ok"`
	User *StatusUser `json:"user,omitempty"`
}

// StatusUser describes the authenticated Notion user.
type StatusUser struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Type  string `json:"type,omitempty"`
}

// StatusDatabase describes the selected Notion database.
type StatusDatabase struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// StatusView describes a database view.
type StatusView struct {
	ID   string  `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
	URL  string  `json:"url,omitempty"`
	Type *string `json:"type,omitempty"`
}

// StatusSchema describes schema validation state.
type StatusSchema struct {
	Checked         bool     `json:"checked,omitempty"`
	Required        []string `json:"required,omitempty"`
	Optional        []string `json:"optional,omitempty"`
	Detected        []string `json:"detected,omitempty"`
	Missing         []string `json:"missing,omitempty"`
	OptionalMissing []string `json:"optional_missing,omitempty"`
}

// ArchiveCapability describes archive support visibility.
type ArchiveCapability struct {
	Supported         bool     `json:"supported"`
	Mode              string   `json:"mode,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	SupportedCommands []string `json:"supported_commands,omitempty"`
}

// StatusState captures local bridge state information.
type StatusState struct {
	Path           string         `json:"path,omitempty"`
	Present        bool           `json:"present,omitempty"`
	ManagedCount   int            `json:"managed_count,omitempty"`
	ViewConfigured bool           `json:"view_configured,omitempty"`
	DoctorSummary  *DoctorSummary `json:"doctor_summary,omitempty"`
}

// DoctorSummary captures machine-readable state health.
type DoctorSummary struct {
	OK                    bool `json:"ok"`
	TotalCount            int  `json:"total_count,omitempty"`
	OKCount               int  `json:"ok_count,omitempty"`
	MissingPageCount      int  `json:"missing_page_count,omitempty"`
	IDDriftCount          int  `json:"id_drift_count,omitempty"`
	PropertyMismatchCount int  `json:"property_mismatch_count,omitempty"`
}

// PullResponse is the machine-readable output from `bdnotion beads pull --json`.
type PullResponse struct {
	Issues  []PulledIssue      `json:"issues"`
	Archive *ArchiveCapability `json:"archive,omitempty"`
	State   *StatusState       `json:"state,omitempty"`
}

// PulledIssue is the normalized issue record returned by bdnotion beads pull.
type PulledIssue struct {
	ID           string          `json:"id,omitempty"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	URL          string          `json:"url,omitempty"`
	Status       string          `json:"status,omitempty"`
	Priority     string          `json:"priority,omitempty"`
	Type         string          `json:"type,omitempty"`
	IssueType    string          `json:"issue_type,omitempty"`
	Assignee     string          `json:"assignee,omitempty"`
	Labels       []string        `json:"labels,omitempty"`
	ExternalRef  string          `json:"external_ref,omitempty"`
	NotionPageID string          `json:"notion_page_id,omitempty"`
	CreatedAt    NullableString  `json:"created_at,omitempty"`
	UpdatedAt    NullableString  `json:"updated_at,omitempty"`
	Archived     bool            `json:"archived,omitempty"`
	Body         string          `json:"body,omitempty"`
	Comments     []PulledComment `json:"comments,omitempty"`
}

// PulledComment is a normalized comment record returned by bdnotion beads pull.
type PulledComment struct {
	CommentID    string         `json:"comment_id,omitempty"`
	DiscussionID string         `json:"discussion_id,omitempty"`
	Body         string         `json:"body,omitempty"`
	Author       string         `json:"author,omitempty"`
	URL          string         `json:"url,omitempty"`
	CreatedAt    NullableString `json:"created_at,omitempty"`
}

// NullableString accepts either a JSON string or null and normalizes null to the zero value.
type NullableString string

// UnmarshalJSON decodes a string or null into the nullable string.
func (s *NullableString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	*s = NullableString(value)
	return nil
}

// PushResponse is the machine-readable output from `bdnotion beads push --json`.
type PushResponse struct {
	DryRun               bool              `json:"dry_run"`
	ArchiveRequested     bool              `json:"archive_requested,omitempty"`
	ArchiveSupported     bool              `json:"archive_supported,omitempty"`
	ArchiveReason        string            `json:"archive_reason,omitempty"`
	InputCount           int               `json:"input_count"`
	CreatedCount         int               `json:"created_count"`
	UpdatedCount         int               `json:"updated_count"`
	SkippedCount         int               `json:"skipped_count"`
	ArchivedCount        int               `json:"archived_count,omitempty"`
	BodyUpdatedCount     int               `json:"body_updated_count,omitempty"`
	CommentsCreatedCount int               `json:"comments_created_count,omitempty"`
	Errors               []PushResultError `json:"errors,omitempty"`
	Created              []PushResultItem  `json:"created,omitempty"`
	Updated              []PushResultItem  `json:"updated,omitempty"`
	Skipped              []PushResultItem  `json:"skipped,omitempty"`
	Archived             []PushResultItem  `json:"archived,omitempty"`
	BodyUpdated          []PushResultItem  `json:"body_updated,omitempty"`
	CommentsCreated      []PushResultItem  `json:"comments_created,omitempty"`
}

// PushResultItem describes one create/update/skip/archive result.
type PushResultItem struct {
	ID           string `json:"id,omitempty"`
	Title        string `json:"title,omitempty"`
	ExternalRef  string `json:"external_ref,omitempty"`
	NotionPageID string `json:"notion_page_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// PushResultError describes a machine-readable push error entry.
type PushResultError struct {
	ID      string `json:"id,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message,omitempty"`
}

// CommandError wraps bridge execution or decoding failures.
type CommandError struct {
	Operation string
	Command   string
	ExitCode  int
	Stderr    string
	Err       error
}

func (e *CommandError) Error() string {
	if e == nil {
		return "<nil>"
	}

	var parts []string
	if e.Operation != "" {
		parts = append(parts, fmt.Sprintf("notion %s failed", e.Operation))
	} else {
		parts = append(parts, "notion command failed")
	}
	if e.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit=%d", e.ExitCode))
	}
	if e.Stderr != "" {
		parts = append(parts, fmt.Sprintf("stderr=%q", e.Stderr))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying process or decode error.
func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusService exposes the client method used by higher layers.
type StatusService interface {
	Status(ctx context.Context, req StatusRequest) (*StatusResponse, error)
}

// PullService exposes the client method used by higher layers.
type PullService interface {
	Pull(ctx context.Context, req PullRequest) (*PullResponse, error)
}

// PushService exposes the client method used by higher layers.
type PushService interface {
	Push(ctx context.Context, req PushRequest) (*PushResponse, error)
}

// PushPayload is the bdnotion beads push input shape.
type PushPayload struct {
	Issues         []PushIssue     `json:"issues"`
	ExistingIssues []ExistingIssue `json:"existing_issues,omitempty"`
}

// PushIssue is one issue entry in the bdnotion beads push payload.
type PushIssue struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	IssueType   string   `json:"issue_type,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	ExternalRef string   `json:"external_ref,omitempty"`
}

// ExistingIssue is an optional authoritative snapshot hint passed to bdnotion beads push.
type ExistingIssue struct {
	ID           string   `json:"id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	IssueType    string   `json:"issue_type,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	ExternalRef  string   `json:"external_ref,omitempty"`
	NotionPageID string   `json:"notion_page_id,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}
