package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
)

type serviceClient interface {
	Status(ctx context.Context, databaseID, viewURL string) error
	Pull(ctx context.Context, cacheMaxAge time.Duration) error
	PushPayload(ctx context.Context, payload []byte, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) error
}

type serviceFactory func(stdout, stderr io.Writer) (serviceClient, error)

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
	var resp StatusResponse
	if err := c.runJSON("status", func(svc serviceClient) error {
		return svc.Status(ctx, req.DatabaseID, req.ViewURL)
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Pull runs the integrated Notion pull flow and decodes the JSON contract.
func (c *Client) Pull(ctx context.Context, req PullRequest) (*PullResponse, error) {
	var resp PullResponse
	if err := c.runJSON("pull", func(svc serviceClient) error {
		return svc.Pull(ctx, req.CacheMaxAge)
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Push runs the integrated Notion push flow and decodes the JSON contract.
func (c *Client) Push(ctx context.Context, req PushRequest) (*PushResponse, error) {
	if len(req.Payload) == 0 {
		return nil, fmt.Errorf("notion push payload is required")
	}

	var resp PushResponse
	if err := c.runJSON("push", func(svc serviceClient) error {
		return svc.PushPayload(ctx, req.Payload, req.DatabaseID, req.ViewURL, false, false, req.CacheMaxAge)
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func defaultServiceFactory(stdout, stderr io.Writer) (serviceClient, error) {
	paths, err := state.DefaultPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve notion paths: %w", err)
	}
	authStore := state.NewAuthStore(paths)
	ioo := output.NewIO(stdout, stderr).WithJSON(true)
	return NewService(ioo, authStore), nil
}

func (c *Client) runJSON(operation string, invoke func(serviceClient) error, target any) error {
	if c == nil {
		return fmt.Errorf("notion client is nil")
	}
	if c.newService == nil {
		return fmt.Errorf("notion service factory is nil")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	svc, err := c.newService(&stdout, &stderr)
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
	if err := decodeStrictJSON(stdout.Bytes(), target); err != nil {
		return &CommandError{
			Operation: operation,
			Stderr:    strings.TrimSpace(stderr.String()),
			Err:       fmt.Errorf("failed to decode JSON response: %w", err),
		}
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

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}

	var extra struct{}
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON data")
}
