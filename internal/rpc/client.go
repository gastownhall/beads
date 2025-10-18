package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// Client represents an RPC client that connects to the daemon
type Client struct {
	conn       net.Conn
	socketPath string
	timeout    time.Duration
}

// TryConnect attempts to connect to the daemon socket
// Returns nil if no daemon is running
func TryConnect(socketPath string) (*Client, error) {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: socket does not exist: %s\n", socketPath)
		}
		return nil, nil
	}

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to dial socket: %v\n", err)
		}
		return nil, nil
	}

	client := &Client{
		conn:       conn,
		socketPath: socketPath,
		timeout:    30 * time.Second,
	}

	if err := client.Ping(); err != nil {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: ping failed: %v\n", err)
		}
		conn.Close()
		return nil, nil
	}

	return client, nil
}

// Close closes the connection to the daemon
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// SetTimeout sets the request timeout duration
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
}

// Execute sends an RPC request and waits for a response
func (c *Client) Execute(operation string, args interface{}) (*Response, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal args: %w", err)
	}

	req := Request{
		Operation: operation,
		Args:      argsJSON,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if c.timeout > 0 {
		deadline := time.Now().Add(c.timeout)
		if err := c.conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("failed to set deadline: %w", err)
		}
	}

	writer := bufio.NewWriter(c.conn)
	if _, err := writer.Write(reqJSON); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}
	if err := writer.WriteByte('\n'); err != nil {
		return nil, fmt.Errorf("failed to write newline: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return nil, fmt.Errorf("failed to flush: %w", err)
	}

	reader := bufio.NewReader(c.conn)
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if !resp.Success {
		return &resp, fmt.Errorf("operation failed: %s", resp.Error)
	}

	return &resp, nil
}

// Ping sends a ping request to verify the daemon is alive
func (c *Client) Ping() error {
	resp, err := c.Execute(OpPing, nil)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("ping failed: %s", resp.Error)
	}

	return nil
}

// Create creates a new issue via the daemon
func (c *Client) Create(args *CreateArgs) (*Response, error) {
	return c.Execute(OpCreate, args)
}

// Update updates an issue via the daemon
func (c *Client) Update(args *UpdateArgs) (*Response, error) {
	return c.Execute(OpUpdate, args)
}

// Close closes an issue via the daemon (operation, not connection)
func (c *Client) CloseIssue(args *CloseArgs) (*Response, error) {
	return c.Execute(OpClose, args)
}

// List lists issues via the daemon
func (c *Client) List(args *ListArgs) (*Response, error) {
	return c.Execute(OpList, args)
}

// Show shows an issue via the daemon
func (c *Client) Show(args *ShowArgs) (*Response, error) {
	return c.Execute(OpShow, args)
}

// Ready gets ready work via the daemon
func (c *Client) Ready(args *ReadyArgs) (*Response, error) {
	return c.Execute(OpReady, args)
}

// Stats gets statistics via the daemon
func (c *Client) Stats() (*Response, error) {
	return c.Execute(OpStats, nil)
}

// AddDependency adds a dependency via the daemon
func (c *Client) AddDependency(args *DepAddArgs) (*Response, error) {
	return c.Execute(OpDepAdd, args)
}

// RemoveDependency removes a dependency via the daemon
func (c *Client) RemoveDependency(args *DepRemoveArgs) (*Response, error) {
	return c.Execute(OpDepRemove, args)
}

// AddLabel adds a label via the daemon
func (c *Client) AddLabel(args *LabelAddArgs) (*Response, error) {
	return c.Execute(OpLabelAdd, args)
}

// RemoveLabel removes a label via the daemon
func (c *Client) RemoveLabel(args *LabelRemoveArgs) (*Response, error) {
	return c.Execute(OpLabelRemove, args)
}

// Batch executes multiple operations atomically
func (c *Client) Batch(args *BatchArgs) (*Response, error) {
	return c.Execute(OpBatch, args)
}

// ReposList lists all cached repositories
func (c *Client) ReposList() (*Response, error) {
	return c.Execute(OpReposList, struct{}{})
}

// ReposReady gets ready work across all repositories
func (c *Client) ReposReady(args *ReposReadyArgs) (*Response, error) {
	return c.Execute(OpReposReady, args)
}

// ReposStats gets combined statistics across all repositories
func (c *Client) ReposStats() (*Response, error) {
	return c.Execute(OpReposStats, struct{}{})
}

// ReposClearCache clears the repository cache
func (c *Client) ReposClearCache() (*Response, error) {
	return c.Execute(OpReposClearCache, struct{}{})
}
