package rpc

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// ServerVersion is the version of this RPC server
// This should match the bd CLI version for proper compatibility checks
// It's set dynamically by daemon.go from cmd/bd/version.go before starting the server
var ServerVersion = "0.0.0" // Placeholder; overridden by daemon startup

const (
	statusUnhealthy = "unhealthy"
)

// Server represents the RPC server that runs in the daemon
type Server struct {
	socketPath    string
	workspacePath string          // Absolute path to workspace root
	dbPath        string          // Absolute path to database file
	storage       storage.Storage // Default storage (for backward compat)
	listener      net.Listener
	mu            sync.RWMutex
	shutdown      bool
	shutdownChan  chan struct{}
	stopOnce      sync.Once
	doneChan      chan struct{} // closed when Start() cleanup is complete
	// Health and metrics
	startTime        time.Time
	lastActivityTime atomic.Value // time.Time - last request timestamp
	metrics          *Metrics
	// Connection limiting
	maxConns      int
	activeConns   int32 // atomic counter
	connSemaphore chan struct{}
	// Request timeout
	requestTimeout time.Duration
	// Ready channel signals when server is listening
	readyChan chan struct{}
	// Auto-import single-flight guard
	importInProgress atomic.Bool
	// Mutation events for event-driven daemon
	mutationChan chan MutationEvent
}

// MutationEvent represents a database mutation for event-driven sync
type MutationEvent struct {
	Type      string    // "create", "update", "delete", "comment"
	IssueID   string    // e.g., "bd-42"
	Timestamp time.Time
}

// NewServer creates a new RPC server
func NewServer(socketPath string, store storage.Storage, workspacePath string, dbPath string) *Server {
	// Parse config from env vars
	maxConns := 100 // default
	if env := os.Getenv("BEADS_DAEMON_MAX_CONNS"); env != "" {
		var conns int
		if _, err := fmt.Sscanf(env, "%d", &conns); err == nil && conns > 0 {
			maxConns = conns
		}
	}

	requestTimeout := 30 * time.Second // default
	if env := os.Getenv("BEADS_DAEMON_REQUEST_TIMEOUT"); env != "" {
		if timeout, err := time.ParseDuration(env); err == nil && timeout > 0 {
			requestTimeout = timeout
		}
	}

	s := &Server{
		socketPath:     socketPath,
		workspacePath:  workspacePath,
		dbPath:         dbPath,
		storage:        store,
		shutdownChan:   make(chan struct{}),
		doneChan:       make(chan struct{}),
		startTime:      time.Now(),
		metrics:        NewMetrics(),
		maxConns:       maxConns,
		connSemaphore:  make(chan struct{}, maxConns),
		requestTimeout: requestTimeout,
		readyChan:      make(chan struct{}),
		mutationChan:   make(chan MutationEvent, 100), // Buffered to avoid blocking
	}
	s.lastActivityTime.Store(time.Now())
	return s
}

// emitMutation sends a mutation event to the daemon's event-driven loop.
// Non-blocking: drops event if channel is full (sync will happen eventually).
func (s *Server) emitMutation(eventType, issueID string) {
	select {
	case s.mutationChan <- MutationEvent{
		Type:      eventType,
		IssueID:   issueID,
		Timestamp: time.Now(),
	}:
		// Event sent successfully
	default:
		// Channel full, event dropped (not critical - sync will happen eventually)
	}
}

// MutationChan returns the mutation event channel for the daemon to consume
func (s *Server) MutationChan() <-chan MutationEvent {
	return s.mutationChan
}
