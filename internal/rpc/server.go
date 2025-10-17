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

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Server represents the RPC server that runs in the daemon
type Server struct {
	socketPath string
	storage    storage.Storage
	listener   net.Listener
	mu         sync.Mutex
	shutdown   bool
}

// NewServer creates a new RPC server
func NewServer(socketPath string, store storage.Storage) *Server {
	return &Server{
		socketPath: socketPath,
		storage:    store,
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
	if err := s.storage.CreateIssue(ctx, issue, s.reqActor(req)); err != nil {
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

	ctx := s.reqCtx(req)
	updates := updatesFromArgs(updateArgs)
	if len(updates) == 0 {
		return Response{Success: true}
	}

	if err := s.storage.UpdateIssue(ctx, updateArgs.ID, updates, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update issue: %v", err),
		}
	}

	issue, err := s.storage.GetIssue(ctx, updateArgs.ID)
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

	ctx := s.reqCtx(req)
	if err := s.storage.CloseIssue(ctx, closeArgs.ID, closeArgs.Reason, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close issue: %v", err),
		}
	}

	issue, _ := s.storage.GetIssue(ctx, closeArgs.ID)
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
	issues, err := s.storage.SearchIssues(ctx, listArgs.Query, filter)
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

	ctx := s.reqCtx(req)
	issue, err := s.storage.GetIssue(ctx, showArgs.ID)
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

	wf := types.WorkFilter{
		Status:   types.StatusOpen,
		Priority: readyArgs.Priority,
		Limit:    readyArgs.Limit,
	}
	if readyArgs.Assignee != "" {
		wf.Assignee = &readyArgs.Assignee
	}

	ctx := s.reqCtx(req)
	issues, err := s.storage.GetReadyWork(ctx, wf)
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
	ctx := s.reqCtx(req)
	stats, err := s.storage.GetStatistics(ctx)
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

	dep := &types.Dependency{
		IssueID:     depArgs.FromID,
		DependsOnID: depArgs.ToID,
		Type:        types.DependencyType(depArgs.DepType),
	}

	ctx := s.reqCtx(req)
	if err := s.storage.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
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

	ctx := s.reqCtx(req)
	if err := s.storage.RemoveDependency(ctx, depArgs.FromID, depArgs.ToID, s.reqActor(req)); err != nil {
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

	ctx := s.reqCtx(req)
	if err := s.storage.AddLabel(ctx, labelArgs.ID, labelArgs.Label, s.reqActor(req)); err != nil {
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

	ctx := s.reqCtx(req)
	if err := s.storage.RemoveLabel(ctx, labelArgs.ID, labelArgs.Label, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to remove label: %v", err),
		}
	}

	return Response{Success: true}
}

func (s *Server) writeResponse(writer *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()
}
