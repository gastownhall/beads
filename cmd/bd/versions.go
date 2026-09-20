package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// errVersionedHistoryOff and errVersionsUnsupported are distinct on purpose.
//
// #5898's third goal is that a history answer has three shapes, not two:
// here it is, there is no such thing, and something prevented a complete
// answer. An empty list is a WRONG answer for both of these cases -- it reads
// as "this bead has no versions", which is a claim about the bead rather than
// about the store. Same principle get_issue.go:38-41 applies to a missing
// leases table: a wrong answer, not an empty one.
var (
	errVersionedHistoryOff = errors.New("versioned history is not enabled on this store")
	errVersionsUnsupported = errors.New("this storage backend cannot serve version history")
)

var versionsCmd = &cobra.Command{
	Use:     "versions <id>",
	GroupID: "views",
	Short:   "List the recorded versions of a bead",
	Long: `List the versions recorded for a bead by versioned history.

Versioned history is off by default. Turn it on with:

  bd config set versioned-history.enabled true
  # or, per invocation:
  BD_VERSIONED_HISTORY_ENABLED=1 bd versions <id>

Versions are recorded from the moment the feature is switched on. Enabling it
does not backfill anything that happened before, so a bead edited yesterday
shows no versions until it is edited again.

Examples:
  bd versions bd-123
  bd versions bd-123 --json`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("versions")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		issueID := args[0]
		if resolved, err := utils.ResolvePartialID(rootCtx, store, issueID); err == nil {
			issueID = resolved
		} else if errors.Is(err, utils.ErrAmbiguousID) {
			return HandleErrorRespectJSON("%v", err)
		}

		versions, err := runVersions(rootCtx, store, issueID, config.GetBool("versioned-history.enabled"))
		switch {
		case errors.Is(err, errVersionedHistoryOff):
			return HandleErrorRespectJSON(
				"versioned history is not enabled on this store, so no versions are recorded.\n"+
					"Enable it with:  bd config set versioned-history.enabled true\n"+
					"or per command:  BD_VERSIONED_HISTORY_ENABLED=1 bd versions %s\n"+
					"Enabling records new versions from that point on; it does not backfill.", issueID)
		case errors.Is(err, errVersionsUnsupported):
			return HandleErrorRespectJSON(
				"this storage backend cannot serve version history (proxied, no-db and non-Dolt backends cannot).\n" +
					"Run this against a Dolt-backed workspace.")
		case err != nil:
			return HandleErrorRespectJSON("failed to list versions: %v", err)
		}

		if jsonOutput {
			return outputJSON(versions)
		}
		printVersions(issueID, versions)
		return nil
	},
}

// runVersions is the testable core: it refuses before it reads, so a caller
// can never confuse "off" or "unsupported" with "none".
func runVersions(ctx context.Context, backend any, issueID string, enabled bool) ([]storage.IssueVersion, error) {
	if !enabled {
		return nil, errVersionedHistoryOff
	}
	lister, ok := versionListerFor(backend)
	if !ok {
		return nil, errVersionsUnsupported
	}
	return lister.ListVersions(ctx, issueID)
}

// versionListerFor finds the VersionLister behind whatever cmd/bd is holding.
//
// The store in cmd/bd is the DECORATED chain (telemetry, externaldeps,
// hooks), and VersionLister is an optional capability rather than a method on
// DoltStorage, so none of those decorators forwards it -- asserting against
// the wrapper reports "unsupported" for a store that can serve versions
// perfectly well. storage.UnwrapStore's own doc names this case: peel before
// asserting optional interfaces.
//
// The direct assertion is tried FIRST so a test double, or any future
// decorator that genuinely implements the capability itself, wins over the
// concrete store underneath -- the same precedence cmd/bd/dolt.go uses.
func versionListerFor(backend any) (storage.VersionLister, bool) {
	if lister, ok := backend.(storage.VersionLister); ok {
		return lister, true
	}
	if ds, ok := backend.(storage.DoltStorage); ok {
		if lister, ok := storage.UnwrapStore(ds).(storage.VersionLister); ok {
			return lister, true
		}
	}
	return nil, false
}

func printVersions(issueID string, versions []storage.IssueVersion) {
	if len(versions) == 0 {
		fmt.Printf("\nNo versions recorded for %s yet.\n", issueID)
		fmt.Println(ui.RenderMuted("Versioned history records a version on each accepted change from the time it was enabled; it does not backfill."))
		return
	}

	fmt.Printf("\nVersions of %s (%d)\n\n", issueID, len(versions))
	fmt.Printf("  %-5s  %-19s  %-22s  %s\n",
		ui.RenderMuted("REV"), ui.RenderMuted("WHEN"), ui.RenderMuted("WHO"), ui.RenderMuted("ATTRIB"))

	for _, v := range versions {
		if v.Removed() {
			// A removed version is a restriction, not a blank row: name which
			// restriction applied and when, so it is never mistaken for an
			// absence. #5898's four values are distinct answers.
			fmt.Printf("  %-5d  %s\n", v.Revision,
				ui.RenderMuted(fmt.Sprintf("✗ gone · %s%s · %s",
					restrictionLabel(v.RemovedRestriction),
					reasonSuffix(v.RemovedReason),
					v.RemovedAt.Format("2006-01-02 15:04"))))
			continue
		}
		who := v.ChangeActor
		if who == "" {
			who = ui.RenderMuted("—")
		}
		fmt.Printf("  %-5d  %-19s  %-22s  %s\n",
			v.Revision,
			v.ChangeAt.Format("2006-01-02 15:04"),
			who,
			attributionLabel(v.AttributionStatus))
		if v.ChangeMessage != "" {
			fmt.Printf("         %s\n", ui.RenderMuted(v.ChangeMessage))
		}
	}

	fmt.Println()
	fmt.Println(ui.RenderMuted("REV is local to this store and is not a citable address: two clones can"))
	fmt.Println(ui.RenderMuted("both hold revision 8 of the same bead. Portable version addresses arrive"))
	fmt.Println(ui.RenderMuted("with the version_id change."))
}

// restrictionLabel renders #5898's removal vocabulary. "unknown" is NOT
// "gone": it means this store has no lineage knowledge, which the conformance
// suite pins separately (a never-synced store answers Unknown, not Gone).
func restrictionLabel(r string) string {
	switch r {
	case "gone_retention":
		return "outside the retention window"
	case "gone_erasure":
		return "erased"
	case "gone_reorganization":
		return "voided by an epoch change"
	case "unknown", "":
		return "no lineage knowledge in this store"
	default:
		return r
	}
}

func attributionLabel(s string) string {
	switch s {
	case "claimed":
		return "claimed"
	case "unknown":
		return ui.RenderMuted("unknown")
	case "":
		return ui.RenderMuted("—")
	default:
		return s
	}
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + reason + ")"
}

func init() {
	rootCmd.AddCommand(versionsCmd)
}
