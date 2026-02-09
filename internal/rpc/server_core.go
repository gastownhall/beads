package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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
	activeConns   int32          // atomic counter (for metrics/diagnostics)
	connWg        sync.WaitGroup // tracks in-flight connection goroutines for clean drain
	connSemaphore chan struct{}
	// Request timeout
	requestTimeout time.Duration
	// Ready channel signals when server is listening
	readyChan chan struct{}
	// Auto-import single-flight guard
	importInProgress atomic.Bool
	// Pending shutdown: set by handleShutdown, checked by handleConnection after response write
	pendingShutdown atomic.Bool
	// Mutation events for event-driven daemon
	mutationChan  chan MutationEvent
	droppedEvents atomic.Int64 // Counter for dropped mutation events
	// Recent mutations buffer for polling (circular buffer, max 100 events)
	recentMutations   []MutationEvent
	recentMutationsMu sync.RWMutex
	maxMutationBuffer int
	// Daemon configuration (set via SetConfig after creation)
	autoCommit   bool
	autoPush     bool
	autoPull     bool
	localMode    bool
	syncInterval string
	daemonMode   string
}

// Mutation event types
const (
	MutationCreate  = "create"
	MutationUpdate  = "update"
	MutationDelete  = "delete"
	MutationComment = "comment"
	// Molecule-specific event types for activity feed
	MutationBonded   = "bonded"   // Molecule bonded to parent (dynamic bond)
	MutationSquashed = "squashed" // Wisp squashed to digest
	MutationBurned   = "burned"   // Wisp discarded without digest
	MutationStatus   = "status"   // Status change (in_progress, completed, failed)
)

// MutationEvent represents a database mutation for event-driven sync
type MutationEvent struct {
	Type      string // One of the Mutation* constants
	IssueID   string // e.g., "bd-42"
	Title     string // Issue title for display context (may be empty for some operations)
	Assignee  string // Issue assignee for display context (may be empty)
	Actor     string // Who performed the action (may differ from assignee)
	Timestamp time.Time
	// Optional metadata for richer events (used by status, bonded, etc.)
	OldStatus string `json:"old_status,omitempty"` // Previous status (for status events)
	NewStatus string `json:"new_status,omitempty"` // New status (for status events)
	ParentID  string `json:"parent_id,omitempty"`  // Parent molecule (for bonded events)
	StepCount int    `json:"step_count,omitempty"` // Number of steps (for bonded events)
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

	mutationBufferSize := 512 // default (increased from 100 for better burst handling)
	if env := os.Getenv("BEADS_MUTATION_BUFFER"); env != "" {
		var bufSize int
		if _, err := fmt.Sscanf(env, "%d", &bufSize); err == nil && bufSize > 0 {
			mutationBufferSize = bufSize
		}
	}

	s := &Server{
		socketPath:        socketPath,
		workspacePath:     workspacePath,
		dbPath:            dbPath,
		storage:           store,
		shutdownChan:      make(chan struct{}),
		doneChan:          make(chan struct{}),
		startTime:         time.Now(),
		metrics:           NewMetrics(),
		maxConns:          maxConns,
		connSemaphore:     make(chan struct{}, maxConns),
		requestTimeout:    requestTimeout,
		readyChan:         make(chan struct{}),
		mutationChan:      make(chan MutationEvent, mutationBufferSize), // Configurable buffer
		recentMutations:   make([]MutationEvent, 0, 100),
		maxMutationBuffer: 100,
	}
	s.lastActivityTime.Store(time.Now())
	return s
}

// emitMutation sends a mutation event to the daemon's event-driven loop.
// Non-blocking: drops event if channel is full (sync will happen eventually).
// Also stores in recent mutations buffer for polling.
// Title and assignee provide context for activity feeds; pass empty strings if unknown.
func (s *Server) emitMutation(eventType, issueID, title, assignee string) {
	s.emitRichMutation(MutationEvent{
		Type:     eventType,
		IssueID:  issueID,
		Title:    title,
		Assignee: assignee,
	})
}

// emitRichMutation sends a pre-built mutation event with optional metadata.
// Use this for events that include additional context (status changes, bonded events, etc.)
// Non-blocking: drops event if channel is full (sync will happen eventually).
func (s *Server) emitRichMutation(event MutationEvent) {
	// Always set timestamp if not provided
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Send to mutation channel for daemon
	select {
	case s.mutationChan <- event:
		// Event sent successfully
	default:
		// Channel full, increment dropped events counter
		s.droppedEvents.Add(1)
	}

	// Store in recent mutations buffer for polling
	s.recentMutationsMu.Lock()
	s.recentMutations = append(s.recentMutations, event)
	// Keep buffer size limited: copy active window to new slice so the old
	// backing array can be GC'd (reslicing with [1:] leaks memory because
	// dropped head elements remain reachable via the original backing array).
	if len(s.recentMutations) > s.maxMutationBuffer {
		newBuf := make([]MutationEvent, s.maxMutationBuffer)
		copy(newBuf, s.recentMutations[len(s.recentMutations)-s.maxMutationBuffer:])
		s.recentMutations = newBuf
	}
	s.recentMutationsMu.Unlock()
}

// MutationChan returns the mutation event channel for the daemon to consume
func (s *Server) MutationChan() <-chan MutationEvent {
	return s.mutationChan
}

// SetConfig sets the daemon configuration for status reporting
func (s *Server) SetConfig(autoCommit, autoPush, autoPull, localMode bool, syncInterval, daemonMode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoCommit = autoCommit
	s.autoPush = autoPush
	s.autoPull = autoPull
	s.localMode = localMode
	s.syncInterval = syncInterval
	s.daemonMode = daemonMode
}

// ResetDroppedEventsCount resets the dropped events counter and returns the previous value
func (s *Server) ResetDroppedEventsCount() int64 {
	return s.droppedEvents.Swap(0)
}

// GetRecentMutations returns mutations since the given timestamp
func (s *Server) GetRecentMutations(sinceMillis int64) []MutationEvent {
	s.recentMutationsMu.RLock()
	defer s.recentMutationsMu.RUnlock()

	var result []MutationEvent
	for _, m := range s.recentMutations {
		if m.Timestamp.UnixMilli() > sinceMillis {
			result = append(result, m)
		}
	}
	return result
}

// handleGetMutations handles the get_mutations RPC operation
func (s *Server) handleGetMutations(req *Request) Response {
	var args GetMutationsArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	mutations := s.GetRecentMutations(args.Since)
	data, _ := json.Marshal(mutations)

	return Response{
		Success: true,
		Data:    data,
	}
}

// handleGetMoleculeProgress handles the get_molecule_progress RPC operation
// Returns detailed progress for a molecule (parent issue with child steps)
func (s *Server) handleGetMoleculeProgress(req *Request) Response {
	var args GetMoleculeProgressArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Get the molecule (parent issue)
	molecule, err := store.GetIssue(ctx, args.MoleculeID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get molecule: %v", err),
		}
	}
	if molecule == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("molecule not found: %s", args.MoleculeID),
		}
	}

	// Get children (issues that have parent-child dependency on this molecule)
	var children []*types.IssueWithDependencyMetadata
	if sqliteStore, ok := store.(interface {
		GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	}); ok {
		allDependents, err := sqliteStore.GetDependentsWithMetadata(ctx, args.MoleculeID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get molecule children: %v", err),
			}
		}
		// Filter for parent-child relationships only
		for _, dep := range allDependents {
			if dep.DependencyType == types.DepParentChild {
				children = append(children, dep)
			}
		}
	}

	// Get blocked issue IDs for status computation
	blockedIDs := make(map[string]bool)
	if sqliteStore, ok := store.(interface {
		GetBlockedIssueIDs(ctx context.Context) ([]string, error)
	}); ok {
		ids, err := sqliteStore.GetBlockedIssueIDs(ctx)
		if err == nil {
			for _, id := range ids {
				blockedIDs[id] = true
			}
		}
	}

	// Build steps from children
	steps := make([]MoleculeStep, 0, len(children))
	for _, child := range children {
		step := MoleculeStep{
			ID:    child.ID,
			Title: child.Title,
		}

		// Compute step status
		switch child.Status {
		case types.StatusClosed:
			step.Status = "done"
		case types.StatusInProgress:
			step.Status = "current"
		default: // open, blocked, etc.
			if blockedIDs[child.ID] {
				step.Status = "blocked"
			} else {
				step.Status = "ready"
			}
		}

		// Set timestamps
		startTime := child.CreatedAt.Format(time.RFC3339)
		step.StartTime = &startTime

		if child.ClosedAt != nil {
			closeTime := child.ClosedAt.Format(time.RFC3339)
			step.CloseTime = &closeTime
		}

		steps = append(steps, step)
	}

	progress := MoleculeProgress{
		MoleculeID: molecule.ID,
		Title:      molecule.Title,
		Assignee:   molecule.Assignee,
		Steps:      steps,
	}

	data, _ := json.Marshal(progress)
	return Response{
		Success: true,
		Data:    data,
	}
}
