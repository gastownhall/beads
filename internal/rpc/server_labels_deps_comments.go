package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func (s *Server) handleDepAdd(req *Request) Response {
	var depArgs DepAddArgs
	if err := json.Unmarshal(req.Args, &depArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid dep add args: %v", err),
		}
	}

	store := s.storage

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

	store := s.storage

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

	store := s.storage

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

	store := s.storage

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
