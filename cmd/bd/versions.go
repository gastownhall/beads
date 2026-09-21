package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
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
	errNoSuchBead          = errors.New("no such bead")
)

// versionsOutcome is what runVersions resolved, kept apart from the rendering
// so the four ways of having nothing to show stay four different answers.
type versionsOutcome struct {
	Versions []storage.IssueVersion
	// Recording reports whether the store is recording versions RIGHT NOW.
	// It is independent of whether Versions is empty: a store that recorded
	// for a month and was then switched off still has versions, and saying
	// otherwise would be a claim about the store made from a fact about this
	// invocation's config.
	Recording bool
}

var versionsCmd = &cobra.Command{
	Use:     "versions <id>",
	GroupID: "views",
	Short:   "List the recorded versions of a bead",
	Long: `List the versions recorded for a bead by versioned history.

Recording is off by default. Turn it on with:

  bd config set versioned-history.enabled true

That writes the setting into the store, so every client of that store agrees
about whether a write is recorded. For a single run without changing the
store:

  BD_VERSIONED_HISTORY_ENABLED=1 bd versions <id>

Either source turning it on is enough; neither can switch the other off.

Versions are recorded from the moment recording is turned on. It does not
backfill, so a bead edited yesterday shows nothing until it is edited again.

Turning recording off does not hide what was already recorded — this command
still lists it, and says that recording is currently off.

A caution while the feature is young: keep recording ON for every writer of a
shared store, or leave it off for all of them. A write from a client with
recording off advances nothing, and the next recorded write mints a version
that absorbs that unrecorded change under its own actor — the revisions stay
contiguous and the listing looks gapless when it is not.

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
		resolved := false
		if full, err := utils.ResolvePartialID(rootCtx, store, issueID); err == nil {
			issueID, resolved = full, true
		} else if errors.Is(err, utils.ErrAmbiguousID) {
			return HandleErrorRespectJSON("%v", err)
		}

		recording := versionedHistoryEnabled(rootCtx, store)

		outcome, err := runVersions(rootCtx, store, issueID, recording, resolved)
		switch {
		case errors.Is(err, errNoSuchBead):
			return HandleErrorRespectJSON(
				"no bead %s in this store, and no versions recorded under that id.", issueID)
		case errors.Is(err, errVersionedHistoryOff):
			return HandleErrorRespectJSON(
				"versioned history is not being recorded on this store, and nothing was recorded earlier.\n"+
					"Turn it on with:  bd config set %s true\n"+
					"or for one run:   BD_VERSIONED_HISTORY_ENABLED=1 bd versions %s\n"+
					"Recording starts from that point on; it does not backfill.",
				versionedHistorySettingKey, issueID)
		case errors.Is(err, errVersionsUnsupported):
			return HandleErrorRespectJSON(
				"this storage backend cannot serve version history (proxied, no-db and non-Dolt backends cannot).\n" +
					"Run this against a Dolt-backed workspace.")
		case err != nil:
			return HandleErrorRespectJSON("failed to list versions: %v", err)
		}

		if jsonOutput {
			return outputJSON(outcome.Versions)
		}
		printVersions(issueID, outcome)
		return nil
	},
}

// runVersions is the testable core. It separates four situations that all
// look like "nothing here" and each need a different response.
//
// It deliberately READS EVEN WHEN RECORDING IS OFF. An earlier version
// refused up front, which meant a store that recorded for a month and was
// then switched off reported "no versions are recorded" -- a claim about the
// store, derived from a fact about this invocation's config. storage
// .VersionLister's own doc already says the two are independent: holding the
// capability says the backend can READ versions and says nothing about
// whether recording is on. (bee-ghosttrack, #6661 review, finding 3.)
//
// resolved reports whether issueID named a bead the store knows. An
// unresolved id with no versions is "no such bead", not "none yet" -- falling
// through on an unresolved id is right, because a deleted bead can still have
// versions, but only until the answer turns out to be empty. (Same review,
// finding 4.)
func runVersions(ctx context.Context, backend any, issueID string, recording, resolved bool) (versionsOutcome, error) {
	lister, ok := versionListerFor(backend)
	if !ok {
		return versionsOutcome{}, errVersionsUnsupported
	}

	versions, err := lister.ListVersions(ctx, issueID)
	if err != nil {
		return versionsOutcome{}, err
	}
	if len(versions) > 0 {
		// Something was recorded. Whether recording is on right now changes
		// the footnote, never whether these rows are shown.
		return versionsOutcome{Versions: versions, Recording: recording}, nil
	}

	// Nothing to show. Which of the three reasons it is decides the message.
	if !resolved {
		return versionsOutcome{}, errNoSuchBead
	}
	if !recording {
		return versionsOutcome{}, errVersionedHistoryOff
	}
	return versionsOutcome{Recording: true}, nil
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

func printVersions(issueID string, outcome versionsOutcome) {
	versions := outcome.Versions
	if len(versions) == 0 {
		// Recording is on (runVersions refuses otherwise) and the bead
		// resolved, so this is the honest empty: nothing has changed yet.
		fmt.Printf("\nNo versions recorded for %s yet.\n", issueID)
		fmt.Println(ui.RenderMuted("A version is recorded on each accepted change from the time recording was turned on; it does not backfill."))
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
	if !outcome.Recording {
		// The rows are real; recording is simply off now. Saying nothing here
		// would let a stale listing read as current.
		fmt.Println(ui.RenderWarn("Recording is currently OFF — this listing ends where it was switched off."))
		fmt.Println(ui.RenderMuted(fmt.Sprintf("Turn it back on with:  bd config set %s true", versionedHistorySettingKey)))
		fmt.Println()
	}
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
