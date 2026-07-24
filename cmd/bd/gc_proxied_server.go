package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

func runGCProxiedServer(ctx context.Context) error {
	evt := metrics.NewCommandEvent("gc")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if !gcDryRun {
		CheckReadonly("gc")
	}
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	start := time.Now()

	if gcOlderThan < 0 {
		return HandleErrorRespectJSON("--older-than must be non-negative")
	}

	type phaseResult struct {
		name    string
		skipped bool
		detail  string
	}
	var results []phaseResult
	var gcFailed bool

	if gcSkipDecay {
		results = append(results, phaseResult{name: "Decay", skipped: true})
	} else {
		if !jsonOutput {
			fmt.Println("Phase 1/3: Decay (delete old closed issues)")
		}

		cutoffDays := gcOlderThan
		cutoffTime := time.Now().UTC().AddDate(0, 0, -cutoffDays)
		statusClosed := types.StatusClosed
		filter := types.IssueFilter{
			Status:       &statusClosed,
			ClosedBefore: &cutoffTime,
		}

		closedIssues, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.Issue, error) {
			page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
			if err != nil {
				return nil, err
			}
			return page.Items, nil
		})
		if err != nil {
			return HandleErrorRespectJSON("searching closed issues: %v", err)
		}

		var stats closedDeletionCandidateStats
		closedIssues, stats = filterClosedDeletionCandidates(closedIssues, &cutoffTime)
		warnClosedDeletionSafetySkips(stats)

		switch {
		case len(closedIssues) == 0:
			if !jsonOutput {
				fmt.Printf("  No closed issues older than %d days\n", cutoffDays)
			}
			results = append(results, phaseResult{name: "Decay", detail: "0 issues deleted"})
		case gcDryRun:
			if !jsonOutput {
				fmt.Printf("  Would delete %d closed issue(s)\n", len(closedIssues))
			}
			results = append(results, phaseResult{name: "Decay", detail: fmt.Sprintf("%d issues (dry-run)", len(closedIssues))})
		case !gcForce:
			return HandleErrorWithHintRespectJSON(
				fmt.Sprintf("would delete %d closed issue(s) older than %d days", len(closedIssues), cutoffDays),
				"Use --force to confirm or --dry-run to preview.")
		default:
			ids := make([]string, len(closedIssues))
			for i, issue := range closedIssues {
				ids[i] = issue.ID
			}
			deleteResult, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (domain.DeleteIssuesResult, string, error) {
				res, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{IDs: ids}, actor)
				if err != nil {
					return domain.DeleteIssuesResult{}, "", err
				}
				return res, fmt.Sprintf("bd: gc decay %d issue(s)", res.DeletedCount), nil
			})
			if err != nil {
				return HandleErrorRespectJSON("deleting closed issues: %v", err)
			}
			commandDidWrite.Store(true)
			if !jsonOutput {
				fmt.Printf("  Deleted %d issue(s)\n", deleteResult.DeletedCount)
			}
			results = append(results, phaseResult{name: "Decay", detail: fmt.Sprintf("%d issues deleted", deleteResult.DeletedCount)})
		}
		if !jsonOutput {
			fmt.Println()
		}
	}

	if !jsonOutput {
		fmt.Println("Phase 2/3: Compact (Dolt commit history info)")
	}

	commitCount, logErr := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (int, error) {
		res, err := uw.RawSQLUseCase().Query(ctx, "SELECT COUNT(*) FROM dolt_log")
		if err != nil {
			return 0, err
		}
		return scalarCount(res), nil
	})
	if logErr != nil {
		WarnError("could not read Dolt commit log: %v", logErr)
		commitCount = 0
	}

	if commitCount <= 1 {
		if !jsonOutput {
			fmt.Printf("  Only %d commit(s), nothing to compact\n\n", commitCount)
		}
		results = append(results, phaseResult{name: "Compact", detail: "nothing to compact"})
	} else if gcDryRun {
		if !jsonOutput {
			fmt.Printf("  %d commits in history (use bd flatten to squash)\n\n", commitCount)
		}
		results = append(results, phaseResult{name: "Compact", detail: fmt.Sprintf("%d commits (dry-run)", commitCount)})
	} else {
		if !jsonOutput {
			fmt.Printf("  %d commits in history\n", commitCount)
			fmt.Printf("  Tip: use 'bd flatten' to squash all history to one commit\n\n")
		}
		results = append(results, phaseResult{name: "Compact", detail: fmt.Sprintf("%d commits", commitCount)})
	}

	if gcSkipDolt {
		results = append(results, phaseResult{name: "Dolt GC", skipped: true})
	} else {
		if !jsonOutput {
			fmt.Println("Phase 3/3: Dolt GC (reclaim disk space)")
		}
		if gcDryRun {
			if !jsonOutput {
				fmt.Println("  Would run DOLT_GC() and DOLT_STATS_GC()")
			}
			results = append(results, phaseResult{name: "Dolt GC", detail: "dry-run"})
		} else {
			err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
				if err := versioncontrolops.DoltGC(ctx, conn); err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, "CALL DOLT_STATS_GC()"); err != nil {
					return fmt.Errorf("dolt_stats_gc: %w", err)
				}
				return nil
			})
			if err != nil {
				WarnError("dolt gc failed: %v", err)
				results = append(results, phaseResult{name: "Dolt GC", detail: "failed"})
				gcFailed = true
			} else {
				if !jsonOutput {
					fmt.Println("  Done (complete)")
				}
				results = append(results, phaseResult{name: "Dolt GC", detail: "complete"})
			}
		}
		if !jsonOutput {
			fmt.Println()
		}
	}

	elapsed := time.Since(start)

	if jsonOutput {
		summaryMap := make(map[string]interface{})
		summaryMap["dry_run"] = gcDryRun
		summaryMap["success"] = !gcFailed
		summaryMap["elapsed_ms"] = elapsed.Milliseconds()
		phases := make([]map[string]interface{}, 0, len(results))
		for _, r := range results {
			p := map[string]interface{}{
				"name":    r.name,
				"skipped": r.skipped,
			}
			if r.detail != "" {
				p["detail"] = r.detail
			}
			phases = append(phases, p)
		}
		summaryMap["phases"] = phases
		if err := outputJSON(summaryMap); err != nil {
			return err
		}
		if gcFailed {
			return SilentExit()
		}
		return nil
	}

	mode := "✓ GC complete"
	switch {
	case gcDryRun:
		mode = "DRY RUN complete"
	case gcFailed:
		mode = "⚠ GC completed with errors"
	}
	fmt.Printf("%s (%v)\n", mode, elapsed.Round(time.Millisecond))
	for _, r := range results {
		if r.skipped {
			fmt.Printf("  %s: skipped\n", r.name)
		} else {
			fmt.Printf("  %s: %s\n", r.name, r.detail)
		}
	}
	if gcFailed {
		return SilentExit()
	}
	return nil
}

func scalarCount(res *domain.RawSQLResult) int {
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	switch v := res.Rows[0][0].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case uint64:
		return int(v)
	case float64:
		return int(v)
	case []byte:
		n, _ := strconv.Atoi(string(v))
		return n
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}
