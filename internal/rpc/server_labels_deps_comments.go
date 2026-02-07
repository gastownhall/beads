package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// lookupIssueMeta fetches title and assignee for mutation events.
// Returns empty strings on error (acceptable for non-critical mutation metadata).
func (s *Server) lookupIssueMeta(ctx context.Context, issueID string) (title, assignee string) {
	if s.storage == nil {
		return "", ""
	}
	issue, err := s.storage.GetIssue(ctx, issueID)
	if err != nil || issue == nil {
		return "", ""
	}
	return issue.Title, issue.Assignee
}

// isChildOf returns true if childID is a hierarchical child of parentID.
// For example, "bd-abc.1" is a child of "bd-abc", and "bd-abc.1.2" is a child of "bd-abc.1".
func isChildOf(childID, parentID string) bool {
	_, actualParentID, depth := types.ParseHierarchicalID(childID)
	if depth == 0 {
		return false // Not a hierarchical ID
	}
	if actualParentID == parentID {
		return true
	}
	return strings.HasPrefix(childID, parentID+".")
}

func (s *Server) handleDepAdd(req *Request) Response {
	var depArgs DepAddArgs
	if err := json.Unmarshal(req.Args, &depArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid dep add args: %v", err),
		}
	}

	// Check for child->parent dependency anti-pattern
	// This creates a deadlock: child can't start (parent open), parent can't close (children not done)
	if isChildOf(depArgs.FromID, depArgs.ToID) {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("cannot add dependency: %s is already a child of %s (children inherit dependency via hierarchy)", depArgs.FromID, depArgs.ToID),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	dep := &types.Dependency{
		IssueID:     depArgs.FromID,
		DependsOnID: depArgs.ToID,
		Type:        types.DependencyType(depArgs.DepType),
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to add dependency: %v", err),
		}
	}

	// Emit mutation event for event-driven daemon
	title, assignee := s.lookupIssueMeta(ctx, depArgs.FromID)
	s.emitMutation(MutationUpdate, depArgs.FromID, title, assignee)

	result := map[string]interface{}{
		"status":        "added",
		"issue_id":      depArgs.FromID,
		"depends_on_id": depArgs.ToID,
		"type":          depArgs.DepType,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// Generic handler for simple store operations with standard error handling.
// issueIDFunc is called after args are unmarshaled to extract the issue ID
// for mutation events (passing the ID directly would evaluate before unmarshal).
func (s *Server) handleSimpleStoreOp(req *Request, argsPtr interface{}, argDesc string,
	opFunc func(context.Context, storage.Storage, string) error, issueIDFunc func() string,
	responseData func() map[string]interface{}) Response {
	if err := json.Unmarshal(req.Args, argsPtr); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid %s args: %v", argDesc, err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	if err := opFunc(ctx, store, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to %s: %v", argDesc, err),
		}
	}

	// Emit mutation event for event-driven daemon
	issueID := issueIDFunc()
	title, assignee := s.lookupIssueMeta(ctx, issueID)
	s.emitMutation(MutationUpdate, issueID, title, assignee)

	if responseData != nil {
		data, _ := json.Marshal(responseData())
		return Response{Success: true, Data: data}
	}
	return Response{Success: true}
}

func (s *Server) handleDepRemove(req *Request) Response {
	var depArgs DepRemoveArgs
	return s.handleSimpleStoreOp(req, &depArgs, "dep remove",
		func(ctx context.Context, store storage.Storage, actor string) error {
			return store.RemoveDependency(ctx, depArgs.FromID, depArgs.ToID, actor)
		},
		func() string { return depArgs.FromID },
		func() map[string]interface{} {
			return map[string]interface{}{
				"status":        "removed",
				"issue_id":      depArgs.FromID,
				"depends_on_id": depArgs.ToID,
			}
		},
	)
}

func (s *Server) handleLabelAdd(req *Request) Response {
	var labelArgs LabelAddArgs
	return s.handleSimpleStoreOp(req, &labelArgs, "label add", func(ctx context.Context, store storage.Storage, actor string) error {
		return store.AddLabel(ctx, labelArgs.ID, labelArgs.Label, actor)
	}, func() string { return labelArgs.ID }, nil)
}

func (s *Server) handleLabelRemove(req *Request) Response {
	var labelArgs LabelRemoveArgs
	return s.handleSimpleStoreOp(req, &labelArgs, "label remove", func(ctx context.Context, store storage.Storage, actor string) error {
		return store.RemoveLabel(ctx, labelArgs.ID, labelArgs.Label, actor)
	}, func() string { return labelArgs.ID }, nil)
}

func (s *Server) handleCommentList(req *Request) Response {
	var commentArgs CommentListArgs
	if err := json.Unmarshal(req.Args, &commentArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid comment list args: %v", err),
		}
	}

	store := s.storage

	ctx, cancel := s.reqCtx(req)
	defer cancel()
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

	store := s.storage

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	comment, err := store.AddIssueComment(ctx, commentArgs.ID, commentArgs.Author, commentArgs.Text)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to add comment: %v", err),
		}
	}

	// Emit mutation event for event-driven daemon
	title, assignee := s.lookupIssueMeta(ctx, commentArgs.ID)
	s.emitMutation(MutationComment, commentArgs.ID, title, assignee)

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
