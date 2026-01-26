package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/lockfile"
)

// rpcDebugEnabled returns true if BD_DEBUG_RPC environment variable is set
func rpcDebugEnabled() bool {
	val := os.Getenv("BD_DEBUG_RPC")
	return val == "1" || val == "true"
}

// rpcDebugLog logs to stderr if BD_DEBUG_RPC is enabled
func rpcDebugLog(format string, args ...interface{}) {
	if rpcDebugEnabled() {
		fmt.Fprintf(os.Stderr, "[RPC DEBUG] "+format+"\n", args...)
	}
}

// ClientVersion is the version of this RPC client
// This should match the bd CLI version for proper compatibility checks
// It's set dynamically by main.go from cmd/bd/version.go before making RPC calls
var ClientVersion = "0.0.0" // Placeholder; overridden at startup

// Client represents an RPC client that connects to the daemon
type Client struct {
	conn       net.Conn
	socketPath string
	timeout    time.Duration
	dbPath     string // Expected database path for validation
	actor      string // Actor for audit trail (who is performing operations)
}

// TryConnect attempts to connect to the daemon socket
// Returns nil if no daemon is running or unhealthy
func TryConnect(socketPath string) (*Client, error) {
	return TryConnectWithTimeout(socketPath, 200*time.Millisecond)
}

// TryConnectWithTimeout attempts to connect to the daemon socket using the provided dial timeout.
// Returns nil if no daemon is running or unhealthy.
func TryConnectWithTimeout(socketPath string, dialTimeout time.Duration) (*Client, error) {
	rpcDebugLog("attempting connection to socket: %s", socketPath)

	// Fast probe: check daemon lock before attempting RPC connection if socket doesn't exist
	// This eliminates unnecessary connection attempts when no daemon is running
	// If socket exists, we skip lock check for backwards compatibility and test scenarios
	socketExists := endpointExists(socketPath)
	rpcDebugLog("socket exists check: %v", socketExists)

	if !socketExists {
		beadsDir := filepath.Dir(socketPath)
		running, _ := lockfile.TryDaemonLock(beadsDir)
		if !running {
			debug.Logf("daemon lock not held and socket missing (no daemon running)")
			rpcDebugLog("daemon lock not held (no daemon running)")
			// Self-heal: clean up stale artifacts when lock is free and socket is missing
			cleanupStaleDaemonArtifacts(beadsDir)
			return nil, nil
		}
		// Lock is held but socket was missing - re-check socket existence atomically
		// to handle race where daemon just started between first check and lock check
		rpcDebugLog("daemon lock held but socket was missing - re-checking socket existence")
		socketExists = endpointExists(socketPath)
		if !socketExists {
			// Lock held but socket still missing after re-check - daemon startup or crash
			debug.Logf("daemon lock held but socket missing after re-check (startup race or crash): %s", socketPath)
			rpcDebugLog("connection aborted: socket still missing despite lock being held")
			return nil, nil
		}
		rpcDebugLog("socket now exists after re-check (daemon startup race resolved)")
	}

	if dialTimeout <= 0 {
		dialTimeout = 200 * time.Millisecond
	}
	
	rpcDebugLog("dialing socket (timeout: %v)", dialTimeout)
	dialStart := time.Now()
	conn, err := dialRPC(socketPath, dialTimeout)
	dialDuration := time.Since(dialStart)
	
	if err != nil {
		debug.Logf("failed to connect to RPC endpoint: %v", err)
		rpcDebugLog("dial failed after %v: %v", dialDuration, err)

		// Fast-fail: socket exists but dial failed - check if daemon actually alive
		// If lock is not held, daemon crashed and left stale socket - clean up immediately
		beadsDir := filepath.Dir(socketPath)
		running, _ := lockfile.TryDaemonLock(beadsDir)
		if !running {
			rpcDebugLog("daemon not running (lock free) - cleaning up stale socket")
			cleanupStaleDaemonArtifacts(beadsDir)
			_ = os.Remove(socketPath) // Also remove stale socket
		}
		return nil, nil
	}
	
	rpcDebugLog("dial succeeded in %v", dialDuration)

	client := &Client{
		conn:       conn,
		socketPath: socketPath,
		timeout:    30 * time.Second,
	}

	rpcDebugLog("performing health check")
	healthStart := time.Now()
	health, err := client.Health()
	healthDuration := time.Since(healthStart)
	
	if err != nil {
		debug.Logf("health check failed: %v", err)
		rpcDebugLog("health check failed after %v: %v", healthDuration, err)
		_ = conn.Close()
		return nil, nil
	}

	if health.Status == "unhealthy" {
		debug.Logf("daemon unhealthy: %s", health.Error)
		rpcDebugLog("daemon unhealthy (checked in %v): %s", healthDuration, health.Error)
		_ = conn.Close()
		return nil, nil
	}

	debug.Logf("connected to daemon (status: %s, uptime: %.1fs)",
		health.Status, health.Uptime)
	rpcDebugLog("connection successful (health check: %v, status: %s, uptime: %.1fs)",
		healthDuration, health.Status, health.Uptime)

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

// SetDatabasePath sets the expected database path for validation
func (c *Client) SetDatabasePath(dbPath string) {
	c.dbPath = dbPath
}

// SetActor sets the actor for audit trail (who is performing operations)
func (c *Client) SetActor(actor string) {
	c.actor = actor
}

// Execute sends an RPC request and waits for a response
func (c *Client) Execute(operation string, args interface{}) (*Response, error) {
	return c.ExecuteWithCwd(operation, args, "")
}

// ExecuteWithCwd sends an RPC request with an explicit cwd (or current dir if empty string)
func (c *Client) ExecuteWithCwd(operation string, args interface{}, cwd string) (*Response, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal args: %w", err)
	}

	// Use provided cwd, or get current working directory for database routing
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	req := Request{
		Operation:     operation,
		Args:          argsJSON,
		Actor:         c.actor, // Who is performing this operation
		ClientVersion: ClientVersion,
		Cwd:           cwd,
		ExpectedDB:    c.dbPath, // Send expected database path for validation
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

// Status retrieves daemon status metadata
func (c *Client) Status() (*StatusResponse, error) {
	resp, err := c.Execute(OpStatus, nil)
	if err != nil {
		return nil, err
	}

	var status StatusResponse
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status response: %w", err)
	}

	return &status, nil
}

// Health sends a health check request to verify the daemon is healthy
func (c *Client) Health() (*HealthResponse, error) {
	resp, err := c.Execute(OpHealth, nil)
	if err != nil {
		return nil, err
	}

	var health HealthResponse
	if err := json.Unmarshal(resp.Data, &health); err != nil {
		return nil, fmt.Errorf("failed to unmarshal health response: %w", err)
	}

	return &health, nil
}

// Shutdown sends a graceful shutdown request to the daemon
func (c *Client) Shutdown() error {
	_, err := c.Execute(OpShutdown, nil)
	return err
}

// Metrics retrieves daemon metrics
func (c *Client) Metrics() (*MetricsSnapshot, error) {
	resp, err := c.Execute(OpMetrics, nil)
	if err != nil {
		return nil, err
	}

	var metrics MetricsSnapshot
	if err := json.Unmarshal(resp.Data, &metrics); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metrics response: %w", err)
	}

	return &metrics, nil
}

// Create creates a new issue via the daemon
func (c *Client) Create(args *CreateArgs) (*Response, error) {
	return c.Execute(OpCreate, args)
}

// Update updates an issue via the daemon
func (c *Client) Update(args *UpdateArgs) (*Response, error) {
	return c.Execute(OpUpdate, args)
}

// CloseIssue marks an issue as closed via the daemon.
func (c *Client) CloseIssue(args *CloseArgs) (*Response, error) {
	return c.Execute(OpClose, args)
}

// Delete deletes one or more issues via the daemon.
func (c *Client) Delete(args *DeleteArgs) (*Response, error) {
	return c.Execute(OpDelete, args)
}

// List lists issues via the daemon
func (c *Client) List(args *ListArgs) (*Response, error) {
	return c.Execute(OpList, args)
}

// Count counts issues via the daemon
func (c *Client) Count(args *CountArgs) (*Response, error) {
	return c.Execute(OpCount, args)
}

// Show shows an issue via the daemon
func (c *Client) Show(args *ShowArgs) (*Response, error) {
	return c.Execute(OpShow, args)
}

// ResolveID resolves a partial issue ID to a full ID via the daemon
func (c *Client) ResolveID(args *ResolveIDArgs) (*Response, error) {
	return c.Execute(OpResolveID, args)
}

// Ready gets ready work via the daemon
func (c *Client) Ready(args *ReadyArgs) (*Response, error) {
	return c.Execute(OpReady, args)
}

// Blocked gets blocked issues via the daemon
func (c *Client) Blocked(args *BlockedArgs) (*Response, error) {
	return c.Execute(OpBlocked, args)
}

// Stale gets stale issues via the daemon
func (c *Client) Stale(args *StaleArgs) (*Response, error) {
	return c.Execute(OpStale, args)
}

// Stats gets statistics via the daemon
func (c *Client) Stats() (*Response, error) {
	return c.Execute(OpStats, nil)
}

// GetMutations retrieves recent mutations from the daemon
func (c *Client) GetMutations(args *GetMutationsArgs) (*Response, error) {
	return c.Execute(OpGetMutations, args)
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

// ListComments retrieves comments for an issue via the daemon
func (c *Client) ListComments(args *CommentListArgs) (*Response, error) {
	return c.Execute(OpCommentList, args)
}

// AddComment adds a comment to an issue via the daemon
func (c *Client) AddComment(args *CommentAddArgs) (*Response, error) {
	return c.Execute(OpCommentAdd, args)
}

// Batch executes multiple operations atomically
func (c *Client) Batch(args *BatchArgs) (*Response, error) {
	return c.Execute(OpBatch, args)
}



// Export exports the database to JSONL format
func (c *Client) Export(args *ExportArgs) (*Response, error) {
	return c.Execute(OpExport, args)
}

// EpicStatus gets epic completion status via the daemon
func (c *Client) EpicStatus(args *EpicStatusArgs) (*Response, error) {
	return c.Execute(OpEpicStatus, args)
}

// Gate operations

// GateCreate creates a gate via the daemon
func (c *Client) GateCreate(args *GateCreateArgs) (*Response, error) {
	return c.Execute(OpGateCreate, args)
}

// GateList lists gates via the daemon
func (c *Client) GateList(args *GateListArgs) (*Response, error) {
	return c.Execute(OpGateList, args)
}

// GateShow shows a gate via the daemon
func (c *Client) GateShow(args *GateShowArgs) (*Response, error) {
	return c.Execute(OpGateShow, args)
}

// GateClose closes a gate via the daemon
func (c *Client) GateClose(args *GateCloseArgs) (*Response, error) {
	return c.Execute(OpGateClose, args)
}

// GateWait adds waiters to a gate via the daemon
func (c *Client) GateWait(args *GateWaitArgs) (*Response, error) {
	return c.Execute(OpGateWait, args)
}

// GetWorkerStatus retrieves worker status via the daemon
func (c *Client) GetWorkerStatus(args *GetWorkerStatusArgs) (*GetWorkerStatusResponse, error) {
	resp, err := c.Execute(OpGetWorkerStatus, args)
	if err != nil {
		return nil, err
	}

	var result GetWorkerStatusResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal worker status response: %w", err)
	}

	return &result, nil
}

// GetConfig retrieves a config value from the daemon's database
func (c *Client) GetConfig(args *GetConfigArgs) (*GetConfigResponse, error) {
	resp, err := c.Execute(OpGetConfig, args)
	if err != nil {
		return nil, err
	}

	var result GetConfigResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config response: %w", err)
	}

	return &result, nil
}

// MolStale retrieves stale molecules (complete-but-unclosed) via the daemon
func (c *Client) MolStale(args *MolStaleArgs) (*MolStaleResponse, error) {
	resp, err := c.Execute(OpMolStale, args)
	if err != nil {
		return nil, err
	}

	var result MolStaleResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mol stale response: %w", err)
	}

	return &result, nil
}

// cleanupStaleDaemonArtifacts removes stale daemon.pid file when socket is missing and lock is free.
// This prevents stale artifacts from accumulating after daemon crashes.
// Only removes pid file - lock file is managed by OS (released on process exit).
func cleanupStaleDaemonArtifacts(beadsDir string) {
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	
	// Check if pid file exists
	if _, err := os.Stat(pidFile); err != nil {
		// No pid file to clean up
		return
	}
	
	// Remove stale pid file
	if err := os.Remove(pidFile); err != nil {
		debug.Logf("failed to remove stale pid file: %v", err)
		return
	}
	
	debug.Logf("removed stale daemon.pid file (lock free, socket missing)")
}
