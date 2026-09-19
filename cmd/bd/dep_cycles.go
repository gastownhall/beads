package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// `bd dep cycles`, and the post-add warning every route that wires an edge
// prints, on ONE role and ONE renderer.

// openCycleDetector hands back the cycle role for whichever route this
// invocation is on.
func openCycleDetector() (issueops.CycleDetector, error) {
	if usesProxiedServer() {
		return proxiedCycleDetector()
	}
	return store.CycleDetector()
}

// proxiedCycleDetector hands back the guarded cycle-report surface for the
// proxied-server provider, through the provider's own capability accessor.
func proxiedCycleDetector() (issueops.CycleDetector, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.CycleDetectorSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the cycle-report surface", uowProvider)
	}
	return src.CycleDetector()
}

// runDepCycles is the whole of `bd dep cycles` on both routes.
func runDepCycles() error {
	detector, err := openCycleDetector()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	report, err := detector.DetectCycles(rootCtx, issueops.DetectCyclesRequest{})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		// The role's slice is empty rather than nil for an acyclic workspace, so
		// this is `[]` and never `null` without a guard here.
		return outputJSON(report.Cycles)
	}

	if len(report.Cycles) == 0 {
		fmt.Printf("\n%s No dependency cycles detected\n\n", ui.RenderPass("✓"))
		return nil
	}

	fmt.Printf("\n%s Found %d dependency cycles:\n\n", ui.RenderFail("⚠"), len(report.Cycles))
	for i, cycle := range report.Cycles {
		if missing := unknownCycleMembers(cycle); missing > 0 {
			fmt.Printf("%d. Cycle involving (%d of %d members have no record in this database):\n",
				i+1, missing, len(cycle.Members))
		} else {
			fmt.Printf("%d. Cycle involving:\n", i+1)
		}
		for _, member := range cycle.Members {
			fmt.Printf("   - %s: %s\n", member.ID, cycleMemberTitle(member))
		}
		fmt.Println()
	}
	return nil
}

// cycleMemberTitle renders a member's description, naming the absence rather
// than printing a blank line after the id.
func cycleMemberTitle(member issueops.CycleMember) string {
	if member.Issue == nil {
		return ui.RenderMuted("(no record in this database)")
	}
	return member.Issue.Title
}

func unknownCycleMembers(cycle issueops.Cycle) int {
	missing := 0
	for _, member := range cycle.Members {
		if member.Issue == nil {
			missing++
		}
	}
	return missing
}

// warnIfCyclesExist sweeps for cycles after an edge lands and prints a warning
// on stderr if it finds any.
//
// It takes a STORE rather than using the global one because the direct dep and
// link routes resolve their endpoints with routing: the edge may have landed in
// another project's database, and sweeping this one would answer about a graph
// the write did not touch.
func warnIfCyclesExist(s storage.DoltStorage) {
	if s == nil {
		return // Skip the sweep if the store is not available.
	}
	detector, err := s.CycleDetector()
	if err != nil {
		printCycleDetectionError(err)
		return
	}
	report, err := detector.DetectCycles(rootCtx, issueops.DetectCyclesRequest{})
	if err != nil {
		printCycleDetectionError(err)
		return
	}
	printCycleWarnings(report.Cycles)
}

func printCycleDetectionError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to check for cycles: %v\n", err)
	}
}

// printCycleWarnings renders the post-add warning: one arrow path per cycle,
// closed by repeating the first member.
//
// The path is spelled from the role's MEMBER IDS, which are complete whether or
// not each member has a row behind it: a cycle whose middle node has no record
// used to print as a shorter path, or not at all.
func printCycleWarnings(cycles []issueops.Cycle) {
	if len(cycles) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s Warning: Dependency cycle detected!\n", ui.RenderWarn("⚠"))
	fmt.Fprintf(os.Stderr, "This can hide issues from the ready work list and cause confusion.\n\n")
	fmt.Fprintf(os.Stderr, "Cycle path:\n")
	for _, cycle := range cycles {
		for j, member := range cycle.Members {
			if j == 0 {
				fmt.Fprintf(os.Stderr, "  %s", member.ID)
			} else {
				fmt.Fprintf(os.Stderr, " → %s", member.ID)
			}
		}
		if len(cycle.Members) > 0 {
			fmt.Fprintf(os.Stderr, " → %s", cycle.Members[0].ID)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
	fmt.Fprintf(os.Stderr, "\nRun 'bd dep cycles' for detailed analysis.\n\n")
}
