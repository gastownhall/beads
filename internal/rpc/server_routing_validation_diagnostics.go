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

	// Compare full versions - daemon must be >= client (strict minor version gating)
	// This prevents stale daemons from serving requests with old schema assumptions
	cmp := semver.Compare(serverVer, clientVer)
	if cmp < 0 {
		// Server is older than client - refuse connection
		// Extract minor versions for clearer error message
		serverMinor := semver.MajorMinor(serverVer)
		clientMinor := semver.MajorMinor(clientVer)
		
		if serverMinor != clientMinor {
			// Minor version mismatch - schema may be incompatible
			return fmt.Errorf("version mismatch: client v%s requires daemon upgrade (daemon is v%s). The client may expect schema changes not present in this daemon version. Run: bd daemons killall",
				clientVersion, ServerVersion)
		}
		
		// Patch version difference - usually safe but warn
		return fmt.Errorf("version mismatch: daemon v%s is older than client v%s. Upgrade and restart daemon: bd daemons killall",
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

	// Local daemon always uses single storage
	daemonDB := s.storage.Path()

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

	// Check for stale JSONL and auto-import if needed
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
	case OpDelete:
		resp = s.handleDelete(req)
	case OpList:
		resp = s.handleList(req)
	case OpCount:
		resp = s.handleCount(req)
	case OpShow:
		resp = s.handleShow(req)
	case OpResolveID:
		resp = s.handleResolveID(req)
	case OpReady:
		resp = s.handleReady(req)
	case OpBlocked:
		resp = s.handleBlocked(req)
	case OpStale:
		resp = s.handleStale(req)
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
	case OpGetMutations:
		resp = s.handleGetMutations(req)
	case OpGetMoleculeProgress:
		resp = s.handleGetMoleculeProgress(req)
	case OpGetWorkerStatus:
		resp = s.handleGetWorkerStatus(req)
	case OpGetConfig:
		resp = s.handleGetConfig(req)
	case OpMolStale:
		resp = s.handleMolStale(req)
	case OpShutdown:
		resp = s.handleShutdown(req)
	// Gate operations
	case OpGateCreate:
		resp = s.handleGateCreate(req)
	case OpGateList:
		resp = s.handleGateList(req)
	case OpGateShow:
		resp = s.handleGateShow(req)
	case OpGateClose:
		resp = s.handleGateClose(req)
	case OpGateWait:
		resp = s.handleGateWait(req)
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
	
	// Read config under lock
	s.mu.RLock()
	autoCommit := s.autoCommit
	autoPush := s.autoPush
	autoPull := s.autoPull
	localMode := s.localMode
	syncInterval := s.syncInterval
	daemonMode := s.daemonMode
	s.mu.RUnlock()
	
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
		AutoCommit:          autoCommit,
		AutoPush:            autoPush,
		AutoPull:            autoPull,
		LocalMode:           localMode,
		SyncInterval:        syncInterval,
		DaemonMode:          daemonMode,
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
	snapshot := s.metrics.Snapshot(
		int(atomic.LoadInt32(&s.activeConns)),
	)

	data, _ := json.Marshal(snapshot)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleGetWorkerStatus(req *Request) Response {
	ctx := s.reqCtx(req)

	// Parse optional args
	var args GetWorkerStatusArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid args: %v", err),
			}
		}
	}

	// Build filter: find all in_progress issues with assignees
	filter := types.IssueFilter{
		Status: func() *types.Status { s := types.StatusInProgress; return &s }(),
	}
	if args.Assignee != "" {
		filter.Assignee = &args.Assignee
	}

	// Get all in_progress issues (potential workers)
	issues, err := s.storage.SearchIssues(ctx, "", filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to search issues: %v", err),
		}
	}

	var workers []WorkerStatus
	for _, issue := range issues {
		// Skip issues without assignees
		if issue.Assignee == "" {
			continue
		}

		worker := WorkerStatus{
			Assignee:     issue.Assignee,
			LastActivity: issue.UpdatedAt.Format(time.RFC3339),
			Status:       string(issue.Status),
		}

		// Check if this issue is a child of a molecule/epic (has parent-child dependency)
		deps, err := s.storage.GetDependencyRecords(ctx, issue.ID)
		if err == nil {
			for _, dep := range deps {
				if dep.Type == types.DepParentChild {
					// This issue is a child - get the parent molecule
					parentIssue, err := s.storage.GetIssue(ctx, dep.DependsOnID)
					if err == nil && parentIssue != nil {
						worker.MoleculeID = parentIssue.ID
						worker.MoleculeTitle = parentIssue.Title
						worker.StepID = issue.ID
						worker.StepTitle = issue.Title

						// Count total steps and determine current step number
						// by getting all children of the molecule
						children, err := s.storage.GetDependents(ctx, parentIssue.ID)
						if err == nil {
							// Filter to only parent-child dependencies
							var steps []*types.Issue
							for _, child := range children {
								childDeps, err := s.storage.GetDependencyRecords(ctx, child.ID)
								if err == nil {
									for _, childDep := range childDeps {
										if childDep.Type == types.DepParentChild && childDep.DependsOnID == parentIssue.ID {
											steps = append(steps, child)
											break
										}
									}
								}
							}
							worker.TotalSteps = len(steps)

							// Find current step number (1-indexed)
							for i, step := range steps {
								if step.ID == issue.ID {
									worker.CurrentStep = i + 1
									break
								}
							}
						}
					}
					break // Found the parent, no need to check other deps
				}
			}
		}

		workers = append(workers, worker)
	}

	resp := GetWorkerStatusResponse{
		Workers: workers,
	}

	data, _ := json.Marshal(resp)
	return Response{
		Success: true,
		Data:    data,
	}
}
