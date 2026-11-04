package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// purgeScope is what `bd purge` and `bd prune` still differ by once the
// operation itself is behind issueops.Sweeper: a tier, a reference policy, and
// the words each command prints.
type purgeScope struct {
	// cmdName is the user-visible command name (e.g. "purge", "prune").
	// Used in messages and the suggested `--force` hint.
	cmdName string
	// pastTense is the user-visible completed action (e.g. "purged", "pruned").
	pastTense string
	// countKey is the JSON key used for the actual deletion count.
	countKey string
	// dryRunCountKey is the JSON key used for the dry-run deletion count.
	dryRunCountKey string
	// subjectNoun describes what's being purged, in singular form
	// (e.g. "closed ephemeral bead", "closed bead"). "(s)" is appended by
	// the printer when multiple items are involved.
	subjectNoun string
	// tier is the plane this command sweeps. The two are DISJOINT: `prune`
	// never touches a wisp that `purge` would handle, and vice versa.
	tier issueops.SweepTier
	// protectReferenced asks the role to skip candidates cited by a bead that
	// is not done. `bd prune` asks unless --ignore-references; `bd purge`
	// never does, because a wisp's citations are as transient as the wisp.
	protectReferenced bool
	// protectLiveDependents asks the role to skip candidates a live bead
	// depends on through a parent-child, tracks or blocks edge. `bd purge`
	// always asks: deleting a closed wisp whose child, tracker or blocked bead
	// is still live cuts that live work loose from its root, and there is no
	// retention reason to want that — a deliberate removal is `bd delete`.
	protectLiveDependents bool
	// reportsReferences publishes the reference-skip keys under --json. It is
	// separate from protectReferenced because `bd prune --ignore-references`
	// still publishes them, as zeroes, which is the shipped shape.
	reportsReferences bool
}

var purgeCmd = &cobra.Command{
	Use:     "purge",
	GroupID: "maint",
	Short:   "Delete closed ephemeral beads to reclaim space",
	Long: `Permanently delete closed ephemeral beads and their associated data.

Closed ephemeral beads (wisps, transient molecules) accumulate rapidly and
have no value once closed. This command removes them to reclaim storage.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads, and closed beads a live bead still depends on through a
parent-child, tracks or blocks edge (a closed molecule root whose step is open,
a closed bead a live convoy tracks). Live means any status that is not done.
Without --retain-ids it also skips beads whose type the workspace's
types.retained setting names (their ids are idempotency keys); --json reports
them as retention_required.

--wisps-plane selects by storage plane instead: every closed row stored in the
wisps table, including --no-history beads, which the default ephemeral
selection leaves to ` + "`bd prune`" + `. It reaches durable-tier rows, so it requires
--older-than or --pattern, like ` + "`bd prune`" + `.

--limit N caps one run at N beads, oldest-closed first, so a large backlog can
be drained in bounded transactions; --json then reports "remaining" and
"has_more". Loop while has_more is true.

--older-than takes days (7, 7d), weeks (2w) or a duration with hour precision
(36h, 168h, 90m).

--retain-ids retires every id the sweep deletes: a permanent tombstone (the id
and its closed_at) is written in the same transaction, and from then on a
create naming that id is refused (code idempotency_expired) and an import
skips it. Use it when ids are idempotency keys that must never run twice.
Tombstones are permanent.

To delete closed non-ephemeral beads (regular tasks, features, bugs, etc.)
use ` + "`bd prune`" + ` instead.

For full Dolt storage reclaim after deleting many rows, follow with ` + "`bd flatten`" + `
so history can be collapsed and old chunks can be garbage-collected.

EXAMPLES:
  bd purge                           # Preview what would be purged
  bd purge --force                   # Delete all closed ephemeral beads
  bd purge --older-than 7d --force   # Only purge items closed 7+ days ago
  bd purge --pattern "*-wisp-*"      # Only purge matching ID pattern
  bd purge --older-than 36h --force  # Hour precision: closed 36+ hours ago
  bd purge --wisps-plane --older-than 168h --force
                                     # Every closed wisps-table row, incl. no-history
  bd purge --dry-run                 # Detailed preview with stats`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("purge")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		scope := purgeScope{
			cmdName:               "purge",
			pastTense:             "purged",
			countKey:              "purged_count",
			dryRunCountKey:        "purge_count",
			subjectNoun:           "closed ephemeral bead",
			tier:                  issueops.SweepEphemeral,
			protectLiveDependents: true,
		}
		if wispsPlane, _ := cmd.Flags().GetBool("wisps-plane"); wispsPlane {
			scope.subjectNoun = "closed wisps-plane bead"
			scope.tier = issueops.SweepWispsPlane
		}
		return runPurgeOrPrune(cmd, scope)
	},
}

// openSweeper hands back the bulk-clearance role for whichever route this
// invocation is on.
func openSweeper() (issueops.Sweeper, error) {
	if usesProxiedServer() {
		return proxiedSweeper()
	}
	if store == nil {
		if err := ensureStoreActive(); err != nil {
			return nil, err
		}
	}
	return store.Sweeper()
}

// runPurgeOrPrune implements the shared delete-closed-beads flow used by both
// `bd purge` (ephemeral tier) and `bd prune` (durable tier), on both routes.
func runPurgeOrPrune(cmd *cobra.Command, scope purgeScope) error {
	CheckReadonly(scope.cmdName)
	warnIgnoredYAMLRetainedTypes()

	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	olderThan, _ := cmd.Flags().GetString("older-than")
	pattern, _ := cmd.Flags().GetString("pattern")
	// Only `bd purge` defines --limit; GetInt on an undefined flag is 0, "no cap".
	limit, _ := cmd.Flags().GetInt("limit")
	retainIDs, _ := cmd.Flags().GetBool("retain-ids")
	if limit < 0 {
		return HandleErrorRespectJSON("invalid --limit value %d: must be 0 (no limit) or positive", limit)
	}

	// The ROLE refuses an unfiltered durable sweep — that guard is
	// workapi.ValidateSweepRequest, below every front door. This branch is here
	// for the MESSAGE: the role's refusal names request fields, not the two
	// flags a person who typed `bd prune --force` has to reach for. The contract
	// case RunSweeperRefusesAnUnfilteredDurableSweep proves the guard survives
	// this branch being deleted.
	if scope.tier != issueops.SweepEphemeral && olderThan == "" && pattern == "" {
		command := scope.cmdName
		if scope.tier == issueops.SweepWispsPlane {
			command += " --wisps-plane"
		}
		return HandleErrorWithHint(
			fmt.Sprintf("bd %s requires --older-than or --pattern", command),
			"Protects against accidental bulk deletion. Use `--pattern '*'` to\n"+
				"  include all closed beads in this scope, or `--older-than 1d`\n"+
				"  / `--pattern '<glob>'` to narrow the deletion.")
	}

	request := issueops.SweepRequest{
		Actor:                 currentActor(),
		Tier:                  scope.tier,
		IDPattern:             pattern,
		ProtectReferenced:     scope.protectReferenced,
		ProtectLiveDependents: scope.protectLiveDependents,
		Limit:                 limit,
		RetainIDs:             retainIDs,
		// A --dry-run and an UNCONFIRMED run ask the role the same question —
		// "what would this do" — so both send DryRun. --force is this
		// command's confirmation, not a request field.
		DryRun: dryRun || !force,
	}
	if olderThan != "" {
		age, err := parseOlderThan(olderThan)
		if err != nil {
			return HandleErrorRespectJSON("invalid --older-than value %q: %v", olderThan, err)
		}
		cutoff := time.Now().UTC().Add(-age)
		request.ClosedBefore = &cutoff
	}

	sweeper, err := openSweeper()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	// The role mints the sweep's version commit itself, so batch and off modes
	// have to be said on the CONTEXT — the same way `bd delete` says them. Left
	// off, an embedded `bd prune --dolt-auto-commit batch` advances HEAD, which
	// is the one thing batch mode promises it will not do.
	opsCtx, err := issueOpsContext(rootCtx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	result, err := sweeper.Sweep(opsCtx, request)
	if err != nil {
		return HandleErrorRespectJSON("%s failed: %v", scope.cmdName, err)
	}

	warnSweepDefenseSkips(result.Skipped)

	switch {
	case result.Swept == 0:
		return emitSweepEmpty(scope, olderThan, pattern, limit, result)
	case dryRun:
		return emitSweepDryRun(scope, limit, result)
	case !force:
		return emitSweepConfirm(scope, olderThan, pattern, limit, result)
	}

	commandDidWrite.Store(true)
	commandMayEmptyJSONLExport.Store(true)
	return emitSweepResult(scope, limit, result)
}

// warnSweepDefenseSkips reports the candidates the role's own recheck threw
// out. A non-zero count means the tier query and the recheck disagreed about
// which rows are closed, which earns a line on stderr in any output mode.
func warnSweepDefenseSkips(skips issueops.SweepSkips) {
	total := skips.Unreadable + skips.NotClosed + skips.UnknownClosedAt + skips.ClosedAtOrAfterCutoff
	if total == 0 {
		return
	}
	WarnError("skipped %d deletion candidate(s) after closed_at safety recheck (nil=%d, non_closed=%d, missing_closed_at=%d, too_recent=%d)",
		total,
		skips.Unreadable,
		skips.NotClosed,
		skips.UnknownClosedAt,
		skips.ClosedAtOrAfterCutoff,
	)
}

// addReferenceStats attaches the reference-skip members to a --json payload.
// `bd prune` publishes them whether or not the protection was asked for, which
// is the shipped shape; `bd purge` never does.
func addReferenceStats(scope purgeScope, stats map[string]interface{}, result issueops.SweepResult) {
	if !scope.reportsReferences {
		return
	}
	stats["referenced_skipped"] = result.Skipped.Referenced
	stats["referenced_count"] = result.Skipped.Referenced
	if len(result.ReferencedIDs) > 0 {
		stats["referenced_ids_sample"] = result.ReferencedIDs
	}
}

// addLimitStats reports what --limit left for the next run. It is attached
// only when the caller asked for a limit, so the unlimited shape is unchanged.
func addLimitStats(stats map[string]interface{}, limit int, result issueops.SweepResult) {
	if limit <= 0 {
		return
	}
	stats["remaining"] = result.Remaining
	stats["has_more"] = result.Remaining > 0
}

// addRetainStats reports that the sweep retired (or, previewed, would retire)
// every id it counts. It is attached only when --retain-ids asked for it, so
// a plain sweep's shape is unchanged.
func addRetainStats(stats map[string]interface{}, result issueops.SweepResult) {
	if result.RetainIDs {
		stats["retain_ids"] = true
	}
}

// printRetainNote tells a text reader how many ids the sweep retired or, in a
// preview, would retire.
func printRetainNote(result issueops.SweepResult, dryRun bool) {
	if !result.RetainIDs || result.Swept == 0 {
		return
	}
	if dryRun {
		fmt.Printf("  Would retire %d id(s): a create of any of them would be refused from then on\n", result.Swept)
		return
	}
	fmt.Printf("  Retired %d id(s): a create of any of them is refused from now on\n", result.Swept)
}

// addRetentionStats reports the closed beads the sweep kept because their type
// is one the workspace retains (types.retained). It is attached only when it
// held something back, the way pinned_skipped is.
func addRetentionStats(stats map[string]interface{}, result issueops.SweepResult) {
	if result.Skipped.RetentionRequired > 0 {
		stats["retention_required"] = result.Skipped.RetentionRequired
	}
}

// retentionNote is the text line for addRetentionStats' count. It names the
// one flag that takes those beads, so the reader is not left with a sweep that
// silently keeps rows.
func retentionNote(result issueops.SweepResult) string {
	return fmt.Sprintf("%d closed bead(s) of a retained type kept (types.retained) — only --retain-ids deletes and retires them",
		result.Skipped.RetentionRequired)
}

// printLimitNote tells a text reader that --limit left rows behind.
func printLimitNote(result issueops.SweepResult) {
	if result.Remaining > 0 {
		fmt.Printf("  Remaining (over --limit): %d — run again to continue\n", result.Remaining)
	}
}

// addLiveDependentStats attaches the live-dependent skip count to a --json
// payload when the protection held anything back, the way pinned_skipped is
// reported.
func addLiveDependentStats(stats map[string]interface{}, result issueops.SweepResult) {
	if result.Skipped.LiveDependent > 0 {
		stats["live_dependent_skipped"] = result.Skipped.LiveDependent
	}
}

func emitSweepEmpty(scope purgeScope, olderThan, pattern string, limit int, result issueops.SweepResult) error {
	if jsonOutput {
		stats := map[string]interface{}{
			scope.countKey: 0,
			"message":      fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName),
		}
		addLiveDependentStats(stats, result)
		addRetentionStats(stats, result)
		addLimitStats(stats, limit, result)
		addReferenceStats(scope, stats, result)
		return outputJSON(stats)
	}
	msg := fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName)
	if olderThan != "" {
		msg += fmt.Sprintf(" (older than %s)", olderThan)
	}
	if pattern != "" {
		msg += fmt.Sprintf(" (matching %q)", pattern)
	}
	fmt.Println(msg)
	if result.Skipped.LiveDependent > 0 {
		fmt.Println(ui.MutedStyle.Render(fmt.Sprintf(
			"  (%d closed bead(s) protected by live dependents)", result.Skipped.LiveDependent)))
	}
	if result.Skipped.RetentionRequired > 0 {
		fmt.Println(ui.MutedStyle.Render("  (" + retentionNote(result) + ")"))
	}
	if result.Skipped.Referenced > 0 {
		fmt.Println(ui.MutedStyle.Render(fmt.Sprintf(
			"  (%d closed bead(s) protected by open-bead references — use --ignore-references to override)",
			result.Skipped.Referenced)))
	}
	return nil
}

func emitSweepDryRun(scope purgeScope, limit int, result issueops.SweepResult) error {
	if jsonOutput {
		stats := map[string]interface{}{
			"dry_run":            true,
			scope.dryRunCountKey: result.Swept,
			"dependencies":       result.Dependencies,
			"labels":             result.Labels,
			"events":             result.Events,
		}
		if result.Skipped.Pinned > 0 {
			stats["pinned_skipped"] = result.Skipped.Pinned
		}
		addLiveDependentStats(stats, result)
		addRetentionStats(stats, result)
		addLimitStats(stats, limit, result)
		addReferenceStats(scope, stats, result)
		addRetainStats(stats, result)
		return outputJSON(stats)
	}
	fmt.Printf("Would %s %d %s(s)\n", scope.cmdName, result.Swept, scope.subjectNoun)
	fmt.Printf("  Dependencies: %d\n", result.Dependencies)
	fmt.Printf("  Labels:       %d\n", result.Labels)
	fmt.Printf("  Events:       %d\n", result.Events)
	if result.Skipped.Pinned > 0 {
		fmt.Printf("  Pinned (skipped): %d\n", result.Skipped.Pinned)
	}
	if result.Skipped.LiveDependent > 0 {
		fmt.Printf("  Live dependent (skipped): %d\n", result.Skipped.LiveDependent)
	}
	if result.Skipped.RetentionRequired > 0 {
		fmt.Printf("  %s\n", retentionNote(result))
	}
	if result.Skipped.Referenced > 0 {
		fmt.Printf("  %s   %d\n", ui.MutedStyle.Render("Referenced (skipped):"), result.Skipped.Referenced)
		sample := result.ReferencedIDs
		if len(sample) > 5 {
			sample = sample[:5]
		}
		idStrs := make([]string, len(sample))
		for i, id := range sample {
			idStrs[i] = ui.IDStyle.Render(id)
		}
		suffix := ""
		if result.Skipped.Referenced > 5 {
			suffix = ui.MutedStyle.Render(", ...")
		}
		fmt.Printf("  %s %s%s\n", ui.MutedStyle.Render("Referenced IDs (sample):"), strings.Join(idStrs, ", "), suffix)
	}
	printLimitNote(result)
	printRetainNote(result, true)
	fmt.Printf("\n(Dry-run mode — no changes made)\n")
	return nil
}

func emitSweepConfirm(scope purgeScope, olderThan, pattern string, limit int, result issueops.SweepResult) error {
	fmt.Printf("Found %d %s(s) to %s\n", result.Swept, scope.subjectNoun, scope.cmdName)
	if result.Skipped.Pinned > 0 {
		fmt.Printf("Skipping %d pinned bead(s)\n", result.Skipped.Pinned)
	}
	if result.Skipped.LiveDependent > 0 {
		fmt.Printf("Skipping %d bead(s) a live bead depends on\n", result.Skipped.LiveDependent)
	}
	if result.Skipped.RetentionRequired > 0 {
		fmt.Printf("Skipping %s\n", retentionNote(result))
	}
	if result.Skipped.Referenced > 0 {
		fmt.Println(ui.MutedStyle.Render(fmt.Sprintf("Skipping %d referenced bead(s)", result.Skipped.Referenced)))
	}
	return HandleErrorWithHint(
		fmt.Sprintf("would %s %d bead(s)", scope.cmdName, result.Swept),
		fmt.Sprintf("Use --force to confirm or --dry-run to preview.\n  %s", sweepConfirmHint(scope, olderThan, pattern, limit, result.RetainIDs)))
}

// sweepConfirmHint is the command the confirmation suggests: the caller's own
// selection with --force added. Every selector the caller gave is carried,
// --limit among them — a hint that dropped the limit previewed one deletion
// and, followed, deleted every eligible row, no-history beads included — and
// so is --retain-ids.
func sweepConfirmHint(scope purgeScope, olderThan, pattern string, limit int, retainIDs bool) string {
	hint := fmt.Sprintf("bd %s --force", scope.cmdName)
	if scope.tier == issueops.SweepWispsPlane {
		hint += " --wisps-plane"
	}
	if olderThan != "" {
		hint += " --older-than " + olderThan
	}
	if pattern != "" {
		hint += " --pattern " + pattern
	}
	if limit > 0 {
		hint += fmt.Sprintf(" --limit %d", limit)
	}
	if retainIDs {
		// The confirmation must ask for the same sweep that was previewed: a
		// hint that dropped the flag would run one that leaves the ids reusable.
		hint += " --retain-ids"
	}
	return hint
}

func emitSweepResult(scope purgeScope, limit int, result issueops.SweepResult) error {
	if jsonOutput {
		stats := map[string]interface{}{
			scope.countKey: result.Swept,
			"dependencies": result.Dependencies,
			"labels":       result.Labels,
			"events":       result.Events,
		}
		if result.Skipped.Pinned > 0 {
			stats["pinned_skipped"] = result.Skipped.Pinned
		}
		addLiveDependentStats(stats, result)
		addRetentionStats(stats, result)
		addLimitStats(stats, limit, result)
		addReferenceStats(scope, stats, result)
		addRetainStats(stats, result)
		return outputJSON(stats)
	}
	fmt.Printf("%s %s %d %s(s)\n", ui.RenderPass("✓"), capitalize(scope.pastTense), result.Swept, scope.subjectNoun)
	fmt.Printf("  Dependencies removed: %d\n", result.Dependencies)
	fmt.Printf("  Labels removed:       %d\n", result.Labels)
	fmt.Printf("  Events removed:       %d\n", result.Events)
	if result.Skipped.Pinned > 0 {
		fmt.Printf("  Pinned (skipped):     %d\n", result.Skipped.Pinned)
	}
	if result.Skipped.LiveDependent > 0 {
		fmt.Printf("  Live dependent (skipped): %d\n", result.Skipped.LiveDependent)
	}
	if result.Skipped.RetentionRequired > 0 {
		fmt.Printf("  %s\n", retentionNote(result))
	}
	if result.Skipped.Referenced > 0 {
		fmt.Printf("  %s %d\n", ui.MutedStyle.Render("Referenced (skipped):"), result.Skipped.Referenced)
	}
	printLimitNote(result)
	printRetainNote(result, false)
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// parseOlderThan parses an --older-than value into an age.
//
// Day-denominated values keep the meaning they always had: a bare number is
// days ("7"), and "7d" / "2w" are days and weeks. Anything else is a Go
// duration with hour precision or finer — "36h", "168h", "90m", "1h30m" — and
// is taken exactly; "Nh" used to be floored to whole days, which made "36h"
// sweep rows only 24 hours old. The suffix letters are case-insensitive, as
// they always were. The age must be positive.
func parseOlderThan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	if days, err := strconv.Atoi(s); err == nil {
		return daysAge(int64(days))
	}

	unit := s[len(s)-1]
	switch unit {
	case 'd', 'D', 'w', 'W':
		num, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, fmt.Errorf("invalid number %q", s[:len(s)-1])
		}
		days := int64(num)
		if unit == 'w' || unit == 'W' {
			if days > maxOlderThanDays/7 || days < -maxOlderThanDays/7 {
				return 0, errOlderThanRange
			}
			days *= 7
		}
		return daysAge(days)
	}

	age, err := time.ParseDuration(strings.ToLower(s))
	if err != nil {
		// time.ParseDuration refuses an out-of-range value ("9999999h") the
		// same way it refuses a malformed one, so neither can wrap around.
		return 0, fmt.Errorf("invalid or out-of-range duration %q (use a number of days, Nd, Nw, or a duration such as 36h or 90m)", s)
	}
	return positiveAge(age)
}

// maxOlderThanDays is the largest day count a time.Duration can hold. A larger
// value used to overflow silently — 213504d wrapped to 25 minutes — which on a
// destructive command selected nearly everything instead of nothing.
const maxOlderThanDays = math.MaxInt64 / int64(24*time.Hour)

var errOlderThanRange = fmt.Errorf("duration out of range (at most %d days)", maxOlderThanDays)

func daysAge(days int64) (time.Duration, error) {
	// Only the upper bound was checked here; a day count negative enough
	// overflows the int64-nanosecond multiplication into a small positive
	// duration, so positiveAge's own check below never sees the sign flip.
	// Reject non-positive counts before the multiplication can run.
	if days <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	if days > maxOlderThanDays {
		return 0, errOlderThanRange
	}
	return positiveAge(time.Duration(days) * 24 * time.Hour)
}

func positiveAge(age time.Duration) (time.Duration, error) {
	if age <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return age, nil
}

func init() {
	purgeCmd.Flags().BoolP("force", "f", false, "Actually purge (without this, shows preview)")
	purgeCmd.Flags().Bool("dry-run", false, "Preview what would be purged with stats")
	purgeCmd.Flags().String("older-than", "", "Only purge beads closed more than N ago (e.g., 7d, 2w, 30, 36h)")
	purgeCmd.Flags().Int("limit", 0, "Purge at most N beads this run, oldest-closed first (0 = no limit); --json reports remaining/has_more")
	purgeCmd.Flags().Bool("wisps-plane", false, "Select every closed row in the wisps table, including --no-history beads (requires --older-than or --pattern)")
	purgeCmd.Flags().String("pattern", "", "Only purge beads matching ID glob pattern (e.g., *-wisp-*)")
	purgeCmd.Flags().Bool("retain-ids", false, "Retire every purged id with a permanent tombstone so it can never be created again (see --help)")
	rootCmd.AddCommand(purgeCmd)
}
