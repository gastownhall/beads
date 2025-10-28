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
	// Per-request storage routing with eviction support
	storageCache  map[string]*StorageCacheEntry // repoRoot -> entry
	cacheMu       sync.RWMutex
	maxCacheSize  int
	cacheTTL      time.Duration
	cleanupTicker *time.Ticker
	// Health and metrics
	startTime        time.Time
	lastActivityTime atomic.Value // time.Time - last request timestamp
	cacheHits        int64
	cacheMisses      int64
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
}

// NewServer creates a new RPC server
func NewServer(socketPath string, store storage.Storage, workspacePath string, dbPath string) *Server {
	// Parse config from env vars
	maxCacheSize := 50 // default
	if env := os.Getenv("BEADS_DAEMON_MAX_CACHE_SIZE"); env != "" {
		// Parse as integer
		var size int
		if _, err := fmt.Sscanf(env, "%d", &size); err == nil && size > 0 {
			maxCacheSize = size
		}
	}

	cacheTTL := 30 * time.Minute // default
	if env := os.Getenv("BEADS_DAEMON_CACHE_TTL"); env != "" {
		if ttl, err := time.ParseDuration(env); err == nil && ttl > 0 {
			cacheTTL = ttl
		}
	}

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
		storageCache:   make(map[string]*StorageCacheEntry),
		maxCacheSize:   maxCacheSize,
		cacheTTL:       cacheTTL,
		shutdownChan:   make(chan struct{}),
		doneChan:       make(chan struct{}),
		startTime:      time.Now(),
		metrics:        NewMetrics(),
		maxConns:       maxConns,
		connSemaphore:  make(chan struct{}, maxConns),
		requestTimeout: requestTimeout,
		readyChan:      make(chan struct{}),
	}
	s.lastActivityTime.Store(time.Now())
	return s
}
