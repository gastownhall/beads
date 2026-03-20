package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// CommandRunner executes the ncli process.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error)
}

// ExecRunner executes ncli through exec.CommandContext.
type ExecRunner struct{}

// Run executes a command and returns stdout, stderr, and the run error.
func (ExecRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err := cmd.Run()

	return stdout.Bytes(), stderr.Bytes(), err
}

// ClientOption mutates a Client at construction time.
type ClientOption func(*Client)

// WithBinaryPath overrides the ncli binary path.
func WithBinaryPath(path string) ClientOption {
	return func(c *Client) {
		if strings.TrimSpace(path) != "" {
			c.binaryPath = path
		}
	}
}

// WithRunner overrides the process runner. Useful for tests.
func WithRunner(runner CommandRunner) ClientOption {
	return func(c *Client) {
		if runner != nil {
			c.runner = runner
		}
	}
}

// NewClient creates a new Notion client backed by ncli beads commands.
func NewClient(opts ...ClientOption) *Client {
	client := &Client{
		binaryPath: DefaultBinaryPath,
		runner:     ExecRunner{},
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// BinaryPath returns the configured ncli binary path.
func (c *Client) BinaryPath() string {
	return c.binaryPath
}

// Status runs `ncli beads status --json`.
func (c *Client) Status(ctx context.Context, req StatusRequest) (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.runJSON(ctx, "status", req.args(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Pull runs `ncli beads pull --json`.
func (c *Client) Pull(ctx context.Context, req PullRequest) (*PullResponse, error) {
	var resp PullResponse
	if err := c.runJSON(ctx, "pull", req.args(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Push runs `ncli beads push --json --input -`.
func (c *Client) Push(ctx context.Context, req PushRequest) (*PushResponse, error) {
	if len(req.Payload) == 0 {
		return nil, fmt.Errorf("notion push payload is required")
	}

	var resp PushResponse
	if err := c.runJSON(ctx, "push", req.args(), req.Payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) runJSON(ctx context.Context, operation string, args []string, stdin []byte, target any) error {
	if c == nil {
		return fmt.Errorf("notion client is nil")
	}
	if c.runner == nil {
		return fmt.Errorf("notion command runner is nil")
	}
	if strings.TrimSpace(c.binaryPath) == "" {
		return fmt.Errorf("notion binary path is empty")
	}

	fullArgs := make([]string, 0, len(args)+2)
	fullArgs = append(fullArgs, "beads", operation)
	fullArgs = append(fullArgs, args...)

	stdout, stderr, err := c.runner.Run(ctx, c.binaryPath, fullArgs, stdin)
	if err != nil {
		if bridgeErr := decodeBridgeCLIError(operation, c.binaryPath, fullArgs, stdout, err); bridgeErr != nil {
			return bridgeErr
		}
		return newCommandError(operation, c.binaryPath, fullArgs, stderr, err)
	}
	if err := decodeStrictJSON(stdout, target); err != nil {
		return &CommandError{
			Operation: operation,
			Command:   buildCommandString(c.binaryPath, fullArgs),
			Stderr:    strings.TrimSpace(string(stderr)),
			Err:       fmt.Errorf("failed to decode JSON response: %w", err),
		}
	}
	return nil
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
	if err == nil {
		return fmt.Errorf("unexpected trailing JSON content")
	}
	return err
}

func buildCommandString(binaryPath string, args []string) string {
	parts := append([]string{binaryPath}, args...)
	return strings.Join(parts, " ")
}

func decodeBridgeCLIError(operation, binaryPath string, args []string, stdout []byte, runErr error) error {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil
	}

	var payload bridgeCLIErrorPayload
	if err := decodeStrictJSON(stdout, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.Error) == "" {
		return nil
	}

	bridgeErr := &BridgeCLIError{
		What:      strings.TrimSpace(payload.Error),
		Why:       strings.TrimSpace(payload.Why),
		Hint:      strings.TrimSpace(payload.Hint),
		Operation: operation,
		Command:   buildCommandString(binaryPath, args),
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		bridgeErr.ExitCode = exitErr.ExitCode()
		return bridgeErr
	}

	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		bridgeErr.ExitCode = -1
		return bridgeErr
	}

	bridgeErr.ExitCode = -1
	return bridgeErr
}

func newCommandError(operation, binaryPath string, args []string, stderr []byte, runErr error) *CommandError {
	commandErr := &CommandError{
		Operation: operation,
		Command:   buildCommandString(binaryPath, args),
		Stderr:    strings.TrimSpace(string(stderr)),
		Err:       runErr,
	}

	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		commandErr.ExitCode = -1
		return commandErr
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		commandErr.ExitCode = exitErr.ExitCode()
		return commandErr
	}

	commandErr.ExitCode = -1

	return commandErr
}
