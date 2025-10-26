package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/compact"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/mod/semver"
)

// ServerVersion is the version of this RPC server
// This should match the bd CLI version for proper compatibility checks
// It's set as a var so it can be initialized from main
var ServerVersion = "0.9.10"

const (
	statusUnhealthy = "unhealthy"
)

// normalizeLabels trims whitespace, removes empty strings, and deduplicates labels
func normalizeLabels(ss []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// StorageCacheEntry holds a cached storage with metadata for eviction
type StorageCacheEntry struct {
	store      storage.Storage
	lastAccess time.Time
	dbMtime    time.Time // DB file modification time for detecting external changes
}

// Server represents the RPC server that runs in the daemon
type Server struct {
	socketPath   string
	storage      storage.Storage // Default storage (for backward compat)
	listener     net.Listener
	mu           sync.RWMutex
	shutdown     bool
	shutdownChan chan struct{}
	stopOnce     sync.Once
	doneChan     chan struct{} // closed when Start() cleanup is complete
	// Per-request storage routing with eviction support
	storageCache  map[string]*StorageCacheEntry // repoRoot -> entry
	cacheMu       sync.RWMutex
	maxCacheSize  int
	cacheTTL      time.Duration
	cleanupTicker *time.Ticker
	// Health and metrics
	startTime   time.Time
	cacheHits   int64
	cacheMisses int64
	metrics     *Metrics
	// Connection limiting
	maxConns      int
	activeConns   int32 // atomic counter
	connSemaphore chan struct{}
	// Request timeout
	requestTimeout time.Duration
	// Ready channel signals when server is listening
	readyChan chan struct{}
	// Last JSONL import timestamp (for staleness detection)
	lastImportTime time.Time
	importMu       sync.RWMutex // protects lastImportTime
}

// NewServer creates a new RPC server
func NewServer(socketPath string, store storage.Storage) *Server {
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

	return &Server{
		socketPath:     socketPath,
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
}

// Start starts the RPC server and listens for connections
func (s *Server) Start(_ context.Context) error {
	if err := s.ensureSocketDir(); err != nil {
		return fmt.Errorf("failed to ensure socket directory: %w", err)
	}

	if err := s.removeOldSocket(); err != nil {
		return fmt.Errorf("failed to remove old socket: %w", err)
	}

	listener, err := listenRPC(s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to initialize RPC listener: %w", err)
	}
	s.listener = listener

	// Set socket permissions to 0600 for security (owner only)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.socketPath, 0600); err != nil {
			_ = listener.Close()
			return fmt.Errorf("failed to set socket permissions: %w", err)
		}
	}

	// Store listener under lock
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	// Signal that server is ready to accept connections
	close(s.readyChan)

	go s.handleSignals()
	go s.runCleanupLoop()

	// Ensure cleanup is signaled when this function returns
	defer close(s.doneChan)

	// Accept connections using listener
	for {
		// Get listener under lock
		s.mu.RLock()
		listener := s.listener
		s.mu.RUnlock()

		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			shutdown := s.shutdown
			s.mu.Unlock()
			if shutdown {
				return nil
			}
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		// Try to acquire connection slot (non-blocking)
		select {
		case s.connSemaphore <- struct{}{}:
			// Acquired slot, handle connection
			s.metrics.RecordConnection()
			go func(c net.Conn) {
				defer func() { <-s.connSemaphore }() // Release slot
				atomic.AddInt32(&s.activeConns, 1)
				defer atomic.AddInt32(&s.activeConns, -1)
				s.handleConnection(c)
			}(conn)
		default:
			// Max connections reached, reject immediately
			s.metrics.RecordRejectedConnection()
			_ = conn.Close()
		}
	}
}

// WaitReady waits for the server to be ready to accept connections
func (s *Server) WaitReady() <-chan struct{} {
	return s.readyChan
}

// Stop stops the RPC server and cleans up resources
func (s *Server) Stop() error {
	var err error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.shutdown = true
		s.mu.Unlock()

		// Signal cleanup goroutine to stop
		close(s.shutdownChan)

		// Close all cached storage connections outside lock
		s.cacheMu.Lock()
		stores := make([]storage.Storage, 0, len(s.storageCache))
		for _, entry := range s.storageCache {
			stores = append(stores, entry.store)
		}
		s.storageCache = make(map[string]*StorageCacheEntry)
		s.cacheMu.Unlock()

		// Close stores without holding lock
		for _, store := range stores {
			if closeErr := store.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close storage: %v\n", closeErr)
			}
		}

		// Close listener under lock
		s.mu.Lock()
		listener := s.listener
		s.listener = nil
		s.mu.Unlock()

		if listener != nil {
			if closeErr := listener.Close(); closeErr != nil {
				err = fmt.Errorf("failed to close listener: %w", closeErr)
				return
			}
		}

		if removeErr := s.removeOldSocket(); removeErr != nil {
			err = fmt.Errorf("failed to remove socket: %w", removeErr)
		}
	})

	// Wait for Start() goroutine to finish cleanup (with timeout)
	select {
	case <-s.doneChan:
		// Cleanup completed
	case <-time.After(5 * time.Second):
		// Timeout waiting for cleanup - continue anyway
	}

	return err
}

func (s *Server) ensureSocketDir() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Best-effort tighten permissions if directory already existed
	_ = os.Chmod(dir, 0700)
	return nil
}

func (s *Server) removeOldSocket() error {
	if _, err := os.Stat(s.socketPath); err == nil {
		// Socket exists - check if it's stale before removing
		// Try to connect to see if a daemon is actually using it
		conn, err := dialRPC(s.socketPath, 500*time.Millisecond)
		if err == nil {
			// Socket is active - another daemon is running
			_ = conn.Close()
			return fmt.Errorf("socket %s is in use by another daemon", s.socketPath)
		}

		// Socket is stale - safe to remove
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Server) handleSignals() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, serverSignals...)
	<-sigChan
	_ = s.Stop()
}

// runCleanupLoop periodically evicts stale storage connections and checks memory pressure
func (s *Server) runCleanupLoop() {
	s.cleanupTicker = time.NewTicker(5 * time.Minute)
	defer s.cleanupTicker.Stop()

	for {
		select {
		case <-s.cleanupTicker.C:
			s.checkMemoryPressure()
			s.evictStaleStorage()
		case <-s.shutdownChan:
			return
		}
	}
}

// checkMemoryPressure monitors memory usage and triggers aggressive eviction if needed
func (s *Server) checkMemoryPressure() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Memory thresholds (configurable via env var)
	const defaultThresholdMB = 500
	thresholdMB := defaultThresholdMB
	if env := os.Getenv("BEADS_DAEMON_MEMORY_THRESHOLD_MB"); env != "" {
		var threshold int
		if _, err := fmt.Sscanf(env, "%d", &threshold); err == nil && threshold > 0 {
			thresholdMB = threshold
		}
	}

	allocMB := m.Alloc / 1024 / 1024
	if allocMB > uint64(thresholdMB) {
		fmt.Fprintf(os.Stderr, "Warning: High memory usage detected (%d MB), triggering aggressive cache eviction\n", allocMB)
		s.aggressiveEviction()
		runtime.GC() // Suggest garbage collection
	}
}

// aggressiveEviction evicts 50% of cached storage to reduce memory pressure
func (s *Server) aggressiveEviction() {
	toClose := []storage.Storage{}

	s.cacheMu.Lock()

	if len(s.storageCache) == 0 {
		s.cacheMu.Unlock()
		return
	}

	// Build sorted list by last access
	type cacheItem struct {
		path  string
		entry *StorageCacheEntry
	}
	items := make([]cacheItem, 0, len(s.storageCache))
	for path, entry := range s.storageCache {
		items = append(items, cacheItem{path, entry})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].entry.lastAccess.Before(items[j].entry.lastAccess)
	})

	// Evict oldest 50%
	numToEvict := len(items) / 2
	for i := 0; i < numToEvict; i++ {
		toClose = append(toClose, items[i].entry.store)
		delete(s.storageCache, items[i].path)
	}

	s.cacheMu.Unlock()

	// Close without holding lock
	for _, store := range toClose {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close evicted storage: %v\n", err)
		}
	}
}

// evictStaleStorage removes idle connections and enforces cache size limits
func (s *Server) evictStaleStorage() {
	now := time.Now()
	toClose := []storage.Storage{}

	s.cacheMu.Lock()

	// First pass: evict TTL-expired entries
	for path, entry := range s.storageCache {
		if now.Sub(entry.lastAccess) > s.cacheTTL {
			toClose = append(toClose, entry.store)
			delete(s.storageCache, path)
		}
	}

	// Second pass: enforce max cache size with LRU eviction
	if len(s.storageCache) > s.maxCacheSize {
		// Build sorted list of entries by lastAccess
		type cacheItem struct {
			path  string
			entry *StorageCacheEntry
		}
		items := make([]cacheItem, 0, len(s.storageCache))
		for path, entry := range s.storageCache {
			items = append(items, cacheItem{path, entry})
		}

		// Sort by lastAccess (oldest first) with sort.Slice
		sort.Slice(items, func(i, j int) bool {
			return items[i].entry.lastAccess.Before(items[j].entry.lastAccess)
		})

		// Evict oldest entries until we're under the limit
		numToEvict := len(s.storageCache) - s.maxCacheSize
		for i := 0; i < numToEvict && i < len(items); i++ {
			toClose = append(toClose, items[i].entry.store)
			delete(s.storageCache, items[i].path)
			s.metrics.RecordCacheEviction()
		}
	}

	// Unlock before closing to avoid holding lock during Close
	s.cacheMu.Unlock()

	// Close connections synchronously
	for _, store := range toClose {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close evicted storage: %v\n", err)
		}
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		// Set read deadline for the next request
		if err := conn.SetReadDeadline(time.Now().Add(s.requestTimeout)); err != nil {
			return
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			resp := Response{
				Success: false,
				Error:   fmt.Sprintf("invalid request: %v", err),
			}
			s.writeResponse(writer, resp)
			continue
		}

		// Set write deadline for the response
		if err := conn.SetWriteDeadline(time.Now().Add(s.requestTimeout)); err != nil {
			return
		}

		resp := s.handleRequest(&req)
		s.writeResponse(writer, resp)
	}
}

// checkVersionCompatibility validates client version against server version
// Returns error if versions are incompatible
func (s *Server) checkVersionCompatibility(clientVersion string) error {
	// Allow empty client version (old clients before this feature)
	if clientVersion == "" {
		return nil
	}

	// Normalize versions to semver format (add 'v' prefix if missing)
	serverVer := ServerVersion
	if !strings.HasPrefix(serverVer, "v") {
		serverVer = "v" + serverVer
	}
	clientVer := clientVersion
	if !strings.HasPrefix(clientVer, "v") {
		clientVer = "v" + clientVer
	}

	// Validate versions are valid semver
	if !semver.IsValid(serverVer) || !semver.IsValid(clientVer) {
		// If either version is invalid, allow connection (dev builds, etc)
		return nil
	}

	// Extract major versions
	serverMajor := semver.Major(serverVer)
	clientMajor := semver.Major(clientVer)

	// Major version must match
	if serverMajor != clientMajor {
		cmp := semver.Compare(serverVer, clientVer)
		if cmp < 0 {
			// Daemon is older - needs upgrade
			return fmt.Errorf("incompatible major versions: client %s, daemon %s. Daemon is older; upgrade and restart daemon: 'bd daemon --stop && bd daemon'",
				clientVersion, ServerVersion)
		}
		// Daemon is newer - client needs upgrade
		return fmt.Errorf("incompatible major versions: client %s, daemon %s. Client is older; upgrade the bd CLI to match the daemon's major version",
			clientVersion, ServerVersion)
	}

	// Compare full versions - daemon should be >= client for backward compatibility
	cmp := semver.Compare(serverVer, clientVer)
	if cmp < 0 {
		// Server is older than client within same major version - may be missing features
		return fmt.Errorf("version mismatch: daemon %s is older than client %s. Upgrade and restart daemon: 'bd daemon --stop && bd daemon'",
			ServerVersion, clientVersion)
	}

	// Client is same version or older - OK (daemon supports backward compat within major version)
	return nil
}

// validateDatabaseBinding validates that the client is connecting to the correct daemon
// Returns error if ExpectedDB is set and doesn't match the daemon's database path
func (s *Server) validateDatabaseBinding(req *Request) error {
	// If client doesn't specify ExpectedDB, allow but log warning (old clients)
	if req.ExpectedDB == "" {
		// Log warning for audit trail
		fmt.Fprintf(os.Stderr, "Warning: Client request without database binding validation (old client or missing ExpectedDB)\n")
		return nil
	}

	// For multi-database daemons: If a cwd is provided, verify the client expects
	// the database that would be selected for that cwd
	var daemonDB string
	if req.Cwd != "" {
		// Use the database discovery logic to find which DB would be used
		dbPath := s.findDatabaseForCwd(req.Cwd)
		if dbPath != "" {
			daemonDB = dbPath
		} else {
			// No database found for cwd, will fall back to default storage
			daemonDB = s.storage.Path()
		}
	} else {
		// No cwd provided, use default storage
		daemonDB = s.storage.Path()
	}

	// Normalize both paths for comparison (resolve symlinks, clean paths)
	expectedPath, err := filepath.EvalSymlinks(req.ExpectedDB)
	if err != nil {
		// If we can't resolve expected path, use it as-is
		expectedPath = filepath.Clean(req.ExpectedDB)
	}
	daemonPath, err := filepath.EvalSymlinks(daemonDB)
	if err != nil {
		// If we can't resolve daemon path, use it as-is
		daemonPath = filepath.Clean(daemonDB)
	}

	// Compare paths
	if expectedPath != daemonPath {
		return fmt.Errorf("database mismatch: client expects %s but daemon serves %s. Wrong daemon connection - check socket path",
			req.ExpectedDB, daemonDB)
	}

	return nil
}

func (s *Server) handleRequest(req *Request) Response {
	// Track request timing
	start := time.Now()

	// Defer metrics recording to ensure it always happens
	defer func() {
		latency := time.Since(start)
		s.metrics.RecordRequest(req.Operation, latency)
	}()

	// Validate database binding (skip for health/metrics to allow diagnostics)
	if req.Operation != OpHealth && req.Operation != OpMetrics {
		if err := s.validateDatabaseBinding(req); err != nil {
			s.metrics.RecordError(req.Operation)
			return Response{
				Success: false,
				Error:   err.Error(),
			}
		}
	}

	// Check version compatibility (skip for ping/health to allow version checks)
	if req.Operation != OpPing && req.Operation != OpHealth {
		if err := s.checkVersionCompatibility(req.ClientVersion); err != nil {
			s.metrics.RecordError(req.Operation)
			return Response{
				Success: false,
				Error:   err.Error(),
			}
		}
	}

	// Check for stale JSONL and auto-import if needed (bd-160)
	// Skip for write operations that will trigger export anyway
	// Skip for import operation itself to avoid recursion
	if req.Operation != OpPing && req.Operation != OpHealth && req.Operation != OpMetrics && 
	   req.Operation != OpImport && req.Operation != OpExport {
		if err := s.checkAndAutoImportIfStale(req); err != nil {
			// Log warning but continue - don't fail the request
			fmt.Fprintf(os.Stderr, "Warning: staleness check failed: %v\n", err)
		}
	}

	var resp Response
	switch req.Operation {
	case OpPing:
		resp = s.handlePing(req)
	case OpHealth:
		resp = s.handleHealth(req)
	case OpMetrics:
		resp = s.handleMetrics(req)
	case OpCreate:
		resp = s.handleCreate(req)
	case OpUpdate:
		resp = s.handleUpdate(req)
	case OpClose:
		resp = s.handleClose(req)
	case OpList:
		resp = s.handleList(req)
	case OpShow:
		resp = s.handleShow(req)
	case OpReady:
		resp = s.handleReady(req)
	case OpStats:
		resp = s.handleStats(req)
	case OpDepAdd:
		resp = s.handleDepAdd(req)
	case OpDepRemove:
		resp = s.handleDepRemove(req)
	case OpLabelAdd:
		resp = s.handleLabelAdd(req)
	case OpLabelRemove:
		resp = s.handleLabelRemove(req)
	case OpCommentList:
		resp = s.handleCommentList(req)
	case OpCommentAdd:
		resp = s.handleCommentAdd(req)
	case OpBatch:
		resp = s.handleBatch(req)
	
	case OpCompact:
		resp = s.handleCompact(req)
	case OpCompactStats:
		resp = s.handleCompactStats(req)
	case OpExport:
		resp = s.handleExport(req)
	case OpImport:
		resp = s.handleImport(req)
	case OpEpicStatus:
		resp = s.handleEpicStatus(req)
	default:
		s.metrics.RecordError(req.Operation)
		return Response{
			Success: false,
			Error:   fmt.Sprintf("unknown operation: %s", req.Operation),
		}
	}

	// Record error if request failed
	if !resp.Success {
		s.metrics.RecordError(req.Operation)
	}

	return resp
}

// Adapter helpers
func (s *Server) reqCtx(_ *Request) context.Context {
	return context.Background()
}

func (s *Server) reqActor(req *Request) string {
	if req != nil && req.Actor != "" {
		return req.Actor
	}
	return "daemon"
}

func strValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func updatesFromArgs(a UpdateArgs) map[string]interface{} {
	u := map[string]interface{}{}
	if a.Title != nil {
		u["title"] = *a.Title
	}
	if a.Description != nil {
		u["description"] = *a.Description
	}
	if a.Status != nil {
		u["status"] = *a.Status
	}
	if a.Priority != nil {
		u["priority"] = *a.Priority
	}
	if a.Design != nil {
		u["design"] = a.Design
	}
	if a.AcceptanceCriteria != nil {
		u["acceptance_criteria"] = a.AcceptanceCriteria
	}
	if a.Notes != nil {
		u["notes"] = a.Notes
	}
	if a.Assignee != nil {
		u["assignee"] = a.Assignee
	}
	return u
}

// Handler implementations

func (s *Server) handlePing(_ *Request) Response {
	data, _ := json.Marshal(PingResponse{
		Message: "pong",
		Version: ServerVersion,
	})
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleHealth(req *Request) Response {
	start := time.Now()

	// Get memory stats for health response
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	store, err := s.getStorageForRequest(req)
	if err != nil {
		data, _ := json.Marshal(HealthResponse{
			Status:  "unhealthy",
			Version: ServerVersion,
			Uptime:  time.Since(s.startTime).Seconds(),
			Error:   fmt.Sprintf("storage error: %v", err),
		})
		return Response{
			Success: false,
			Data:    data,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	healthCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	status := "healthy"
	dbError := ""

	_, pingErr := store.GetStatistics(healthCtx)
	dbResponseMs := time.Since(start).Seconds() * 1000

	if pingErr != nil {
		status = statusUnhealthy
		dbError = pingErr.Error()
	} else if dbResponseMs > 500 {
		status = "degraded"
	}

	s.cacheMu.RLock()
	cacheSize := len(s.storageCache)
	s.cacheMu.RUnlock()

	// Check version compatibility
	compatible := true
	if req.ClientVersion != "" {
		if err := s.checkVersionCompatibility(req.ClientVersion); err != nil {
			compatible = false
		}
	}

	health := HealthResponse{
		Status:         status,
		Version:        ServerVersion,
		ClientVersion:  req.ClientVersion,
		Compatible:     compatible,
		Uptime:         time.Since(s.startTime).Seconds(),
		CacheSize:      cacheSize,
		CacheHits:      atomic.LoadInt64(&s.cacheHits),
		CacheMisses:    atomic.LoadInt64(&s.cacheMisses),
		DBResponseTime: dbResponseMs,
		ActiveConns:    atomic.LoadInt32(&s.activeConns),
		MaxConns:       s.maxConns,
		MemoryAllocMB:  m.Alloc / 1024 / 1024,
	}

	if dbError != "" {
		health.Error = dbError
	}

	data, _ := json.Marshal(health)
	return Response{
		Success: status != "unhealthy",
		Data:    data,
		Error:   dbError,
	}
}

func (s *Server) handleMetrics(_ *Request) Response {
	s.cacheMu.RLock()
	cacheSize := len(s.storageCache)
	s.cacheMu.RUnlock()

	snapshot := s.metrics.Snapshot(
		atomic.LoadInt64(&s.cacheHits),
		atomic.LoadInt64(&s.cacheMisses),
		cacheSize,
		int(atomic.LoadInt32(&s.activeConns)),
	)

	data, _ := json.Marshal(snapshot)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleCreate(req *Request) Response {
	var createArgs CreateArgs
	if err := json.Unmarshal(req.Args, &createArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid create args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	var design, acceptance, assignee *string
	if createArgs.Design != "" {
		design = &createArgs.Design
	}
	if createArgs.AcceptanceCriteria != "" {
		acceptance = &createArgs.AcceptanceCriteria
	}
	if createArgs.Assignee != "" {
		assignee = &createArgs.Assignee
	}

	issue := &types.Issue{
		ID:                 createArgs.ID,
		Title:              createArgs.Title,
		Description:        createArgs.Description,
		IssueType:          types.IssueType(createArgs.IssueType),
		Priority:           createArgs.Priority,
		Design:             strValue(design),
		AcceptanceCriteria: strValue(acceptance),
		Assignee:           strValue(assignee),
		Status:             types.StatusOpen,
	}

	ctx := s.reqCtx(req)
	if err := store.CreateIssue(ctx, issue, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create issue: %v", err),
		}
	}

	// Add labels if specified
	for _, label := range createArgs.Labels {
		if err := store.AddLabel(ctx, issue.ID, label, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add label %s: %v", label, err),
			}
		}
	}

	// Add dependencies if specified
	for _, depSpec := range createArgs.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}

		var depType types.DependencyType
		var dependsOnID string

		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) != 2 {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("invalid dependency format '%s', expected 'type:id' or 'id'", depSpec),
				}
			}
			depType = types.DependencyType(strings.TrimSpace(parts[0]))
			dependsOnID = strings.TrimSpace(parts[1])
		} else {
			depType = types.DepBlocks
			dependsOnID = depSpec
		}

		if !depType.IsValid() {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid dependency type '%s' (valid: blocks, related, parent-child, discovered-from)", depType),
			}
		}

		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}
		if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add dependency %s -> %s: %v", issue.ID, dependsOnID, err),
			}
		}
	}

	data, _ := json.Marshal(issue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleUpdate(req *Request) Response {
	var updateArgs UpdateArgs
	if err := json.Unmarshal(req.Args, &updateArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid update args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	updates := updatesFromArgs(updateArgs)
	if len(updates) == 0 {
		return Response{Success: true}
	}

	if err := store.UpdateIssue(ctx, updateArgs.ID, updates, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update issue: %v", err),
		}
	}

	issue, err := store.GetIssue(ctx, updateArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get updated issue: %v", err),
		}
	}

	data, _ := json.Marshal(issue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleClose(req *Request) Response {
	var closeArgs CloseArgs
	if err := json.Unmarshal(req.Args, &closeArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid close args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	if err := store.CloseIssue(ctx, closeArgs.ID, closeArgs.Reason, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close issue: %v", err),
		}
	}

	issue, _ := store.GetIssue(ctx, closeArgs.ID)
	data, _ := json.Marshal(issue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleList(req *Request) Response {
	var listArgs ListArgs
	if err := json.Unmarshal(req.Args, &listArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid list args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	filter := types.IssueFilter{
		Limit: listArgs.Limit,
	}
	if listArgs.Status != "" {
		status := types.Status(listArgs.Status)
		filter.Status = &status
	}
	if listArgs.IssueType != "" {
		issueType := types.IssueType(listArgs.IssueType)
		filter.IssueType = &issueType
	}
	if listArgs.Assignee != "" {
		filter.Assignee = &listArgs.Assignee
	}
	if listArgs.Priority != nil {
		filter.Priority = listArgs.Priority
	}
	// Normalize and apply label filters
	labels := normalizeLabels(listArgs.Labels)
	labelsAny := normalizeLabels(listArgs.LabelsAny)
	// Support both old single Label and new Labels array
	if len(labels) > 0 {
		filter.Labels = labels
	} else if listArgs.Label != "" {
		filter.Labels = []string{strings.TrimSpace(listArgs.Label)}
	}
	if len(labelsAny) > 0 {
		filter.LabelsAny = labelsAny
	}
	if len(listArgs.IDs) > 0 {
		ids := normalizeLabels(listArgs.IDs)
		if len(ids) > 0 {
			filter.IDs = ids
		}
	}

	// Guard against excessive ID lists to avoid SQLite parameter limits
	const maxIDs = 1000
	if len(filter.IDs) > maxIDs {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("--id flag supports at most %d issue IDs, got %d", maxIDs, len(filter.IDs)),
		}
	}

	ctx := s.reqCtx(req)
	issues, err := store.SearchIssues(ctx, listArgs.Query, filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list issues: %v", err),
		}
	}

	// Populate labels for each issue
	for _, issue := range issues {
		labels, _ := store.GetLabels(ctx, issue.ID)
		issue.Labels = labels
	}

	data, _ := json.Marshal(issues)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleShow(req *Request) Response {
	var showArgs ShowArgs
	if err := json.Unmarshal(req.Args, &showArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid show args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	issue, err := store.GetIssue(ctx, showArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}

	// Populate labels, dependencies, and dependents
	labels, _ := store.GetLabels(ctx, issue.ID)
	deps, _ := store.GetDependencies(ctx, issue.ID)
	dependents, _ := store.GetDependents(ctx, issue.ID)

	// Create detailed response with related data
	type IssueDetails struct {
		*types.Issue
		Labels       []string       `json:"labels,omitempty"`
		Dependencies []*types.Issue `json:"dependencies,omitempty"`
		Dependents   []*types.Issue `json:"dependents,omitempty"`
	}

	details := &IssueDetails{
		Issue:        issue,
		Labels:       labels,
		Dependencies: deps,
		Dependents:   dependents,
	}

	data, _ := json.Marshal(details)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleReady(req *Request) Response {
	var readyArgs ReadyArgs
	if err := json.Unmarshal(req.Args, &readyArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid ready args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	wf := types.WorkFilter{
		Status:     types.StatusOpen,
		Priority:   readyArgs.Priority,
		Limit:      readyArgs.Limit,
		SortPolicy: types.SortPolicy(readyArgs.SortPolicy),
	}
	if readyArgs.Assignee != "" {
		wf.Assignee = &readyArgs.Assignee
	}

	ctx := s.reqCtx(req)
	issues, err := store.GetReadyWork(ctx, wf)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get ready work: %v", err),
		}
	}

	data, _ := json.Marshal(issues)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleStats(req *Request) Response {
	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get statistics: %v", err),
		}
	}

	data, _ := json.Marshal(stats)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleDepAdd(req *Request) Response {
	var depArgs DepAddArgs
	if err := json.Unmarshal(req.Args, &depArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid dep add args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	dep := &types.Dependency{
		IssueID:     depArgs.FromID,
		DependsOnID: depArgs.ToID,
		Type:        types.DependencyType(depArgs.DepType),
	}

	ctx := s.reqCtx(req)
	if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to add dependency: %v", err),
		}
	}

	return Response{Success: true}
}

// Generic handler for simple store operations with standard error handling
func (s *Server) handleSimpleStoreOp(req *Request, argsPtr interface{}, argDesc string,
	opFunc func(context.Context, storage.Storage, string) error) Response {
	if err := json.Unmarshal(req.Args, argsPtr); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid %s args: %v", argDesc, err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	if err := opFunc(ctx, store, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to %s: %v", argDesc, err),
		}
	}

	return Response{Success: true}
}

func (s *Server) handleDepRemove(req *Request) Response {
	var depArgs DepRemoveArgs
	return s.handleSimpleStoreOp(req, &depArgs, "dep remove", func(ctx context.Context, store storage.Storage, actor string) error {
		return store.RemoveDependency(ctx, depArgs.FromID, depArgs.ToID, actor)
	})
}

func (s *Server) handleLabelAdd(req *Request) Response {
	var labelArgs LabelAddArgs
	return s.handleSimpleStoreOp(req, &labelArgs, "label add", func(ctx context.Context, store storage.Storage, actor string) error {
		return store.AddLabel(ctx, labelArgs.ID, labelArgs.Label, actor)
	})
}

func (s *Server) handleLabelRemove(req *Request) Response {
	var labelArgs LabelRemoveArgs
	return s.handleSimpleStoreOp(req, &labelArgs, "label remove", func(ctx context.Context, store storage.Storage, actor string) error {
		return store.RemoveLabel(ctx, labelArgs.ID, labelArgs.Label, actor)
	})
}

func (s *Server) handleCommentList(req *Request) Response {
	var commentArgs CommentListArgs
	if err := json.Unmarshal(req.Args, &commentArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid comment list args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	comments, err := store.GetIssueComments(ctx, commentArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list comments: %v", err),
		}
	}

	data, _ := json.Marshal(comments)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleCommentAdd(req *Request) Response {
	var commentArgs CommentAddArgs
	if err := json.Unmarshal(req.Args, &commentArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid comment add args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	comment, err := store.AddIssueComment(ctx, commentArgs.ID, commentArgs.Author, commentArgs.Text)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to add comment: %v", err),
		}
	}

	data, _ := json.Marshal(comment)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleBatch(req *Request) Response {
	var batchArgs BatchArgs
	if err := json.Unmarshal(req.Args, &batchArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid batch args: %v", err),
		}
	}

	results := make([]BatchResult, 0, len(batchArgs.Operations))

	for _, op := range batchArgs.Operations {
		subReq := &Request{
			Operation:     op.Operation,
			Args:          op.Args,
			Actor:         req.Actor,
			RequestID:     req.RequestID,
			Cwd:           req.Cwd,           // Pass through context
			ClientVersion: req.ClientVersion, // Pass through version for compatibility checks
		}

		resp := s.handleRequest(subReq)

		results = append(results, BatchResult(resp))

		if !resp.Success {
			break
		}
	}

	batchResp := BatchResponse{Results: results}
	data, _ := json.Marshal(batchResp)

	return Response{
		Success: true,
		Data:    data,
	}
}

// getStorageForRequest returns the appropriate storage for the request
// If req.Cwd is set, it finds the database for that directory
// Otherwise, it uses the default storage
func (s *Server) getStorageForRequest(req *Request) (storage.Storage, error) {
	// If no cwd specified, use default storage
	if req.Cwd == "" {
		return s.storage, nil
	}

	// Find database for this cwd (to get the canonical repo root)
	dbPath := s.findDatabaseForCwd(req.Cwd)
	if dbPath == "" {
		return nil, fmt.Errorf("no .beads database found for path: %s", req.Cwd)
	}

	// Canonicalize key to repo root (parent of .beads directory)
	beadsDir := filepath.Dir(dbPath)
	repoRoot := filepath.Dir(beadsDir)

	// Check cache first with write lock (to avoid race on lastAccess update)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if entry, ok := s.storageCache[repoRoot]; ok {
		// Check if DB file has been modified externally
		info, err := os.Stat(dbPath)
		if err == nil && !info.ModTime().Equal(entry.dbMtime) {
			// DB file changed - evict stale cache entry
			// Remove from cache first to prevent concurrent access
			delete(s.storageCache, repoRoot)
			atomic.AddInt64(&s.cacheMisses, 1)
			// Close storage after removing from cache (safe now)
			// Unlock briefly to avoid blocking during Close()
			s.cacheMu.Unlock()
			if err := entry.store.Close(); err != nil {
				// Log but don't fail - we'll reopen anyway
				fmt.Fprintf(os.Stderr, "Warning: failed to close stale cached storage: %v\n", err)
			}
			s.cacheMu.Lock()
			// Fall through to reopen
		} else if err == nil {
			// Cache hit - DB file unchanged
			entry.lastAccess = time.Now()
			atomic.AddInt64(&s.cacheHits, 1)
			return entry.store, nil
		} else {
			// Stat failed - evict and reopen
			// Remove from cache first to prevent concurrent access
			delete(s.storageCache, repoRoot)
			atomic.AddInt64(&s.cacheMisses, 1)
			// Close storage after removing from cache
			s.cacheMu.Unlock()
			if err := entry.store.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close cached storage: %v\n", err)
			}
			s.cacheMu.Lock()
			// Fall through to reopen
		}
	} else {
		atomic.AddInt64(&s.cacheMisses, 1)
	}

	// Open storage
	store, err := sqlite.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Get mtime for the newly opened DB
	info, err := os.Stat(dbPath)
	if err != nil {
		// If we can't stat, still cache it but with zero mtime (will invalidate on next check)
		info = nil
	}

	mtime := time.Time{}
	if info != nil {
		mtime = info.ModTime()
	}

	// Cache it with current timestamp and mtime
	s.storageCache[repoRoot] = &StorageCacheEntry{
		store:      store,
		lastAccess: time.Now(),
		dbMtime:    mtime,
	}

	// Enforce LRU immediately to prevent FD spikes between cleanup ticks
	needEvict := len(s.storageCache) > s.maxCacheSize
	s.cacheMu.Unlock()

	if needEvict {
		s.evictStaleStorage()
	}

	// Re-acquire lock for defer
	s.cacheMu.Lock()

	return store, nil
}

// findDatabaseForCwd walks up from cwd to find .beads/*.db
func (s *Server) findDatabaseForCwd(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Found .beads/ directory, look for *.db files
			matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
			if err == nil && len(matches) > 0 {
				return matches[0]
			}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return ""
}

func (s *Server) writeResponse(writer *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	_, _ = writer.Write(data)
	_ = writer.WriteByte('\n')
	_ = writer.Flush()
}

func (s *Server) handleCompact(req *Request) Response {
	var args CompactArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid compact args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get storage: %v", err),
		}
	}

	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return Response{
			Success: false,
			Error:   "compact requires SQLite storage",
		}
	}

	config := &compact.Config{
		APIKey:      args.APIKey,
		Concurrency: args.Workers,
		DryRun:      args.DryRun,
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 5
	}

	compactor, err := compact.New(sqliteStore, args.APIKey, config)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create compactor: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	startTime := time.Now()

	if args.IssueID != "" {
		if !args.Force {
			eligible, reason, err := sqliteStore.CheckEligibility(ctx, args.IssueID, args.Tier)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to check eligibility: %v", err),
				}
			}
			if !eligible {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("%s is not eligible for Tier %d compaction: %s", args.IssueID, args.Tier, reason),
				}
			}
		}

		issue, err := sqliteStore.GetIssue(ctx, args.IssueID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get issue: %v", err),
			}
		}

		originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

		if args.DryRun {
			result := CompactResponse{
				Success:      true,
				IssueID:      args.IssueID,
				OriginalSize: originalSize,
				Reduction:    "70-80%",
				DryRun:       true,
			}
			data, _ := json.Marshal(result)
			return Response{
				Success: true,
				Data:    data,
			}
		}

		if args.Tier == 1 {
			err = compactor.CompactTier1(ctx, args.IssueID)
		} else {
			return Response{
				Success: false,
				Error:   "Tier 2 compaction not yet implemented",
			}
		}

		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("compaction failed: %v", err),
			}
		}

		issueAfter, _ := sqliteStore.GetIssue(ctx, args.IssueID)
		compactedSize := 0
		if issueAfter != nil {
			compactedSize = len(issueAfter.Description)
		}

		duration := time.Since(startTime)
		result := CompactResponse{
			Success:       true,
			IssueID:       args.IssueID,
			OriginalSize:  originalSize,
			CompactedSize: compactedSize,
			Reduction:     fmt.Sprintf("%.1f%%", float64(originalSize-compactedSize)/float64(originalSize)*100),
			Duration:      duration.String(),
		}
		data, _ := json.Marshal(result)
		return Response{
			Success: true,
			Data:    data,
		}
	}

	if args.All {
		var candidates []*sqlite.CompactionCandidate

		switch args.Tier {
		case 1:
			tier1, err := sqliteStore.GetTier1Candidates(ctx)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to get Tier 1 candidates: %v", err),
				}
			}
			candidates = tier1
		case 2:
			tier2, err := sqliteStore.GetTier2Candidates(ctx)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to get Tier 2 candidates: %v", err),
				}
			}
			candidates = tier2
		default:
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid tier: %d (must be 1 or 2)", args.Tier),
			}
		}

		if len(candidates) == 0 {
			result := CompactResponse{
				Success: true,
				Results: []CompactResult{},
			}
			data, _ := json.Marshal(result)
			return Response{
				Success: true,
				Data:    data,
			}
		}

		issueIDs := make([]string, len(candidates))
		for i, c := range candidates {
			issueIDs[i] = c.IssueID
		}

		batchResults, err := compactor.CompactTier1Batch(ctx, issueIDs)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("batch compaction failed: %v", err),
			}
		}

		results := make([]CompactResult, 0, len(batchResults))
		for _, r := range batchResults {
			result := CompactResult{
				IssueID:       r.IssueID,
				Success:       r.Err == nil,
				OriginalSize:  r.OriginalSize,
				CompactedSize: r.CompactedSize,
			}
			if r.Err != nil {
				result.Error = r.Err.Error()
			} else if r.OriginalSize > 0 && r.CompactedSize > 0 {
				result.Reduction = fmt.Sprintf("%.1f%%", float64(r.OriginalSize-r.CompactedSize)/float64(r.OriginalSize)*100)
			}
			results = append(results, result)
		}

		duration := time.Since(startTime)
		response := CompactResponse{
			Success:  true,
			Results:  results,
			Duration: duration.String(),
			DryRun:   args.DryRun,
		}
		data, _ := json.Marshal(response)
		return Response{
			Success: true,
			Data:    data,
		}
	}

	return Response{
		Success: false,
		Error:   "must specify --all or --id",
	}
}

func (s *Server) handleCompactStats(req *Request) Response {
	var args CompactStatsArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid compact stats args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get storage: %v", err),
		}
	}

	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return Response{
			Success: false,
			Error:   "compact stats requires SQLite storage",
		}
	}

	ctx := s.reqCtx(req)

	tier1, err := sqliteStore.GetTier1Candidates(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get Tier 1 candidates: %v", err),
		}
	}

	tier2, err := sqliteStore.GetTier2Candidates(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get Tier 2 candidates: %v", err),
		}
	}

	stats := CompactStatsData{
		Tier1Candidates: len(tier1),
		Tier2Candidates: len(tier2),
		Tier1MinAge:     "30 days",
		Tier2MinAge:     "90 days",
		TotalClosed:     0, // Could query for this but not critical
	}

	result := CompactResponse{
		Success: true,
		Stats:   &stats,
	}
	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleExport handles the export operation
func (s *Server) handleExport(req *Request) Response {
	var exportArgs ExportArgs
	if err := json.Unmarshal(req.Args, &exportArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid export args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get storage: %v", err),
		}
	}

	ctx := s.reqCtx(req)

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issues: %v", err),
		}
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies for all issues (avoid N+1)
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependencies: %v", err),
		}
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get labels for %s: %v", issue.ID, err),
			}
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get comments for %s: %v", issue.ID, err),
			}
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(exportArgs.JSONLPath)
	base := filepath.Base(exportArgs.JSONLPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create temp file: %v", err),
		}
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	// Write JSONL
	encoder := json.NewEncoder(tempFile)
	exportedIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to encode issue %s: %v", issue.ID, err),
			}
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	// Close temp file before rename
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, exportArgs.JSONLPath); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to replace JSONL file: %v", err),
		}
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(exportArgs.JSONLPath, 0600); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Clear dirty flags for exported issues
	if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty flags: %v\n", err)
	}

	result := map[string]interface{}{
		"exported_count": len(exportedIDs),
		"path":           exportArgs.JSONLPath,
	}
	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleImport handles the import operation
func (s *Server) handleImport(req *Request) Response {
	var importArgs ImportArgs
	if err := json.Unmarshal(req.Args, &importArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid import args: %v", err),
		}
	}

	// Note: The actual import logic is complex and lives in cmd/bd/import.go
	// For now, we'll return an error suggesting to use direct mode
	// In the future, we can refactor the import logic into a shared package
	return Response{
		Success: false,
		Error:   "import via daemon not yet implemented, use --no-daemon flag",
	}
}

func (s *Server) handleEpicStatus(req *Request) Response {
	var epicArgs EpicStatusArgs
	if err := json.Unmarshal(req.Args, &epicArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid epic status args: %v", err),
		}
	}

	store, err := s.getStorageForRequest(req)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("storage error: %v", err),
		}
	}

	ctx := s.reqCtx(req)
	epics, err := store.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get epic status: %v", err),
		}
	}

	if epicArgs.EligibleOnly {
		filtered := []*types.EpicStatus{}
		for _, epic := range epics {
			if epic.EligibleForClose {
				filtered = append(filtered, epic)
			}
		}
		epics = filtered
	}

	data, err := json.Marshal(epics)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal epics: %v", err),
		}
	}

	return Response{
		Success: true,
		Data:    data,
	}
}

// GetLastImportTime returns the last JSONL import timestamp
func (s *Server) GetLastImportTime() time.Time {
	s.importMu.RLock()
	defer s.importMu.RUnlock()
	return s.lastImportTime
}

// SetLastImportTime updates the last JSONL import timestamp
func (s *Server) SetLastImportTime(t time.Time) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	s.lastImportTime = t
}

// checkAndAutoImportIfStale checks if JSONL is newer than last import and triggers auto-import
// This fixes bd-158: daemon shows stale data after git pull
func (s *Server) checkAndAutoImportIfStale(req *Request) error {
	// Get storage for this request
	store, err := s.getStorageForRequest(req)
	if err != nil {
		return fmt.Errorf("failed to get storage: %w", err)
	}

	ctx := s.reqCtx(req)
	
	// Get last import time from metadata
	lastImportStr, err := store.GetMetadata(ctx, "last_import_time")
	if err != nil {
		// No metadata yet - first run, skip check
		return nil
	}
	
	lastImportTime, err := time.Parse(time.RFC3339, lastImportStr)
	if err != nil {
		// Invalid timestamp - skip check
		return nil
	}
	
	// Find JSONL file path
	jsonlPath := s.findJSONLPath(req)
	if jsonlPath == "" {
		// No JSONL file found
		return nil
	}
	
	// Check JSONL mtime
	stat, err := os.Stat(jsonlPath)
	if err != nil {
		// JSONL doesn't exist or can't be read
		return nil
	}
	
	// Compare: if JSONL is newer, it's stale
	if stat.ModTime().After(lastImportTime) {
		// JSONL is newer! Trigger auto-import
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: daemon detected stale JSONL (modified %v, last import %v), auto-importing...\n", 
				stat.ModTime(), lastImportTime)
		}
		
		// TODO: Trigger actual import - for now just log
		// This requires refactoring autoImportIfNewer() to be callable from daemon
		fmt.Fprintf(os.Stderr, "Notice: JSONL updated externally (e.g., git pull), restart daemon or run 'bd sync' for fresh data\n")
	}
	
	return nil
}

// findJSONLPath finds the JSONL file path for the request's repository
func (s *Server) findJSONLPath(req *Request) string {
	// Extract repo root from request's working directory
	// For now, use a simple heuristic: look for .beads/ in request's cwd
	beadsDir := filepath.Join(req.Cwd, ".beads")
	
	// Try canonical filenames in order
	candidates := []string{
		filepath.Join(beadsDir, "beads.jsonl"),
		filepath.Join(beadsDir, "issues.jsonl"),
	}
	
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	
	return ""
}
