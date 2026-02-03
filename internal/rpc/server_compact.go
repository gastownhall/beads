package rpc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/compact"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func (s *Server) handleCompact(req *Request) Response {
	var args CompactArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid compact args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	// Check if storage supports compaction (not all backends do, e.g., Dolt)
	compactStore, ok := store.(storage.CompactableStorage)
	if !ok {
		return Response{
			Success: false,
			Error:   "compact requires a storage backend that supports compaction",
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

	compactor, err := compact.New(compactStore, args.APIKey, config)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create compactor: %v", err),
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	startTime := time.Now()

	if args.IssueID != "" {
		if !args.Force {
			eligible, reason, err := compactStore.CheckEligibility(ctx, args.IssueID, args.Tier)
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

		issue, err := compactStore.GetIssue(ctx, args.IssueID)
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

		issueAfter, _ := compactStore.GetIssue(ctx, args.IssueID)
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
		var candidates []*types.CompactionCandidate

		switch args.Tier {
		case 1:
			tier1, err := compactStore.GetTier1Candidates(ctx)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to get Tier 1 candidates: %v", err),
				}
			}
			candidates = tier1
		case 2:
			tier2, err := compactStore.GetTier2Candidates(ctx)
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

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	// Check if storage supports compaction (not all backends do, e.g., Dolt)
	compactStore, ok := store.(storage.CompactableStorage)
	if !ok {
		return Response{
			Success: false,
			Error:   "compact stats requires a storage backend that supports compaction",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	tier1, err := compactStore.GetTier1Candidates(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get Tier 1 candidates: %v", err),
		}
	}

	tier2, err := compactStore.GetTier2Candidates(ctx)
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
