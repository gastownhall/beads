package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// leaseTTLFlagName is the --ttl flag `bd update --claim`, `bd ready --claim`
// and `bd heartbeat` share: the lease length the command arms or refreshes,
// in place of storageissueops.DefaultLeaseTTL.
//
// It reaches the lease code through storageissueops.WithLeaseTTL on the
// context, which crosses no process boundary. Every route that runs the lease
// code in this process honors it: the embedded store, a Dolt server store and
// the proxied-server unit-of-work provider. A remote bd serve backend runs
// the lease code in the server process, and its claim, claim-next and
// heartbeat requests carry no lease length, so --ttl is refused there
// (leaseTTLRemoteRefusal) rather than accepted and dropped.
const leaseTTLFlagName = "ttl"

// claimLeaseTTLUsage is --ttl's help on `bd update` and `bd ready`.
//
// Neither usage names the default's length: it is whatever the lease code
// resolves when no --ttl is given (storageissueops.LeaseTTL), so a number
// printed here would go stale the day that default changes.
//
// The heartbeat sentence is the behavior runLeaseTTLFlagJourney's "heartbeat
// without --ttl" step and TestProxiedServerHeartbeat's
// ttl_sets_the_lease_per_call subtest pin: a heartbeat sets the expiry to now
// plus its OWN lease length, so a lease armed with --ttl 2h is cut back to the
// default by the first heartbeat that does not pass --ttl again.
const claimLeaseTTLUsage = "With --claim: the claim's lease expires this long from now (a Go duration such as 30m or 2h) instead of after the default lease length. " +
	"Each bd heartbeat resets the expiry to now plus its own --ttl, or the default length without one, so pass the same --ttl on every heartbeat. " +
	"A re-claim of an issue you already hold leaves its lease unchanged, and a wisp has no lease. Refused on a remote bd serve backend"

// heartbeatLeaseTTLUsage is --ttl's help on `bd heartbeat`.
const heartbeatLeaseTTLUsage = "The refreshed lease expires this long from now (a Go duration such as 30m or 2h) instead of after the default lease length. " +
	"The length is per call: a heartbeat without --ttl resets the expiry to now plus the default length, even on a claim armed with a longer --ttl, so pass it on every heartbeat. " +
	"Refused on a remote bd serve backend"

// addLeaseTTLFlag registers --ttl on cmd. The zero default means "not given":
// leaseTTLFromFlags tells an absent flag from an explicit 0 by Changed.
func addLeaseTTLFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().Duration(leaseTTLFlagName, 0, usage)
}

// leaseTTLFromFlags reads and validates --ttl. It returns 0 when the flag was
// not given, which leaves the store's default lease in force.
//
// requiresClaim names the flag --ttl rides on (`--claim` for update and
// ready, "" for heartbeat, whose every call refreshes a lease); when it is set
// and claim is false the flag is refused, because it would change nothing and
// a silent no-op hides a typo. A zero or negative duration is refused because
// the lease code falls back to the default for it, and a caller that typed
// --ttl 0 would hold a lease it did not ask for.
func leaseTTLFromFlags(cmd *cobra.Command, requiresClaim string, claim bool) (time.Duration, error) {
	if !cmd.Flags().Changed(leaseTTLFlagName) {
		return 0, nil
	}
	if requiresClaim != "" && !claim {
		return 0, HandleErrorRespectJSON("--ttl requires %s: it sets the length of the lease a claim arms", requiresClaim)
	}
	ttl, err := cmd.Flags().GetDuration(leaseTTLFlagName)
	if err != nil {
		return 0, HandleErrorRespectJSON("invalid --ttl: %v", err)
	}
	if ttl <= 0 {
		return 0, HandleErrorRespectJSON("--ttl must be a positive duration such as 30m or 2h, got %s", ttl)
	}
	return ttl, nil
}

// withLeaseTTL returns ctx carrying ttl as the lease length for the claims
// and heartbeats run under it, or ctx itself when ttl is 0 (--ttl not given).
func withLeaseTTL(ctx context.Context, ttl time.Duration) context.Context {
	if ttl <= 0 {
		return ctx
	}
	return storageissueops.WithLeaseTTL(ctx, ttl)
}

// leaseTTLRemoteRefusal refuses --ttl on a store that is a network client of
// a remote bd serve. The lease is granted in the server process, the context
// value does not travel with the request, and the request types have no lease
// member, so the server would grant its default lease to a worker that asked
// for a longer one, and bd reclaim would take that worker's claim away.
//
// It returns the bare refusal; each caller prints it the way that command
// prints its other errors.
func leaseTTLRemoteRefusal(s storage.DoltStorage, ttl time.Duration, command string) error {
	if ttl <= 0 || !storeIsRemoteBackend(s) {
		return nil
	}
	return fmt.Errorf("--ttl is not supported on the remote bd serve route: its %s request carries no lease length, so the server would grant its default lease", command)
}

// warnLeaseTTLNotApplied warns when `bd update --claim --ttl` succeeded
// without arming a lease of that length, so the worker is told instead of
// believing it holds the longer lease. There are two such claims:
//
//   - A wisp: an issue on the wisp plane (storageissueops.IsWisp, an
//     ephemeral or a no-history issue). The claim never leases one (it is not
//     reclaimable work), so there is no expiry for --ttl to set.
//   - An idempotent re-claim. A re-claim of an issue the actor already holds
//     returns before the lease is touched, so the lease keeps the expiry it
//     had. The warning names the command that does set it.
//
// before is the issue as read before the claim. That read and the claim are
// two steps, so a claim released by someone else in between is re-armed with
// --ttl and still draws the re-claim warning; the heartbeat it names is then
// redundant and harmless.
func warnLeaseTTLNotApplied(id string, before *types.Issue, claimant string, ttl time.Duration) {
	if ttl <= 0 || before == nil {
		return
	}
	if storageissueops.IsWisp(before) {
		warnLeaseTTLNotAppliedToWisp(id, ttl)
		return
	}
	if before.Status != types.StatusInProgress || !storageissueops.ActorMatches(before.Assignee, claimant) {
		return
	}
	// The ID is the store's: it goes into the bd heartbeat command as one
	// shell word, or as a placeholder named escaped after it
	// (remedy.CommandArg), and the prose names it as printedIssueID spells it.
	idArg, note := remedy.CommandArg(runtime.GOOS, id, "<issue-id>")
	fmt.Fprintf(os.Stderr, "%s --ttl %s not applied to %s: you already held it, and a re-claim leaves the lease as it was (bd heartbeat --ttl %s %s sets its length)%s\n",
		ui.RenderWarn("!"), ttl, printedIssueID(id), ttl, idArg, note)
}

// warnLeaseTTLNotAppliedToReadyClaim is warnLeaseTTLNotApplied for
// `bd ready --claim --ttl`, on either route. The ready set can hand the claim
// a wisp (no-history issues always, ephemeral ones under --include-ephemeral),
// and the claim leases none.
//
// claimed is the row the claim returned, so only its plane is read: the claim
// has just made it in_progress with the actor as assignee, which
// warnLeaseTTLNotApplied would take for a re-claim. A ready claim never is
// one, because it takes only an open, unassigned issue.
func warnLeaseTTLNotAppliedToReadyClaim(claimed *types.Issue, ttl time.Duration) {
	if ttl <= 0 || claimed == nil || !storageissueops.IsWisp(claimed) {
		return
	}
	warnLeaseTTLNotAppliedToWisp(claimed.ID, ttl)
}

// warnLeaseTTLNotAppliedToWisp prints the wisp warning. It names no command:
// a wisp has no lease for bd heartbeat to refresh either.
func warnLeaseTTLNotAppliedToWisp(id string, ttl time.Duration) {
	fmt.Fprintf(os.Stderr, "%s --ttl %s not applied to %s: it is a wisp (an ephemeral or no-history issue), and a wisp claim has no lease\n",
		ui.RenderWarn("!"), ttl, printedIssueID(id))
}
