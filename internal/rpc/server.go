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
	"sync"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// Server represents the RPC server that runs in the daemon
type Server struct {
	socketPath string
	storage    storage.Storage // Default storage (for backward compat)
	listener   net.Listener
	mu         sync.RWMutex
	shutdown   bool
	// Per-request storage routing
	storageCache map[string]storage.Storage // path -> storage
	cacheMu      sync.RWMutex
}

// NewServer creates a new RPC server
func NewServer(socketPath string, store storage.Storage) *Server {
	return &Server{
		socketPath:   socketPath,
		storage:      store,
		storageCache: make(map[string]storage.Storage),
	}
}

// Start starts the RPC server and listens for connections
func (s *Server) Start(ctx context.Context) error {
	if err := s.ensureSocketDir(); err != nil {
		return fmt.Errorf("failed to ensure socket directory: %w", err)
	}

	if err := s.removeOldSocket(); err != nil {
		return fmt.Errorf("failed to remove old socket: %w", err)
	}

	var err error
	s.listener, err = net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on socket: %w", err)
	}

	// Set socket permissions to 0600 for security (owner only)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		s.listener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	go s.handleSignals()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			shutdown := s.shutdown
			s.mu.Unlock()
			if shutdown {
				return nil
			}
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		go s.handleConnection(conn)
	}
}

// Stop stops the RPC server and cleans up resources
func (s *Server) Stop() error {
	s.mu.Lock()
	s.shutdown = true
	s.mu.Unlock()

	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	if err := s.removeOldSocket(); err != nil {
		return fmt.Errorf("failed to remove socket: %w", err)
	}

	return nil
}

func (s *Server) ensureSocketDir() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return nil
}

func (s *Server) removeOldSocket() error {
	if _, err := os.Stat(s.socketPath); err == nil {
		if err := os.Remove(s.socketPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleSignals() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	s.Stop()
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
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

		resp := s.handleRequest(&req)
		s.writeResponse(writer, resp)
	}
}

func (s *Server) handleRequest(req *Request) Response {
	switch req.Operation {
	case OpPing:
		return s.handlePing(req)
	case OpCreate:
		return s.handleCreate(req)
	case OpUpdate:
		return s.handleUpdate(req)
	case OpClose:
		return s.handleClose(req)
	case OpList:
		return s.handleList(req)
	case OpShow:
		return s.handleShow(req)
	case OpReady:
		return s.handleReady(req)
	case OpStats:
		return s.handleStats(req)
	case OpDepAdd:
		return s.handleDepAdd(req)
	case OpDepRemove:
		return s.handleDepRemove(req)
	case OpLabelAdd:
		return s.handleLabelAdd(req)
	case OpLabelRemove:
		return s.handleLabelRemove(req)
	case OpBatch:
		return s.handleBatch(req)
	case OpReposList:
		return s.handleReposList(req)
	case OpReposReady:
		return s.handleReposReady(req)
	case OpReposStats:
		return s.handleReposStats(req)
	case OpReposClearCache:
		return s.handleReposClearCache(req)
	default:
		return Response{
			Success: false,
			Error:   fmt.Sprintf("unknown operation: %s", req.Operation),
		}
	}
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

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func updatesFromArgs(a UpdateArgs) map[string]interface{} {
	u := map[string]interface{}{}
	if a.Title != nil {
		u["title"] = *a.Title
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
		Version: "0.9.8",
	})
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

	ctx := s.reqCtx(req)
	issues, err := store.SearchIssues(ctx, listArgs.Query, filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list issues: %v", err),
		}
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

	data, _ := json.Marshal(issue)
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
		Status:   types.StatusOpen,
		Priority: readyArgs.Priority,
		Limit:    readyArgs.Limit,
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

func (s *Server) handleDepRemove(req *Request) Response {
	var depArgs DepRemoveArgs
	if err := json.Unmarshal(req.Args, &depArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid dep remove args: %v", err),
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
	if err := store.RemoveDependency(ctx, depArgs.FromID, depArgs.ToID, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to remove dependency: %v", err),
		}
	}

	return Response{Success: true}
}

func (s *Server) handleLabelAdd(req *Request) Response {
	var labelArgs LabelAddArgs
	if err := json.Unmarshal(req.Args, &labelArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid label add args: %v", err),
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
	if err := store.AddLabel(ctx, labelArgs.ID, labelArgs.Label, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to add label: %v", err),
		}
	}

	return Response{Success: true}
}

func (s *Server) handleLabelRemove(req *Request) Response {
	var labelArgs LabelRemoveArgs
	if err := json.Unmarshal(req.Args, &labelArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid label remove args: %v", err),
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
	if err := store.RemoveLabel(ctx, labelArgs.ID, labelArgs.Label, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to remove label: %v", err),
		}
	}

	return Response{Success: true}
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
			Operation: op.Operation,
			Args:      op.Args,
			Actor:     req.Actor,
			RequestID: req.RequestID,
			Cwd:       req.Cwd, // Pass through context
		}

		resp := s.handleRequest(subReq)

		results = append(results, BatchResult{
			Success: resp.Success,
			Data:    resp.Data,
			Error:   resp.Error,
		})

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

	// Check cache first
	s.cacheMu.RLock()
	cached, ok := s.storageCache[req.Cwd]
	s.cacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	// Find database for this cwd
	dbPath := s.findDatabaseForCwd(req.Cwd)
	if dbPath == "" {
		return nil, fmt.Errorf("no .beads database found for path: %s", req.Cwd)
	}

	// Open storage
	store, err := sqlite.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Cache it
	s.cacheMu.Lock()
	s.storageCache[req.Cwd] = store
	s.cacheMu.Unlock()

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
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()
}

// Multi-repo handlers

func (s *Server) handleReposList(_ *Request) Response {
	// Keep read lock during iteration to prevent stores from being closed mid-query
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	repos := make([]RepoInfo, 0, len(s.storageCache))
	for path, store := range s.storageCache {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stats, err := store.GetStatistics(ctx)
		cancel()
		if err != nil {
			continue
		}

		// Extract prefix from a sample issue
		filter := types.IssueFilter{Limit: 1}
		ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
		issues, err := store.SearchIssues(ctx2, "", filter)
		cancel2()
		prefix := ""
		if err == nil && len(issues) > 0 && len(issues[0].ID) > 0 {
			// Extract prefix (everything before the last hyphen and number)
			id := issues[0].ID
			for i := len(id) - 1; i >= 0; i-- {
				if id[i] == '-' {
					prefix = id[:i+1]
					break
				}
			}
		}

		repos = append(repos, RepoInfo{
			Path:       path,
			Prefix:     prefix,
			IssueCount: stats.TotalIssues,
			LastAccess: "active",
		})
	}

	data, _ := json.Marshal(repos)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleReposReady(req *Request) Response {
	var args ReposReadyArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid args: %v", err),
		}
	}

	// Keep read lock during iteration to prevent stores from being closed mid-query
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	if args.GroupByRepo {
		result := make([]RepoReadyWork, 0, len(s.storageCache))
		for path, store := range s.storageCache {
			filter := types.WorkFilter{
				Status: types.StatusOpen,
				Limit:  args.Limit,
			}
			if args.Priority != nil {
				filter.Priority = args.Priority
			}
			if args.Assignee != "" {
				filter.Assignee = &args.Assignee
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			issues, err := store.GetReadyWork(ctx, filter)
			cancel()
			if err != nil || len(issues) == 0 {
				continue
			}

			result = append(result, RepoReadyWork{
				RepoPath: path,
				Issues:   issues,
			})
		}

		data, _ := json.Marshal(result)
		return Response{
			Success: true,
			Data:    data,
		}
	}

	// Flat list of all ready issues across all repos
	allIssues := make([]ReposReadyIssue, 0)
	for path, store := range s.storageCache {
		filter := types.WorkFilter{
			Status: types.StatusOpen,
			Limit:  args.Limit,
		}
		if args.Priority != nil {
			filter.Priority = args.Priority
		}
		if args.Assignee != "" {
			filter.Assignee = &args.Assignee
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		issues, err := store.GetReadyWork(ctx, filter)
		cancel()
		if err != nil {
			continue
		}

		for _, issue := range issues {
			allIssues = append(allIssues, ReposReadyIssue{
				RepoPath: path,
				Issue:    issue,
			})
		}
	}

	data, _ := json.Marshal(allIssues)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleReposStats(_ *Request) Response {
	// Keep read lock during iteration to prevent stores from being closed mid-query
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	total := types.Statistics{}
	perRepo := make(map[string]types.Statistics)
	errors := make(map[string]string)

	for path, store := range s.storageCache {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stats, err := store.GetStatistics(ctx)
		cancel()
		if err != nil {
			errors[path] = err.Error()
			continue
		}

		perRepo[path] = *stats

		// Aggregate totals
		total.TotalIssues += stats.TotalIssues
		total.OpenIssues += stats.OpenIssues
		total.InProgressIssues += stats.InProgressIssues
		total.ClosedIssues += stats.ClosedIssues
		total.BlockedIssues += stats.BlockedIssues
		total.ReadyIssues += stats.ReadyIssues
		total.EpicsEligibleForClosure += stats.EpicsEligibleForClosure
	}

	result := ReposStatsResponse{
		Total:   total,
		PerRepo: perRepo,
	}
	if len(errors) > 0 {
		result.Errors = errors
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleReposClearCache(_ *Request) Response {
	// Copy stores under write lock, clear cache, then close outside lock
	// to avoid holding lock during potentially slow Close() operations
	s.cacheMu.Lock()
	stores := make([]storage.Storage, 0, len(s.storageCache))
	for _, store := range s.storageCache {
		stores = append(stores, store)
	}
	s.storageCache = make(map[string]storage.Storage)
	s.cacheMu.Unlock()

	// Close all storage connections without holding lock
	for _, store := range stores {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close storage: %v\n", err)
		}
	}

	return Response{
		Success: true,
		Data:    json.RawMessage(`{"message":"Cache cleared successfully"}`),
	}
}
