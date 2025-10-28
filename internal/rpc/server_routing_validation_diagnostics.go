package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/mod/semver"
)

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

	// Update last activity timestamp
	s.lastActivityTime.Store(time.Now())

	var resp Response
	switch req.Operation {
	case OpPing:
		resp = s.handlePing(req)
	case OpStatus:
		resp = s.handleStatus(req)
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
	case OpShutdown:
		resp = s.handleShutdown(req)
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

func (s *Server) handleStatus(_ *Request) Response {
	// Get last activity timestamp
	lastActivity := s.lastActivityTime.Load().(time.Time)
	
	// Check for exclusive lock
	lockActive := false
	lockHolder := ""
	if s.workspacePath != "" {
		if skip, holder, _ := types.ShouldSkipDatabase(s.workspacePath); skip {
			lockActive = true
			lockHolder = holder
		}
	}
	
	statusResp := StatusResponse{
		Version:             ServerVersion,
		WorkspacePath:       s.workspacePath,
		DatabasePath:        s.dbPath,
		SocketPath:          s.socketPath,
		PID:                 os.Getpid(),
		UptimeSeconds:       time.Since(s.startTime).Seconds(),
		LastActivityTime:    lastActivity.Format(time.RFC3339),
		ExclusiveLockActive: lockActive,
		ExclusiveLockHolder: lockHolder,
	}
	
	data, _ := json.Marshal(statusResp)
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

	store := s.storage

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
