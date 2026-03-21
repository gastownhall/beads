package notion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
	"github.com/steveyegge/beads/internal/notion/wire"
)

type serviceClient interface {
	StatusResponse(ctx context.Context, databaseID, viewURL string) (*serviceStatusResponse, error)
	PullResponse(ctx context.Context, cacheMaxAge time.Duration) (*servicePullResponse, error)
	PushPayloadResponse(ctx context.Context, payload []byte, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) (*servicePushResponse, error)
}

type serviceFactory func(stderr io.Writer) (serviceClient, error)

// ClientOption mutates a Client at construction time.
type ClientOption func(*Client)

// WithServiceFactory overrides the in-process service factory. Useful for tests.
func WithServiceFactory(factory serviceFactory) ClientOption {
	return func(c *Client) {
		if factory != nil {
			c.newService = factory
		}
	}
}

// NewClient creates a new in-process Notion client backed by internal/notion service calls.
func NewClient(opts ...ClientOption) *Client {
	client := &Client{
		newService: defaultServiceFactory,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// Status runs the integrated Notion status flow and decodes the JSON contract.
func (c *Client) Status(ctx context.Context, req StatusRequest) (*StatusResponse, error) {
	var resp *serviceStatusResponse
	if err := c.withService("status", func(svc serviceClient) error {
		var err error
		resp, err = svc.StatusResponse(ctx, req.DatabaseID, req.ViewURL)
		return err
	}); err != nil {
		return nil, err
	}
	return statusResponseFromService(resp), nil
}

// Pull runs the integrated Notion pull flow and decodes the JSON contract.
func (c *Client) Pull(ctx context.Context, req PullRequest) (*PullResponse, error) {
	var resp *servicePullResponse
	if err := c.withService("pull", func(svc serviceClient) error {
		var err error
		resp, err = svc.PullResponse(ctx, req.CacheMaxAge)
		return err
	}); err != nil {
		return nil, err
	}
	return pullResponseFromService(resp), nil
}

// Push runs the integrated Notion push flow and decodes the JSON contract.
func (c *Client) Push(ctx context.Context, req PushRequest) (*PushResponse, error) {
	if len(req.Payload) == 0 {
		return nil, fmt.Errorf("notion push payload is required")
	}

	var resp *servicePushResponse
	if err := c.withService("push", func(svc serviceClient) error {
		var err error
		resp, err = svc.PushPayloadResponse(ctx, req.Payload, req.DatabaseID, req.ViewURL, false, false, req.CacheMaxAge)
		return err
	}); err != nil {
		return nil, err
	}
	return pushResponseFromService(resp), nil
}

func defaultServiceFactory(stderr io.Writer) (serviceClient, error) {
	paths, err := state.DefaultPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve notion paths: %w", err)
	}
	authStore := state.NewAuthStore(paths)
	ioo := output.NewIO(io.Discard, stderr).WithJSON(true)
	return NewService(ioo, authStore), nil
}

func (c *Client) withService(operation string, invoke func(serviceClient) error) error {
	if c == nil {
		return fmt.Errorf("notion client is nil")
	}
	if c.newService == nil {
		return fmt.Errorf("notion service factory is nil")
	}

	var stderr bytes.Buffer

	svc, err := c.newService(&stderr)
	if err != nil {
		return &CommandError{
			Operation: operation,
			Stderr:    strings.TrimSpace(stderr.String()),
			Err:       err,
		}
	}
	if err := invoke(svc); err != nil {
		return mapServiceError(operation, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func mapServiceError(operation, stderr string, err error) error {
	var cliErr *output.Error
	if errors.As(err, &cliErr) {
		return &BridgeCLIError{
			What:      strings.TrimSpace(cliErr.What),
			Why:       strings.TrimSpace(cliErr.Why),
			Hint:      strings.TrimSpace(cliErr.Hint),
			Operation: operation,
		}
	}
	return &CommandError{
		Operation: operation,
		Stderr:    stderr,
		Err:       err,
	}
}

func statusResponseFromService(resp *serviceStatusResponse) *StatusResponse {
	if resp == nil {
		return nil
	}
	return &StatusResponse{
		Ready:         resp.Ready,
		DataSourceID:  resp.DataSourceID,
		ViewURL:       resp.ViewURL,
		SchemaVersion: resp.SchemaVersion,
		Configured:    resp.Configured,
		SavedConfig:   resp.SavedConfig,
		ConfigSource:  resp.ConfigSource,
		Auth:          statusAuthFromService(resp.Auth),
		Database:      statusDatabaseFromService(resp.Database),
		Views:         statusViewsFromService(resp.Views),
		Schema:        statusSchemaFromWire(resp.Schema),
		Archive:       archiveCapabilityFromWire(resp.Archive),
		State:         statusStateFromService(resp.State),
	}
}

func pullResponseFromService(resp *servicePullResponse) *PullResponse {
	if resp == nil {
		return nil
	}
	issues := make([]PulledIssue, 0, len(resp.Issues))
	for _, issue := range resp.Issues {
		issues = append(issues, pulledIssueFromWire(issue))
	}
	return &PullResponse{
		Issues:  issues,
		Archive: archiveCapabilityFromWire(resp.Archive),
		State:   statusStateFromService(resp.State),
	}
}

func pushResponseFromService(resp *servicePushResponse) *PushResponse {
	if resp == nil {
		return nil
	}
	return &PushResponse{
		DryRun:               resp.DryRun,
		ArchiveRequested:     resp.ArchiveRequested,
		ArchiveSupported:     resp.ArchiveSupported,
		ArchiveReason:        resp.ArchiveReason,
		InputCount:           resp.InputCount,
		CreatedCount:         resp.CreatedCount,
		UpdatedCount:         resp.UpdatedCount,
		SkippedCount:         resp.SkippedCount,
		ArchivedCount:        resp.ArchivedCount,
		BodyUpdatedCount:     resp.BodyUpdatedCount,
		CommentsCreatedCount: resp.CommentsCreatedCount,
		Errors:               pushResultErrorsFromService(resp.Errors),
		Warnings:             append([]string(nil), resp.Warnings...),
		Created:              pushResultItemsFromService(resp.Created),
		Updated:              pushResultItemsFromService(resp.Updated),
		Skipped:              pushResultItemsFromService(resp.Skipped),
		Archived:             pushResultItemsFromService(resp.Archived),
		BodyUpdated:          pushResultItemsFromService(resp.BodyUpdated),
		CommentsCreated:      pushResultItemsFromService(resp.CommentsCreated),
	}
}

func statusAuthFromService(auth *serviceStatusAuth) *StatusAuth {
	if auth == nil {
		return nil
	}
	var user *StatusUser
	if auth.User != nil {
		user = &StatusUser{
			ID:    auth.User.ID,
			Name:  auth.User.Name,
			Email: auth.User.Email,
			Type:  auth.User.Type,
		}
	}
	return &StatusAuth{OK: auth.OK, User: user}
}

func statusDatabaseFromService(database *serviceStatusDatabase) *StatusDatabase {
	if database == nil {
		return nil
	}
	return &StatusDatabase{ID: database.ID, Title: database.Title, URL: database.URL}
}

func statusViewsFromService(views []wire.ViewInfo) []StatusView {
	if len(views) == 0 {
		return nil
	}
	result := make([]StatusView, 0, len(views))
	for _, view := range views {
		result = append(result, StatusView{
			ID:   view.ID,
			Name: view.Name,
			URL:  view.URL,
			Type: view.Type,
		})
	}
	return result
}

func statusSchemaFromWire(schema *wire.SchemaStatus) *StatusSchema {
	if schema == nil {
		return nil
	}
	return &StatusSchema{
		Checked:         schema.Checked,
		Required:        append([]string(nil), schema.Required...),
		Optional:        append([]string(nil), schema.Optional...),
		Detected:        append([]string(nil), schema.Detected...),
		Missing:         append([]string(nil), schema.Missing...),
		OptionalMissing: append([]string(nil), schema.OptionalMissing...),
	}
}

func archiveCapabilityFromWire(archive *wire.ArchiveCapability) *ArchiveCapability {
	if archive == nil {
		return nil
	}
	return &ArchiveCapability{
		Supported:         archive.Supported,
		Mode:              archive.Mode,
		Reason:            archive.Reason,
		SupportedCommands: append([]string(nil), archive.SupportedCommands...),
	}
}

func statusStateFromService(state *serviceStatusState) *StatusState {
	if state == nil {
		return nil
	}
	var summary *DoctorSummary
	if state.DoctorSummary != nil {
		summary = &DoctorSummary{
			OK:                    state.DoctorSummary.OK,
			TotalCount:            state.DoctorSummary.TotalCount,
			OKCount:               state.DoctorSummary.OKCount,
			MissingPageCount:      state.DoctorSummary.MissingPageCount,
			IDDriftCount:          state.DoctorSummary.IDDriftCount,
			PropertyMismatchCount: state.DoctorSummary.PropertyMismatchCount,
		}
	}
	return &StatusState{
		Path:           state.Path,
		Present:        state.Present,
		ManagedCount:   state.ManagedCount,
		ViewConfigured: state.ViewConfigured,
		DoctorSummary:  summary,
	}
}

func pulledIssueFromWire(issue wire.Issue) PulledIssue {
	return PulledIssue{
		ID:           issue.ID,
		Title:        issue.Title,
		Description:  issue.Description,
		Status:       issue.Status,
		Priority:     issue.Priority,
		Type:         issue.Type,
		IssueType:    issue.IssueType,
		Assignee:     issue.Assignee,
		Labels:       append([]string(nil), issue.Labels...),
		ExternalRef:  issue.ExternalRef,
		NotionPageID: issue.NotionPageID,
		CreatedAt:    NullableString(issue.CreatedAt),
		UpdatedAt:    NullableString(issue.UpdatedAt),
	}
}

func pushResultItemsFromService(items []servicePushResultItem) []PushResultItem {
	if len(items) == 0 {
		return nil
	}
	result := make([]PushResultItem, 0, len(items))
	for _, item := range items {
		result = append(result, PushResultItem{
			ID:           item.ID,
			Title:        item.Title,
			ExternalRef:  item.ExternalRef,
			NotionPageID: item.NotionPageID,
			Reason:       item.Reason,
		})
	}
	return result
}

func pushResultErrorsFromService(items []servicePushResultError) []PushResultError {
	if len(items) == 0 {
		return nil
	}
	result := make([]PushResultError, 0, len(items))
	for _, item := range items {
		result = append(result, PushResultError{
			ID:      item.ID,
			Stage:   item.Stage,
			Message: item.Message,
		})
	}
	return result
}
