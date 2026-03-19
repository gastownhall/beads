package notion

import (
	"context"
	"fmt"
	"strings"
)

// DefaultBinaryPath is the default executable used for Notion operations.
const DefaultBinaryPath = "ncli"

// Client executes ncli beads commands for Notion sync.
type Client struct {
	binaryPath string
	runner     CommandRunner
}

// StatusRequest describes the inputs for `ncli beads status`.
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

// PullRequest describes the inputs for `ncli beads pull`.
type PullRequest struct {
	ViewURL string
}

func (r PullRequest) args() []string {
	args := []string{"--json"}
	if strings.TrimSpace(r.ViewURL) != "" {
		args = append(args, "--view-url", r.ViewURL)
	}
	return args
}

// PushRequest describes the inputs for `ncli beads push`.
type PushRequest struct {
	DatabaseID string
	ViewURL    string
	Payload    []byte
}

func (r PushRequest) args() []string {
	args := []string{"--json", "--input", "-"}
	if strings.TrimSpace(r.DatabaseID) != "" {
		args = append(args, "--database-id", r.DatabaseID)
	}
	if strings.TrimSpace(r.ViewURL) != "" {
		args = append(args, "--view-url", r.ViewURL)
	}
	return args
}

// StatusResponse is the machine-readable output from `ncli beads status --json`.
type StatusResponse struct {
	Ready         bool               `json:"ready"`
	DataSourceID  string             `json:"data_source_id,omitempty"`
	Auth          *StatusAuth        `json:"auth,omitempty"`
	Database      *StatusDatabase    `json:"database,omitempty"`
	Views         []StatusView       `json:"views,omitempty"`
	Schema        *StatusSchema      `json:"schema,omitempty"`
	Archive       *ArchiveCapability `json:"archive,omitempty"`
	DoctorSummary *DoctorSummary     `json:"doctor_summary,omitempty"`
}

// StatusAuth describes authentication state.
type StatusAuth struct {
	Ready  bool   `json:"ready,omitempty"`
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
}

// StatusDatabase describes the selected Notion database.
type StatusDatabase struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// StatusView describes a database view.
type StatusView struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"`
}

// StatusSchema describes schema validation state.
type StatusSchema struct {
	Version         string   `json:"version,omitempty"`
	Required        []string `json:"required,omitempty"`
	Missing         []string `json:"missing,omitempty"`
	OptionalMissing []string `json:"optional_missing,omitempty"`
}

// ArchiveCapability describes archive support visibility.
type ArchiveCapability struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// DoctorSummary captures machine-readable state health.
type DoctorSummary struct {
	OK               bool     `json:"ok"`
	MissingPages     []string `json:"missing_pages,omitempty"`
	BeadsIDDrift     []string `json:"beads_id_drift,omitempty"`
	PropertyMismatch []string `json:"property_mismatch,omitempty"`
}

// PullResponse is the machine-readable output from `ncli beads pull --json`.
type PullResponse struct {
	Issues []PulledIssue `json:"issues"`
}

// PulledIssue is the normalized issue record returned by ncli beads pull.
type PulledIssue struct {
	ID           string          `json:"id,omitempty"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	Status       string          `json:"status,omitempty"`
	Priority     string          `json:"priority,omitempty"`
	IssueType    string          `json:"issue_type,omitempty"`
	Assignee     string          `json:"assignee,omitempty"`
	Labels       []string        `json:"labels,omitempty"`
	ExternalRef  string          `json:"external_ref,omitempty"`
	NotionPageID string          `json:"notion_page_id,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
	Archived     bool            `json:"archived,omitempty"`
	Body         string          `json:"body,omitempty"`
	Comments     []PulledComment `json:"comments,omitempty"`
}

// PulledComment is a normalized comment record returned by ncli beads pull.
type PulledComment struct {
	CommentID string `json:"comment_id,omitempty"`
	Body      string `json:"body,omitempty"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// PushResponse is the machine-readable output from `ncli beads push --json`.
type PushResponse struct {
	DryRun               bool              `json:"dry_run"`
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

// CommandError wraps ncli execution or decoding failures.
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
