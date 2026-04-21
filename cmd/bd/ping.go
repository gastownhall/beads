package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var pingCmd = &cobra.Command{
	Use:     "ping",
	GroupID: "maint",
	Short:   "Check database connectivity",
	Long: `Lightweight health check that confirms bd can reach its database.

Steps:
  1. Resolve the .beads workspace
  2. Open the store (embedded or server)
  3. Run a trivial query (issue count)
  4. Report timing

Exit 0 on success, exit 1 on failure.

Examples:
  bd ping              # Quick connectivity check
  bd ping --json       # Structured output for automation`,
	Run: func(cmd *cobra.Command, args []string) {
		start := time.Now()

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			pingFail(start, "no .beads directory found")
			return
		}
		resolveMs := time.Since(start).Milliseconds()

		st := getStore()
		if st == nil {
			pingFail(start, "store not initialized")
			return
		}
		if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
			pingFail(start, "store is closed")
			return
		}
		storeMs := time.Since(start).Milliseconds()

		filter := types.IssueFilter{Limit: 1}
		_, err := st.SearchIssues(rootCtx, "", filter)
		if err != nil {
			pingFail(start, fmt.Sprintf("query failed: %v", err))
			return
		}
		totalMs := time.Since(start).Milliseconds()
		queryMs := totalMs - storeMs

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":     "ok",
				"resolve_ms": resolveMs,
				"store_ms":   storeMs - resolveMs,
				"query_ms":   queryMs,
				"total_ms":   totalMs,
			})
			return
		}

		fmt.Fprintf(os.Stdout, "%s bd ping: ok (%dms)\n", ui.RenderPass("✓"), totalMs)
	},
}

func pingFail(start time.Time, reason string) {
	totalMs := time.Since(start).Milliseconds()
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":   "error",
			"error":    reason,
			"total_ms": totalMs,
		})
		os.Exit(1)
		return
	}
	fmt.Fprintf(os.Stderr, "%s bd ping: %s (%dms)\n", ui.RenderFail("✗"), reason, totalMs)
	os.Exit(1)
}

func init() {
	rootCmd.AddCommand(pingCmd)
}
