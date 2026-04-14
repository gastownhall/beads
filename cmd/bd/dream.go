package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/dream"
)

// dream.* config keys live in the Dolt config table (NOT under kv.*).
// They track threshold state and last-run metadata for `bd dream`.
const (
	dreamKeyLastRunAt          = "dream.last-run-at"
	dreamKeyLastRunStatus      = "dream.last-run-status"
	dreamKeyLastRunSummary     = "dream.last-run-summary"
	dreamKeyMemoryCountAtLast  = "dream.memory-count-at-last-run"
	dreamKeyMinIntervalHours   = "dream.min-interval-hours"
	dreamKeyMinChurn           = "dream.min-churn"
	dreamDefaultMinIntervalHrs = 24
	dreamDefaultMinChurn       = 5
)

// Flags for `bd dream run`.
var (
	dreamFlagDryRun        bool
	dreamFlagForce         bool
	dreamFlagCheck         bool
	dreamFlagModelOverride string
)

// dreamCmd is the parent command. With no subcommand it runs `dream run`.
var dreamCmd = &cobra.Command{
	Use:     "dream [run|status]",
	Short:   "Consolidate persistent memories with an LLM",
	GroupID: "setup",
	Long: `Consolidate the memory store (bd remember / bd memories) by asking an LLM
to identify duplicates, stale references, and low-signal entries.

Inspired by Claude Code's AutoDream feature: a periodic background pass that
keeps long-lived memory clean without the user thinking about it. Beads has
no built-in scheduler, so triggering is left to cron / launchd / your editor's
session-stop hook.

API key resolution (same as bd's other AI features):
  ANTHROPIC_API_KEY env var > ai.api_key config

Default model is config.ai.model (override per-run with --model).

Examples:
  bd dream                       # alias of "bd dream run"
  bd dream run --dry-run         # show plan without applying
  bd dream run --force           # ignore threshold (interval + churn)
  bd dream run --check           # exit 0 if eligible, 1 if not (for schedulers)
  bd dream status                # show last run + threshold state`,
	Run: func(cmd *cobra.Command, args []string) {
		dreamRunCmd.Run(cmd, args)
	},
}

// dreamRunCmd performs (or simulates) a consolidation pass.
var dreamRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a consolidation pass (or check eligibility)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		// --check is read-only and returns an exit code instead of applying.
		if dreamFlagCheck {
			runDreamCheck()
			return
		}
		// dry-run still talks to the LLM but doesn't write — read-only as far
		// as bd's storage is concerned.
		if !dreamFlagDryRun {
			CheckReadonly("dream run")
		}
		if err := ensureDirectMode("dream requires direct database access"); err != nil {
			FatalError("%v", err)
		}
		runDreamApply()
	},
}

// dreamStatusCmd shows the last run + whether the next is eligible.
var dreamStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show last dream run and threshold state",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("dream status requires direct database access"); err != nil {
			FatalError("%v", err)
		}
		runDreamStatus()
	},
}

func init() {
	dreamRunCmd.Flags().BoolVar(&dreamFlagDryRun, "dry-run", false, "Print the plan without applying it")
	dreamRunCmd.Flags().BoolVar(&dreamFlagForce, "force", false, "Ignore the time/churn threshold and run anyway")
	dreamRunCmd.Flags().BoolVar(&dreamFlagCheck, "check", false, "Only evaluate the threshold; exit 0 if eligible, 1 if not")
	dreamRunCmd.Flags().StringVar(&dreamFlagModelOverride, "model", "", "Override the AI model for this run (default: config ai.model)")
	dreamCmd.AddCommand(dreamRunCmd)
	dreamCmd.AddCommand(dreamStatusCmd)
	rootCmd.AddCommand(dreamCmd)
}

// loadMemories reads all kv.memory.* entries from the store and returns them
// as dream.Memory slices sorted by key (deterministic input → deterministic plan).
func loadMemories() ([]dream.Memory, error) {
	ctx := rootCtx
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return nil, err
	}
	fullPrefix := kvPrefix + memoryPrefix
	keys := make([]string, 0)
	for k := range allConfig {
		if strings.HasPrefix(k, fullPrefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]dream.Memory, 0, len(keys))
	for _, k := range keys {
		out = append(out, dream.Memory{
			Key:     strings.TrimPrefix(k, fullPrefix),
			Content: allConfig[k],
		})
	}
	return out, nil
}

// thresholdState is the per-repo dream metadata used for eligibility decisions.
type thresholdState struct {
	LastRunAt            time.Time // zero if never run
	LastRunStatus        string
	LastRunSummary       string
	MemoryCountAtLastRun int
	MinIntervalHours     int
	MinChurn             int
	NeverRun             bool
}

func loadThresholdState() (*thresholdState, error) {
	ctx := rootCtx
	all, err := store.GetAllConfig(ctx)
	if err != nil {
		return nil, err
	}
	s := &thresholdState{
		MinIntervalHours: dreamDefaultMinIntervalHrs,
		MinChurn:         dreamDefaultMinChurn,
	}
	if v := all[dreamKeyLastRunAt]; v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			s.LastRunAt = t
		}
	}
	s.NeverRun = s.LastRunAt.IsZero()
	s.LastRunStatus = all[dreamKeyLastRunStatus]
	s.LastRunSummary = all[dreamKeyLastRunSummary]
	if v := all[dreamKeyMemoryCountAtLast]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.MemoryCountAtLastRun = n
		}
	}
	if v := all[dreamKeyMinIntervalHours]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.MinIntervalHours = n
		}
	}
	if v := all[dreamKeyMinChurn]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.MinChurn = n
		}
	}
	return s, nil
}

// evaluateEligibility reports whether a run should proceed and (if not) why.
//
// The "too few memories" floor applies even with --force: there is nothing
// useful to consolidate. Force only bypasses the time/churn thresholds.
func evaluateEligibility(s *thresholdState, currentCount int, force bool) (eligible bool, reasons []string) {
	if currentCount < 2 {
		return false, []string{"too few memories to consolidate"}
	}
	if force {
		return true, nil
	}
	if s.NeverRun {
		return true, nil
	}
	elapsedHours := time.Since(s.LastRunAt).Hours()
	churn := absInt(currentCount - s.MemoryCountAtLastRun)
	if elapsedHours < float64(s.MinIntervalHours) {
		reasons = append(reasons, fmt.Sprintf("interval not elapsed (%.1fh < %dh)", elapsedHours, s.MinIntervalHours))
	}
	if churn < s.MinChurn {
		reasons = append(reasons, fmt.Sprintf("churn below threshold (%d < %d)", churn, s.MinChurn))
	}
	return len(reasons) == 0, reasons
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// runDreamCheck implements `bd dream run --check`.
// Exit 0 = eligible. Exit 1 = not eligible (reason on stderr / json).
func runDreamCheck() {
	memories, err := loadMemories()
	if err != nil {
		FatalErrorRespectJSON("listing memories: %v", err)
	}
	state, err := loadThresholdState()
	if err != nil {
		FatalErrorRespectJSON("loading dream state: %v", err)
	}
	eligible, reasons := evaluateEligibility(state, len(memories), false)
	if jsonOutput {
		outputJSON(map[string]any{
			"eligible":     eligible,
			"reasons":      reasons,
			"memory_count": len(memories),
		})
	} else if eligible {
		fmt.Println("eligible")
	} else {
		fmt.Fprintln(os.Stderr, "not eligible: "+strings.Join(reasons, "; "))
	}
	if !eligible {
		os.Exit(1)
	}
}

// runDreamApply implements the main `bd dream run` (and --dry-run) flow.
func runDreamApply() {
	memories, err := loadMemories()
	if err != nil {
		FatalErrorRespectJSON("listing memories: %v", err)
	}
	state, err := loadThresholdState()
	if err != nil {
		FatalErrorRespectJSON("loading dream state: %v", err)
	}

	eligible, reasons := evaluateEligibility(state, len(memories), dreamFlagForce)
	if !eligible {
		summary := "skipped: " + strings.Join(reasons, "; ")
		if jsonOutput {
			outputJSON(map[string]any{
				"applied": false,
				"skipped": true,
				"reasons": reasons,
			})
		} else {
			fmt.Println(summary)
		}
		return
	}

	consolidator, err := dream.New("", dreamFlagModelOverride)
	if err != nil {
		if errors.Is(err, dream.ErrAPIKeyRequired) {
			FatalErrorWithHintRespectJSON(
				"missing Anthropic API key",
				"set ANTHROPIC_API_KEY or run: bd config set ai.api_key sk-...",
			)
		}
		FatalErrorRespectJSON("%v", err)
	}

	plan, err := consolidator.Consolidate(rootCtx, memories)
	if err != nil {
		// Record failure in state so `dream status` shows it, then exit.
		_ = recordRun(state, len(memories), "error: "+err.Error(), "")
		FatalErrorRespectJSON("consolidation failed: %v", err)
	}

	if dreamFlagDryRun {
		emitPlan(plan, len(memories), true)
		return
	}

	applied, applyErr := applyPlan(plan)
	emitPlan(plan, len(memories), false)
	if applyErr != nil {
		WarnError("partial apply: %v", applyErr)
	}
	if err := recordRun(state, len(memories)-deletedCount(plan), "ok", plan.Summary); err != nil {
		WarnError("recording dream state: %v", err)
	}
	_ = applied // future: report counts in JSON
}

// applyPlan executes the operations in order. Errors are accumulated rather
// than fatal so a single bad op doesn't strand the rest.
func applyPlan(plan *dream.Plan) (applied int, retErr error) {
	ctx := rootCtx
	var firstErr error
	noteErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, op := range plan.Operations {
		switch op.Action {
		case dream.ActionForget:
			storageKey := kvPrefix + memoryPrefix + op.Key
			if err := store.DeleteConfig(ctx, storageKey); err != nil {
				noteErr(fmt.Errorf("forget %s: %w", op.Key, err))
				continue
			}
			applied++
		case dream.ActionMerge:
			if op.NewKey == "" || op.NewContent == "" || len(op.AbsorbedKeys) < 2 {
				noteErr(fmt.Errorf("merge: invalid operation (need new_key, new_content, >= 2 absorbed_keys)"))
				continue
			}
			newStorageKey := kvPrefix + memoryPrefix + op.NewKey
			if err := store.SetConfig(ctx, newStorageKey, op.NewContent); err != nil {
				noteErr(fmt.Errorf("merge write %s: %w", op.NewKey, err))
				continue
			}
			for _, k := range op.AbsorbedKeys {
				if k == op.NewKey {
					continue // don't delete the merged target
				}
				if err := store.DeleteConfig(ctx, kvPrefix+memoryPrefix+k); err != nil {
					noteErr(fmt.Errorf("merge delete %s: %w", k, err))
				}
			}
			applied++
		case dream.ActionUpdate:
			if op.Key == "" || op.NewContent == "" {
				noteErr(fmt.Errorf("update: invalid operation (need key and new_content)"))
				continue
			}
			storageKey := kvPrefix + memoryPrefix + op.Key
			if err := store.SetConfig(ctx, storageKey, op.NewContent); err != nil {
				noteErr(fmt.Errorf("update %s: %w", op.Key, err))
				continue
			}
			applied++
		default:
			noteErr(fmt.Errorf("unknown action: %s", op.Action))
		}
	}
	if _, err := store.CommitPending(ctx, getActor()); err != nil {
		noteErr(fmt.Errorf("commit: %w", err))
	}
	return applied, firstErr
}

// deletedCount returns how many memories the plan removes (used for accounting).
func deletedCount(plan *dream.Plan) int {
	n := 0
	for _, op := range plan.Operations {
		switch op.Action {
		case dream.ActionForget:
			n++
		case dream.ActionMerge:
			// Net: -len(absorbed) + 1 (new entry). If new_key == one of absorbed, it's -len+1+0 = -len+1.
			n += len(op.AbsorbedKeys) - 1
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

// recordRun persists the dream state after a run (success or skipped-with-attempt).
// `nextMemoryCount` is the post-apply count snapshot for churn detection.
func recordRun(_ *thresholdState, nextMemoryCount int, status, summary string) error {
	ctx := rootCtx
	now := time.Now().UTC().Format(time.RFC3339)
	_ = store.SetConfig(ctx, dreamKeyLastRunAt, now)
	_ = store.SetConfig(ctx, dreamKeyLastRunStatus, status)
	_ = store.SetConfig(ctx, dreamKeyLastRunSummary, summary)
	_ = store.SetConfig(ctx, dreamKeyMemoryCountAtLast, strconv.Itoa(nextMemoryCount))
	if _, err := store.CommitPending(ctx, getActor()); err != nil {
		return err
	}
	return nil
}

// emitPlan prints (or JSON-emits) the plan for human or machine consumption.
func emitPlan(plan *dream.Plan, memoryCount int, dryRun bool) {
	if jsonOutput {
		outputJSON(map[string]any{
			"applied":      !dryRun,
			"dry_run":      dryRun,
			"summary":      plan.Summary,
			"operations":   plan.Operations,
			"memory_count": memoryCount,
		})
		return
	}
	mode := "applied"
	if dryRun {
		mode = "dry-run"
	}
	fmt.Printf("dream %s — %s (memories=%d, ops=%d)\n", mode, plan.Summary, memoryCount, len(plan.Operations))
	for i, op := range plan.Operations {
		fmt.Printf("  %d. %s", i+1, op.Action)
		switch op.Action {
		case dream.ActionForget:
			fmt.Printf(" %s", op.Key)
		case dream.ActionMerge:
			fmt.Printf(" %s ← %s", op.NewKey, strings.Join(op.AbsorbedKeys, ", "))
		case dream.ActionUpdate:
			fmt.Printf(" %s", op.Key)
		}
		if op.Reason != "" {
			fmt.Printf("  (%s)", op.Reason)
		}
		fmt.Println()
	}
}

// runDreamStatus implements `bd dream status`.
func runDreamStatus() {
	memories, err := loadMemories()
	if err != nil {
		FatalErrorRespectJSON("listing memories: %v", err)
	}
	state, err := loadThresholdState()
	if err != nil {
		FatalErrorRespectJSON("loading dream state: %v", err)
	}
	eligible, reasons := evaluateEligibility(state, len(memories), false)

	if jsonOutput {
		out := map[string]any{
			"eligible":           eligible,
			"reasons":            reasons,
			"memory_count":       len(memories),
			"min_interval_hours": state.MinIntervalHours,
			"min_churn":          state.MinChurn,
			"never_run":          state.NeverRun,
		}
		if !state.NeverRun {
			out["last_run_at"] = state.LastRunAt.Format(time.RFC3339)
			out["last_run_status"] = state.LastRunStatus
			out["last_run_summary"] = state.LastRunSummary
			out["memory_count_at_last_run"] = state.MemoryCountAtLastRun
		}
		outputJSON(out)
		return
	}

	if state.NeverRun {
		fmt.Println("dream has never run on this repo")
	} else {
		fmt.Printf("last run: %s (%s)\n", state.LastRunAt.Format(time.RFC3339), state.LastRunStatus)
		if state.LastRunSummary != "" {
			fmt.Printf("  summary: %s\n", state.LastRunSummary)
		}
	}
	fmt.Printf("memories: %d (was %d at last run)\n", len(memories), state.MemoryCountAtLastRun)
	fmt.Printf("threshold: %dh interval, %d churn\n", state.MinIntervalHours, state.MinChurn)
	if eligible {
		fmt.Println("status: eligible")
	} else {
		fmt.Println("status: not eligible — " + strings.Join(reasons, "; "))
	}
}
