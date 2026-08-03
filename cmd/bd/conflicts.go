package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/ui"
)

// `bd conflicts` is the operator surface for a merge that halted (federation
// ask #3). Without it, resolving means dropping into the raw dolt CLI inside
// .beads/dolt/<db> — `dolt conflicts cat issues`, `dolt conflicts resolve
// --ours issues`, `dolt add -A && dolt commit` — whose flag surface differs
// from git's just enough to bite. Here the same work is issue-oriented: which
// issues are conflicted, what each side says field by field, and a resolution
// that can name a single issue instead of a whole table.
//
// It reads the LIVE working set. beads' own pull path auto-resolves the safe
// conflict classes and aborts anything else (restoring the working set), so
// what lands here is what a CLI-level pull — the federation bridge's — left
// behind, plus anything a `bd vc merge` left unresolved.

const conflictsResolveHint = "Resolve with: bd conflicts resolve <issue-id> --ours|--theirs"

var conflictsCmd = &cobra.Command{
	Use:     "conflicts",
	GroupID: "sync",
	Short:   "Inspect and resolve live merge conflicts",
	Long: `Inspect and resolve the merge conflicts sitting in the working set.

Conflicts appear when a pull or merge brought in changes that collide with
local ones and could not be settled automatically. These commands present them
per issue and per field, and resolve them without the raw dolt CLI.

Examples:
  bd conflicts list                          # which tables and issues are conflicted
  bd conflicts show                          # every conflicted row, field by field
  bd conflicts show bd-1234                  # one issue
  bd conflicts resolve bd-1234 --ours        # keep our side of one issue
  bd conflicts resolve --all --theirs        # take their side of everything`,
}

var (
	conflictsShowAllFields bool
	conflictsShowTable     string
	conflictsResolveOurs   bool
	conflictsResolveTheirs bool
	conflictsResolveStrat  string
	conflictsResolveAll    bool
	conflictsResolveTable  string
	conflictsNoCommit      bool
	conflictsConclude      bool
)

var conflictsListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List tables and issues with live merge conflicts",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer conflictsMetrics("conflicts-list")()
		ctx := rootCtx
		if err := requireConflictSupport(); err != nil {
			return err
		}
		tables, err := conflictedTables(ctx)
		if err != nil {
			return HandleErrorRespectJSON("failed to read conflicts: %v", err)
		}
		type tableOut struct {
			Table string   `json:"table"`
			Count int      `json:"count"`
			Keys  []string `json:"keys,omitempty"`
		}
		out := make([]tableOut, 0, len(tables))
		total := 0
		for _, t := range tables {
			rows, err := conflictRows(ctx, t.Field)
			if err != nil {
				return HandleErrorRespectJSON("failed to read conflicts for %s: %v", t.Field, err)
			}
			keys := make([]string, 0, len(rows))
			for _, r := range rows {
				if r.Key != "" {
					keys = append(keys, r.Key)
				}
			}
			// dolt_conflicts' own count is authoritative for tables whose
			// rows we could not key (keyless tables list no keys).
			count := len(rows)
			if count == 0 {
				count = t.Count
			}
			total += count
			out = append(out, tableOut{Table: t.Field, Count: count, Keys: keys})
		}
		// Schema conflicts and constraint violations are outstanding merge
		// state that dolt_conflicts never lists, so "No merge conflicts."
		// over a wedged merge was a lie (wy-36ilm F12).
		blockers, blockerErr := mergeBlockers(ctx)
		if jsonOutput {
			payload := map[string]interface{}{
				"conflicts": total,
				"tables":    out,
				"blockers":  blockers,
			}
			if blockerErr != nil {
				payload["blockers_error"] = blockerErr.Error()
			}
			return outputJSON(payload)
		}
		if total == 0 {
			switch {
			case blockers.Blocked():
				fmt.Println("No conflicted rows.")
				printMergeBlockers(blockers)
			case blockers.Merging:
				fmt.Println("No merge conflicts; a merge is open and resolved.")
				fmt.Println("Conclude it with: bd conflicts resolve --conclude")
			default:
				fmt.Println("No merge conflicts.")
			}
			if blockerErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not read schema conflicts/constraint violations: %v\n", blockerErr)
			}
			return nil
		}
		fmt.Printf("\n%s %d live merge conflict(s):\n\n", ui.RenderAccent("!!"), total)
		for _, t := range out {
			fmt.Printf("  %s (%d)\n", ui.RenderAccent(t.Table), t.Count)
			for _, k := range t.Keys {
				fmt.Printf("    %s\n", k)
			}
		}
		printMergeBlockers(blockers)
		if blockerErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read schema conflicts/constraint violations: %v\n", blockerErr)
		}
		fmt.Printf("\nInspect with: bd conflicts show [<issue-id>]\n%s\n\n", conflictsResolveHint)
		return nil
	},
}

var conflictsShowCmd = &cobra.Command{
	Use:   "show [<issue-id>]",
	Short: "Show conflicted rows field by field (base/ours/theirs)",
	Long: `Show each conflicted row with its fields side by side.

Only fields where our side and their side disagree are shown; --all-fields
shows every column. Without an issue ID, every conflicted row of every
conflicted table is shown.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer conflictsMetrics("conflicts-show")()
		ctx := rootCtx
		if err := requireConflictSupport(); err != nil {
			return err
		}
		wantKey := ""
		if len(args) == 1 {
			wantKey = args[0]
		}

		var tables []string
		if conflictsShowTable != "" {
			tables = []string{conflictsShowTable}
		} else {
			ts, err := conflictedTables(ctx)
			if err != nil {
				return HandleErrorRespectJSON("failed to read conflicts: %v", err)
			}
			for _, t := range ts {
				tables = append(tables, t.Field)
			}
		}

		var matched []storage.ConflictRow
		for _, table := range tables {
			rows, err := conflictRows(ctx, table)
			if err != nil {
				return HandleErrorRespectJSON("failed to read conflicts for %s: %v", table, err)
			}
			for _, r := range rows {
				if wantKey != "" && r.Key != wantKey {
					continue
				}
				matched = append(matched, r)
			}
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"conflicts": len(matched),
				"rows":      filterShownFields(matched, conflictsShowAllFields),
			})
		}
		if len(matched) == 0 {
			if wantKey != "" {
				fmt.Printf("No live merge conflict for %s.\n", wantKey)
			} else {
				fmt.Println("No merge conflicts.")
			}
			return nil
		}
		for _, r := range matched {
			printConflictRow(r, conflictsShowAllFields)
		}
		fmt.Printf("%s\n\n", conflictsResolveHint)
		return nil
	},
}

var conflictsResolveCmd = &cobra.Command{
	Use:   "resolve [<issue-id>...]",
	Short: "Resolve merge conflicts with --ours or --theirs",
	Long: `Resolve live merge conflicts, then conclude the merge with a commit.

Named issue IDs are resolved row by row, leaving every other conflicted row
alone. --all resolves whole tables at once (dolt's own table-level
resolution). The merge is committed only once NO conflicts remain, so a
partial resolution leaves the merge open for the next pass.

Row-by-row resolution requires the row to exist on both sides: when one side
deleted it, resolve that table wholesale or edit the row directly.

Examples:
  bd conflicts resolve bd-1234 --ours              # keep our side of one issue
  bd conflicts resolve bd-1234 bd-5678 --theirs    # take their side of two
  bd conflicts resolve --all --ours                # every conflicted table
  bd conflicts resolve --all --table config --theirs
  bd conflicts resolve --conclude                  # commit an already-resolved merge`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer conflictsMetrics("conflicts-resolve")()
		ctx := rootCtx
		if err := requireConflictSupport(); err != nil {
			return err
		}
		// --conclude commits a merge whose conflicts are ALREADY gone: the
		// state left by --no-commit, or by a partial resolve whose later ID
		// errored out. Before this, that merge could not be concluded through
		// bd at all — with zero live conflicts, --all returns early and a
		// named ID errors "no live conflict" (wy-36ilm F4).
		if conflictsConclude {
			if err := concludeFlagConflict(args); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			return concludeResolvedMerge(ctx)
		}

		strategy, err := resolveStrategy()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if conflictsResolveAll == (len(args) > 0) {
			return HandleErrorRespectJSON("name the issue IDs to resolve, or pass --all (not both)")
		}

		// Pre-resolution HEAD scopes the is_blocked recompute the merged-in
		// writes bypassed, exactly as bd vc merge does (bd-578h9.11).
		preHead, _ := store.GetCurrentCommit(ctx)

		resolved := 0
		if conflictsResolveAll {
			tables := []string{}
			if conflictsResolveTable != "" {
				tables = append(tables, conflictsResolveTable)
			} else {
				ts, err := conflictedTables(ctx)
				if err != nil {
					return HandleErrorRespectJSON("failed to read conflicts: %v", err)
				}
				for _, t := range ts {
					tables = append(tables, t.Field)
				}
			}
			if len(tables) == 0 {
				fmt.Println("No merge conflicts.")
				return nil
			}
			for _, table := range tables {
				rows, err := conflictRows(ctx, table)
				if err != nil {
					return HandleErrorRespectJSON("failed to read conflicts for %s: %v", table, err)
				}
				if err := store.ResolveConflicts(ctx, table, strategy); err != nil {
					return HandleErrorRespectJSON("failed to resolve conflicts in %s: %v", table, err)
				}
				resolved += len(rows)
			}
		} else {
			table := conflictsResolveTable
			if table == "" {
				table = "issues"
			}
			inspector, ok := conflictInspector()
			if !ok {
				return HandleErrorRespectJSON("this backend does not support per-issue conflict resolution; use --all")
			}
			if !versioncontrolops.SupportsRowResolve(table) {
				return HandleErrorRespectJSON("per-issue resolution is not supported for table %s; use --all --table %s", table, table)
			}
			n, err := inspector.ResolveConflictRows(ctx, table, args, strategy)
			if err != nil {
				// A partial resolution is real state: report what landed.
				if n > 0 {
					return HandleErrorRespectJSON("resolved %d of %d conflict(s) before failing: %v", n, len(args), err)
				}
				return HandleErrorRespectJSON("failed to resolve conflicts: %v", err)
			}
			resolved = n
		}

		remaining, err := totalConflicts(ctx)
		if err != nil {
			return HandleErrorRespectJSON("resolved %d conflict(s) but failed to re-check conflicts: %v", resolved, err)
		}

		// Schema conflicts and constraint violations survive a clean
		// dolt_conflicts, and CommitMergeResolution would fail on them with a
		// raw dolt error; hold the commit and explain instead (wy-36ilm F12).
		blockers, blockerErr := mergeBlockers(ctx)
		if blockerErr != nil && !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: could not read schema conflicts/constraint violations: %v\n", blockerErr)
		}

		committed := false
		if shouldCommitResolution(resolved, remaining, conflictsNoCommit, blockers) {
			msg := fmt.Sprintf("Resolve %d merge conflict(s) using %s strategy", resolved, strategy)
			if err := commitMergeResolution(ctx, msg, preHead); err != nil {
				return HandleErrorRespectJSON("conflicts resolved but %v", err)
			}
			committed = true
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"resolved":       resolved,
				"strategy":       strategy,
				"remaining":      remaining,
				"committed":      committed,
				"blockers":       blockers,
				"blockers_error": errText(blockerErr),
			})
		}
		fmt.Printf("Resolved %d conflict(s) using '%s'.\n", resolved, strategy)
		switch {
		case remaining > 0:
			fmt.Printf("%d conflict(s) remain; the merge is not committed yet.\nRun: bd conflicts list\n", remaining)
		case committed:
			fmt.Println("Merge committed. Push when ready: bd sync")
		case blockers.Blocked():
			printMergeBlockers(blockers)
		case resolved == 0:
			fmt.Println("Nothing was conflicted; no commit made.")
		default:
			fmt.Println("All conflicts resolved; commit withheld. Conclude the merge with: bd conflicts resolve --conclude")
		}
		return nil
	},
}

// commitMergeResolution concludes the merge: CommitMergeResolution, not
// Commit, because server-mode Commit excludes the config table (GH#2455), so
// a resolved config conflict would be dropped and the merge would re-wedge on
// the next pull (GH#2474). preHead scopes the is_blocked recompute the
// merged-in writes bypassed, exactly as bd vc merge does (bd-578h9.11).
func commitMergeResolution(ctx context.Context, msg, preHead string) error {
	if err := store.CommitMergeResolution(ctx, msg); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}
	// Same unwrap rule as conflictInspector: RecomputeBlockedAfterMerge lives
	// on the concrete Dolt store, not on DoltStorage. Route through the shared
	// helper (cmd/bd/vc.go) so the package has one declaration of the optional
	// interface, and never skip silently (bd vc merge's else branch, wy-163oy).
	if rs, ok := blockedAfterMergeRecomputerFor(store); ok {
		if err := rs.RecomputeBlockedAfterMerge(ctx, preHead); err != nil {
			return fmt.Errorf("is_blocked recompute failed: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Warning: storage backend %T cannot recompute is_blocked after a merge; 'bd ready' may be stale until 'bd recompute-blocked' runs\n", storage.UnwrapStore(store))
	}
	// The store's merge-conclusion path can no-op silently (an unreadable
	// dolt_merge_status degrades to "nothing to commit"), and reporting
	// "Merge committed" over a still-open merge is exactly the wy-36ilm
	// symptom — loudly this time (adversarial review F4).
	if after, err := mergeBlockers(ctx); err == nil && after.Merging {
		return fmt.Errorf("commit did not conclude the merge: dolt still reports a merge in progress; conclude it with `bd dolt commit`")
	}
	return nil
}

// errText renders an error for a JSON payload, nil as an empty string.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// concludeAction is what `--conclude` will do with the merge state it found.
// It exists so the decision is a pure function of that state (planConclude),
// testable without a live merge: a Blocked() inversion or a dropped
// merge-status check is otherwise only visible against real dolt.
type concludeAction int

const (
	// concludeActionCommit: nothing outstanding and a merge to finish.
	concludeActionCommit concludeAction = iota
	// concludeActionConflictsLive: dolt_conflicts still has rows.
	concludeActionConflictsLive
	// concludeActionBlocked: schema conflicts or constraint violations.
	concludeActionBlocked
	// concludeActionNothingToConclude: no merge is open at all.
	concludeActionNothingToConclude
)

// planConclude decides --conclude's outcome from the merge state.
//
// Order is load-bearing: live row conflicts outrank blockers (they are the
// thing the operator can actually resolve with bd), and blockers outrank
// "nothing to conclude" so a wedged merge can never be reported as a no-op.
// "Nothing to conclude" requires the merge status to be BOTH available and
// readable — a backend with no MergeBlockerInspector, or a blocker read that
// errored, reports Merging=false for want of knowing, and must fall through
// to attempting the commit rather than claiming there is no merge.
func planConclude(remaining int, blockers storage.MergeBlockers, blockerErr error, haveStatus bool) concludeAction {
	switch {
	case remaining > 0:
		return concludeActionConflictsLive
	case blockers.Blocked():
		return concludeActionBlocked
	case haveStatus && blockerErr == nil && !blockers.Merging:
		return concludeActionNothingToConclude
	default:
		return concludeActionCommit
	}
}

// shouldCommitResolution is the resolve path's commit-hold gate: the merge is
// committed only when this pass actually resolved something, nothing is left
// in dolt_conflicts, the operator did not ask to hold the commit, and no
// schema conflict or constraint violation would make the commit fail with a
// raw dolt error (wy-36ilm F12).
func shouldCommitResolution(resolved, remaining int, noCommit bool, blockers storage.MergeBlockers) bool {
	return resolved > 0 && remaining == 0 && !noCommit && !blockers.Blocked()
}

// concludeFlagConflict rejects the flag combinations --conclude has no
// meaning for. --conclude commits an ALREADY-resolved merge, so an issue ID,
// a strategy, --all or --table describe a resolution it will not perform, and
// silently ignoring --table would imply a scoped conclude exists (review F8).
func concludeFlagConflict(args []string) error {
	if len(args) > 0 || conflictsResolveAll || conflictsResolveOurs || conflictsResolveTheirs ||
		conflictsResolveStrat != "" || conflictsResolveTable != "" {
		return fmt.Errorf("--conclude takes no issue IDs, table or strategy: it commits an already-resolved merge")
	}
	if conflictsNoCommit {
		return fmt.Errorf("--conclude and --no-commit are opposites")
	}
	return nil
}

// concludeResolvedMerge commits a merge that has no live conflicts left —
// `bd conflicts resolve --conclude` (wy-36ilm F4). It refuses while anything
// is still outstanding, so it can only ever finish a resolution someone else
// already made, never paper over one.
func concludeResolvedMerge(ctx context.Context) error {
	remaining, err := totalConflicts(ctx)
	if err != nil {
		return HandleErrorRespectJSON("failed to read conflicts: %v", err)
	}
	blockers, blockerErr := mergeBlockers(ctx)
	if blockerErr != nil {
		// The blocker read is diagnosis, never a gate — but a caller that
		// cannot see the blockers must not read "nothing outstanding" into
		// their absence (adversarial review F3).
		fmt.Fprintf(os.Stderr, "Warning: could not read schema conflicts/constraint violations: %v\n", blockerErr)
	}
	// A backend with no MergeBlockerInspector reports Merging=false, so it
	// keeps the old behavior of just attempting the commit.
	_, haveStatus := storage.UnwrapStore(store).(storage.MergeBlockerInspector)
	switch planConclude(remaining, blockers, blockerErr, haveStatus) {
	case concludeActionConflictsLive:
		return HandleErrorRespectJSON("%d conflict(s) are still live; resolve them first (bd conflicts list)", remaining)
	case concludeActionBlocked:
		if !jsonOutput {
			printMergeBlockers(blockers)
		}
		// HandleErrorRespectJSON, not a bare error: it exits non-zero in JSON
		// mode too, so a script cannot read `{"committed":false}` at status 0
		// as success and push over an open merge (adversarial review F2).
		return HandleErrorRespectJSON("merge not concluded: schema conflicts or constraint violations are outstanding")
	case concludeActionNothingToConclude:
		// Say so rather than minting an empty commit.
		return concludeJSON(false, blockers, "No merge is in progress; nothing to conclude.")
	}

	preHead, _ := store.GetCurrentCommit(ctx)
	if err := commitMergeResolution(ctx, "Conclude resolved merge", preHead); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	blockers, _ = mergeBlockers(ctx)
	return concludeJSON(true, blockers, "Merge committed. Push when ready: bd sync")
}

// concludeJSON emits --conclude's one payload shape — same keys on every
// outcome, so a consumer can key on `committed` (review F9) — or the human
// line when JSON is off.
func concludeJSON(committed bool, blockers storage.MergeBlockers, human string) error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"committed": committed,
			"remaining": 0,
			"merging":   blockers.Merging,
			"blockers":  blockers,
		})
	}
	fmt.Println(human)
	return nil
}

// conflictsMetrics starts a command metrics event and returns its closer, so
// each subcommand can arm it with one deferred call.
func conflictsMetrics(name string) func() {
	evt := metrics.NewCommandEvent(name)
	return func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}
}

// requireConflictSupport refuses the modes where conflict inspection has no
// meaning: a proxied server owns its own working set, and a non-Dolt backend
// has no merge state at all.
func requireConflictSupport() error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("bd conflicts is not supported in proxied-server mode")
	}
	if store == nil {
		return HandleErrorRespectJSON("no database open")
	}
	return nil
}

// conflictedTables returns the tables dolt reports as conflicted.
func conflictedTables(ctx context.Context) ([]storage.Conflict, error) {
	conflicts, err := store.GetConflicts(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Field < conflicts[j].Field })
	return conflicts, nil
}

// conflictInspector returns the store's conflict surface. store is ALWAYS
// wrapped (telemetry, and the hook-firing decorator whenever hooks are on),
// and a decorator that embeds the DoltStorage interface promotes only that
// interface's methods — so asserting ConflictInspector on the wrapper is
// always false and every conflict would read as "no conflicts". UnwrapStore
// is the codebase's answer for optional interfaces (hook_decorator.go:67).
func conflictInspector() (storage.ConflictInspector, bool) {
	inspector, ok := storage.UnwrapStore(store).(storage.ConflictInspector)
	return inspector, ok
}

// conflictRows returns the per-field conflicted rows of one table, or an empty
// slice when the backend has no ConflictInspector.
func conflictRows(ctx context.Context, table string) ([]storage.ConflictRow, error) {
	inspector, ok := conflictInspector()
	if !ok {
		return nil, nil
	}
	return inspector.GetConflictRows(ctx, table)
}

// mergeBlockers reports the non-row merge state — schema conflicts and
// constraint violations — that blocks the merge commit while
// dolt_conflicts is empty (wy-36ilm F12). Same UnwrapStore rule as
// conflictInspector. A backend without the interface reports nothing, and a
// read error is the caller's to soften: this is diagnosis, never a gate.
func mergeBlockers(ctx context.Context) (storage.MergeBlockers, error) {
	inspector, ok := storage.UnwrapStore(store).(storage.MergeBlockerInspector)
	if !ok {
		return storage.MergeBlockers{}, nil
	}
	return inspector.GetMergeBlockers(ctx)
}

// printMergeBlockers renders the blockers an operator has to clear by hand.
// dolt offers no ours/theirs for either class, so this is diagnosis plus the
// dolt commands that do resolve them — the guidance the raw commit error
// (wy-36ilm F12) never carried.
func printMergeBlockers(b storage.MergeBlockers) {
	writeMergeBlockers(os.Stdout, b)
}

// writeMergeBlockers is printMergeBlockers' body against an explicit writer,
// so the remedy text is assertable without hijacking stdout. An unblocked
// state writes NOTHING: `bd conflicts list` calls this unconditionally.
func writeMergeBlockers(w io.Writer, b storage.MergeBlockers) {
	if !b.Blocked() {
		return
	}
	fmt.Fprintf(w, "\n%s the merge cannot be committed yet:\n", ui.RenderAccent("!!"))
	for _, t := range b.SchemaConflictTables {
		fmt.Fprintf(w, "  schema conflict: %s\n", t)
	}
	for _, v := range b.ConstraintViolations {
		fmt.Fprintf(w, "  constraint violations: %s (%d)\n", v.Table, v.Count)
	}
	fmt.Fprintln(w, "\nNeither class has an ours/theirs resolution — bd conflicts resolve cannot settle them.")
	if len(b.SchemaConflictTables) > 0 {
		// Not `dolt conflicts resolve`: dolt refuses that outright while a
		// schema conflict is live (dolthub/dolt#6616), so pointing an
		// operator at it sends them to a command that always errors
		// (wy-36ilm adversarial review F1). A schema conflict is aborted and
		// re-merged, not resolved in place.
		fmt.Fprintln(w, "  Schema conflicts:       abort the merge (dolt merge --abort), apply the peer's")
		fmt.Fprintln(w, "                          ALTER TABLE statements locally, then merge again")
	}
	if len(b.ConstraintViolations) > 0 {
		fmt.Fprintln(w, "  Constraint violations:  inspect dolt_constraint_violations_<table>, delete the offending rows,")
		if len(b.SchemaConflictTables) > 0 {
			fmt.Fprintln(w, "                          then re-inspect after aborting and re-merging the schema change")
		} else {
			fmt.Fprintln(w, "                          then conclude with: bd conflicts resolve --conclude")
		}
	}
}

// totalConflicts counts every live conflicted row, the gate on committing the
// merge: dolt refuses a commit while any conflict is live.
func totalConflicts(ctx context.Context) (int, error) {
	tables, err := conflictedTables(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, t := range tables {
		total += t.Count
	}
	return total, nil
}

// resolveStrategy reads the strategy from --ours/--theirs/--strategy.
func resolveStrategy() (string, error) {
	picked := ""
	switch {
	case conflictsResolveOurs && conflictsResolveTheirs:
		return "", fmt.Errorf("pass --ours or --theirs, not both")
	case conflictsResolveOurs:
		picked = versioncontrolops.ConflictStrategyOurs
	case conflictsResolveTheirs:
		picked = versioncontrolops.ConflictStrategyTheirs
	}
	if conflictsResolveStrat != "" {
		if picked != "" && picked != conflictsResolveStrat {
			return "", fmt.Errorf("--strategy %s contradicts --%s", conflictsResolveStrat, picked)
		}
		picked = conflictsResolveStrat
	}
	if picked == "" {
		return "", fmt.Errorf("a resolution strategy is required: --ours or --theirs")
	}
	if err := versioncontrolops.ValidateConflictStrategy(picked); err != nil {
		return "", err
	}
	return picked, nil
}

// filterShownFields drops the agreeing fields from JSON output unless the
// caller asked for all of them, so a conflict reads as its handful of
// diverged fields rather than the whole row.
func filterShownFields(rows []storage.ConflictRow, allFields bool) []storage.ConflictRow {
	if allFields {
		return rows
	}
	out := make([]storage.ConflictRow, 0, len(rows))
	for _, r := range rows {
		trimmed := r
		trimmed.Fields = differingFields(r.Fields)
		out = append(out, trimmed)
	}
	return out
}

func differingFields(fields []storage.ConflictFieldValue) []storage.ConflictFieldValue {
	out := make([]storage.ConflictFieldValue, 0, len(fields))
	for _, f := range fields {
		if f.Differs() {
			out = append(out, f)
		}
	}
	return out
}

// printConflictRow renders one conflicted row for humans.
func printConflictRow(r storage.ConflictRow, allFields bool) {
	key := r.Key
	if key == "" {
		key = "(unkeyed row)"
	}
	fmt.Printf("\n%s %s  [%s]\n", ui.RenderAccent(r.Table), key, conflictKind(r))
	fields := r.Fields
	if !allFields {
		fields = differingFields(fields)
	}
	if len(fields) == 0 {
		fmt.Println("  (no differing fields; the conflict is structural)")
		return
	}
	for _, f := range fields {
		fmt.Printf("  %s\n", f.Name)
		fmt.Printf("    base:   %s\n", conflictValue(f.Base))
		fmt.Printf("    ours:   %s\n", conflictValue(f.Ours))
		fmt.Printf("    theirs: %s\n", conflictValue(f.Theirs))
	}
	fmt.Println()
}

// conflictKind names the conflict class in the vocabulary an operator needs to
// choose a strategy, since ours/theirs mean different things per class.
func conflictKind(r storage.ConflictRow) string {
	switch {
	case !r.OurExists && !r.TheirExists:
		return "both sides deleted"
	case !r.OurExists:
		return "we deleted / they modified"
	case !r.TheirExists:
		return "we modified / they deleted"
	case !r.BaseExists:
		return "both sides added"
	default:
		return "both sides modified"
	}
}

// conflictValue renders a field value, distinguishing SQL NULL from empty.
func conflictValue(v *string) string {
	if v == nil {
		return "(null)"
	}
	s := strings.ReplaceAll(*v, "\n", "\\n")
	if s == "" {
		return `""`
	}
	return s
}

func init() {
	conflictsShowCmd.Flags().BoolVar(&conflictsShowAllFields, "all-fields", false, "Show every column, not just the fields that diverged")
	conflictsShowCmd.Flags().StringVar(&conflictsShowTable, "table", "", "Restrict to one conflicted table (default: all)")

	conflictsResolveCmd.Flags().BoolVar(&conflictsResolveOurs, "ours", false, "Keep our side")
	conflictsResolveCmd.Flags().BoolVar(&conflictsResolveTheirs, "theirs", false, "Take their side")
	conflictsResolveCmd.Flags().StringVar(&conflictsResolveStrat, "strategy", "", "Resolution strategy: ours|theirs")
	conflictsResolveCmd.Flags().BoolVar(&conflictsResolveAll, "all", false, "Resolve whole tables instead of named issues")
	conflictsResolveCmd.Flags().StringVar(&conflictsResolveTable, "table", "", "Table to resolve (default: issues)")
	conflictsResolveCmd.Flags().BoolVar(&conflictsNoCommit, "no-commit", false, "Resolve without committing the merge")
	conflictsResolveCmd.Flags().BoolVar(&conflictsConclude, "conclude", false, "Commit a merge whose conflicts are already resolved")

	conflictsCmd.AddCommand(conflictsListCmd)
	conflictsCmd.AddCommand(conflictsShowCmd)
	conflictsCmd.AddCommand(conflictsResolveCmd)
	rootCmd.AddCommand(conflictsCmd)
}
